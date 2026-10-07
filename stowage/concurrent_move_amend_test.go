package stowage

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件为“同一件已装载货物同时移动与更正重量”补充并发回归保障：登记处
// 允许 Adjust 的移动操作与 AmendCargo 的资料更正安全地并发调用，两个请求
// 各自单独看都合法时，后生效的一个必须按另一个生效后的实际货物位置与重量
// 重新判断，不能各自依据旧的配载放行、共同留下超重配载——
//   - 两个请求是彼此独立的两次调用，沿用 Adjust 的移动与 AmendCargo 的更
//     正这两个现有入口，不新增操作入口：共同生效会使 C2 超重时必须恰好一
//     个成功、另一个按超重拒绝；既不能两个都成功，也不能两个都失败；
//   - 两种拒绝都返回现有的结构化错误 ErrOverweight，涉及舱位 C2，中文说
//     明指出预计总重量 80 千克超过承重 70 千克；移动被拒绝时携带它自己的
//     调整编号且不返回成功变化记录，更正被拒绝时不携带调整编号、也不能把
//     新重量写入货物资料或舱位清单；
//   - 不规定哪一个请求必须成功：更正先生效与移动先生效两种合法先后都要
//     得到保障，返回的成功与失败必须和各自的最终配载严格一致；
//   - 操作结束后货物查询与两舱货物清单一致，G1 恰好属于一个舱位，目的地
//     （上海）与混装许可（允许）保持原值。
//
// 同时保留恰好达到承重的边界：同样同时移动，但只把重量更正为 70 千克时，
// 共同生效后 C2 恰好满载，两个请求都必须成功，不能仅因请求同时发生就拒
// 绝其中一个。
//
// 固定场景：C1 承重 100 千克、C2 承重 70 千克；G1 重 60 千克、目的地上海、
// 允许混装，已装在 C1（C1 已用 60、剩余 40），C2 为空。移动单独进行合法
// （60 ≤ 70），在 C1 单独增重到 80 也合法（80 ≤ 100），但两个请求共同生
// 效会把 80 千克的 G1 放进承重 70 的 C2。

// moveAmendOutcome 记录一轮并发中移动调整与重量更正各自的结果。
type moveAmendOutcome struct {
	moveRes  *AdjustmentResult
	moveErr  error
	amendErr error
}

// moveAdjustmentID 是移动请求使用的调整编号；每轮并发都从全新登记处开始，
// 该编号在当轮必定未被使用。
const moveAdjustmentID = "adj-move-g1-c2"

// newConcurrentMoveAmendRegistry 构造并发“移动+更正”的固定初始配载：
// C1 承重 100、C2 承重 70；G1 重 60 千克、目的地上海、允许混装，已装入
// C1（已用 60、剩余 40），C2 为空。每轮并发都从一份全新配载独立开始。
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
// 并发提交：以未使用的调整编号把 G1 移到 C2，以及把 G1 的重量整体更正为
// newWeight 千克（目的地上海与混装许可保持原值）。delayMove/delayAmend > 0
// 时对应请求开闸后先等待该时长再提交，用于在多轮中覆盖两种生效先后；均为
// 0 时是纯粹的同时锁竞争。返回登记处与两个请求各自的结果，由调用方核对。
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
		out.moveRes, out.moveErr = r.Adjust(moveAdjustmentID, []Op{
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

// assertOverweightError 核对超重拒绝的结构化错误：现有的 ErrOverweight，
// 涉及舱位 C2，中文说明指出预计总重量 80 千克超过承重 70 千克；wantAdjID
// 为移动调整编号（移动被拒绝时携带自己的编号），为空表示更正被拒绝（不
// 携带调整编号）。
func assertOverweightError(t *testing.T, err error, wantAdjID string) {
	t.Helper()
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("超重拒绝应返回 *Error，实际为 %T: %v", err, err)
	}
	if se.Kind != ErrOverweight {
		t.Fatalf("拒绝原因应为超重，实际为 %s（%v）", se.Kind, err)
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
			t.Fatalf("超重说明应指出舱位 C2、预计总重量 80 千克与承重 70 千克，实际: %s", msg)
		}
	}
}

