package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为“不同目的地货物整舱互换”补充回归保障：合法性按整次调整完成
// 后的最终配载判断，因此两个满载舱位整体交换货物（各自装着不同目的地、
// 且全部不允许混装的货物）应作为一个整体合法生效，不能因中间暂时超重或
// 暂时出现不同目的地共舱而被拒绝；同一场景下只交换其中一件货物则最终
// 配载仍存在混装冲突，整次调整必须失败且不留任何中间状态。

// ---------- 测试辅助 ----------

// newCrossDestinationSwapRegistry 构造整舱互换场景：两个承重均为 100 千克
// 的舱位，C1 装有去上海的 G1（40 千克）、G2（60 千克），C2 装有去北京的
// G3（40 千克）、G4（60 千克），四件货物均不允许混装。初始配载每舱均为
// 同一目的地且恰好满载，本身合法。
func newCrossDestinationSwapRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCargo(t, r, "G1", 40, "上海", false)
	mustRegisterCargo(t, r, "G2", 60, "上海", false)
	mustRegisterCargo(t, r, "G3", 40, "北京", false)
	mustRegisterCargo(t, r, "G4", 60, "北京", false)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
	})
	return r
}

// wholeSwapOps 给出整舱互换的操作清单：G1、G2 移到 C2，G3、G4 移到 C1。
func wholeSwapOps() []Op {
	return []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C2"},
		{Kind: OpMove, CargoID: "G3", Target: "C1"},
		{Kind: OpMove, CargoID: "G4", Target: "C1"},
	}
}

// wantSwapCargoChanges 是整舱互换成功后按货物编号排列的归属变化。
var wantSwapCargoChanges = []CargoChange{
	{CargoID: "G1", From: "C1", To: "C2"},
	{CargoID: "G2", From: "C1", To: "C2"},
	{CargoID: "G3", From: "C2", To: "C1"},
	{CargoID: "G4", From: "C2", To: "C1"},
}

// wantSwapCompartmentChanges 是整舱互换成功后的舱位重量变化：两个舱位
// 都是 100 -> 100 千克，重量相等也不能省略。
var wantSwapCompartmentChanges = []CompartmentChange{
	{CompartmentID: "C1", WeightBefore: 100, WeightAfter: 100},
	{CompartmentID: "C2", WeightBefore: 100, WeightAfter: 100},
}

// assertSwappedStowage 核对整舱互换完成后的最终配载：C1 只含 G3、G4，
// C2 只含 G1、G2，两舱已用重量仍为 100 千克、剩余为 0；每件货物的归属
// 与舱内清单一致（不遗漏、不同时留在原舱和目标舱），重量、目的地与
// 混装许可保持登记原值。
func assertSwappedStowage(t *testing.T, r *Registry) {
	t.Helper()

	wantCargo := map[string]CargoView{
		"G1": {ID: "G1", Weight: 40, Destination: "上海", AllowMixed: false, Loaded: true, CompartmentID: "C2"},
		"G2": {ID: "G2", Weight: 60, Destination: "上海", AllowMixed: false, Loaded: true, CompartmentID: "C2"},
		"G3": {ID: "G3", Weight: 40, Destination: "北京", AllowMixed: false, Loaded: true, CompartmentID: "C1"},
		"G4": {ID: "G4", Weight: 60, Destination: "北京", AllowMixed: false, Loaded: true, CompartmentID: "C1"},
	}
	for id, want := range wantCargo {
		got, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if *got != want {
			t.Fatalf("货物 %s 互换后状态异常: 期望 %+v, 实际 %+v", id, want, *got)
		}
	}

	wantLists := map[string][]string{"C1": {"G3", "G4"}, "C2": {"G1", "G2"}}
	for id, wantIDs := range wantLists {
		cpt, err := r.Compartment(id)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", id, err)
		}
		if cpt.UsedWeight != 100 || cpt.RemainingWeight != 0 {
			t.Fatalf("舱位 %s 互换后应已用 100、剩余 0，实际 %+v", id, cpt)
		}
		if !reflect.DeepEqual(cargoIDs(*cpt), wantIDs) {
			t.Fatalf("舱位 %s 互换后清单应为 %v，实际 %v", id, wantIDs, cargoIDs(*cpt))
		}
		// 舱内每件货物的归属必须指向该舱位，不能同时留在原舱。
		for _, cv := range cpt.Cargo {
			if cv.CompartmentID != id || !cv.Loaded {
				t.Fatalf("舱位 %s 清单中的货物 %s 归属不一致: %+v", id, cv.ID, cv)
			}
		}
	}
}

// ---------- 整舱互换成功 ----------

