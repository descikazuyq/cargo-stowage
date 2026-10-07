package stowage

import (
	"reflect"
	"testing"
)

// setupPendingLoadScenario 构造题目场景：舱位 C1 承重 60 千克，舱内已有
// 30 千克、目的地上海且不允许混装的 G1；G2 已登记但未装载，40 千克、
// 目的地北京且允许混装。返回的 ops 是把 G2 装入 C1 的原装载安排。
func setupPendingLoadScenario(t *testing.T) (*Registry, []Op) {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 60); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 40, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A0", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	return r, []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}}
}

// checkOriginalPreview 校验更正前保存的原预览仍保留当时内容：预计总重量
// 70 千克、剩余 -10 千克，先列超重 10 千克再列混装冲突，混装按目的地
// 分别列出 G1 与 G2，且不允许混装的是原本在舱内的 G1。
func checkOriginalPreview(t *testing.T, p *PreviewResult) {
	t.Helper()
	if p.Submittable {
		t.Fatal("原预览应不可提交")
	}
	if len(p.Compartments) != 1 || p.Compartments[0].ID != "C1" {
		t.Fatalf("原预览应只涉及 C1: %+v", p.Compartments)
	}
	after := p.Compartments[0].After
	if after.UsedWeight != 70 || after.RemainingWeight != -10 {
		t.Fatalf("原预览应保留预计 70 千克、剩余 -10 千克: %+v", after)
	}
	if got := cargoIDs(after); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("原预览预计清单应含 G1、G2: %v", got)
	}
	// 原预览中 G2 仍是更正前的资料：40 千克、北京、允许混装。
	g2 := after.Cargo[1]
	if g2.ID != "G2" || g2.Weight != 40 || g2.Destination != "Beijing" || !g2.AllowMixed {
		t.Fatalf("原预览中 G2 应保留更正前资料: %+v", g2)
	}
	if len(p.Rejections) != 2 {
		t.Fatalf("原预览应保留两项拒绝原因: %+v", p.Rejections)
	}
	over := p.Rejections[0]
	if over.Kind != ErrOverweight || over.CompartmentID != "C1" ||
		over.MaxWeight != 60 || over.UsedWeight != 70 ||
		over.RemainingWeight != -10 || over.Overweight != 10 {
		t.Fatalf("原预览首条应为超重 10 千克: %+v", over)
	}
	mixed := p.Rejections[1]
	if mixed.Kind != ErrMixedLoading || mixed.CompartmentID != "C1" {
		t.Fatalf("原预览次条应为混装冲突: %+v", mixed)
	}
	wantDests := []MixedDestination{
		{Destination: "Beijing", CargoIDs: []string{"G2"}},
		{Destination: "Shanghai", CargoIDs: []string{"G1"}},
	}
	if !reflect.DeepEqual(mixed.Destinations, wantDests) {
		t.Fatalf("原预览目的地分组应保留: %+v", mixed.Destinations)
	}
	if !reflect.DeepEqual(mixed.OffendingCargo, []string{"G1"}) {
		t.Fatalf("原预览应指出不允许混装的是原本在舱内的 G1: %+v", mixed.OffendingCargo)
	}
}

// 更正前预览原装载安排：操作本身合法，应返回完整预览而非错误，
// 但安排不可提交，拒绝原因先超重后混装。
func TestPreviewPendingLoadOverweightAndMixed(t *testing.T) {
	r, ops := setupPendingLoadScenario(t)

	res, err := r.Preview(ops)
	if err != nil {
		t.Fatalf("操作本身合法，应返回完整预览而非错误: %v", err)
	}
	checkOriginalPreview(t, res)

	// 调整前清单只有实际装载的 G1。
	before := res.Compartments[0].Before
	if got := cargoIDs(before); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("调整前清单应只有 G1: %v", got)
	}
	if before.UsedWeight != 30 || before.RemainingWeight != 30 {
		t.Fatalf("调整前重量应为 30/30: %+v", before)
	}

	// 预览不改变状态：G2 仍未装载，舱位占用不变。
	cv, _ := r.Cargo("G2")
	if cv.Loaded || cv.CompartmentID != "" {
		t.Fatalf("预览不应装载 G2: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 30 || cpt.RemainingWeight != 30 {
		t.Fatalf("预览不应改变舱位占用: %+v", cpt)
	}
}