// assertMoveSucceeded 核对移动成功的变化记录：G1 从 C1 移到 C2，C1 由
// beforeC1 千克清空、C2 由空舱变为 afterC2 千克。移动与更正并发时，变化
// 记录中的重量取决于移动生效时更正是否已落账，调用方按场景给出预期值。
func assertMoveSucceeded(t *testing.T, res *AdjustmentResult, beforeC1, afterC2 int64) {
	t.Helper()
	if res == nil {
		t.Fatalf("移动成功应返回变化记录")
	}
	if res.ID != moveAdjustmentID {
		t.Fatalf("成功结果编号应为 %q，实际 %q", moveAdjustmentID, res.ID)
	}
	wantCargo := []CargoChange{{CargoID: "G1", From: "C1", To: "C2"}}
	if !reflect.DeepEqual(res.CargoChanges, wantCargo) {
		t.Fatalf("移动成功的货物变化应为 %+v，实际 %+v", wantCargo, res.CargoChanges)
	}
	wantComp := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: beforeC1, WeightAfter: 0},
		{CompartmentID: "C2", WeightBefore: 0, WeightAfter: afterC2},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantComp) {
		t.Fatalf("移动成功的舱位重量变化应为 %+v，实际 %+v", wantComp, res.CompartmentChanges)
	}
}

// assertMoveAmendFinalStowage 按实际生效的请求核对最终配载：G1 的重量与
// 所属舱位符合生效结果、两舱的已用/剩余重量与货物清单正确，货物查询与
// 舱位清单逐项一致，G1 恰好属于一个舱位，目的地与混装许可保持原值。
func assertMoveAmendFinalStowage(t *testing.T, r *Registry, wantWeight int64, wantComp string) {
	t.Helper()

	cv, err := r.Cargo("G1")
	if err != nil {
		t.Fatalf("查询货物 G1 失败: %v", err)
	}
	if !cv.Loaded || cv.CompartmentID != wantComp {
		t.Fatalf("G1 应已装载于 %s，实际 %+v", wantComp, cv)
	}
	if cv.Weight != wantWeight {
		t.Fatalf("G1 重量应为 %d，实际 %d", wantWeight, cv.Weight)
	}
	if cv.Destination != "上海" || !cv.AllowMixed {
		t.Fatalf("G1 的目的地与混装许可应保持原值（上海、允许混装），实际 %+v", cv)
	}

	// 两舱重量与清单：G1 恰好属于一个舱位，另一舱为空。
	wantUsed := map[string]int64{"C1": 0, "C2": 0}
	wantUsed[wantComp] = wantWeight
	wantMax := map[string]int64{"C1": 100, "C2": 70}
	for _, id := range []string{"C1", "C2"} {
		view, err := r.Compartment(id)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", id, err)
		}
		if view.MaxWeight != wantMax[id] ||
			view.UsedWeight != wantUsed[id] ||
			view.RemainingWeight != wantMax[id]-wantUsed[id] {
			t.Fatalf("舱位 %s 应为已用 %d/剩余 %d，实际 %+v",
				id, wantUsed[id], wantMax[id]-wantUsed[id], view)
		}
		if id == wantComp {
			if got := cargoIDs(*view); !reflect.DeepEqual(got, []string{"G1"}) {
				t.Fatalf("舱位 %s 清单应为 [G1]，实际 %v", id, got)
			}
			if len(view.Cargo) != 1 {
				t.Fatalf("舱位 %s 清单应恰好一件货物，实际 %d 件", id, len(view.Cargo))
			}
			item := view.Cargo[0]
			if !item.Loaded || item.CompartmentID != wantComp ||
				item.Weight != cv.Weight || item.Destination != cv.Destination ||
				item.AllowMixed != cv.AllowMixed {
				t.Fatalf("清单中的 G1（%+v）与货物查询（%+v）不一致", item, cv)
			}
		} else if len(view.Cargo) != 0 {
			t.Fatalf("舱位 %s 应为空舱，实际清单 %+v", id, view.Cargo)
		}
	}
}