// 两舱分别装着不同目的地、且全部不允许混装的货物时，整舱互换在同一次
// 正式调整中作为一个整体生效：交换后每舱仍是同一目的地且恰好满载，
// 中间过程的暂时超重或不同目的地共舱不构成拒绝理由。
func TestAdjustSwapWholeCompartmentsAcrossDestinations(t *testing.T) {
	r := newCrossDestinationSwapRegistry(t)

	res, err := r.Adjust("swap-cross", wholeSwapOps())
	if err != nil {
		t.Fatalf("不同目的地整舱互换应成功: %v", err)
	}
	if res == nil || res.ID != "swap-cross" {
		t.Fatalf("成功结果应携带调整编号 swap-cross: %+v", res)
	}
	// 四件货物各自的原舱位与新舱位都保留在结果中，按货物编号排列。
	if !reflect.DeepEqual(res.CargoChanges, wantSwapCargoChanges) {
		t.Fatalf("货物变化记录异常: 期望 %+v, 实际 %+v", wantSwapCargoChanges, res.CargoChanges)
	}
	// 两个舱位都有 100 -> 100 的重量变化记录，不因重量相等而省略。
	if !reflect.DeepEqual(res.CompartmentChanges, wantSwapCompartmentChanges) {
		t.Fatalf("舱位变化记录异常: 期望 %+v, 实际 %+v", wantSwapCompartmentChanges, res.CompartmentChanges)
	}
	assertSwappedStowage(t, r)
}

// 交换操作在清单中的先后次序不影响能否成功、最终配载与返回的变化记录。
func TestAdjustSwapWholeCompartmentsOpOrderIrrelevant(t *testing.T) {
	orderings := map[string][]Op{
		// 先移出 C1 的货物，再移出 C2 的货物。
		"C1先": wholeSwapOps(),
		// 先移出 C2 的货物，再移出 C1 的货物。
		"C2先": {
			{Kind: OpMove, CargoID: "G3", Target: "C1"},
			{Kind: OpMove, CargoID: "G4", Target: "C1"},
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
			{Kind: OpMove, CargoID: "G2", Target: "C2"},
		},
		// 两舱的货物交错出现。
		"交错": {
			{Kind: OpMove, CargoID: "G4", Target: "C1"},
			{Kind: OpMove, CargoID: "G2", Target: "C2"},
			{Kind: OpMove, CargoID: "G3", Target: "C1"},
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		},
	}

	var wantResult *AdjustmentResult
	for name, ops := range orderings {
		r := newCrossDestinationSwapRegistry(t)
		res, err := r.Adjust("swap-cross", ops)
		if err != nil {
			t.Fatalf("操作次序 %s 应成功: %v", name, err)
		}
		if !reflect.DeepEqual(res.CargoChanges, wantSwapCargoChanges) ||
			!reflect.DeepEqual(res.CompartmentChanges, wantSwapCompartmentChanges) {
			t.Fatalf("操作次序 %s 的变化记录异常: %+v", name, res)
		}
		if wantResult == nil {
			wantResult = res
		} else if !reflect.DeepEqual(res, wantResult) {
			t.Fatalf("操作次序 %s 的结果应与其他次序一致: %+v vs %+v", name, res, wantResult)
		}
		assertSwappedStowage(t, r)
	}
}

// ---------- 部分互换被拒绝 ----------

// 同一场景下只交换 G1 与 G3：两舱最终各自仍有上海和北京的货物，总重量
// 没有超限（各 100 千克），但货物均不允许混装，整次调整必须失败。错误
// 报告字典序最靠前的违规舱位 C1 的混装冲突，并指出其中编号最靠前的不
// 允许混装货物 G2；失败后四件货物仍在各自原舱，不能先完成其中一次移动。
func TestAdjustPartialSwapAcrossDestinationsRejected(t *testing.T) {
	r := newCrossDestinationSwapRegistry(t)

	compBefore := snapshotCompartments(r, "C1", "C2")
	cargoBefore := snapshotCargo(r, "G1", "G2", "G3", "G4")

	se := submitExpectReject(t, r, "swap-partial", []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G3", Target: "C1"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G2" {
		t.Fatalf("应报告 C1 的混装冲突并指出 G2，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if !strings.Contains(se.Error(), "C1") {
		t.Fatalf("混装说明应指出冲突舱位 C1: %s", se.Error())
	}
	if se.AdjustmentID != "swap-partial" {
		t.Fatalf("错误应携带调整编号 swap-partial，实际 %q", se.AdjustmentID)
	}

	// 整批原子性：四件货物仍在各自原舱，两个舱位的完整清单、已用与
	// 剩余重量保持原样，G1->C2 这一次移动不能单独先生效。
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
}
