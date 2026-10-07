package stowage

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件为同一件已装载货物的“移动 + 重量更正”组合补充并发回归保障：
// 对同一件货物同时提交一次配载调整（Adjust 移动）与一次资料更正
// （AmendCargo 增重），两个请求各自单独看都合法、共同生效却会使目标舱位
// 超重时，登记处在并发调用下必须按实际生效时的货物位置与重量判断，让
// “返回的成功或拒绝”与“最终配载”严格一致，不能两个请求都依据旧配载
// 放行、随后留下超重舱位——
//   - 两个请求是彼此独立的两次提交，沿用 Adjust 的移动操作与 AmendCargo
//     的现有入口，不增加新的操作入口：必须恰好一个成功、另一个按超重
//     拒绝；既不能两个都成功，也不能两个都失败；
//   - 拒绝原因都是现有的结构化错误 ErrOverweight，涉及舱位 C2，中文说明
//     指出预计总重量 80 千克超过最大承重 70 千克；移动被拒绝时携带它自己
//     的调整编号，更正被拒绝时不携带调整编号；
//   - 若更正先成功：G1 保持 80 千克并仍在 C1（C1 已用 80、剩余 20），C2
//     继续为空，移动不返回成功变化记录；
//   - 若移动先成功：G1 保持原来的 60 千克并位于 C2（C1 清空，C2 已用 60、
//     剩余 10），失败的更正不能把新重量写入货物资料或舱位清单；
//   - 并发不指定哪一个必须成功，两种合法先后都应得到保障；操作结束后货物
//     查询与两舱货物清单一致，G1 只属于一个舱位，目的地（上海）与混装
//     许可（允许）保持原值。
//
// 同时保留恰好达到承重的边界：同样同时移动，但只把重量改为 70 千克时，
// 两个请求都必须成功，最终 G1 在 C2、重量 70 千克，C1 为空，C2 剩余重量
// 为零，不能仅因请求同时发生而拒绝其中一个。
//
// 固定场景：C1 承重 100 千克，C2 承重 70 千克；G1 重 60 千克，目的地上海、
// 允许混装，已装在 C1，C2 为空。超重情形把重量整体更正为 80 千克（移动
// 单独进行合法：C2 计入 60；在 C1 单独增重合法：C1 计入 80；共同生效则
// C2 计入 80 超重）。恰好够用情形把重量更正为 70 千克（共同生效后 C2
// 恰好满载）。

// moveAmendOutcome 记录一轮并发中移动调整与重量更正各自的返回结果。
type moveAmendOutcome struct {
	moveRes *AdjustmentResult // 移动调整的返回（失败时为 nil）
	moveErr error
	amendErr error
}

// newConcurrentMoveAmendRegistry 构造并发“移动 + 更正”的固定初始配载：
// C1 承重 100、C2 承重 70；G1 重 60 千克、目的地上海、允许混装，已装入
// C1（C1 已用 60、剩余 40，C2 为空）。每轮并发都从一份全新配载独立开始。
func newConcurrentMoveAmendRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 70)
	mustRegisterCargo(t, r, "G1", 60, "上海", true)
	if _, err := r.Adjust("load-g1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	}); err != nil {
		t.Fatalf("初始装载失败: %v", err)
	}
	c1, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.MaxWeight != 100 || c1.UsedWeight != 60 || c1.RemainingWeight != 40 {
		t.Fatalf("初始配载应为 C1 已用 60/剩余 40，实际 %+v", c1)
	}
	if got := cargoIDs(*c1); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("初始 C1 清单应为 [G1]，实际 %v", got)
	}
	c2, err := r.Compartment("C2")
	if err != nil {
		t.Fatal(err)
	}
	if c2.UsedWeight != 0 || len(c2.Cargo) != 0 {
		t.Fatalf("初始 C2 应为空舱，实际 %+v", c2)
	}
	return r
}

