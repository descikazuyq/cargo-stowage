package stowage

import (
	"reflect"
	"testing"
)

// 本组回归测试保护这条使用过程：一份因超重与混装冲突被拒绝的预览保存后，
// 更正待装货物的资料并沿用现有 Preview、AmendCargo 与查询功能重新预览
// 同一装载安排。新预览能否提交及拒绝原因必须依据更正后的当前货物资料
// 重新判断；先前返回的预览记录是独立快照，始终保留当时的预计重量、
// 货物资料、目的地分组与拒绝原因。资料更正与预览都不是正式装载：未装载
// 的货物更正后仍未装载，预览再多次也不改变实际配载、不占用调整编号。

// setupRePreviewScenario 建立共同的初始场景：C1 承重 60 千克；G1 重
// 30 千克、目的地上海、不允许混装，已通过一次正式调整装入 C1；G2 重
// 40 千克、目的地北京、允许混装，已登记但尚未装载。
//
// 返回登记处、“把 G2 装入 C1”的原清单，以及对该清单的首次预览。这条
// 装载操作本身合法（货物与舱位都存在、G2 尚未装载），所以预览返回完整
// 结果而不是调用错误；但预计配载 30+40=70 千克超过承重 60 千克，且
// 上海的 G1 不允许与北京的 G2 混装，安排不可提交。预览后实际配载保持
// 原样。
func setupRePreviewScenario(t *testing.T) (*Registry, []Op, *PreviewResult) {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 60); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 40, "北京", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}

	ops := []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}}
	first, err := r.Preview(ops)
	if err != nil {
		t.Fatalf("操作本身合法（G2 未装载、C1 存在），应返回完整预览而非调用错误: %v", err)
	}
	if first == nil {
		t.Fatal("合法操作的预览必须返回完整结果，不能为 nil")
	}
	if first.Submittable {
		t.Fatalf("预计 70/60 千克且目的地不同，应不可提交: %+v", first)
	}

	// 货物变化只有一件：G2 从未装载预计装入 C1。
	if !reflect.DeepEqual(first.CargoChanges, []CargoChange{
		{CargoID: "G2", From: "", To: "C1"},
	}) {
		t.Fatalf("货物变化应为 G2 从未装载到 C1: %+v", first.CargoChanges)
	}

	// 装载只影响目标舱位 C1。
	if len(first.Compartments) != 1 || first.Compartments[0].ID != "C1" {
		t.Fatalf("受影响舱位应只有 C1: %+v", first.Compartments)
	}
	c1 := first.Compartments[0]

	// 调整前清单只有实际装载的 G1：30 千克、剩余 30 千克。
	if c1.Before.MaxWeight != 60 ||
		c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) {
		t.Fatalf("预览调整前 C1 应只有 G1 一件 30/30: %+v", c1.Before)
	}
	g1Before := previewCargoView(c1.Before, "G1")
	if g1Before.Weight != 30 || g1Before.Destination != "上海" || g1Before.AllowMixed ||
		!g1Before.Loaded || g1Before.CompartmentID != "C1" {
		t.Fatalf("调整前清单中的 G1 应保持实际登记资料与位置: %+v", g1Before)
	}

	// 预计清单完整包含 G1、G2 各一次，共 70 千克、剩余 -10 千克；
	// 两件都显示为已装载且属于 C1（预计快照的展示方式）。
	if c1.After.MaxWeight != 60 ||
		c1.After.UsedWeight != 70 || c1.After.RemainingWeight != -10 ||
		!reflect.DeepEqual(cargoIDs(c1.After), []string{"G1", "G2"}) {
		t.Fatalf("C1 预计应含 G1、G2 共 70 千克、剩余 -10 千克: %+v", c1.After)
	}
	g1After := previewCargoView(c1.After, "G1")
	if g1After.Weight != 30 || g1After.Destination != "上海" || g1After.AllowMixed ||
		!g1After.Loaded || g1After.CompartmentID != "C1" {
		t.Fatalf("预计清单中的 G1 应为 30 千克、上海、不允许混装且预计在 C1: %+v", g1After)
	}
	g2After := previewCargoView(c1.After, "G2")
	if g2After.Weight != 40 || g2After.Destination != "北京" || !g2After.AllowMixed ||
		!g2After.Loaded || g2After.CompartmentID != "C1" {
		t.Fatalf("预计清单中的 G2 应为 40 千克、北京、允许混装且预计装载于 C1: %+v", g2After)
	}

	// 拒绝原因先列超重 10 千克，再列混装冲突。
	if len(first.Rejections) != 2 {
		t.Fatalf("应同时列出超重与混装冲突两条原因: %+v", first.Rejections)
	}
	over := first.Rejections[0]
	if over.Kind != ErrOverweight || over.CompartmentID != "C1" ||
		over.MaxWeight != 60 || over.UsedWeight != 70 ||
		over.RemainingWeight != -10 || over.Overweight != 10 {
		t.Fatalf("首条应为 C1 超重 10 千克（70/60、剩余 -10）: %+v", over)
	}
	if len(over.Destinations) != 0 || len(over.OffendingCargo) != 0 {
		t.Fatalf("超重原因不应携带混装信息: %+v", over)
	}
	mixed := first.Rejections[1]
	if mixed.Kind != ErrMixedLoading || mixed.CompartmentID != "C1" {
		t.Fatalf("第二条应为 C1 的混装冲突: %+v", mixed)
	}
	// 目的地按字典序排列：上海（G1）在前，北京（G2）在后；
	// 不允许混装的是原本就在舱内的 G1。
	if !reflect.DeepEqual(mixed.Destinations, []MixedDestination{
		{Destination: "上海", CargoIDs: []string{"G1"}},
		{Destination: "北京", CargoIDs: []string{"G2"}},
	}) {
		t.Fatalf("混装说明应分别列出上海的 G1 与北京的 G2: %+v", mixed.Destinations)
	}
	if !reflect.DeepEqual(mixed.OffendingCargo, []string{"G1"}) {
		t.Fatalf("不允许混装的应是原本在舱内的 G1: %+v", mixed.OffendingCargo)
	}
	if mixed.UsedWeight != 0 || mixed.RemainingWeight != 0 || mixed.Overweight != 0 {
		t.Fatalf("混装原因不应携带重量数值: %+v", mixed)
	}

	// 预览只读：C1 实际仍只有 G1（30/30），G2 实际仍未装载。
	c1Now, _ := r.Compartment("C1")
	if c1Now.UsedWeight != 30 || c1Now.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(*c1Now), []string{"G1"}) {
		t.Fatalf("预览后 C1 实际应仍为 G1 一件 30/30: %+v", c1Now)
	}
	g2Now, _ := r.Cargo("G2")
	if g2Now.Loaded || g2Now.CompartmentID != "" ||
		g2Now.Weight != 40 || g2Now.Destination != "北京" || !g2Now.AllowMixed {
		t.Fatalf("预览后 G2 应仍未装载且资料不变: %+v", g2Now)
	}
	return r, ops, first
}

