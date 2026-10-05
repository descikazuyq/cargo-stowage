package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本组回归测试保护这条使用过程：预览通过后取消了货物的混装许可，不重新
// 预览、直接提交原安排时，正式提交仍按更正后的许可判断——旧的可提交结果
// 不能掩盖最新的混装冲突，旧预览快照本身也不能被更正或提交改写。被更正
// 许可的 G1 并不在这批操作里，但提交判断必须覆盖最终配载中的全部货物。

// setupStaleMixedScenario 建立共同的初始场景：C1、C2 承重均为 100 千克；
// G1 重 20 千克、目的地上海，已装入 C1；G2 重 30 千克、目的地为
// g2Destination，已装入 C2；G3 重 10 千克、目的地上海，尚未装载。
// 三件货物最初都允许混装。
//
// 返回登记处、“把 G2 移入 C1、同时把 G3 装入 C1”的原清单，以及对该清单
// 的可提交预览（C1 预计三件货物共 60 千克、剩余 40 千克，C2 预计清空）。
// 预览后实际配载保持原样。
func setupStaleMixedScenario(t *testing.T, g2Destination string) (*Registry, []Op, *PreviewResult) {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 20, "上海", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, g2Destination, true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "上海", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}

	ops := []Op{
		{Kind: OpMove, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	}
	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Submittable {
		t.Fatalf("三件货物均允许混装，原清单应可提交: %+v", pv.Rejections)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("受影响舱位应为 C1、C2: %+v", pv.Compartments)
	}
	// C1 预计含三件货物共 60 千克、剩余 40 千克，其中 G1 仍允许混装。
	c1After := pv.Compartments[0].After
	if c1After.UsedWeight != 60 || c1After.RemainingWeight != 40 ||
		!reflect.DeepEqual(cargoIDs(c1After), []string{"G1", "G2", "G3"}) {
		t.Fatalf("C1 预计应含三件货物共 60 千克、剩余 40 千克: %+v", c1After)
	}
	if c1After.Cargo[0].ID != "G1" || !c1After.Cargo[0].AllowMixed {
		t.Fatalf("C1 预计清单中 G1 应仍允许混装: %+v", c1After.Cargo)
	}
	// C2 预计清空。
	if c2After := pv.Compartments[1].After; len(c2After.Cargo) != 0 ||
		c2After.UsedWeight != 0 || c2After.RemainingWeight != 100 {
		t.Fatalf("C2 预计应清空: %+v", c2After)
	}

	// 预览只读：实际配载仍保持原样。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 20 || c1.RemainingWeight != 80 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("预览后 C1 应保持 G1 一件 20/80: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 30 || c2.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("预览后 C2 应保持 G2 一件 30/70: %+v", c2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("预览后 G3 应仍未装载: %+v", g3)
	}
	return r, ops, pv
}

