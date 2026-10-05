package stowage

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件为装载功能补充并发回归保障：同一件货物被两份各自合法的调整同时
// 安排到不同舱位、且两次正式提交使用不同调整编号时，登记处在并发调用下
// 仍必须保证只有一份安排整体生效——
//   - 恰好一次成功，另一份返回状态不符错误 ErrStateMismatch；
//   - 失败错误指向被抢先装载的货物 G1，携带失败调整自己的编号，中文说明
//     能让调用方知道 G1 已经装载在哪一个舱位，且失败不返回成功变化记录；
//   - 成功结果中的货物去向、舱位重量变化与最终实际配载完全一致；
//   - 整份调整一起生效：失败安排中的另一件货物不得留下已装载状态或重量；
//   - 货物查询给出的所属舱位与舱位清单一致，三件货物的登记资料保持原样。
//
// 两份安排单独提交都符合承重与混装规则，因此并发下的拒绝只能来自
// G1 已被另一份安排装载，而不是其他资料错误。首尾空白按现有编号规则
// 去掉后，“ G1 ”与“G1”仍指同一件货物，上述保障不变。

// contestPlan 描述一份竞争中的装载安排：把 G1 与另一件货物装入同一舱位。
type contestPlan struct {
	name        string // 安排名称，用于判定哪一份生效
	adjID       string // 提交时使用的调整编号（可能带首尾空白）
	g1Ref       string // 安排中 G1 的写法（可能带首尾空白）
	otherCargo  string // 同一份安排中的另一件货物
	winComp     string // 目标舱位，也是该安排生效后 G1 所在舱位
	totalWeight int64  // 该安排生效后目标舱位的已用重量
}

// contestOutcome 记录一份安排的提交结果。
type contestOutcome struct {
	plan contestPlan
	res  *AdjustmentResult
	err  error
}

// 两份安排：A 把 G1、G2 装入 C1（50 千克）；B 把 G1、G3 装入 C2
// （70 千克）。三件货物同目的地且 C1、C2 承重均为 100 千克，每份安排
// 单独看都满足承重与混装规则。
func contestPlans(g1RefA, g1RefB, idA, idB string) (contestPlan, contestPlan) {
	planA := contestPlan{
		name:        "A",
		adjID:       idA,
		g1Ref:       g1RefA,
		otherCargo:  "G2",
		winComp:     "C1",
		totalWeight: 50,
	}
	planB := contestPlan{
		name:        "B",
		adjID:       idB,
		g1Ref:       g1RefB,
		otherCargo:  "G3",
		winComp:     "C2",
		totalWeight: 70,
	}
	return planA, planB
}

// newConcurrentContestRegistry 登记两个空舱位 C1、C2（承重均 100 千克）
// 与三件同目的地、均不允许混装的未装载货物 G1(30)、G2(20)、G3(40)。
func newConcurrentContestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCargo(t, r, "G1", 30, "东港", false)
	mustRegisterCargo(t, r, "G2", 20, "东港", false)
	mustRegisterCargo(t, r, "G3", 40, "东港", false)
	return r
}

// contestOps 按安排中各编号的原始写法（可能含首尾空白）构造装载操作。
func contestOps(p contestPlan) []Op {
	return []Op{
		{Kind: OpLoad, CargoID: p.otherCargo, Target: p.winComp},
		{Kind: OpLoad, CargoID: p.g1Ref, Target: " " + p.winComp + " "},
	}
}