// previewCargoView 从舱位快照中按编号取出一件货物的视图，不存在则使
// 当前测试失败（舱位清单按编号字典序排列，但按编号查找比依赖下标更直白）。
func previewCargoView(v CompartmentView, id string) CargoView {
	for _, c := range v.Cargo {
		if c.ID == id {
			return c
		}
	}
	return CargoView{ID: id + "（不在清单中）"}
}

// assertOldRejectedPreview 断言首次被拒绝的预览始终保留当时的内容：
// 不可提交、货物变化不变、预计 70 千克与剩余 -10 千克、G1/G2 的原始
// 资料与目的地分组、先超重后混装的两条拒绝原因。无论后续更正让新预览
// 变为合法还是仍被拒绝，这份旧快照都不变。
func assertOldRejectedPreview(t *testing.T, old *PreviewResult) {
	t.Helper()
	if old.Submittable || len(old.Rejections) != 2 {
		t.Fatalf("旧预览应仍不可提交且保留两条拒绝原因: %+v", old)
	}
	if !reflect.DeepEqual(old.CargoChanges, []CargoChange{
		{CargoID: "G2", From: "", To: "C1"},
	}) {
		t.Fatalf("旧预览的货物变化不应被改写: %+v", old.CargoChanges)
	}
	if len(old.Compartments) != 1 || old.Compartments[0].ID != "C1" {
		t.Fatalf("旧预览的受影响舱位不应被改写: %+v", old.Compartments)
	}
	c1 := old.Compartments[0]

	// 旧快照的调整前清单仍只有实际装载的 G1。
	if c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) {
		t.Fatalf("旧预览的调整前清单不应被改写: %+v", c1.Before)
	}
	g1Before := previewCargoView(c1.Before, "G1")
	if g1Before.Weight != 30 || g1Before.Destination != "上海" || g1Before.AllowMixed {
		t.Fatalf("旧预览调整前清单中的 G1 资料不应被改写: %+v", g1Before)
	}

	// 旧快照的预计重量仍是 70 千克，两件货物仍是原来的资料。
	if c1.After.UsedWeight != 70 || c1.After.RemainingWeight != -10 ||
		!reflect.DeepEqual(cargoIDs(c1.After), []string{"G1", "G2"}) {
		t.Fatalf("旧预览应保留 C1 预计 70 千克、剩余 -10 千克: %+v", c1.After)
	}
	g1 := previewCargoView(c1.After, "G1")
	if g1.Weight != 30 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("旧预览中的 G1 应保留 30 千克、上海、不允许混装: %+v", g1)
	}
	g2 := previewCargoView(c1.After, "G2")
	if g2.Weight != 40 || g2.Destination != "北京" || !g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("旧预览中的 G2 应保留 40 千克、北京、允许混装且预计在 C1: %+v", g2)
	}

	over := old.Rejections[0]
	if over.Kind != ErrOverweight || over.CompartmentID != "C1" ||
		over.MaxWeight != 60 || over.UsedWeight != 70 ||
		over.RemainingWeight != -10 || over.Overweight != 10 {
		t.Fatalf("旧预览应保留超重 10 千克的原因: %+v", over)
	}
	mixed := old.Rejections[1]
	if mixed.Kind != ErrMixedLoading || mixed.CompartmentID != "C1" ||
		!reflect.DeepEqual(mixed.Destinations, []MixedDestination{
			{Destination: "上海", CargoIDs: []string{"G1"}},
			{Destination: "北京", CargoIDs: []string{"G2"}},
		}) ||
		!reflect.DeepEqual(mixed.OffendingCargo, []string{"G1"}) {
		t.Fatalf("旧预览应保留原来的目的地分组与混装责任: %+v", mixed)
	}
}