// amendG1DisallowMixed 只把 G1 的混装许可改为不允许，重量与目的地保持
// 原值；当前 C1 只有 G1 一件货物，更正应成功，不能因未来计划被拒绝。
func amendG1DisallowMixed(t *testing.T, r *Registry) {
	t.Helper()
	if err := r.AmendCargo("G1", 20, "上海", false); err != nil {
		t.Fatalf("C1 当前只有 G1，取消其混装许可应成功: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if g1.Weight != 20 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("更正后 G1 应以原重量、原目的地与不允许混装留在 C1: %+v", g1)
	}
}

// assertStaleMixedSnapshot 断言旧预览快照仍保留更正前的内容：可提交、
// 货物变化不变、预计清单里 G1 仍允许混装、C1 预计仍为 60/40 千克。
func assertStaleMixedSnapshot(t *testing.T, pv *PreviewResult) {
	t.Helper()
	if !pv.Submittable || len(pv.Rejections) != 0 {
		t.Fatalf("旧预览应仍为可提交且无拒绝原因: %+v", pv)
	}
	wantChanges := []CargoChange{
		{CargoID: "G2", From: "C2", To: "C1"},
		{CargoID: "G3", From: "", To: "C1"},
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("旧预览的货物变化不应被改写: %+v want %+v", pv.CargoChanges, wantChanges)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("旧预览的舱位清单不应被改写: %+v", pv.Compartments)
	}
	c1 := pv.Compartments[0]
	var g1 *CargoView
	for i := range c1.After.Cargo {
		if c1.After.Cargo[i].ID == "G1" {
			g1 = &c1.After.Cargo[i]
		}
	}
	if g1 == nil {
		t.Fatalf("旧预览 C1 预计清单仍应包含 G1: %+v", c1.After.Cargo)
	}
	if !g1.AllowMixed {
		t.Fatalf("旧预览应保留 G1 允许混装，实际不允许: %+v", g1)
	}
	if c1.After.UsedWeight != 60 || c1.After.RemainingWeight != 40 {
		t.Fatalf("旧预览应保留 C1 预计 60/40 千克: %+v", c1.After)
	}
}

// TestStalePreviewSubmitAfterAmendMixedRejected 覆盖核心回归：预览可提交
// 后把 G1 的混装许可改为不允许，不重新预览直接提交原清单，正式提交必须
// 按更正后的许可判断——C1 将出现目的地上海（G1、G3）与北京（G2）共舱而
// G1 不允许混装，整次调整拒绝，且不能返回成功调整结果。
func TestStalePreviewSubmitAfterAmendMixedRejected(t *testing.T) {
	r, ops, old := setupStaleMixedScenario(t, "北京")
	amendG1DisallowMixed(t, r)

	// 保留旧预览，直接用一个未使用过的调整编号提交原来那批操作。
	res, err := r.Adjust("A1", ops)
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("混装冲突时必须拒绝整次调整，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("混装错误应指出不允许混装的 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("混装错误应携带调整编号 A1，实际 %q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "G1") {
		t.Fatalf("混装说明应指出发生冲突的 C1 与不允许混装的 G1: %s", msg)
	}

	// 拒绝后整次调整不生效，资料更正也不能被撤回。
	g1, _ := r.Cargo("G1")
	if g1.Weight != 20 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("拒绝后 G1 应仍以更正后的许可留在 C1（更正不撤回）: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 30 || !g2.Loaded || g2.CompartmentID != "C2" {
		t.Fatalf("拒绝后 G2 应仍以 30 千克留在 C2: %+v", g2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Weight != 10 || g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("拒绝后 G3 应仍为 10 千克且未装载（不能只执行装载一项）: %+v", g3)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 20 || c1.RemainingWeight != 80 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("拒绝后 C1 应为 G1 一件、已用 20 剩余 80: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 30 || c2.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("拒绝后 C2 应为 G2 一件、已用 30 剩余 70: %+v", c2)
	}

	// 更正与失败提交都不能改写之前保存的旧预览快照。
	assertStaleMixedSnapshot(t, old)
}

// TestStalePreviewSubmitAfterAmendMixedSameDestinationSucceeds 保护同一规则
// 的边界：G2 的目的地也是上海，取消 G1 的混装许可后提交原安排，最终三件
// 货物目的地相同，混装限制不生效，应成功；成功后的货物查询与舱位清单
// 一致反映最新许可与归属。
func TestStalePreviewSubmitAfterAmendMixedSameDestinationSucceeds(t *testing.T) {
	r, ops, old := setupStaleMixedScenario(t, "上海")
	amendG1DisallowMixed(t, r)

	// 不重新预览，直接用未使用过的编号提交原清单，应成功。
	res, err := r.Adjust("A1", ops)
	if err != nil {
		t.Fatalf("最终三件货物目的地均为上海，应提交成功: %v", err)
	}
	if res == nil || res.ID != "A1" {
		t.Fatalf("应返回编号为 A1 的成功调整结果: %+v", res)
	}
	wantChanges := []CargoChange{
		{CargoID: "G2", From: "C2", To: "C1"},
		{CargoID: "G3", From: "", To: "C1"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
		t.Fatalf("货物变化错误: %+v want %+v", res.CargoChanges, wantChanges)
	}
	wantCompChanges := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 20, WeightAfter: 60},
		{CompartmentID: "C2", WeightBefore: 30, WeightAfter: 0},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
		t.Fatalf("舱位重量变化错误: %+v want %+v", res.CompartmentChanges, wantCompChanges)
	}

	// C1 同时包含三件货物，已用 60 千克、剩余 40 千克；C2 清空。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 60 || c1.RemainingWeight != 40 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G2", "G3"}) {
		t.Fatalf("提交后 C1 应含三件货物、已用 60 剩余 40: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 0 || c2.RemainingWeight != 100 || len(c2.Cargo) != 0 {
		t.Fatalf("提交后 C2 应清空、已用 0 剩余 100: %+v", c2)
	}

	// 货物查询与舱位清单中的许可、位置一致：G1 不允许混装，G2、G3 允许。
	wantCargo := map[string]bool{"G1": false, "G2": true, "G3": true}
	for id, allowMixed := range wantCargo {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatal(err)
		}
		if !cv.Loaded || cv.CompartmentID != "C1" || cv.AllowMixed != allowMixed {
			t.Fatalf("货物 %s 查询应位于 C1 且混装许可为 %v: %+v", id, allowMixed, cv)
		}
	}
	for _, cv := range c1.Cargo {
		if cv.AllowMixed != wantCargo[cv.ID] || !cv.Loaded || cv.CompartmentID != "C1" {
			t.Fatalf("舱位清单中 %s 的许可或位置与货物查询不一致: %+v", cv.ID, cv)
		}
	}

	// 成功提交同样不能改写之前保存的旧预览快照。
	assertStaleMixedSnapshot(t, old)
}