// runConcurrentMoveAmendRound 在同一个登记处上让两个请求从同一道起跑线后
// 并发提交：以一个未使用的调整编号把 G1 移到 C2，以及把 G1 的重量整体
// 更正为 newWeight 千克（目的地上海与混装许可保持原值）。delayMove/
// delayAmend > 0 时对应请求开闸后先等待该时长再提交，用于在多轮中覆盖
// 两种生效先后；均为 0 时是纯粹的同时锁竞争。返回登记处与两个请求各自
// 的结果，由调用方按场景核对。
func runConcurrentMoveAmendRound(t *testing.T, newWeight int64, delayMove, delayAmend time.Duration) (*Registry, moveAmendOutcome) {
	t.Helper()
	r := newConcurrentMoveAmendRegistry(t)

	start := make(chan struct{})
	var out moveAmendOutcome
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if delayMove > 0 {
			time.Sleep(delayMove)
		}
		out.moveRes, out.moveErr = r.Adjust("move-g1-to-c2", []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		if delayAmend > 0 {
			time.Sleep(delayAmend)
		}
		out.amendErr = r.AmendCargo("G1", newWeight, "上海", true)
	}()
	close(start)
	wg.Wait()
	return r, out
}

// assertOverweightError 核对一次超重拒绝：现有的 ErrOverweight 结构化
// 错误，涉及舱位 C2，中文说明指出预计总重量 80 千克与最大承重 70 千克；
// wantAdjID 为移动自己的调整编号（移动被拒绝时）或空串（更正被拒绝时）。
func assertOverweightError(t *testing.T, err error, wantAdjID string) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("被拒绝的请求应返回 *Error，实际为 %T: %v", err, err)
	}
	if se.Kind != ErrOverweight {
		t.Fatalf("应按超重拒绝，实际为 %s（%v）", se.Kind, err)
	}
	if se.ID != "C2" {
		t.Fatalf("超重错误应涉及舱位 C2，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != wantAdjID {
		t.Fatalf("超重错误的调整编号应为 %q，实际 %q", wantAdjID, se.AdjustmentID)
	}
	msg := se.Error()
	for _, want := range []string{"C2", "80", "70"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("超重说明应指出舱位 C2、预计总重量 80 千克与最大承重 70 千克，实际: %s", msg)
		}
	}
}

// assertMoveAmendConsistentViews 核对操作结束后的查询一致性：G1 的货物
// 查询显示其位于 wantComp、重量 wantWeight、目的地上海、允许混装；两个
// 舱位的货物清单与货物查询逐项一致，G1 恰好出现在一个舱位的清单中。
func assertMoveAmendConsistentViews(t *testing.T, r *Registry, wantComp string, wantWeight int64) {
	t.Helper()
	cv, err := r.Cargo("G1")
	if err != nil {
		t.Fatalf("查询货物 G1 失败: %v", err)
	}
	if !cv.Loaded || cv.CompartmentID != wantComp {
		t.Fatalf("G1 应显示已装载于 %s，实际 %+v", wantComp, cv)
	}
	if cv.Weight != wantWeight {
		t.Fatalf("G1 重量应为 %d，实际 %d", wantWeight, cv.Weight)
	}
	if cv.Destination != "上海" || !cv.AllowMixed {
		t.Fatalf("G1 的目的地与混装许可应保持原值（上海、允许混装），实际 %+v", cv)
	}

	// G1 恰好属于一个舱位，且清单中的资料与货物单独查询逐项一致。
	seen := 0
	for _, compID := range []string{"C1", "C2"} {
		cpt, err := r.Compartment(compID)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", compID, err)
		}
		for _, item := range cpt.Cargo {
			if item.ID != "G1" {
				t.Fatalf("舱位 %s 清单中出现意外货物 %s", compID, item.ID)
			}
			seen++
			if !item.Loaded || item.CompartmentID != compID {
				t.Fatalf("清单中的 G1 应显示已装载于 %s，实际 %+v", compID, item)
			}
			if item.Weight != cv.Weight || item.Destination != cv.Destination ||
				item.AllowMixed != cv.AllowMixed {
				t.Fatalf("清单中 G1 的资料（%d/%s/%t）与货物查询（%d/%s/%t）不一致",
					item.Weight, item.Destination, item.AllowMixed,
					cv.Weight, cv.Destination, cv.AllowMixed)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("G1 应恰好出现在一个舱位的清单中，实际出现 %d 次", seen)
	}
}

// assertAmendWinsRound 核对“更正先成功”的并发轮：G1 保持 80 千克并仍在
// C1（已用 80、剩余 20），C2 继续为空；移动按超重被拒绝、携带自己的调整
// 编号，且不返回成功变化记录。
func assertAmendWinsRound(t *testing.T, r *Registry, out moveAmendOutcome) {
	t.Helper()
	if out.amendErr != nil {
		t.Fatalf("本轮更正先生效，更正应成功: %v", out.amendErr)
	}
	if out.moveRes != nil {
		t.Fatalf("被拒绝的移动不应返回成功变化记录: %+v", out.moveRes)
	}
	assertOverweightError(t, out.moveErr, "move-g1-to-c2")

	c1, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询 C1 失败: %v", err)
	}
	if c1.UsedWeight != 80 || c1.RemainingWeight != 20 {
		t.Fatalf("更正先生效后 C1 应已用 80/剩余 20，实际 %+v", c1)
	}
	if got := cargoIDs(*c1); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("更正先生效后 C1 清单应为 [G1]，实际 %v", got)
	}
	c2, err := r.Compartment("C2")
	if err != nil {
		t.Fatalf("查询 C2 失败: %v", err)
	}
	if c2.UsedWeight != 0 || c2.RemainingWeight != 70 || len(c2.Cargo) != 0 {
		t.Fatalf("更正先生效后 C2 应继续为空，实际 %+v", c2)
	}
	assertMoveAmendConsistentViews(t, r, "C1", 80)
}

