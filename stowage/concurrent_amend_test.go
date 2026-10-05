package stowage

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件为已装载货物的重量更正（AmendCargo）补充并发回归保障：同一舱位内
// 两件货物同时增重，而两次更正各自单独看都合法、共同生效却会超过舱位承重
// 时，登记处在并发调用下仍必须让“返回的成功或拒绝”与“最终配载”严格一致，
// 不能两次都依据旧的剩余重量放行、随后留下超重舱位——
//   - 两次更正是彼此独立的两次请求，沿用 AmendCargo 现有入口与规则，不是
//     一次整体成败的批量操作：必须恰好一次成功，另一次按超重拒绝；既不能
//     两次都成功，也不能两次都失败；
//   - 被拒绝的更正返回现有的结构化错误 ErrOverweight，涉及编号为舱位 C1，
//     中文说明指出预计总重量 120 千克与最大承重 100 千克，更正不携带调整
//     编号；失败不产生任何副作用；
//   - 成功更正的货物采用新重量，失败更正的货物保留旧重量；失败请求既不能
//     覆盖成功请求的结果，也不能在舱位中留下自己的新重量；
//   - 两件货物都继续显示已装载于 C1，目的地（上海）与混装许可（允许）不变；
//     货物单独查询与舱位货物清单中的资料逐项一致，清单仍完整包含两件、各一次。
//
// 同时覆盖承重恰好够用的对照情形：两次增重共同生效后恰好等于承重时，两次
// 都必须成功，不能仅因更正并发发生就拒绝其中一次——保障必须能区分业务上
// 必须拒绝的增重与仍可接受的增重，整体替换与失败保留原资料的行为继续适用。
//
// 固定场景：舱位 C1 承重 100 千克，G1、G2 各重 30 千克，目的地均为上海且
// 都允许混装，两件已装入 C1，舱位已用 60 千克、剩余 40 千克。超重情形把
// 两件分别改为 60 千克（单独作用合计 90 合法，共同作用合计 120 超重）；
// 恰好够用情形把两件分别改为 50 千克（共同作用合计恰好 100）。

// amendOutcome 记录一次并发更正针对的货物、提交的新重量与返回错误。
type amendOutcome struct {
	cargoID   string
	newWeight int64
	err       error
}

// newConcurrentAmendRegistry 构造并发更正的固定初始配载：C1 承重 100 千克，
// G1、G2 各 30 千克、目的地上海、都允许混装，两件已装入 C1（已用 60 千克、
// 剩余 40 千克，清单为 [G1 G2]）。每轮并发都从一份全新配载独立开始。
func newConcurrentAmendRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCargo(t, r, "G1", 30, "上海", true)
	mustRegisterCargo(t, r, "G2", 30, "上海", true)
	if _, err := r.Adjust("load-g1-g2", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatalf("初始装载失败: %v", err)
	}
	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if cpt.MaxWeight != 100 || cpt.UsedWeight != 60 || cpt.RemainingWeight != 40 {
		t.Fatalf("初始配载应为 C1 已用 60/剩余 40，实际 %+v", cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("初始舱位清单应为 [G1 G2]，实际 %v", got)
	}
	return r
}

// runConcurrentAmendRound 在同一个登记处上让两次更正从同一道起跑线后并发
// 提交：分别把 G1、G2 改为 newWeight 千克，目的地（上海）与混装许可（允许）
// 保持原值。delayG1/delayG2 > 0 时对应更正开闸后先等待该时长再提交，用于
// 在多轮中覆盖两种生效先后；均为 0 时是纯粹的同时锁竞争。返回登记处与两次
// 更正各自的结果，由调用方按场景核对。
func runConcurrentAmendRound(t *testing.T, newWeight int64, delayG1, delayG2 time.Duration) (*Registry, []amendOutcome) {
	t.Helper()
	r := newConcurrentAmendRegistry(t)

	start := make(chan struct{})
	outcomes := make(chan amendOutcome, 2)
	var wg sync.WaitGroup
	submit := func(cargoID string, delay time.Duration) {
		defer wg.Done()
		<-start
		if delay > 0 {
			time.Sleep(delay)
		}
		err := r.AmendCargo(cargoID, newWeight, "上海", true)
		outcomes <- amendOutcome{cargoID: cargoID, newWeight: newWeight, err: err}
	}
	wg.Add(2)
	go submit("G1", delayG1)
	go submit("G2", delayG2)
	close(start)
	wg.Wait()
	close(outcomes)

	got := make([]amendOutcome, 0, 2)
	for o := range outcomes {
		got = append(got, o)
	}
	return r, got
}