// runContestRound 在同一个登记处上让两份安排在同一道起跑线后并发提交。
// delayA/delayB > 0 时，对应安排在开闸后先等待该时长再提交，用于在多轮
// 中覆盖两种生效先后；两者均为 0 时是纯粹的同时竞争。返回生效安排名称。
func runContestRound(t *testing.T, planA, planB contestPlan, delayA, delayB time.Duration) string {
	t.Helper()
	r := newConcurrentContestRegistry(t)

	start := make(chan struct{})
	outcomes := make(chan contestOutcome, 2)
	var wg sync.WaitGroup
	submit := func(p contestPlan, delay time.Duration) {
		defer wg.Done()
		<-start
		if delay > 0 {
			time.Sleep(delay)
		}
		res, err := r.Adjust(p.adjID, contestOps(p))
		outcomes <- contestOutcome{plan: p, res: res, err: err}
	}
	wg.Add(2)
	go submit(planA, delayA)
	go submit(planB, delayB)
	close(start)
	wg.Wait()
	close(outcomes)

	var winner, loser *contestOutcome
	for o := range outcomes {
		o := o
		if o.err == nil {
			if winner != nil {
				t.Fatalf("两份安排都成功：%s 与 %s 都返回了成功结果，G1 被重复装载",
					winner.plan.name, o.plan.name)
			}
			winner = &o
		} else {
			if loser != nil {
				t.Fatalf("两份安排都失败：%s 返回 %v，%s 返回 %v，应有恰好一次成功",
					loser.plan.name, loser.err, o.plan.name, o.err)
			}
			loser = &o
		}
	}
	if winner == nil || loser == nil {
		t.Fatalf("并发提交结束后成功/失败数量异常: winner=%v loser=%v", winner, loser)
	}

	assertLoserStateMismatch(t, *loser, winner.plan)
	assertWinnerResult(t, *winner)
	assertContestFinalStowage(t, r, winner.plan, loser.plan)
	assertContestRegistrationsUnchanged(t, r)
	return winner.plan.name
}