// 把 G2 一次更正为 30 千克、目的地上海且不允许混装后，重新预览同一
// 装载安排：应可提交且无拒绝原因，预计总重量恰好 60 千克。
func TestAmendThenRepreviewSubmittable(t *testing.T) {
	r, ops := setupPendingLoadScenario(t)

	oldPreview, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.AmendCargo("G2", 30, "Shanghai", false); err != nil {
		t.Fatalf("未装载货物的合法更正应成功: %v", err)
	}

	res, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable || len(res.Rejections) != 0 {
		t.Fatalf("更正后预览应可提交且无拒绝原因: %+v", res.Rejections)
	}

	cp := res.Compartments[0]
	// 恰好达到承重也应允许：预计 60 千克、剩余 0。
	if cp.After.UsedWeight != 60 || cp.After.RemainingWeight != 0 {
		t.Fatalf("预计重量应为 60/0: %+v", cp.After)
	}
	// 预计清单完整包含 G1、G2 各一次。
	if got := cargoIDs(cp.After); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("预计清单应含 G1、G2 各一次: %v", got)
	}
	g1, g2 := cp.After.Cargo[0], cp.After.Cargo[1]
	if g1.ID != "G1" || g1.Weight != 30 || g1.Destination != "Shanghai" || g1.AllowMixed {
		t.Fatalf("G1 资料应保持登记内容: %+v", g1)
	}
	// G2 显示更正后的三项资料，并显示为预计装载于 C1。
	if g2.ID != "G2" || g2.Weight != 30 || g2.Destination != "Shanghai" || g2.AllowMixed {
		t.Fatalf("G2 应显示更正后的三项资料: %+v", g2)
	}
	if !g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("G2 应显示为预计装载于 C1: %+v", g2)
	}

	// 调整前清单仍只有实际装载的 G1。
	if got := cargoIDs(cp.Before); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("调整前清单应仍只有 G1: %v", got)
	}

	// 更正与预览都不替代正式装载：舱位查询仍为已用 30、剩余 30。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 30 || cpt.RemainingWeight != 30 {
		t.Fatalf("舱位查询应仍为 30/30: %+v", cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("舱位实际清单应仍只有 G1: %v", got)
	}
	// G2 查询仍为未装载，仅登记资料采用新值。
	cv, _ := r.Cargo("G2")
	if cv.Loaded || cv.CompartmentID != "" {
		t.Fatalf("G2 应仍为未装载: %+v", cv)
	}
	if cv.Weight != 30 || cv.Destination != "Shanghai" || cv.AllowMixed {
		t.Fatalf("G2 登记资料应采用新值: %+v", cv)
	}

	// 保存的原预览保留当时内容。
	checkOriginalPreview(t, oldPreview)
}

// 只消除一项限制：仅把 G2 减轻为 30 千克，保留北京目的地和允许混装，
// 超重原因消失，但仍因 G1 不允许与不同目的地货物共舱而不可提交。
func TestAmendWeightOnlyThenRepreviewStillMixed(t *testing.T) {
	r, ops := setupPendingLoadScenario(t)

	oldPreview, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.AmendCargo("G2", 30, "Beijing", true); err != nil {
		t.Fatal(err)
	}

	res, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable {
		t.Fatal("仍应因混装冲突而不可提交")
	}
	// 拒绝原因只剩混装冲突，超重已消失。
	if len(res.Rejections) != 1 {
		t.Fatalf("拒绝原因应只剩混装冲突一条: %+v", res.Rejections)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrMixedLoading || rej.CompartmentID != "C1" {
		t.Fatalf("仅剩的原因应为 C1 混装冲突: %+v", rej)
	}
	wantDests := []MixedDestination{
		{Destination: "Beijing", CargoIDs: []string{"G2"}},
		{Destination: "Shanghai", CargoIDs: []string{"G1"}},
	}
	if !reflect.DeepEqual(rej.Destinations, wantDests) {
		t.Fatalf("混装应分别列出北京的 G2 与上海的 G1: %+v", rej.Destinations)
	}
	if !reflect.DeepEqual(rej.OffendingCargo, []string{"G1"}) {
		t.Fatalf("不允许混装的应是原本在舱内的 G1: %+v", rej.OffendingCargo)
	}
	// 预计重量为 60 千克、剩余 0，不再超重。
	after := res.Compartments[0].After
	if after.UsedWeight != 60 || after.RemainingWeight != 0 {
		t.Fatalf("预计重量应为 60/0: %+v", after)
	}

	// 状态不变：G2 仍未装载，舱位占用不变。
	cv, _ := r.Cargo("G2")
	if cv.Loaded || cv.Weight != 30 || cv.Destination != "Beijing" || !cv.AllowMixed {
		t.Fatalf("G2 应仍未装载且仅资料更新: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 30 || cpt.RemainingWeight != 30 {
		t.Fatalf("舱位查询应仍为 30/30: %+v", cpt)
	}

	// 保存的原预览保留当时内容。
	checkOriginalPreview(t, oldPreview)
}