// assertAmendOverweightRound 核对超重竞争轮：恰好一次成功、一次超重拒绝，
// 错误结构、最终舱位重量与两件货物资料全部符合要求。返回成功更正的货物
// 编号（G1 或 G2，业务允许任意一件先成功，不规定固定成功者）。
func assertAmendOverweightRound(t *testing.T, r *Registry, outcomes []amendOutcome) string {
	t.Helper()

	var winnerID string
	var loser *amendOutcome
	successes, failures := 0, 0
	for i := range outcomes {
		o := &outcomes[i]
		if o.err == nil {
			successes++
			winnerID = o.cargoID
		} else {
			failures++
			loser = o
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("两次并发更正应恰好一次成功、一次拒绝（不是整体成败的批量操作），"+
			"实际成功 %d 次、失败 %d 次，结果: %+v", successes, failures, outcomes)
	}
	if winnerID != "G1" && winnerID != "G2" {
		t.Fatalf("成功者编号异常: %q", winnerID)
	}
	loserID := loser.cargoID
	if loserID == winnerID || (loserID != "G1" && loserID != "G2") {
		t.Fatalf("失败者编号异常: %q（成功者 %q）", loserID, winnerID)
	}

	// 被拒绝的更正：现有 ErrOverweight 结构化错误，指向舱位 C1，说明给出
	// 预计总重量 120 与最大承重 100，不携带调整编号。
	var se *Error
	if !errors.As(loser.err, &se) {
		t.Fatalf("失败更正 %s 应返回 *Error，实际为 %T: %v", loserID, loser.err, loser.err)
	}
	if se.Kind != ErrOverweight {
		t.Fatalf("失败更正 %s 应按超重拒绝，实际为 %s（%v）", loserID, se.Kind, loser.err)
	}
	if se.ID != "C1" {
		t.Fatalf("超重错误应涉及舱位 C1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "" {
		t.Fatalf("更正不携带调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	msg := se.Error()
	for _, want := range []string{"C1", "120", "100"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("超重说明应指出舱位 C1、预计总重量 120 千克与最大承重 100 千克，实际: %s", msg)
		}
	}

	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询 C1 失败: %v", err)
	}
	// 最终配载：成功的一件 60、失败的一件保留 30，已用 90、剩余 10。
	if cpt.MaxWeight != 100 || cpt.UsedWeight != 90 || cpt.RemainingWeight != 10 {
		t.Fatalf("一次成功一次拒绝后 C1 应已用 90/剩余 10，实际 %+v（成功者 %s）", cpt, winnerID)
	}

	// 两件货物的重量与登记资料：成功者 60、失败者仍为 30，都继续装载于 C1，
	// 目的地与混装许可不变。
	wantWeight := map[string]int64{winnerID: 60, loserID: 30}
	views := make(map[string]*CargoView, 2)
	for _, id := range []string{"G1", "G2"} {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !cv.Loaded || cv.CompartmentID != "C1" {
			t.Fatalf("货物 %s 应继续显示已装载于 C1，实际 %+v（成功者 %s）", id, cv, winnerID)
		}
		if cv.Weight != wantWeight[id] {
			t.Fatalf("货物 %s 重量应为 %d（成功者 60、失败者保留 30），实际 %d（成功者 %s）",
				id, wantWeight[id], cv.Weight, winnerID)
		}
		if cv.Destination != "上海" || !cv.AllowMixed {
			t.Fatalf("货物 %s 的目的地与混装许可应保持原值（上海、允许混装），实际 %+v", id, cv)
		}
		views[id] = cv
	}

	// 舱位清单仍完整包含两件、各出现一次，且与货物单独查询逐项一致。
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("C1 清单应完整包含 G1、G2 各一次，实际 %v", got)
	}
	if len(cpt.Cargo) != 2 {
		t.Fatalf("C1 清单应恰好两件货物，实际 %d 件", len(cpt.Cargo))
	}
	for _, item := range cpt.Cargo {
		cv := views[item.ID]
		if !item.Loaded || item.CompartmentID != "C1" {
			t.Fatalf("清单中的 %s 应显示已装载于 C1，实际 %+v", item.ID, item)
		}
		if item.Weight != cv.Weight || item.Destination != cv.Destination ||
			item.AllowMixed != cv.AllowMixed {
			t.Fatalf("清单中 %s 的资料（%d/%s/%t）与货物查询（%d/%s/%t）不一致",
				item.ID, item.Weight, item.Destination, item.AllowMixed,
				cv.Weight, cv.Destination, cv.AllowMixed)
		}
	}
	return winnerID
}

// assertAmendExactCapacityRound 核对恰好够用的竞争轮：两次更正都成功，
// 最终已用 100、剩余 0，两件货物均为 50 千克且继续装载于 C1。
func assertAmendExactCapacityRound(t *testing.T, r *Registry, outcomes []amendOutcome) {
	t.Helper()
	if len(outcomes) != 2 {
		t.Fatalf("应有两次更正结果，实际 %d 个", len(outcomes))
	}
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("两件各改为 50 千克后合计恰好 100，%s 的更正不应被拒绝（不能因并发就拒绝）: %v",
				o.cargoID, o.err)
		}
		if o.newWeight != 50 {
			t.Fatalf("结果记录的新重量应为 50，实际 %d", o.newWeight)
		}
	}

	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询 C1 失败: %v", err)
	}
	if cpt.MaxWeight != 100 || cpt.UsedWeight != 100 || cpt.RemainingWeight != 0 {
		t.Fatalf("两次更正都成功后 C1 应已用 100/剩余 0，实际 %+v", cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("C1 清单应完整包含 G1、G2 各一次，实际 %v", got)
	}
	for _, id := range []string{"G1", "G2"} {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !cv.Loaded || cv.CompartmentID != "C1" || cv.Weight != 50 ||
			cv.Destination != "上海" || !cv.AllowMixed {
			t.Fatalf("货物 %s 应为 50 千克、上海、允许混装、仍在 C1，实际 %+v", id, cv)
		}
	}
	for _, item := range cpt.Cargo {
		cv, _ := r.Cargo(item.ID)
		if item.Weight != cv.Weight || item.Destination != cv.Destination ||
			item.AllowMixed != cv.AllowMixed || item.CompartmentID != cv.CompartmentID {
			t.Fatalf("清单中 %s 的资料与货物查询不一致: %+v vs %+v", item.ID, item, cv)
		}
	}
}