// assertAmendAndPreviewDoNotLoad 断言资料更正与预览都没有替代正式装载：
// 登记处只保存初始的 A0 一次成功调整，C1 实际仍只装着 G1（30/30），
// G2 带着给定的最新资料保持未装载。
func assertAmendAndPreviewDoNotLoad(t *testing.T, r *Registry, g2Weight int64, g2Dest string, g2AllowMixed bool) {
	t.Helper()
	if len(r.adjustments) != 1 {
		t.Fatalf("更正与预览都不应产生正式调整，实际已保存 %d 笔: %+v", len(r.adjustments), r.adjustments)
	}
	if _, ok := r.adjustments["A0"]; !ok {
		t.Fatalf("登记处应只保存初始调整 A0: %+v", r.adjustments)
	}
	c1, _ := r.Compartment("C1")
	if c1.MaxWeight != 60 || c1.UsedWeight != 30 || c1.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("C1 实际应仍为已用 30、剩余 30、只有 G1: %+v", c1)
	}
	g1, _ := r.Cargo("G1")
	if g1.Weight != 30 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("G1 实际资料与位置应保持不变: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != g2Weight || g2.Destination != g2Dest || g2.AllowMixed != g2AllowMixed ||
		g2.Loaded || g2.CompartmentID != "" {
		t.Fatalf("G2 应以 %d 千克、%s、允许混装 %v 的新资料保持未装载: %+v",
			g2Weight, g2Dest, g2AllowMixed, g2)
	}
}