// assertMoveAmendOverweightRound 核对超重竞争轮：恰好一个请求成功、另一个
// 按超重拒绝，错误结构、变化记录与最终配载全部符合要求。返回生效请求的
// 名称（"move" 或 "amend"，业务允许任意一个先成功，不规定固定成功者）。
func assertMoveAmendOverweightRound(t *testing.T, r *Registry, out moveAmendOutcome) string {
	t.Helper()
	moveOK := out.moveErr == nil
	amendOK := out.amendErr == nil
	if moveOK == amendOK {
		t.Fatalf("两个并发请求应恰好一个成功、另一个超重拒绝（不是整体成败），"+
			"实际移动 err=%v、更正 err=%v", out.moveErr, out.amendErr)
	}

	if amendOK {
		// 更正先生效：G1 保持 80 千克并仍在 C1，C1 已用 80、剩余 20，
		// C2 继续为空；移动因超重被拒绝，携带自己的调整编号，不返回
		// 成功变化记录。
		assertOverweightError(t, out.moveErr, moveAdjustmentID)
		if out.moveRes != nil {
			t.Fatalf("移动被拒绝时不应返回成功变化记录: %+v", out.moveRes)
		}
		assertMoveAmendFinalStowage(t, r, 80, "C1")
		return "amend"
	}

	// 移动先生效：G1 保持原来的 60 千克并位于 C2，C1 清空，C2 已用 60、
	// 剩余 10；更正因超重被拒绝，不携带调整编号，也不能把新重量写入
	// 货物资料或舱位清单。
	assertMoveSucceeded(t, out.moveRes, 60, 60)
	assertOverweightError(t, out.amendErr, "")
	assertMoveAmendFinalStowage(t, r, 60, "C2")
	return "move"
}

// TestConcurrentMoveAndAmendSameCargoExactlyOneTakesEffect 是核心并发回归：
// 对同一件已装载货物 G1 并发提交移动（移到 C2）与重量更正（改为 80 千克）。
// 任一请求单独作用于初始配载都合法，但两个共同生效会使 C2 达到 80 千克、
// 超过承重 70，因此多轮并发中必须始终恰好一个成功、另一个按超重拒绝，且
// 两种生效先后都实际出现过（不规定哪个成功），无论哪个成功，错误与最终
// 配载都满足各自要求。
func TestConcurrentMoveAndAmendSameCargoExactlyOneTakesEffect(t *testing.T) {
	// 前置事实：两个请求各自单独作用于初始配载都合法，并发下的拒绝只能
	// 来自共同生效后的超重，而不是资料或编号本身错误。
	t.Run("每个请求单独提交都合法", func(t *testing.T) {
		rMove := newConcurrentMoveAmendRegistry(t)
		res, err := rMove.Adjust(moveAdjustmentID, []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		})
		if err != nil {
			t.Fatalf("移动单独提交本应合法（60 ≤ 70）: %v", err)
		}
		assertMoveSucceeded(t, res, 60, 60)
		assertMoveAmendFinalStowage(t, rMove, 60, "C2")

		rAmend := newConcurrentMoveAmendRegistry(t)
		if err := rAmend.AmendCargo("G1", 80, "上海", true); err != nil {
			t.Fatalf("更正单独提交本应合法（80 ≤ 100）: %v", err)
		}
		assertMoveAmendFinalStowage(t, rAmend, 80, "C1")
	})

	// 并发竞争：多轮覆盖两种先后，并穿插不偏不倚的同时竞争。
	wins := map[string]int{"move": 0, "amend": 0}
	const rounds = 40
	for i := 0; i < rounds; i++ {
		var delayMove, delayAmend time.Duration
		switch i % 4 {
		case 0:
			delayAmend = 2 * time.Millisecond // 偏向移动先生效
		case 1:
			delayMove = 2 * time.Millisecond // 偏向更正先生效
		case 2:
			delayAmend = 5 * time.Millisecond // 再次偏向移动，加大先后差
		default:
			// 两个请求零等待同时提交，纯粹的锁竞争。
		}
		r, out := runConcurrentMoveAmendRound(t, 80, delayMove, delayAmend)
		wins[assertMoveAmendOverweightRound(t, r, out)]++
	}

	// 极端调度下若某种先后始终未出现，补几轮强偏向竞争；仍不出现才判定
	// 失败。两种生效先后都必须被实际观察到（不规定哪个成功，但任一个都
	// 应能在并发提交中正常生效）。
	for j := 0; j < 3 && wins["move"] == 0; j++ {
		r, out := runConcurrentMoveAmendRound(t, 80, 0, 25*time.Millisecond)
		wins[assertMoveAmendOverweightRound(t, r, out)]++
	}
	for j := 0; j < 3 && wins["amend"] == 0; j++ {
		r, out := runConcurrentMoveAmendRound(t, 80, 25*time.Millisecond, 0)
		wins[assertMoveAmendOverweightRound(t, r, out)]++
	}
	if wins["move"] == 0 || wins["amend"] == 0 {
		t.Fatalf("经过 %d 轮并发提交仍只观察到一种生效先后（移动生效 %d 次、更正生效 %d 次），"+
			"两种先后都应被接受", rounds, wins["move"], wins["amend"])
	}
	t.Logf("并发竞争 %d 轮：移动生效 %d 次，更正生效 %d 次（每次均为另一个请求超重拒绝）",
		rounds, wins["move"], wins["amend"])
}