// assertMoveWinsRound 核对“移动先成功”的并发轮：G1 保持原来的 60 千克并
// 位于 C2（C1 清空，C2 已用 60、剩余 10）；更正按超重被拒绝、不携带调整
// 编号，且不能把新重量写入货物资料或舱位清单；移动返回的成功变化记录与
// 最终配载一致。
func assertMoveWinsRound(t *testing.T, r *Registry, out moveAmendOutcome) {
	t.Helper()
	if out.moveErr != nil {
		t.Fatalf("本轮移动先生效，移动应成功: %v", out.moveErr)
	}
	assertOverweightError(t, out.amendErr, "")

	// 移动的成功变化记录：G1 从 C1 到 C2；C1 60→0，C2 0→60。
	if out.moveRes == nil {
		t.Fatalf("移动先生效时应返回成功结果")
	}
	if out.moveRes.ID != "move-g1-to-c2" {
		t.Fatalf("移动结果编号应为 move-g1-to-c2，实际 %q", out.moveRes.ID)
	}
	wantCargo := []CargoChange{{CargoID: "G1", From: "C1", To: "C2"}}
	if !reflect.DeepEqual(out.moveRes.CargoChanges, wantCargo) {
		t.Fatalf("移动的货物变化应为 %+v，实际 %+v", wantCargo, out.moveRes.CargoChanges)
	}
	wantComp := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 60, WeightAfter: 0},
		{CompartmentID: "C2", WeightBefore: 0, WeightAfter: 60},
	}
	if !reflect.DeepEqual(out.moveRes.CompartmentChanges, wantComp) {
		t.Fatalf("移动的舱位重量变化应为 %+v，实际 %+v", wantComp, out.moveRes.CompartmentChanges)
	}

	c1, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询 C1 失败: %v", err)
	}
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("移动先生效后 C1 应清空，实际 %+v", c1)
	}
	c2, err := r.Compartment("C2")
	if err != nil {
		t.Fatalf("查询 C2 失败: %v", err)
	}
	if c2.UsedWeight != 60 || c2.RemainingWeight != 10 {
		t.Fatalf("移动先生效后 C2 应已用 60/剩余 10，实际 %+v", c2)
	}
	if got := cargoIDs(*c2); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("移动先生效后 C2 清单应为 [G1]，实际 %v", got)
	}
	// 失败的更正不写入新重量：货物查询与舱位清单都仍是 60 千克。
	assertMoveAmendConsistentViews(t, r, "C2", 60)
}