// TestRePreviewAfterAmendAllFieldsNowSubmittable 覆盖更正使安排变为合法的
// 情况：把待装的 G2 一次更正为 30 千克、目的地上海、不允许混装，再预览
// 同一装载安排。两件货物目的地相同，即使都不允许混装也可以共舱，恰好
// 达到 60 千克承重也应允许——新预览可提交且没有拒绝原因；旧预览继续
// 保留 70 千克的预计重量、原始资料与两项拒绝原因。
func TestRePreviewAfterAmendAllFieldsNowSubmittable(t *testing.T) {
	r, ops, old := setupRePreviewScenario(t)

	// G2 尚未装载，三项资料一次整体替换：40/北京/允许 -> 30/上海/不允许。
	if err := r.AmendCargo("G2", 30, "上海", false); err != nil {
		t.Fatalf("未装载货物的资料更正应成功: %v", err)
	}
	assertAmendAndPreviewDoNotLoad(t, r, 30, "上海", false)

	// 沿用同一份清单重新预览：应按更正后的当前资料判断为可提交。
	res, err := r.Preview(ops)
	if err != nil {
		t.Fatalf("重新预览应正常返回完整结果: %v", err)
	}
	if !res.Submittable || len(res.Rejections) != 0 {
		t.Fatalf("30+30=60 恰好达承重且同为上海，新预览应可提交且无拒绝原因: %+v", res)
	}
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{
		{CargoID: "G2", From: "", To: "C1"},
	}) {
		t.Fatalf("新预览的货物变化仍应为 G2 从未装载到 C1: %+v", res.CargoChanges)
	}
	if len(res.Compartments) != 1 || res.Compartments[0].ID != "C1" {
		t.Fatalf("新预览的受影响舱位应只有 C1: %+v", res.Compartments)
	}
	c1 := res.Compartments[0]

	// 新预览调整前的清单仍只有实际装载的 G1，重量 30/30。
	if c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) {
		t.Fatalf("新预览调整前清单应仍只有实际装载的 G1（30/30）: %+v", c1.Before)
	}

	// 预计清单完整包含 G1、G2 各一次，共 60 千克、剩余 0。
	if c1.After.UsedWeight != 60 || c1.After.RemainingWeight != 0 ||
		!reflect.DeepEqual(cargoIDs(c1.After), []string{"G1", "G2"}) {
		t.Fatalf("新预览 C1 预计应含 G1、G2 共 60 千克、剩余 0: %+v", c1.After)
	}
	g1 := previewCargoView(c1.After, "G1")
	if g1.Weight != 30 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("预计清单中的 G1 应保持 30 千克、上海、不允许混装且预计在 C1: %+v", g1)
	}
	g2 := previewCargoView(c1.After, "G2")
	// G2 显示更正后的三项资料，并显示为预计装载于 C1。
	if g2.Weight != 30 || g2.Destination != "上海" || g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("预计清单中的 G2 应显示更正后的三项资料且预计装载于 C1: %+v", g2)
	}

	// 重新预览同样是只读的：舱位查询仍为已用 30、剩余 30，G2 查询仍为
	// 未装载，仅登记资料采用新值；不产生新的正式调整。
	assertAmendAndPreviewDoNotLoad(t, r, 30, "上海", false)

	// 更正让新预览合法，不能改写之前保存的、当时被拒绝的旧预览。
	assertOldRejectedPreview(t, old)
}