// assertLoserStateMismatch 核对失败安排：错误是携带调整编号的 *Error，
// 原因为状态不符、对象为 G1、编号属于失败安排自己，中文说明指出 G1 已在
// 胜出安排的舱位中，且失败不返回成功变化记录。
func assertLoserStateMismatch(t *testing.T, loser contestOutcome, winning contestPlan) {
	t.Helper()
	if loser.res != nil {
		t.Fatalf("失败安排 %s 不应返回成功变化记录: %+v", loser.plan.name, loser.res)
	}
	var se *Error
	if !errors.As(loser.err, &se) {
		t.Fatalf("安排 %s 应返回 *Error，实际为 %T: %v", loser.plan.name, loser.err, loser.err)
	}
	if se.Kind != ErrStateMismatch {
		t.Fatalf("安排 %s 应因 G1 已被另一份安排装载而报状态不符，实际为 %s（%v）",
			loser.plan.name, se.Kind, loser.err)
	}
	if se.ID != "G1" {
		t.Fatalf("失败错误应指向 G1，实际 ID=%q", se.ID)
	}
	wantAID := strings.TrimSpace(loser.plan.adjID)
	if se.AdjustmentID != wantAID {
		t.Fatalf("失败错误应携带失败安排自己的编号 %q，实际 %q", wantAID, se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "G1") {
		t.Fatalf("失败说明应指出货物 G1: %s", msg)
	}
	if !strings.Contains(msg, winning.winComp) {
		t.Fatalf("失败说明应让调用方知道 G1 已装载在舱位 %s: %s", winning.winComp, msg)
	}
	if !strings.Contains(msg, "已装载") {
		t.Fatalf("失败说明应表明 G1 已经装载: %s", msg)
	}
}

// assertWinnerResult 核对成功结果中的货物去向与舱位重量变化与该安排一致。
func assertWinnerResult(t *testing.T, winner contestOutcome) {
	t.Helper()
	p := winner.plan
	if winner.res == nil {
		t.Fatalf("生效安排 %s 应返回成功结果", p.name)
	}
	if winner.res.ID != strings.TrimSpace(p.adjID) {
		t.Fatalf("成功结果编号应为去空白后的 %q，实际 %q",
			strings.TrimSpace(p.adjID), winner.res.ID)
	}
	wantCargoChanges := []CargoChange{
		{CargoID: "G1", From: "", To: p.winComp},
		{CargoID: p.otherCargo, From: "", To: p.winComp},
	}
	if !reflect.DeepEqual(winner.res.CargoChanges, wantCargoChanges) {
		t.Fatalf("安排 %s 成功的货物变化应为 %+v，实际 %+v",
			p.name, wantCargoChanges, winner.res.CargoChanges)
	}
	wantCompChanges := []CompartmentChange{
		{CompartmentID: p.winComp, WeightBefore: 0, WeightAfter: p.totalWeight},
	}
	if !reflect.DeepEqual(winner.res.CompartmentChanges, wantCompChanges) {
		t.Fatalf("安排 %s 成功的舱位重量变化应为 %+v，实际 %+v",
			p.name, wantCompChanges, winner.res.CompartmentChanges)
	}
}

// assertContestFinalStowage 按实际生效的安排核对最终配载：胜出舱位只有
// G1 与该安排的另一件货物、重量正确；另一份安排的目标舱位仍为空；
// 货物查询与舱位清单逐一对应；失败安排中的另一件货物保持未装载、不占重量。
func assertContestFinalStowage(t *testing.T, r *Registry, winning, losing contestPlan) {
	t.Helper()
	winView, err := r.Compartment(winning.winComp)
	if err != nil {
		t.Fatalf("查询胜出舱位 %s 失败: %v", winning.winComp, err)
	}
	wantIDs := []string{"G1", winning.otherCargo}
	if got := cargoIDs(*winView); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("安排 %s 生效后 %s 应只有 %v，实际 %v",
			winning.name, winning.winComp, wantIDs, got)
	}
	if winView.MaxWeight != 100 ||
		winView.UsedWeight != winning.totalWeight ||
		winView.RemainingWeight != 100-winning.totalWeight {
		t.Fatalf("安排 %s 生效后 %s 重量应为已用 %d/剩余 %d，实际 %+v",
			winning.name, winning.winComp,
			winning.totalWeight, 100-winning.totalWeight, winView)
	}

	// 失败安排的目标舱位仍为空：其另一件货物不得留下已装载状态或占用重量。
	loseView, err := r.Compartment(losing.winComp)
	if err != nil {
		t.Fatalf("查询失败安排目标舱位 %s 失败: %v", losing.winComp, err)
	}
	if loseView.MaxWeight != 100 || loseView.UsedWeight != 0 ||
		loseView.RemainingWeight != 100 || len(loseView.Cargo) != 0 {
		t.Fatalf("安排 %s 失败后 %s 应仍为空舱，实际 %+v",
			losing.name, losing.winComp, loseView)
	}

	// 货物查询给出的所属舱位必须与舱位清单一致。
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != winning.winComp {
		t.Fatalf("G1 应由生效安排 %s 装入 %s，实际 %+v",
			winning.name, winning.winComp, g1)
	}
	other, _ := r.Cargo(winning.otherCargo)
	if !other.Loaded || other.CompartmentID != winning.winComp {
		t.Fatalf("%s 应随生效安排在 %s，实际 %+v",
			winning.otherCargo, winning.winComp, other)
	}
	leftOut, _ := r.Cargo(losing.otherCargo)
	if leftOut.Loaded || leftOut.CompartmentID != "" {
		t.Fatalf("失败安排中的 %s 应仍未装载、不占重量，实际 %+v",
			losing.otherCargo, leftOut)
	}
}

// assertContestRegistrationsUnchanged 确认三件货物的登记资料（重量、
// 目的地、混装许可）在并发提交后保持原样。
func assertContestRegistrationsUnchanged(t *testing.T, r *Registry) {
	t.Helper()
	want := map[string]struct {
		weight int64
		dest   string
	}{
		"G1": {30, "东港"},
		"G2": {20, "东港"},
		"G3": {40, "东港"},
	}
	for id, w := range want {
		c, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if c.Weight != w.weight || c.Destination != w.dest || c.AllowMixed {
			t.Fatalf("货物 %s 的登记资料被改动: %+v（应保持 %d 千克、%s、不允许混装）",
				id, c, w.weight, w.dest)
		}
	}
}