// TestConcurrentMoveAndAmendOnlyOneTakesEffect 是核心并发回归：对同一件
// 已装载货物 G1 并发地提交移动（到 C2）与重量更正（60→80 千克）。任一
// 请求单独作用于初始配载都合法，但两个共同生效会使 C2 达到 80 千克、
// 超过承重 70 千克，因此多轮并发中必须始终恰好一个成功、另一个按超重
// 拒绝，且两种生效先后都实际出现过（不规定哪个成功），无论哪个成功，
// 错误与最终配载都满足各自要求。
func TestConcurrentMoveAndAmendOnlyOneTakesEffect(t *testing.T) {
	// 前置事实：任一请求单独作用于初始配载都合法，并发下的拒绝只能来自
	// 两个请求共同占用 C2 承重，而不是资料本身错误。
	t.Run("任一请求单独提交都合法", func(t *testing.T) {
		rMove := newConcurrentMoveAmendRegistry(t)
		res, err := rMove.Adjust("move-g1-to-c2", []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		})
		if err != nil {
			t.Fatalf("移动单独提交本应合法（C2 计入 60）: %v", err)
		}
		if res == nil {
			t.Fatalf("移动单独提交应返回成功结果")
		}
		c2, _ := rMove.Compartment("C2")
		if c2.UsedWeight != 60 || c2.RemainingWeight != 10 {
			t.Fatalf("移动单独生效后 C2 应已用 60/剩余 10，实际 %+v", c2)
		}

		rAmend := newConcurrentMoveAmendRegistry(t)
		if err := rAmend.AmendCargo("G1", 80, "上海", true); err != nil {
			t.Fatalf("在 C1 单独增重到 80 千克本应合法（C1 承重 100）: %v", err)
		}
		cv, _ := rAmend.Cargo("G1")
		if cv.Weight != 80 || cv.CompartmentID != "C1" {
			t.Fatalf("单独更正后 G1 应为 80 千克且仍在 C1: %+v", cv)
		}
		c1, _ := rAmend.Compartment("C1")
		if c1.UsedWeight != 80 || c1.RemainingWeight != 20 {
			t.Fatalf("单独更正后 C1 应已用 80/剩余 20，实际 %+v", c1)
		}
	})

	// 并发竞争：多轮覆盖两种先后，并穿插不偏不倚的同时竞争。
	wins := map[string]int{"move": 0, "amend": 0}
	account := func(r *Registry, out moveAmendOutcome) {
		t.Helper()
		switch {
		case out.moveErr == nil && out.amendErr == nil:
			t.Fatalf("两个请求都成功：共同生效会使 C2 达到 80 千克、超过承重 70 千克，"+
				"不能一起留下超重配载（移动结果 %+v）", out.moveRes)
		case out.moveErr != nil && out.amendErr != nil:
			t.Fatalf("两个请求都失败：移动 %v，更正 %v，应有恰好一个成功",
				out.moveErr, out.amendErr)
		case out.amendErr == nil:
			wins["amend"]++
			assertAmendWinsRound(t, r, out)
		default:
			wins["move"]++
			assertMoveWinsRound(t, r, out)
		}
	}

	const rounds = 40
	for i := 0; i < rounds; i++ {
		var delayMove, delayAmend time.Duration
		switch i % 4 {
		case 0:
			delayAmend = 2 * time.Millisecond // 偏向移动先提交
		case 1:
			delayMove = 2 * time.Millisecond // 偏向更正先提交
		case 2:
			delayAmend = 5 * time.Millisecond // 再次偏向移动，加大先后差
		default:
			// 两个请求零等待同时提交，纯粹的锁竞争。
		}
		r, out := runConcurrentMoveAmendRound(t, 80, delayMove, delayAmend)
		account(r, out)
	}

	// 极端调度下若某种先后始终未出现，补几轮强偏向竞争；仍不出现才判定
	// 失败。两种生效先后都必须被实际观察到（不规定哪个请求成功，但任一
	// 个都应能在并发提交中正常生效）。
	for j := 0; j < 3 && wins["move"] == 0; j++ {
		r, out := runConcurrentMoveAmendRound(t, 80, 0, 25*time.Millisecond)
		account(r, out)
	}
	for j := 0; j < 3 && wins["amend"] == 0; j++ {
		r, out := runConcurrentMoveAmendRound(t, 80, 25*time.Millisecond, 0)
		account(r, out)
	}
	if wins["move"] == 0 || wins["amend"] == 0 {
		t.Fatalf("经过 %d 轮并发仍只观察到一种生效先后（移动成功 %d 次、更正成功 %d 次），"+
			"两种先后都应被接受", rounds, wins["move"], wins["amend"])
	}
	t.Logf("并发移动+更正 %d 轮：移动先生效 %d 次，更正先生效 %d 次（每次均为另一请求超重拒绝）",
		rounds, wins["move"], wins["amend"])
}

