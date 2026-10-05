package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本组回归测试保护这条使用过程：预览通过后，货物又被另一笔成功的调整
// 移走（或卸下、或直接移到原清单的目标舱位），不重新预览、直接按原
// 清单正式提交。预览只表示当时的预计配载，不预留舱位，也不固定货物
// 的来源；正式移动必须按提交时货物实际所在舱位处理。正式提交一律使用
// 尚未成功使用过的调整编号，避免把已成功编号返回的历史结果误当成执行
// 了一次新移动。

// setupStaleMoveScenario 建立共同的初始场景：
// C1（甲舱）、C2（乙舱）、C3（丙舱）承重均为 100 千克；G1 重 30 千克、
// 目的地 X、不允许混装，已装入 C1；G2 重 40 千克、目的地 X、不允许混装，
// 已装入 C2，作为全程不参与操作的其他货物。
//
// 返回登记处、“把 G1 从 C1 移到 C2”的原清单，以及对该清单的可提交预览
// （C1 预计清空，C2 预计含 G1、G2 共 70 千克、剩余 30 千克）。预览后
// 实际配载保持原样。
func setupStaleMoveScenario(t *testing.T) (*Registry, []Op, *PreviewResult) {
	t.Helper()
	r := NewRegistry()
	for _, id := range []string{"C1", "C2", "C3"} {
		if err := r.RegisterCompartment(id, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.RegisterCargo("G1", 30, "X", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 40, "X", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}

	ops := []Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}}
	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Submittable {
		t.Fatalf("G1 30 千克移入已有 G2 40 千克的 C2 共 70 千克，应可提交: %+v", pv.Rejections)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("原预览受影响舱位应为 C1、C2: %+v", pv.Compartments)
	}
	// 预览前：C1 仅 G1（30/70），C2 仅 G2（40/60）。
	c1 := pv.Compartments[0]
	if c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) {
		t.Fatalf("原预览 C1 调整前应为 G1 一件 30/70: %+v", c1.Before)
	}
	c2 := pv.Compartments[1]
	if c2.Before.UsedWeight != 40 || c2.Before.RemainingWeight != 60 ||
		!reflect.DeepEqual(cargoIDs(c2.Before), []string{"G2"}) {
		t.Fatalf("原预览 C2 调整前应为 G2 一件 40/60: %+v", c2.Before)
	}
	// 预览后：C1 清空；C2 含 G1、G2 共 70 千克、剩余 30 千克。
	if c1.After.UsedWeight != 0 || c1.After.RemainingWeight != 100 ||
		len(c1.After.Cargo) != 0 {
		t.Fatalf("原预览 C1 预计应清空: %+v", c1.After)
	}
	if c2.After.UsedWeight != 70 || c2.After.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(c2.After), []string{"G1", "G2"}) {
		t.Fatalf("原预览 C2 预计应含 G1、G2 共 70 千克、剩余 30 千克: %+v", c2.After)
	}

	// 预览只读：实际配载仍保持原样，C3 本来就空。
	gotC1, _ := r.Compartment("C1")
	if gotC1.UsedWeight != 30 || gotC1.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(*gotC1), []string{"G1"}) {
		t.Fatalf("预览后 C1 应保持 G1 一件 30/70: %+v", gotC1)
	}
	gotC2, _ := r.Compartment("C2")
	if gotC2.UsedWeight != 40 || gotC2.RemainingWeight != 60 ||
		!reflect.DeepEqual(cargoIDs(*gotC2), []string{"G2"}) {
		t.Fatalf("预览后 C2 应保持 G2 一件 40/60: %+v", gotC2)
	}
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("预览后 G1 应仍在 C1: %+v", g1)
	}
	return r, ops, pv
}

