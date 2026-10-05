package stowage

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// 本文件为并发正式提交（Adjust）补充回归保障：同一件货物被两份各自合法
// 的安排同时争抢时，登记处的互斥保证只有一份安排生效，另一份以现有的
// 状态不符错误 ErrStateMismatch 被拒绝，且整份调整一起生效——失败方
// 不留下任何已装载状态或重量占用，成功方的货物去向与舱位重量变化与
// 实际生效的安排一致。货物编号按去掉首尾空白后的值识别，" G1 " 与
// "G1" 指同一件货物，并发争抢的结论不变。

// ---------- 测试辅助 ----------

// adjustOutcome 记录一次并发提交的结局：成功时 res 非空、err 为空；
// 失败时 res 为空、err 为原始错误（断言时核对为结构化 *Error）。
type adjustOutcome struct {
	res *AdjustmentResult
	err error
}

// newContentionRegistry 登记两个承重均为 100 千克的空舱位 C1、C2，以及
// 三件目的地相同、均不允许混装的未装载货物：G1 30 千克、G2 20 千克、
// G3 40 千克。同目的地共舱不受混装限制，因此 {G1,G2}->C1 与 {G1,G3}->C2
// 各自单独提交都合法，唯一的冲突点是两份安排都要装载 G1。
func newContentionRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCargo(t, r, "G1", 30, "上海", false)
	mustRegisterCargo(t, r, "G2", 20, "上海", false)
	mustRegisterCargo(t, r, "G3", 40, "上海", false)
	return r
}

// submitConcurrently 用同一道闸门并发提交两份调整，返回各自的结局。
// 两份调整编号不同且各自有效。
func submitConcurrently(r *Registry, idA string, opsA []Op, idB string, opsB []Op) (adjustOutcome, adjustOutcome) {
	var outA, outB adjustOutcome
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		outA.res, outA.err = r.Adjust(idA, opsA)
	}()
	go func() {
		defer wg.Done()
		<-start
		outB.res, outB.err = r.Adjust(idB, opsB)
	}()
	close(start)
	wg.Wait()
	return outA, outB
}

// ---------- 并发争抢同一件货物 ----------

// 两份各自合法的安排并发争抢 G1：必须恰好一份成功、一份以 ErrStateMismatch
// 被拒；成功结果与最终配载必须与实际生效的那份安排一致，失败方不留下
// 任何装载状态或重量占用。
func TestConcurrentAdjustSameCargoOnlyOneSucceeds(t *testing.T) {
	r := newContentionRegistry(t)

	// 安排甲：G1、G2 装入 C1（合计 50 千克，合法）。
	opsA := []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}
	// 安排乙：G1、G3 装入 C2（合计 70 千克，合法）。
	opsB := []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	}
	outA, outB := submitConcurrently(r, "adj-a", opsA, "adj-b", opsB)

	assertContentionOutcome(t, r, "adj-a", outA, "adj-b", outB)
}

// 两份安排中同一件货物分别写作 " G1 " 与 "G1"：首尾空白沿用现有编号
// 规则去掉后仍指同一件货物，成功数量、失败原因与最终配载要求不变。
func TestConcurrentAdjustSameCargoWithWhitespaceID(t *testing.T) {
	r := newContentionRegistry(t)

	// 安排甲把 " G1 "（去空白后为 G1）与 G2 装入 C1。
	opsA := []Op{
		{Kind: OpLoad, CargoID: " G1 ", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}
	// 安排乙把 G1 与 G3 装入 C2。
	opsB := []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	}
	outA, outB := submitConcurrently(r, "adj-ws-a", opsA, "adj-ws-b", opsB)

	assertContentionOutcome(t, r, "adj-ws-a", outA, "adj-ws-b", outB)
}