// TestConcurrentAdjustSameCargoOnlyOneCommitTakesEffect 是核心并发回归：
// 两份各自合法、编号不同的安排并发抢装同一件 G1，多轮竞争中必须恰好有
// 一份生效，且两种生效先后都实际出现过（不规定哪份成功），无论哪份成功，
// 成功结果、失败错误与最终配载都满足各自的要求。
func TestConcurrentAdjustSameCargoOnlyOneCommitTakesEffect(t *testing.T) {
	cases := []struct {
		name     string
		g1RefA   string
		g1RefB   string
		adjIDInA string
		adjIDInB string
	}{
		{
			name:     "两份安排都写作G1",
			g1RefA:   "G1",
			g1RefB:   "G1",
			adjIDInA: "adj-to-c1",
			adjIDInB: "adj-to-c2",
		},
		{
			name:     "一份写作带首尾空白的G1",
			g1RefA:   " G1 ",
			g1RefB:   "G1",
			adjIDInA: "  adj-to-c1  ",
			adjIDInB: " adj-to-c2 ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planA, planB := contestPlans(tc.g1RefA, tc.g1RefB, tc.adjIDInA, tc.adjIDInB)

			// 前置事实：两份安排各自单独提交都合法，并发下的拒绝只能来自
			// G1 已被另一份安排装载，而不是承重、混装或编号等资料错误。
			t.Run("每份安排单独提交都合法", func(t *testing.T) {
				rA := newConcurrentContestRegistry(t)
				resA, err := rA.Adjust(planA.adjID, contestOps(planA))
				if err != nil {
					t.Fatalf("安排 A 单独提交本应合法: %v", err)
				}
				assertWinnerResult(t, contestOutcome{plan: planA, res: resA})
				assertContestFinalStowage(t, rA, planA, planB)
				assertContestRegistrationsUnchanged(t, rA)

				rB := newConcurrentContestRegistry(t)
				resB, err := rB.Adjust(planB.adjID, contestOps(planB))
				if err != nil {
					t.Fatalf("安排 B 单独提交本应合法: %v", err)
				}
				assertWinnerResult(t, contestOutcome{plan: planB, res: resB})
				assertContestFinalStowage(t, rB, planB, planA)
				assertContestRegistrationsUnchanged(t, rB)
			})

			// 并发竞争：多轮覆盖两种先后，并穿插不偏不倚的同时竞争。
			wins := map[string]int{"A": 0, "B": 0}
			const rounds = 40
			for i := 0; i < rounds; i++ {
				var delayA, delayB time.Duration
				switch i % 4 {
				case 0:
					delayB = 2 * time.Millisecond // 偏向安排 A 先提交
				case 1:
					delayA = 2 * time.Millisecond // 偏向安排 B 先提交
				case 2:
					delayB = 5 * time.Millisecond // 再次偏向 A，加大先后差
				default:
					// 两份安排零等待同时开抢，纯粹的锁竞争。
				}
				wins[runContestRound(t, planA, planB, delayA, delayB)]++
			}

			// 极端调度下若某种先后始终未出现，补几轮强偏向竞争；仍不出现
			// 才判定失败。两种生效先后都必须被实际观察到（不规定哪份成功，
			// 但任一份都应能在并发提交中正常生效）。
			forceMissing := func(missing string, planMissing, planOther contestPlan) {
				for j := 0; j < 3 && wins[missing] == 0; j++ {
					if missing == "A" {
						wins[runContestRound(t, planMissing, planOther, 0, 25*time.Millisecond)]++
					} else {
						wins[runContestRound(t, planMissing, planOther, 25*time.Millisecond, 0)]++
					}
				}
			}
			if wins["A"] == 0 {
				forceMissing("A", planA, planB)
			}
			if wins["B"] == 0 {
				forceMissing("B", planA, planB)
			}
			if wins["A"] == 0 || wins["B"] == 0 {
				t.Fatalf("经过 %d 轮并发提交仍只观察到一种生效先后（A 胜 %d 次、B 胜 %d 次），"+
					"两种先后都应被接受", rounds, wins["A"], wins["B"])
			}
			t.Logf("并发竞争 %d 轮：安排 A 生效 %d 次，安排 B 生效 %d 次",
				rounds, wins["A"], wins["B"])
		})
	}
}