// TestConcurrentAmendSameCompartmentOnlyOneWeightGainTakesEffect 是核心并发
// 回归：G1、G2 并发地各自从 30 千克更正为 60 千克。任一更正单独作用于初始
// 配载都合法（合计 90），但两次共同生效会达到 120 千克，因此多轮并发中必须
// 始终恰好一次成功、另一次按超重拒绝，且两种生效先后都实际出现过（不规定
// 哪件成功），无论哪件成功，错误与最终配载都满足各自要求。
func TestConcurrentAmendSameCompartmentOnlyOneWeightGainTakesEffect(t *testing.T) {
	// 前置事实：任一更正单独作用于初始配载都合法，并发下的拒绝只能来自
	// 两次增重共同占用承重，而不是资料本身错误。
	t.Run("任一更正单独提交都合法", func(t *testing.T) {
		for _, id := range []string{"G1", "G2"} {
			r := newConcurrentAmendRegistry(t)
			if err := r.AmendCargo(id, 60, "上海", true); err != nil {
				t.Fatalf("%s 单独改为 60 千克本应合法（合计 90）: %v", id, err)
			}
			cv, _ := r.Cargo(id)
			if cv.Weight != 60 || cv.CompartmentID != "C1" {
				t.Fatalf("%s 单独更正后应为 60 千克且仍在 C1: %+v", id, cv)
			}
			other := map[string]string{"G1": "G2", "G2": "G1"}[id]
			ov, _ := r.Cargo(other)
			if ov.Weight != 30 {
				t.Fatalf("更正 %s 不应影响另一件 %s 的重量", id, other)
			}
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != 90 || cpt.RemainingWeight != 10 {
				t.Fatalf("%s 单独更正后 C1 应已用 90/剩余 10，实际 %+v", id, cpt)
			}
		}
	})

	// 并发竞争：多轮覆盖两种先后，并穿插不偏不倚的同时竞争。
	wins := map[string]int{"G1": 0, "G2": 0}
	const rounds = 40
	for i := 0; i < rounds; i++ {
		var delayG1, delayG2 time.Duration
		switch i % 4 {
		case 0:
			delayG2 = 2 * time.Millisecond // 偏向 G1 先更正
		case 1:
			delayG1 = 2 * time.Millisecond // 偏向 G2 先更正
		case 2:
			delayG2 = 5 * time.Millisecond // 再次偏向 G1，加大先后差
		default:
			// 两次更正零等待同时提交，纯粹的锁竞争。
		}
		r, outcomes := runConcurrentAmendRound(t, 60, delayG1, delayG2)
		wins[assertAmendOverweightRound(t, r, outcomes)]++
	}

	// 极端调度下若某种先后始终未出现，补几轮强偏向竞争；仍不出现才判定
	// 失败。两种生效先后都必须被实际观察到（不规定哪件成功，但任一件都
	// 应能在并发提交中正常生效）。
	for j := 0; j < 3 && wins["G1"] == 0; j++ {
		r, outcomes := runConcurrentAmendRound(t, 60, 0, 25*time.Millisecond)
		wins[assertAmendOverweightRound(t, r, outcomes)]++
	}
	for j := 0; j < 3 && wins["G2"] == 0; j++ {
		r, outcomes := runConcurrentAmendRound(t, 60, 25*time.Millisecond, 0)
		wins[assertAmendOverweightRound(t, r, outcomes)]++
	}
	if wins["G1"] == 0 || wins["G2"] == 0 {
		t.Fatalf("经过 %d 轮并发更正仍只观察到一种生效先后（G1 成功 %d 次、G2 成功 %d 次），"+
			"两种先后都应被接受", rounds, wins["G1"], wins["G2"])
	}
	t.Logf("并发更正 %d 轮：G1 成功 %d 次，G2 成功 %d 次（每次均为另一件超重拒绝）",
		rounds, wins["G1"], wins["G2"])
}