// TestConcurrentMoveAndAmendExactCapacityBothSucceed 覆盖恰好达到承重的
// 边界：同样同时移动并更正，但重量只改为 70 千克，共同生效后 C2 恰好
// 满载（已用 70、剩余 0），两个请求都必须成功，不能仅因请求同时发生而
// 拒绝其中一个。先核对顺序提交的基线，再在多轮并发（含两种先后与同时
// 提交）中确认并发本身不构成拒绝理由。
func TestConcurrentMoveAndAmendExactCapacityBothSucceed(t *testing.T) {
	// 顺序基线：两种先后下两个请求都成功，最终 G1 在 C2、重量 70 千克。
	t.Run("顺序提交两种先后都成功", func(t *testing.T) {
		// 先更正后移动。
		r1 := newConcurrentMoveAmendRegistry(t)
		if err := r1.AmendCargo("G1", 70, "上海", true); err != nil {
			t.Fatalf("先更正为 70 千克应成功（C1 计入 70）: %v", err)
		}
		if _, err := r1.Adjust("move-g1-to-c2", []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		}); err != nil {
			t.Fatalf("再移动到 C2 应成功（C2 恰好计入 70）: %v", err)
		}
		// 先移动后更正。
		r2 := newConcurrentMoveAmendRegistry(t)
		if _, err := r2.Adjust("move-g1-to-c2", []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		}); err != nil {
			t.Fatalf("先移动到 C2 应成功（C2 计入 60）: %v", err)
		}
		if err := r2.AmendCargo("G1", 70, "上海", true); err != nil {
			t.Fatalf("再更正为 70 千克应成功（C2 恰好计入 70）: %v", err)
		}
		for i, r := range []*Registry{r1, r2} {
			c2, _ := r.Compartment("C2")
			if c2.UsedWeight != 70 || c2.RemainingWeight != 0 {
				t.Fatalf("顺序基线 %d：C2 应已用 70/剩余 0，实际 %+v", i+1, c2)
			}
			assertMoveAmendConsistentViews(t, r, "C2", 70)
		}
	})

	// 并发：多轮覆盖两种先后与同时提交，每轮两个请求都必须成功。
	for i := 0; i < 30; i++ {
		var delayMove, delayAmend time.Duration
		switch i % 3 {
		case 0:
			delayAmend = 2 * time.Millisecond
		case 1:
			delayMove = 2 * time.Millisecond
		default:
			// 零等待同时提交。
		}
		r, out := runConcurrentMoveAmendRound(t, 70, delayMove, delayAmend)
		if out.moveErr != nil {
			t.Fatalf("第 %d 轮：共同生效后 C2 恰好 70 千克等于承重，移动不应被拒绝: %v",
				i, out.moveErr)
		}
		if out.amendErr != nil {
			t.Fatalf("第 %d 轮：共同生效后 C2 恰好 70 千克等于承重，更正不应被拒绝: %v",
				i, out.amendErr)
		}

		// 移动的成功变化记录：G1 从 C1 到 C2。舱位重量变化记录的是移动
		// 生效当时的前后重量（先移动则 C2 记为 0→60，先更正则记为 0→70），
		// 两种先后都合法；最终配载以操作结束后的查询为准，在下方核对。
		if out.moveRes == nil {
			t.Fatalf("第 %d 轮：移动成功时应返回成功结果", i)
		}
		wantCargo := []CargoChange{{CargoID: "G1", From: "C1", To: "C2"}}
		if !reflect.DeepEqual(out.moveRes.CargoChanges, wantCargo) {
			t.Fatalf("第 %d 轮：移动的货物变化应为 %+v，实际 %+v",
				i, wantCargo, out.moveRes.CargoChanges)
		}
		after := map[string]int64{}
		for _, ch := range out.moveRes.CompartmentChanges {
			after[ch.CompartmentID] = ch.WeightAfter
		}
		if after["C1"] != 0 || (after["C2"] != 60 && after["C2"] != 70) {
			t.Fatalf("第 %d 轮：移动结果中 C1 最终应为 0、C2 应为 60 或 70（取决于先后），实际 %+v",
				i, out.moveRes.CompartmentChanges)
		}

		c1, err := r.Compartment("C1")
		if err != nil {
			t.Fatalf("第 %d 轮：查询 C1 失败: %v", i, err)
		}
		if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
			t.Fatalf("第 %d 轮：两个请求都成功后 C1 应为空，实际 %+v", i, c1)
		}
		c2, err := r.Compartment("C2")
		if err != nil {
			t.Fatalf("第 %d 轮：查询 C2 失败: %v", i, err)
		}
		if c2.UsedWeight != 70 || c2.RemainingWeight != 0 {
			t.Fatalf("第 %d 轮：两个请求都成功后 C2 应已用 70/剩余 0，实际 %+v", i, c2)
		}
		if got := cargoIDs(*c2); !reflect.DeepEqual(got, []string{"G1"}) {
			t.Fatalf("第 %d 轮：C2 清单应为 [G1]，实际 %v", i, got)
		}
		assertMoveAmendConsistentViews(t, r, "C2", 70)
	}
}