// TestRePreviewAfterAmendWeightOnlyStillMixedRejected 覆盖只消除一项限制
// 的情况：在相同初始配载下仅把 G2 减轻为 30 千克，保留北京目的地与
// 允许混装的资料，再预览原安排。超重原因应消失（预计恰为 60 千克），
// 但仍应因原本在舱内的 G1 不允许与不同目的地货物共舱而不可提交，
// 拒绝原因只剩混装冲突；旧预览继续保留 70 千克与两项拒绝原因。
func TestRePreviewAfterAmendWeightOnlyStillMixedRejected(t *testing.T) {
	r, ops, old := setupRePreviewScenario(t)

	// 只改重量：40 -> 30，目的地北京与允许混装的资料原样传回。
	if err := r.AmendCargo("G2", 30, "北京", true); err != nil {
		t.Fatalf("未装载货物的重量更正应成功: %v", err)
	}
	assertAmendAndPreviewDoNotLoad(t, r, 30, "北京", true)

	res, err := r.Preview(ops)
	if err != nil {
		t.Fatalf("重新预览应正常返回完整结果: %v", err)
	}
	if res.Submittable {
		t.Fatal("G1 上海不允许混装而 G2 仍去北京，新预览应仍不可提交")
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("超重原因消失后应只剩混装冲突一条原因: %+v", res.Rejections)
	}
	mixed := res.Rejections[0]
	if mixed.Kind != ErrMixedLoading || mixed.CompartmentID != "C1" {
		t.Fatalf("唯一的拒绝原因应为 C1 的混装冲突: %+v", mixed)
	}
	if !reflect.DeepEqual(mixed.Destinations, []MixedDestination{
		{Destination: "上海", CargoIDs: []string{"G1"}},
		{Destination: "北京", CargoIDs: []string{"G2"}},
	}) {
		t.Fatalf("混装说明应分别列出上海的 G1 与北京的 G2: %+v", mixed.Destinations)
	}
	if !reflect.DeepEqual(mixed.OffendingCargo, []string{"G1"}) {
		t.Fatalf("不允许与不同目的地货物共舱的应是原本在舱内的 G1: %+v", mixed.OffendingCargo)
	}

	if len(res.Compartments) != 1 || res.Compartments[0].ID != "C1" {
		t.Fatalf("新预览的受影响舱位应只有 C1: %+v", res.Compartments)
	}
	c1 := res.Compartments[0]
	// 预计重量按更正后的资料为 60 千克、剩余 0，只是混装仍不合法。
	if c1.After.UsedWeight != 60 || c1.After.RemainingWeight != 0 ||
		!reflect.DeepEqual(cargoIDs(c1.After), []string{"G1", "G2"}) {
		t.Fatalf("新预览 C1 预计应含 G1、G2 共 60 千克、剩余 0: %+v", c1.After)
	}
	g2 := previewCargoView(c1.After, "G2")
	if g2.Weight != 30 || g2.Destination != "北京" || !g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("预计清单中的 G2 应为 30 千克、北京、允许混装且预计在 C1: %+v", g2)
	}
	g1 := previewCargoView(c1.After, "G1")
	if g1.Weight != 30 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("预计清单中的 G1 资料与预计位置应不变: %+v", g1)
	}
	// 调整前清单仍只有实际装载的 G1。
	if c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) {
		t.Fatalf("新预览调整前清单应仍只有实际装载的 G1（30/30）: %+v", c1.Before)
	}

	// 更正与预览都不替代正式装载。
	assertAmendAndPreviewDoNotLoad(t, r, 30, "北京", true)

	// 新预览仍被拒绝，也不能改写之前保存的旧预览：它仍显示 70 千克、
	// 原来的货物资料、目的地分组与两项拒绝原因。
	assertOldRejectedPreview(t, old)
}