// TestConcurrentAmendExactCapacityBothWeightGainSucceed 覆盖与超重情形对应的
// 恰好够用情形：G1、G2 并发地各从 30 千克更正为 50 千克，共同生效后合计
// 恰好 100 千克等于承重，两次都必须成功，最终已用 100、剩余 0。先核对顺序
// 提交同样两次成功的基线，再在多轮并发（含两种先后与同时提交）中确认并发
// 本身不构成拒绝理由。
func TestConcurrentAmendExactCapacityBothWeightGainSucceed(t *testing.T) {
	// 顺序基线：先改 G1 再改 G2，两次都成功且最终恰好满舱。
	t.Run("顺序提交两次都成功", func(t *testing.T) {
		r := newConcurrentAmendRegistry(t)
		if err := r.AmendCargo("G1", 50, "上海", true); err != nil {
			t.Fatalf("G1 改为 50 千克应成功（合计 80）: %v", err)
		}
		if err := r.AmendCargo("G2", 50, "上海", true); err != nil {
			t.Fatalf("G2 改为 50 千克应成功（合计恰好 100）: %v", err)
		}
		cpt, _ := r.Compartment("C1")
		if cpt.UsedWeight != 100 || cpt.RemainingWeight != 0 {
			t.Fatalf("顺序两次更正后 C1 应已用 100/剩余 0，实际 %+v", cpt)
		}
	})

	// 并发：多轮覆盖两种先后与同时提交，每轮两次都必须成功。
	for i := 0; i < 30; i++ {
		var delayG1, delayG2 time.Duration
		switch i % 3 {
		case 0:
			delayG2 = 2 * time.Millisecond
		case 1:
			delayG1 = 2 * time.Millisecond
		default:
			// 零等待同时提交。
		}
		r, outcomes := runConcurrentAmendRound(t, 50, delayG1, delayG2)
		assertAmendExactCapacityRound(t, r, outcomes)
	}
}
