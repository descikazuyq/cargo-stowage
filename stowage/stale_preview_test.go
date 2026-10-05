package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本组回归测试保护这条使用过程：预览通过后更正了货物重量，不重新预览、
// 直接提交原安排时，正式提交仍按更正后的重量判断——旧的可提交结果不能
// 掩盖最新的超重问题，旧预览快照本身也不能被更正或提交改写。

// setupStalePreviewScenario 建立共同的初始场景：
// C1、C2 承重均为 100 千克；G1 重 20 千克已装入 C1，G2 重 60 千克已
// 装入 C2，G3 重 10 千克尚未装载；三件货物目的地相同，均不允许混装。
//
// 返回登记处、“把 G1 移到 C2、同时把 G3 装入 C2”的原清单，以及对该
// 清单的可提交预览（C2 预计总重量 90 千克）。预览后实际配载保持原样。
func setupStalePreviewScenario(t *testing.T) (*Registry, []Op, *PreviewResult) {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 20, "X", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 60, "X", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "X", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}

	ops := []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	}
	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Submittable {
		t.Fatalf("原清单按 G1 20、G2 60、G3 10 预计 C2 共 90 千克，应可提交: %+v", pv.Rejections)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("受影响舱位应为 C1、C2: %+v", pv.Compartments)
	}
	// C2 预计含三件货物共 90 千克，其中 G1 保留 20 千克。
	c2After := pv.Compartments[1].After
	if c2After.UsedWeight != 90 || c2After.RemainingWeight != 10 ||
		!reflect.DeepEqual(cargoIDs(c2After), []string{"G1", "G2", "G3"}) {
		t.Fatalf("C2 预计应含三件货物共 90 千克、剩余 10 千克: %+v", c2After)
	}
	if c2After.Cargo[0].ID != "G1" || c2After.Cargo[0].Weight != 20 {
		t.Fatalf("C2 预计清单中 G1 应为 20 千克: %+v", c2After.Cargo)
	}
	// C1 预计清空。
	if c1After := pv.Compartments[0].After; len(c1After.Cargo) != 0 ||
		c1After.UsedWeight != 0 || c1After.RemainingWeight != 100 {
		t.Fatalf("C1 预计应清空: %+v", c1After)
	}

	// 预览只读：实际配载仍保持原样。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 20 || c1.RemainingWeight != 80 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("预览后 C1 应保持 G1 一件 20/80: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 60 || c2.RemainingWeight != 40 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("预览后 C2 应保持 G2 一件 60/40: %+v", c2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("预览后 G3 应仍未装载: %+v", g3)
	}
	return r, ops, pv
}

// assertStalePreviewSnapshot 断言旧预览快照仍保留更正前的内容：
// 可提交、货物变化不变、G1 仍重 20 千克、C2 预计仍为 90 千克。
func assertStalePreviewSnapshot(t *testing.T, pv *PreviewResult) {
	t.Helper()
	if !pv.Submittable || len(pv.Rejections) != 0 {
		t.Fatalf("旧预览应仍为可提交且无拒绝原因: %+v", pv)
	}
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G3", From: "", To: "C2"},
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("旧预览的货物变化不应被改写: %+v want %+v", pv.CargoChanges, wantChanges)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("旧预览的舱位清单不应被改写: %+v", pv.Compartments)
	}
	c2 := pv.Compartments[1]
	var g1 *CargoView
	for i := range c2.After.Cargo {
		if c2.After.Cargo[i].ID == "G1" {
			g1 = &c2.After.Cargo[i]
		}
	}
	if g1 == nil {
		t.Fatalf("旧预览 C2 预计清单仍应包含 G1: %+v", c2.After.Cargo)
	}
	if g1.Weight != 20 {
		t.Fatalf("旧预览应保留 G1 重 20 千克，实际 %d", g1.Weight)
	}
	if c2.After.UsedWeight != 90 || c2.After.RemainingWeight != 10 {
		t.Fatalf("旧预览应保留 C2 预计 90/10 千克: %+v", c2.After)
	}
}