// assertContentionOutcome 核对并发争抢 G1 的完整结局，不规定哪份安排
// 成功：恰好一份成功且其变化记录与生效安排一致；另一份以指向 G1 的
// ErrStateMismatch 被拒，说明指出 G1 已装载的舱位；最终配载是生效安排
// 的整体结果，失败方的另一件货物不留下已装载状态或重量占用；三件货物
// 的登记资料保持原样。
func assertContentionOutcome(t *testing.T, r *Registry, idA string, outA adjustOutcome, idB string, outB adjustOutcome) {
	t.Helper()

	// 恰好一次成功：两份各自合法的安排并发提交，不能两份都生效（G1 会
	// 被装两次），也不能两份都被拒（先获得互斥锁的一份看到的是未装载
	// 的 G1，单独看完全合法）。
	aOK, bOK := outA.err == nil, outB.err == nil
	if aOK == bOK {
		t.Fatalf("两份安排应恰好一份成功，实际 甲(%s)成功=%v 乙(%s)成功=%v", idA, aOK, idB, bOK)
	}

	// 生效安排决定的期望终态：甲成功则 G1、G2 在 C1（已用 50、剩余 50），
	// C2 为空、G3 未装载；乙成功则 G1、G3 在 C2（已用 70、剩余 30），
	// C1 为空、G2 未装载。
	var winID, loseID string
	var winOut, loseOut adjustOutcome
	var winComp string         // G1 实际装载的舱位
	var winCargo []CargoChange // 生效安排应有的货物变化
	var winCompChange []CompartmentChange
	var wantLoaded map[string]string // 货物 -> 应属舱位（仅已装载的）
	var wantUnloaded []string        // 应保持未装载的货物
	var wantCompUsed map[string]int64
	if aOK {
		winID, loseID, winOut, loseOut = idA, idB, outA, outB
		winComp = "C1"
		winCargo = []CargoChange{
			{CargoID: "G1", From: "", To: "C1"},
			{CargoID: "G2", From: "", To: "C1"},
		}
		winCompChange = []CompartmentChange{{CompartmentID: "C1", WeightBefore: 0, WeightAfter: 50}}
		wantLoaded = map[string]string{"G1": "C1", "G2": "C1"}
		wantUnloaded = []string{"G3"}
		wantCompUsed = map[string]int64{"C1": 50, "C2": 0}
	} else {
		winID, loseID, winOut, loseOut = idB, idA, outB, outA
		winComp = "C2"
		winCargo = []CargoChange{
			{CargoID: "G1", From: "", To: "C2"},
			{CargoID: "G3", From: "", To: "C2"},
		}
		winCompChange = []CompartmentChange{{CompartmentID: "C2", WeightBefore: 0, WeightAfter: 70}}
		wantLoaded = map[string]string{"G1": "C2", "G3": "C2"}
		wantUnloaded = []string{"G2"}
		wantCompUsed = map[string]int64{"C1": 0, "C2": 70}
	}

	// 成功结果：编号、货物去向与舱位重量变化必须与实际生效的安排一致。
	if winOut.res == nil {
		t.Fatalf("成功的调整 %s 应返回结果", winID)
	}
	if winOut.res.ID != winID {
		t.Fatalf("成功结果编号应为 %s，实际 %q", winID, winOut.res.ID)
	}
	if !reflect.DeepEqual(winOut.res.CargoChanges, winCargo) {
		t.Fatalf("成功结果的货物变化应为 %+v，实际 %+v", winCargo, winOut.res.CargoChanges)
	}
	if !reflect.DeepEqual(winOut.res.CompartmentChanges, winCompChange) {
		t.Fatalf("成功结果的舱位重量变化应为 %+v，实际 %+v", winCompChange, winOut.res.CompartmentChanges)
	}

	// 失败结果：不返回成功的变化记录；错误为状态不符，指向 G1，携带失败
	// 调整自己的编号，中文说明指出 G1 已装载在哪个舱位。
	if loseOut.res != nil {
		t.Fatalf("失败的调整 %s 不应返回成功结果: %+v", loseID, loseOut.res)
	}
	if loseOut.err == nil {
		t.Fatalf("失败的调整 %s 应返回错误", loseID)
	}
	var se *Error
	if !errors.As(loseOut.err, &se) {
		t.Fatalf("失败错误应为 *Error，实际为 %T: %v", loseOut.err, loseOut.err)
	}
	if se.Kind != ErrStateMismatch {
		t.Fatalf("失败原因应为状态不符 ErrStateMismatch，实际 %s: %v", se.Kind, se)
	}
	if se.ID != "G1" {
		t.Fatalf("失败错误应指向 G1，实际指向 %q", se.ID)
	}
	if se.AdjustmentID != loseID {
		t.Fatalf("失败错误应携带调整自己的编号 %s，实际 %q", loseID, se.AdjustmentID)
	}
	if !strings.Contains(se.Error(), "G1") || !strings.Contains(se.Error(), winComp) {
		t.Fatalf("失败说明应指出 G1 已装载于舱位 %s: %s", winComp, se.Error())
	}

	// 最终配载：成功舱位只有生效安排的货物，已用与剩余重量符合预期；
	// 另一舱位仍为空舱。
	for _, compID := range []string{"C1", "C2"} {
		cpt, err := r.Compartment(compID)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", compID, err)
		}
		used := wantCompUsed[compID]
		if cpt.UsedWeight != used || cpt.RemainingWeight != 100-used {
			t.Fatalf("舱位 %s 已用应为 %d、剩余应为 %d，实际 %+v", compID, used, 100-used, cpt)
		}
		wantIDs := make([]string, 0, 2)
		for cid, comp := range wantLoaded {
			if comp == compID {
				wantIDs = append(wantIDs, cid)
			}
		}
		if len(wantIDs) == 2 && wantIDs[0] > wantIDs[1] {
			wantIDs[0], wantIDs[1] = wantIDs[1], wantIDs[0]
		}
		if gotIDs := cargoIDs(*cpt); !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("舱位 %s 清单应为 %v，实际 %v", compID, wantIDs, gotIDs)
		}
	}

	// 货物查询给出的所属舱位必须与舱位清单一致；失败安排中的另一件
	// 货物不能留下已装载状态。
	for cid, comp := range wantLoaded {
		c, err := r.Cargo(cid)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", cid, err)
		}
		if !c.Loaded || c.CompartmentID != comp {
			t.Fatalf("货物 %s 应已装载于 %s，实际 %+v", cid, comp, c)
		}
	}
	for _, cid := range wantUnloaded {
		c, err := r.Cargo(cid)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", cid, err)
		}
		if c.Loaded || c.CompartmentID != "" {
			t.Fatalf("货物 %s 应仍未装载，实际 %+v", cid, c)
		}
	}

	// 三件货物的登记资料（重量、目的地、混装许可）保持原样。
	wantProfile := map[string]struct {
		weight int64
		dest   string
		mixed  bool
	}{
		"G1": {30, "上海", false},
		"G2": {20, "上海", false},
		"G3": {40, "上海", false},
	}
	for cid, want := range wantProfile {
		c, err := r.Cargo(cid)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", cid, err)
		}
		if c.Weight != want.weight || c.Destination != want.dest || c.AllowMixed != want.mixed {
			t.Fatalf("货物 %s 登记资料应保持 %+v，实际 %+v", cid, want, c)
		}
	}
}