// TestConcurrentMoveAndAmendExactCapacityBothSucceed 覆盖恰好达到承重的边界：
// 同样同时移动与更正，但只把重量改为 70 千克，共同生效后 C2 恰好满载
// （70 等于承重），两个请求都必须成功，最终 G1 在 C2、重量 70 千克，C1 为
// 空，C2 剩余重量为零，不能仅因请求同时发生就拒绝其中一个。先核对两种顺
// 序提交都成功的基线，再在多轮并发中确认并发本身不构成拒绝理由。
func TestConcurrentMoveAndAmendExactCapacityBothSucceed(t *testing.T) {
	// 顺序基线：两种先后下两个请求都应成功，最终配载相同。
	t.Run("顺序提交两个请求都成功", func(t *testing.T) {
		rAmendFirst := newConcurrentMoveAmendRegistry(t)
		if err := rAmendFirst.AmendCargo("G1", 70, "上海", true); err != nil {
			t.Fatalf("先更正为 70 千克应成功（C1 已用 70 ≤ 100）: %v", err)
		}
		res, err := rAmendFirst.Adjust(moveAdjustmentID, []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		})
		if err != nil {
			t.Fatalf("再移动到 C2 应成功（70 恰好等于承重）: %v", err)
		}
		assertMoveSucceeded(t, res, 70, 70)
		assertMoveAmendFinalStowage(t, rAmendFirst, 70, "C2")

		rMoveFirst := newConcurrentMoveAmendRegistry(t)
		res, err = rMoveFirst.Adjust(moveAdjustmentID, []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		})
		if err != nil {
			t.Fatalf("先移动到 C2 应成功（60 ≤ 70）: %v", err)
		}
		assertMoveSucceeded(t, res, 60, 60)
		if err := rMoveFirst.AmendCargo("G1", 70, "上海", true); err != nil {
			t.Fatalf("再更正为 70 千克应成功（C2 恰好满载）: %v", err)
		}
		assertMoveAmendFinalStowage(t, rMoveFirst, 70, "C2")
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
			t.Fatalf("共同生效后 C2 恰好 70 千克等于承重，移动不应被拒绝（不能因并发就拒绝）: %v",
				out.moveErr)
		}
		if out.amendErr != nil {
			t.Fatalf("共同生效后 C2 恰好 70 千克等于承重，更正不应被拒绝（不能因并发就拒绝）: %v",
				out.amendErr)
		}
		// 变化记录中的重量取决于两个请求的生效先后：更正先落账时移动
		// 记录 C1 由 70 清空、C2 变为 70；移动先落账时记录 C1 由 60 清
		// 空、C2 变为 60（更正随后把重量改到 70）。两种先后都合法。
		if out.moveRes == nil {
			t.Fatalf("移动成功应返回变化记录")
		}
		if out.moveRes.ID != moveAdjustmentID {
			t.Fatalf("成功结果编号应为 %q，实际 %q", moveAdjustmentID, out.moveRes.ID)
		}
		wantCargo := []CargoChange{{CargoID: "G1", From: "C1", To: "C2"}}
		if !reflect.DeepEqual(out.moveRes.CargoChanges, wantCargo) {
			t.Fatalf("移动成功的货物变化应为 %+v，实际 %+v", wantCargo, out.moveRes.CargoChanges)
		}
		switch {
		case reflect.DeepEqual(out.moveRes.CompartmentChanges, []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 70, WeightAfter: 0},
			{CompartmentID: "C2", WeightBefore: 0, WeightAfter: 70},
		}):
		case reflect.DeepEqual(out.moveRes.CompartmentChanges, []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 60, WeightAfter: 0},
			{CompartmentID: "C2", WeightBefore: 0, WeightAfter: 60},
		}):
		default:
			t.Fatalf("移动成功的舱位重量变化应为 (70→0, 0→70) 或 (60→0, 0→60)，实际 %+v",
				out.moveRes.CompartmentChanges)
		}
		assertMoveAmendFinalStowage(t, r, 70, "C2")
	}
}