// TestStalePreviewSubmitAfterAmendOverweightRejected 覆盖核心回归：
// 预览可提交后把 G1 从 20 千克更正为 40 千克，不重新预览直接提交原
// 清单，正式提交必须按更正后的重量判断——C2 预计 110 千克超过承重
// 100 千克，整次调整拒绝，且不能返回成功调整结果。
func TestStalePreviewSubmitAfterAmendOverweightRejected(t *testing.T) {
	r, ops, old := setupStalePreviewScenario(t)

	// 更正 G1 重量 20 -> 40；目的地与混装许可保持原值，G1 仍在 C1。
	if err := r.AmendCargo("G1", 40, "X", false); err != nil {
		t.Fatalf("更正 G1 重量为 40 千克应成功（C1 仅 40 千克）: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if g1.Weight != 40 || g1.Destination != "X" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("更正后 G1 应以新重量、原目的地与混装许可留在 C1: %+v", g1)
	}

	// 保留旧预览，直接用一个未使用过的调整编号提交原来那批操作。
	res, err := r.Adjust("A1", ops)
	se := requireError(t, err, ErrOverweight)
	if res != nil {
		t.Fatalf("超重时必须拒绝整次调整，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "C2" {
		t.Fatalf("超重错误应指出舱位 C2，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("超重错误应携带调整编号 A1，实际 %q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C2") ||
		!strings.Contains(msg, "110") || !strings.Contains(msg, "100") {
		t.Fatalf("超重说明应指出 C2、预计 110 千克与最大承重 100 千克: %s", msg)
	}

	// 拒绝后整次调整不生效，资料更正也不能被撤回。
	g1, _ = r.Cargo("G1")
	if g1.Weight != 40 || g1.Destination != "X" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("拒绝后 G1 应仍以 40 千克留在 C1（更正不撤回、移动不执行）: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 60 || !g2.Loaded || g2.CompartmentID != "C2" {
		t.Fatalf("拒绝后 G2 应仍以 60 千克留在 C2: %+v", g2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Weight != 10 || g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("拒绝后 G3 应仍为 10 千克且未装载（不能只执行装载一项）: %+v", g3)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 40 || c1.RemainingWeight != 60 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("拒绝后 C1 应为 G1 一件、已用 40 剩余 60: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 60 || c2.RemainingWeight != 40 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("拒绝后 C2 应为 G2 一件、已用 60 剩余 40: %+v", c2)
	}

	// 更正与失败提交都不能改写之前保存的旧预览快照。
	assertStalePreviewSnapshot(t, old)
}

// 本组回归测试还保护混装许可的同类过程：预览通过后通过 AmendCargo 取消
// 了某件货物的混装许可（即使该货物没有出现在这批操作里），不重新预览、
// 直接提交原安排时，正式提交仍按最新许可重新判断，不能沿用旧预览中的
// 混装许可放行；旧预览快照中该货物仍显示允许混装。

// setupStalePreviewMixedScenario 建立“预览通过后取消货物混装许可”的
// 初始场景：C1、C2 承重均为 100 千克；G1 重 20 千克、目的地上海，已装入
// C1；G2 重 30 千克、目的地由 g2Destination 给出，已装入 C2；G3 重 10
// 千克、目的地上海，尚未装载。三件货物最初都允许混装。
//
// 返回登记处、“把 G2 移入 C1，同时把 G3 装入 C1”的原清单，以及对该清单
// 的可提交预览：C1 预计装有三件货物共 60 千克、剩余 40 千克，C2 预计清空。
// 预览后实际配载保持原样。
func setupStalePreviewMixedScenario(t *testing.T, g2Destination string) (*Registry, []Op, *PreviewResult) {
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
		t.Fatalf("三件货物最初都允许混装，原清单应可提交: %+v", pv.Rejections)
	}
	wantChanges := []CargoChange{
		{CargoID: "G2", From: "C2", To: "C1"},
		{CargoID: "G3", From: "", To: "C1"},
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("货物变化应为 G2 移入 C1、G3 装入 C1: %+v", pv.CargoChanges)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("受影响舱位应为 C1、C2: %+v", pv.Compartments)
	}
	// C1 预计含三件货物共 60 千克、剩余 40 千克。
	c1After := pv.Compartments[0].After
	if c1After.UsedWeight != 60 || c1After.RemainingWeight != 40 ||
		!reflect.DeepEqual(cargoIDs(c1After), []string{"G1", "G2", "G3"}) {
		t.Fatalf("C1 预计应含三件货物共 60 千克、剩余 40 千克: %+v", c1After)
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

// assertStalePreviewMixedSnapshot 断言旧预览快照仍保留更正前的内容：
// 可提交、货物去向不变、C1 预计仍为三件货物 60/40 千克且清单中 G1 仍
// 允许混装、目的地与重量保持原值，C2 预计仍清空。
func assertStalePreviewMixedSnapshot(t *testing.T, pv *PreviewResult, g2Destination string) {
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
	c1 := pv.Compartments[0].After
	if c1.UsedWeight != 60 || c1.RemainingWeight != 40 ||
		!reflect.DeepEqual(cargoIDs(c1), []string{"G1", "G2", "G3"}) {
		t.Fatalf("旧预览应保留 C1 预计三件货物 60/40 千克: %+v", c1)
	}
	wantProfile := map[string]struct {
		weight      int64
		destination string
		allowMixed  bool
	}{
		"G1": {20, "上海", true},
		"G2": {30, g2Destination, true},
		"G3": {10, "上海", true},
	}
	for _, cv := range c1.Cargo {
		want := wantProfile[cv.ID]
		if cv.Weight != want.weight || cv.Destination != want.destination ||
			cv.AllowMixed != want.allowMixed || !cv.Loaded || cv.CompartmentID != "C1" {
			t.Fatalf("旧预览 C1 预计清单中 %s 应保留更正前资料: %+v", cv.ID, cv)
		}
	}
	if c2 := pv.Compartments[1].After; len(c2.Cargo) != 0 ||
		c2.UsedWeight != 0 || c2.RemainingWeight != 100 {
		t.Fatalf("旧预览应保留 C2 预计清空: %+v", c2)
	}
}

// TestStalePreviewSubmitAfterRevokeMixedPermissionRejected 覆盖核心回归：
// 预览可提交后只把 G1 的混装许可改为不允许（重量、目的地不变；G1 不在
// 这批操作里），不重新预览直接提交原安排时，正式提交必须按最新许可
// 重新判断——最终 C1 内上海与北京共舱而 G1 不允许混装，整次调整拒绝，
// 且不能返回成功调整结果。
func TestStalePreviewSubmitAfterRevokeMixedPermissionRejected(t *testing.T) {
	r, ops, old := setupStalePreviewMixedScenario(t, "北京")

	// 只取消 G1 的混装许可；当前 C1 只有 G1 一件，更正应成功，
	// 不能因为预览里的未来计划而拒绝。
	if err := r.AmendCargo("G1", 20, "上海", false); err != nil {
		t.Fatalf("C1 当前只有 G1，取消其混装许可应成功: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if g1.Weight != 20 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("更正后 G1 应以原重量、原目的地和新许可留在 C1: %+v", g1)
	}

	// 保留旧预览，直接用一个未使用过的调整编号提交原来那批操作。
	res, err := r.Adjust("A1", ops)
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("混装冲突时必须拒绝整次调整，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("混装错误应指出不允许混装的货物 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("混装错误应携带调整编号 A1，实际 %q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "G1") {
		t.Fatalf("混装说明应能看出发生冲突的 C1 与不允许混装的 G1: %s", msg)
	}

	// 拒绝后整次调整不生效：G1 留在 C1 且保留更正后的许可，G2 仍在 C2，
	// G3 仍未装载，两舱清单与重量不能出现部分调整的结果。
	g1, _ = r.Cargo("G1")
	if g1.Weight != 20 || g1.Destination != "上海" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("拒绝后 G1 应仍以更正后的许可留在 C1: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 30 || g2.Destination != "北京" || !g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C2" {
		t.Fatalf("拒绝后 G2 应仍以原资料留在 C2（移动不执行）: %+v", g2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Weight != 10 || g3.Destination != "上海" || !g3.AllowMixed ||
		g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("拒绝后 G3 应仍为原资料且未装载（不能只执行一项操作）: %+v", g3)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 20 || c1.RemainingWeight != 80 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("拒绝后 C1 应为 G1 一件、已用 20 剩余 80: %+v", c1)
	}
	if c1.Cargo[0].AllowMixed {
		t.Fatalf("C1 清单中 G1 应保留更正后的不混装许可: %+v", c1.Cargo[0])
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 30 || c2.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("拒绝后 C2 应为 G2 一件、已用 30 剩余 70: %+v", c2)
	}

	// 更正与失败提交都不能改写之前保存的旧预览快照。
	assertStalePreviewMixedSnapshot(t, old, "北京")
}

// TestStalePreviewSubmitAfterRevokeMixedPermissionSameDestinationSucceeds
// 保护同一规则的边界：若初始 G2 的目的地也为上海，取消 G1 的混装许可后
// 提交原安排应成功，因为最终三件货物目的地相同，混装许可不再构成冲突。
func TestStalePreviewSubmitAfterRevokeMixedPermissionSameDestinationSucceeds(t *testing.T) {
	r, ops, old := setupStalePreviewMixedScenario(t, "上海")

	// 只取消 G1 的混装许可；当前 C1 只有 G1，更正应成功。
	if err := r.AmendCargo("G1", 20, "上海", false); err != nil {
		t.Fatalf("C1 当前只有 G1，取消其混装许可应成功: %v", err)
	}

	// 不重新预览，直接用未使用过的编号提交原清单，应成功。
	res, err := r.Adjust("A1", ops)
	if err != nil {
		t.Fatalf("最终三件货物目的地相同，取消 G1 混装许可后应提交成功: %v", err)
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
	// C1 20 -> 60（G2 30、G3 10 并入），C2 30 -> 0。
	wantCompChanges := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 20, WeightAfter: 60},
		{CompartmentID: "C2", WeightBefore: 30, WeightAfter: 0},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
		t.Fatalf("舱位重量变化错误: %+v want %+v",
			res.CompartmentChanges, wantCompChanges)
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

	// 货物查询与舱位清单一致反映最新许可与归属：仅 G1 不允许混装，
	// 三件货物最终都在 C1 且目的地同为上海。
	wantCargo := map[string]struct {
		weight     int64
		allowMixed bool
	}{
		"G1": {20, false},
		"G2": {30, true},
		"G3": {10, true},
	}
	for id, want := range wantCargo {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatal(err)
		}
		if !cv.Loaded || cv.CompartmentID != "C1" ||
			cv.Weight != want.weight || cv.Destination != "上海" ||
			cv.AllowMixed != want.allowMixed {
			t.Fatalf("货物 %s 查询应反映最新许可与归属: %+v", id, cv)
		}
	}
	for _, cv := range c1.Cargo {
		want := wantCargo[cv.ID]
		if cv.Weight != want.weight || cv.Destination != "上海" ||
			cv.AllowMixed != want.allowMixed || !cv.Loaded ||
			cv.CompartmentID != "C1" {
			t.Fatalf("舱位清单中 %s 与货物查询不一致: %+v", cv.ID, cv)
		}
	}

	// 成功提交同样不能改写之前保存的旧预览快照。
	assertStalePreviewMixedSnapshot(t, old, "上海")
}

// TestStalePreviewSubmitAfterAmendExactCapacitySucceeds 保护承重恰好
// 相等的边界：G1 更正为 30 千克后直接提交原安排，C2 预计恰为 100
// 千克，应成功；返回的舱位重量变化依据提交时的实际重量而非旧预览。
func TestStalePreviewSubmitAfterAmendExactCapacitySucceeds(t *testing.T) {
	r, ops, old := setupStalePreviewScenario(t)

	// 更正 G1 重量 20 -> 30；目的地与混装许可保持原值。
	if err := r.AmendCargo("G1", 30, "X", false); err != nil {
		t.Fatalf("更正 G1 重量为 30 千克应成功: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if g1.Weight != 30 || g1.Destination != "X" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("更正后 G1 应以 30 千克留在 C1: %+v", g1)
	}

	// 不重新预览，直接用未使用过的编号提交原清单，应成功。
	res, err := r.Adjust("A1", ops)
	if err != nil {
		t.Fatalf("C2 预计恰好 100 千克等于承重，应提交成功: %v", err)
	}
	if res == nil || res.ID != "A1" {
		t.Fatalf("应返回编号为 A1 的成功调整结果: %+v", res)
	}
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G3", From: "", To: "C2"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
		t.Fatalf("货物变化错误: %+v want %+v", res.CargoChanges, wantChanges)
	}
	// 舱位重量变化依据提交时的实际重量（G1 已为 30 千克），
	// 而不是旧预览中的 20 千克：C1 30 -> 0，C2 60 -> 100。
	wantCompChanges := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 30, WeightAfter: 0},
		{CompartmentID: "C2", WeightBefore: 60, WeightAfter: 100},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
		t.Fatalf("舱位重量变化应按提交时实际重量: %+v want %+v",
			res.CompartmentChanges, wantCompChanges)
	}

	// C1 清空；C2 同时包含三件货物，已用 100 千克、剩余 0 千克。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("提交后 C1 应清空、已用 0 剩余 100: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 100 || c2.RemainingWeight != 0 ||
		c2.MaxWeight != 100 {
		t.Fatalf("提交后 C2 应已用 100、剩余 0: %+v", c2)
	}
	if got := cargoIDs(*c2); !reflect.DeepEqual(got, []string{"G1", "G2", "G3"}) {
		t.Fatalf("C2 应同时包含三件货物: %v", got)
	}

	// 货物查询与舱位清单中的重量、位置一致。
	wantCargo := map[string]int64{"G1": 30, "G2": 60, "G3": 10}
	for id, weight := range wantCargo {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatal(err)
		}
		if !cv.Loaded || cv.CompartmentID != "C2" || cv.Weight != weight {
			t.Fatalf("货物 %s 查询应为 %d 千克且位于 C2: %+v", id, weight, cv)
		}
	}
	for _, cv := range c2.Cargo {
		if cv.Weight != wantCargo[cv.ID] || !cv.Loaded || cv.CompartmentID != "C2" {
			t.Fatalf("舱位清单中 %s 的重量或位置与货物查询不一致: %+v", cv.ID, cv)
		}
	}

	// 成功提交同样不能改写之前保存的旧预览快照。
	assertStalePreviewSnapshot(t, old)
}