// assertStaleMoveSnapshot 断言旧预览快照始终保留最初的内容：可提交、
// 货物变化为 C1 到 C2、受影响舱位只有 C1 与 C2，前后清单与重量都是
// 预览当时的配载，不被后续移动、卸下或正式提交改写。
func assertStaleMoveSnapshot(t *testing.T, pv *PreviewResult) {
	t.Helper()
	if !pv.Submittable || len(pv.Rejections) != 0 {
		t.Fatalf("旧预览应仍为可提交且无拒绝原因: %+v", pv)
	}
	wantChanges := []CargoChange{{CargoID: "G1", From: "C1", To: "C2"}}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("旧预览的货物变化不应被改写: %+v want %+v", pv.CargoChanges, wantChanges)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("旧预览的受影响舱位不应被改写: %+v", pv.Compartments)
	}
	c1 := pv.Compartments[0]
	if !reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) ||
		c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 70 {
		t.Fatalf("旧预览 C1 的调整前配载不应被改写: %+v", c1.Before)
	}
	if len(c1.After.Cargo) != 0 || c1.After.UsedWeight != 0 || c1.After.RemainingWeight != 100 {
		t.Fatalf("旧预览 C1 的预计配载不应被改写: %+v", c1.After)
	}
	c2 := pv.Compartments[1]
	if !reflect.DeepEqual(cargoIDs(c2.Before), []string{"G2"}) ||
		c2.Before.UsedWeight != 40 || c2.Before.RemainingWeight != 60 {
		t.Fatalf("旧预览 C2 的调整前配载不应被改写: %+v", c2.Before)
	}
	if !reflect.DeepEqual(cargoIDs(c2.After), []string{"G1", "G2"}) ||
		c2.After.UsedWeight != 70 || c2.After.RemainingWeight != 30 {
		t.Fatalf("旧预览 C2 的预计配载不应被改写: %+v", c2.After)
	}
	// 旧快照里的 G1 始终保留 30 千克、目的地 X、不允许混装的登记资料。
	var g1 *CargoView
	for i := range c2.After.Cargo {
		if c2.After.Cargo[i].ID == "G1" {
			g1 = &c2.After.Cargo[i]
		}
	}
	if g1 == nil {
		t.Fatalf("旧预览 C2 预计清单仍应包含 G1: %+v", c2.After.Cargo)
	}
	if g1.Weight != 30 || g1.Destination != "X" || g1.AllowMixed {
		t.Fatalf("旧预览应保留 G1 的原始登记资料: %+v", g1)
	}
}

// TestStalePreviewSubmitAfterInterveningMoveSucceeds 覆盖核心回归：
// 预览把 G1（30 千克）从 C1 移到 C2 并显示可提交后，另一笔成功调整先把
// G1 从 C1 移到 C3，C2 仍能接收它（重量、目的地、混装许可均未改变）。
// 不重新预览、用未成功使用过的新编号直接提交原移动安排，应按提交时的
// 实际配载成功：货物变化为 C3 到 C2；C3 收回 30 千克、C2 增加 30 千克；
// C1 不再是受影响舱位，重量不能再次扣减。
func TestStalePreviewSubmitAfterInterveningMoveSucceeds(t *testing.T) {
	r, ops, old := setupStaleMoveScenario(t)

	// 另一笔调整先把 G1 从 C1 移到 C3。
	first := mustAdjust(t, r, "A1", []Op{{Kind: OpMove, CargoID: "G1", Target: "C3"}})
	if !reflect.DeepEqual(first.CargoChanges, []CargoChange{{CargoID: "G1", From: "C1", To: "C3"}}) {
		t.Fatalf("另一笔调整的货物变化应为 C1 到 C3: %+v", first.CargoChanges)
	}
	// 这笔调整只影响 C1 与 C3，与尚未执行的原清单目标 C2 无关。
	if !reflect.DeepEqual(first.CompartmentChanges, []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 30, WeightAfter: 0},
		{CompartmentID: "C3", WeightBefore: 0, WeightAfter: 30},
	}) {
		t.Fatalf("另一笔调整的舱位重量变化错误: %+v", first.CompartmentChanges)
	}
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C3" ||
		g1.Weight != 30 || g1.Destination != "X" || g1.AllowMixed {
		t.Fatalf("另一笔调整后 G1 应带着原登记资料位于 C3: %+v", g1)
	}
	c3, _ := r.Compartment("C3")
	if c3.UsedWeight != 30 || c3.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(*c3), []string{"G1"}) {
		t.Fatalf("另一笔调整后 C3 应含 G1 一件 30/70: %+v", c3)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || len(c1.Cargo) != 0 {
		t.Fatalf("另一笔调整后 C1 应已清空: %+v", c1)
	}

	// 用尚未成功使用过的编号 A2 提交原清单（G1 -> C2），按提交时实际
	// 所在的 C3 处理，应成功。
	res, err := r.Adjust("A2", ops)
	if err != nil {
		t.Fatalf("G1 已在 C3 且 C2 仍可接收，提交原安排应成功: %v", err)
	}
	if res == nil || res.ID != "A2" {
		t.Fatalf("应返回编号为 A2 的成功调整结果: %+v", res)
	}
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{{CargoID: "G1", From: "C3", To: "C2"}}) {
		t.Fatalf("货物变化应按提交时实际位置记为 C3 到 C2: %+v", res.CargoChanges)
	}
	// 重量变化反映此次提交前后的实际配载：C2 40 -> 70，C3 30 -> 0。
	// C1 不受此次移动影响，不能列为受影响舱位，也不能再次扣减。
	if !reflect.DeepEqual(res.CompartmentChanges, []CompartmentChange{
		{CompartmentID: "C2", WeightBefore: 40, WeightAfter: 70},
		{CompartmentID: "C3", WeightBefore: 30, WeightAfter: 0},
	}) {
		t.Fatalf("舱位重量变化应为 C2 增 30、C3 收 30，且不含 C1: %+v", res.CompartmentChanges)
	}

	// 提交后按货物查询：G1 只在 C2，登记资料不变。
	g1, _ = r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C2" ||
		g1.Weight != 30 || g1.Destination != "X" || g1.AllowMixed {
		t.Fatalf("提交后 G1 应带着原登记资料只位于 C2: %+v", g1)
	}
	// 按舱位查看清单：C1、C3 清空，C2 含 G1、G2 共 70 千克、剩余 30 千克。
	c1, _ = r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("提交后 C1 应保持清空、已用 0 剩余 100（不能再次扣减）: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 70 || c2.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
		t.Fatalf("提交后 C2 应含 G1、G2 共 70 千克、剩余 30 千克: %+v", c2)
	}
	c3, _ = r.Compartment("C3")
	if c3.UsedWeight != 0 || c3.RemainingWeight != 100 || len(c3.Cargo) != 0 {
		t.Fatalf("提交后 C3 应已移除 G1、保持清空: %+v", c3)
	}
	// 其他货物保持原状，且货物查询与舱位清单一致。
	g2, _ := r.Cargo("G2")
	if !g2.Loaded || g2.CompartmentID != "C2" || g2.Weight != 40 {
		t.Fatalf("G2 应保持原状留在 C2: %+v", g2)
	}
	for _, cv := range c2.Cargo {
		cargo, _ := r.Cargo(cv.ID)
		if !cv.Loaded || cv.CompartmentID != "C2" ||
			cargo.CompartmentID != "C2" || cargo.Weight != cv.Weight {
			t.Fatalf("舱位清单与货物查询对 %s 的记录不一致: 清单=%+v 查询=%+v", cv.ID, cv, cargo)
		}
	}

	// 已成功使用过的 A1 以原内容再次提交，只返回首次的历史结果
	// （C1 到 C3），不按当前位置执行任何新移动：G1 必须仍在 C2。
	again, err := r.Adjust("A1", []Op{{Kind: OpMove, CargoID: "G1", Target: "C3"}})
	if err != nil {
		t.Fatalf("A1 以原内容再次提交应返回首次结果: %v", err)
	}
	if !reflect.DeepEqual(again.CargoChanges, first.CargoChanges) ||
		!reflect.DeepEqual(again.CompartmentChanges, first.CompartmentChanges) {
		t.Fatalf("A1 再次提交应原样返回首次历史结果: again=%+v first=%+v", again, first)
	}
	g1, _ = r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C2" {
		t.Fatalf("历史结果不能当成新移动执行，G1 应仍在 C2: %+v", g1)
	}
	c2, _ = r.Compartment("C2")
	if c2.UsedWeight != 70 || !reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
		t.Fatalf("历史结果返回后 C2 配载不应改变: %+v", c2)
	}
	c3, _ = r.Compartment("C3")
	if c3.UsedWeight != 0 || len(c3.Cargo) != 0 {
		t.Fatalf("历史结果返回后 C3 应仍清空: %+v", c3)
	}

	// 后续移动与正式提交都不能改写保存下来的旧预览快照。
	assertStaleMoveSnapshot(t, old)
}

// TestStalePreviewSubmitAfterInterveningUnloadStateMismatch 保护不能移动的
// 情况之一：另一笔调整已把 G1 卸下，再提交原来的移动安排，必须返回现有
// 的“状态不符”错误并指出 G1，不能把移动变成重新装载；不返回成功调整
// 结果，错误携带本次提交编号 A2，配载保持另一笔调整完成后的状态。
func TestStalePreviewSubmitAfterInterveningUnloadStateMismatch(t *testing.T) {
	r, ops, old := setupStaleMoveScenario(t)

	// 另一笔调整先把 G1 从 C1 卸下。
	first := mustAdjust(t, r, "A1", []Op{{Kind: OpUnload, CargoID: "G1"}})
	if !reflect.DeepEqual(first.CargoChanges, []CargoChange{{CargoID: "G1", From: "C1", To: ""}}) {
		t.Fatalf("另一笔调整的货物变化应为 C1 卸下: %+v", first.CargoChanges)
	}
	if !reflect.DeepEqual(first.CompartmentChanges, []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 30, WeightAfter: 0},
	}) {
		t.Fatalf("另一笔调整的舱位重量变化错误: %+v", first.CompartmentChanges)
	}
	g1, _ := r.Cargo("G1")
	if g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("另一笔调整后 G1 应已卸下、记录保留: %+v", g1)
	}

	// 直接提交原移动安排：状态不符，不能当成重新装入 C2。
	res, err := r.Adjust("A2", ops)
	se := requireError(t, err, ErrStateMismatch)
	if res != nil {
		t.Fatalf("货物已卸下时必须拒绝移动，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("状态不符错误应指出货物 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A2" {
		t.Fatalf("错误应携带本次提交的调整编号 A2，实际 %q", se.AdjustmentID)
	}
	if msg := se.Error(); !strings.Contains(msg, "G1") {
		t.Fatalf("状态不符说明应指出货物 G1: %s", msg)
	}

	// 配载保留另一笔调整完成后的状态：G1 仍卸下，没有被装入 C2。
	g1, _ = r.Cargo("G1")
	if g1.Loaded || g1.CompartmentID != "" ||
		g1.Weight != 30 || g1.Destination != "X" || g1.AllowMixed {
		t.Fatalf("拒绝后 G1 应保持已卸下且登记资料不变: %+v", g1)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || len(c1.Cargo) != 0 {
		t.Fatalf("拒绝后 C1 应保持清空: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 40 || c2.RemainingWeight != 60 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("拒绝后 C2 应仍只有 G2 一件 40/60，不能把 G1 装进来: %+v", c2)
	}

	// 失败不占用编号：同一编号 A2 再次提交仍得到同样的状态不符，
	// 而不是“调整编号冲突”，配载依旧不变。
	res2, err2 := r.Adjust("A2", ops)
	se2 := requireError(t, err2, ErrStateMismatch)
	if res2 != nil {
		t.Fatalf("失败的调整不应占用编号，也不应返回成功结果: %+v", res2)
	}
	if se2.ID != "G1" || se2.AdjustmentID != "A2" {
		t.Fatalf("再次提交应仍为指出 G1、携带 A2 的状态不符: %+v", se2)
	}
	g1, _ = r.Cargo("G1")
	if g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("再次失败后 G1 应仍保持卸下: %+v", g1)
	}

	// 卸下与失败提交都不能改写保存下来的旧预览快照。
	assertStaleMoveSnapshot(t, old)
}

// TestStalePreviewSubmitAfterAlreadyAtTargetDuplicate 保护不能移动的情况
// 之二：另一笔调整已把 G1 移到原清单的目标 C2，再提交原安排必须返回现有
// 的“重复操作”错误并指出 G1，不能当作一次成功移动，也不能重复占用重量；
// 不返回成功调整结果，错误携带本次提交编号 A2，配载保持另一笔调整完成后
// 的状态。
func TestStalePreviewSubmitAfterAlreadyAtTargetDuplicate(t *testing.T) {
	r, ops, old := setupStaleMoveScenario(t)

	// 另一笔调整先把 G1 从 C1 移到原清单的目标 C2。
	first := mustAdjust(t, r, "A1", []Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}})
	if !reflect.DeepEqual(first.CargoChanges, []CargoChange{{CargoID: "G1", From: "C1", To: "C2"}}) {
		t.Fatalf("另一笔调整的货物变化应为 C1 到 C2: %+v", first.CargoChanges)
	}
	if !reflect.DeepEqual(first.CompartmentChanges, []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 30, WeightAfter: 0},
		{CompartmentID: "C2", WeightBefore: 40, WeightAfter: 70},
	}) {
		t.Fatalf("另一笔调整的舱位重量变化错误: %+v", first.CompartmentChanges)
	}

	// 再提交原移动安排：G1 已在目标舱位 C2，属于重复操作。
	res, err := r.Adjust("A2", ops)
	se := requireError(t, err, ErrDuplicateOp)
	if res != nil {
		t.Fatalf("货物已在目标舱位时必须拒绝，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("重复操作错误应指出货物 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A2" {
		t.Fatalf("错误应携带本次提交的调整编号 A2，实际 %q", se.AdjustmentID)
	}
	if msg := se.Error(); !strings.Contains(msg, "G1") || !strings.Contains(msg, "C2") {
		t.Fatalf("重复操作说明应指出货物 G1 与目标舱位 C2: %s", msg)
	}

	// 不能当作成功移动，也不能重复占用重量：G1 在 C2 清单中只出现一次，
	// C2 已用仍为 70 千克；C1 保持清空。
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C2" ||
		g1.Weight != 30 || g1.Destination != "X" || g1.AllowMixed {
		t.Fatalf("拒绝后 G1 应仍只位于 C2、登记资料不变: %+v", g1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 70 || c2.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
		t.Fatalf("拒绝后 C2 应仍只含 G1、G2 共 70 千克，重量不能重复占用: %+v", c2)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("拒绝后 C1 应保持清空: %+v", c1)
	}
	g2, _ := r.Cargo("G2")
	if !g2.Loaded || g2.CompartmentID != "C2" || g2.Weight != 40 {
		t.Fatalf("G2 应保持原状留在 C2: %+v", g2)
	}

	// 失败不占用编号：同一编号 A2 再次提交仍得到同样的重复操作错误，
	// 而不是“调整编号冲突”，重量仍不重复占用。
	res2, err2 := r.Adjust("A2", ops)
	se2 := requireError(t, err2, ErrDuplicateOp)
	if res2 != nil {
		t.Fatalf("失败的调整不应占用编号，也不应返回成功结果: %+v", res2)
	}
	if se2.ID != "G1" || se2.AdjustmentID != "A2" {
		t.Fatalf("再次提交应仍为指出 G1、携带 A2 的重复操作: %+v", se2)
	}
	c2, _ = r.Compartment("C2")
	if c2.UsedWeight != 70 || c2.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
		t.Fatalf("再次失败后 C2 重量与清单应保持不变: %+v", c2)
	}

	// 移动与失败提交都不能改写保存下来的旧预览快照。
	assertStaleMoveSnapshot(t, old)
}
