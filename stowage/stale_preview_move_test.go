package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本组回归测试保护这条使用过程：预览把货物从甲舱移到乙舱通过后，另一笔
// 成功的调整先把货物移走（或卸下），用户不重新预览、直接以未使用过的调整
// 编号提交原移动安排。预览只表示当时的预计配载，不预留舱位、不固定货物
// 来源；正式提交必须按提交时货物实际所在舱位处理，旧预览快照本身也不能
// 被后续移动、卸下或正式提交改写。

// setupStaleMoveScenario 建立共同的初始场景：
// C1（甲）、C2（乙）、C3（丙）承重均为 100 千克；G1 重 30 千克已装入
// C1，G2 重 20 千克已装入 C2；两件货物目的地相同，均不允许混装。
//
// 返回登记处、“把 G1 移到 C2”的原清单，以及对该清单的可提交预览
// （C1 预计清空，C2 预计两件货物共 50 千克）。预览后实际配载保持原样。
func setupStaleMoveScenario(t *testing.T) (*Registry, []Op, *PreviewResult) {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C3", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", false); err != nil {
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
	}
	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Submittable {
		t.Fatalf("原清单预计 C2 共 50 千克，应可提交: %+v", pv.Rejections)
	}
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("预览货物变化应为 G1 从 C1 到 C2: %+v", pv.CargoChanges)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("受影响舱位应为 C1、C2（不含 C3）: %+v", pv.Compartments)
	}
	// C1 预计清空；C2 预计含 G1、G2 共 50 千克。
	c1After := pv.Compartments[0].After
	if len(c1After.Cargo) != 0 || c1After.UsedWeight != 0 ||
		c1After.RemainingWeight != 100 {
		t.Fatalf("C1 预计应清空: %+v", c1After)
	}
	c2After := pv.Compartments[1].After
	if c2After.UsedWeight != 50 || c2After.RemainingWeight != 50 ||
		!reflect.DeepEqual(cargoIDs(c2After), []string{"G1", "G2"}) {
		t.Fatalf("C2 预计应含两件货物共 50 千克、剩余 50 千克: %+v", c2After)
	}

	// 预览只读：实际配载仍保持原样。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 30 || c1.RemainingWeight != 70 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("预览后 C1 应保持 G1 一件 30/70: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 20 || c2.RemainingWeight != 80 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("预览后 C2 应保持 G2 一件 20/80: %+v", c2)
	}
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("预览后 G1 应仍在 C1: %+v", g1)
	}
	return r, ops, pv
}

// assertStaleMoveSnapshot 断言旧预览快照仍保留最初的内容：可提交、货物
// 变化仍为 C1 到 C2、前后清单与重量不变——后续移动、卸下或正式提交都不
// 能改写它；它与最新查询结果不同是正常的。
func assertStaleMoveSnapshot(t *testing.T, pv *PreviewResult) {
	t.Helper()
	if !pv.Submittable || len(pv.Rejections) != 0 {
		t.Fatalf("旧预览应仍为可提交且无拒绝原因: %+v", pv)
	}
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("旧预览的货物变化不应被改写: %+v want %+v", pv.CargoChanges, wantChanges)
	}
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("旧预览的舱位清单不应被改写: %+v", pv.Compartments)
	}
	c1 := pv.Compartments[0]
	if !reflect.DeepEqual(cargoIDs(c1.Before), []string{"G1"}) ||
		c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 70 {
		t.Fatalf("旧预览应保留 C1 调整前 G1 一件 30/70: %+v", c1.Before)
	}
	if len(c1.After.Cargo) != 0 || c1.After.UsedWeight != 0 ||
		c1.After.RemainingWeight != 100 {
		t.Fatalf("旧预览应保留 C1 预计清空: %+v", c1.After)
	}
	c2 := pv.Compartments[1]
	if !reflect.DeepEqual(cargoIDs(c2.Before), []string{"G2"}) ||
		c2.Before.UsedWeight != 20 || c2.Before.RemainingWeight != 80 {
		t.Fatalf("旧预览应保留 C2 调整前 G2 一件 20/80: %+v", c2.Before)
	}
	if !reflect.DeepEqual(cargoIDs(c2.After), []string{"G1", "G2"}) ||
		c2.After.UsedWeight != 50 || c2.After.RemainingWeight != 50 {
		t.Fatalf("旧预览应保留 C2 预计两件货物 50/50: %+v", c2.After)
	}
}

// TestStalePreviewMoveSubmitAfterOtherMoveSucceeds 覆盖核心回归：预览可
// 提交后，另一笔调整先把 G1 从 C1 移到 C3；G1 的重量、目的地与混装许可
// 都没有改变，C2 仍能接收它。不重新预览、直接以未使用过的编号提交原
// 移动安排应成功，货物变化按提交时实际所在舱位记为 C3 到 C2，舱位重量
// 变化反映此次提交前后的实际配载，C1 不列为受影响舱位、不再扣减重量。
func TestStalePreviewMoveSubmitAfterOtherMoveSucceeds(t *testing.T) {
	r, ops, old := setupStaleMoveScenario(t)

	// 另一笔调整先把 G1 移到 C3。
	inter, err := r.Adjust("A1", []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C3"},
	})
	if err != nil {
		t.Fatalf("另一笔调整应成功: %v", err)
	}
	if inter.CargoChanges[0].From != "C1" || inter.CargoChanges[0].To != "C3" {
		t.Fatalf("另一笔调整应为 G1 从 C1 到 C3: %+v", inter.CargoChanges)
	}
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C3" {
		t.Fatalf("另一笔调整后 G1 应在 C3: %+v", g1)
	}

	// 保留旧预览，直接用一个未成功使用过的调整编号提交原移动安排。
	res, err := r.Adjust("A2", ops)
	if err != nil {
		t.Fatalf("货物重量、目的地与混装许可均未变，C2 仍能接收，应提交成功: %v", err)
	}
	if res == nil || res.ID != "A2" {
		t.Fatalf("应返回编号为 A2 的成功调整结果: %+v", res)
	}
	// 货物变化按提交时实际所在舱位：C3 到 C2，而不是旧预览的 C1 到 C2。
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "C3", To: "C2"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
		t.Fatalf("货物变化应为 C3 到 C2: %+v want %+v", res.CargoChanges, wantChanges)
	}
	// 舱位重量变化反映提交前后的实际配载：C2 20 -> 50，C3 30 -> 0；
	// C1 不是此次移动的受影响舱位，不能再次扣减重量。
	wantCompChanges := []CompartmentChange{
		{CompartmentID: "C2", WeightBefore: 20, WeightAfter: 50},
		{CompartmentID: "C3", WeightBefore: 30, WeightAfter: 0},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
		t.Fatalf("舱位重量变化应只含 C2、C3: %+v want %+v",
			res.CompartmentChanges, wantCompChanges)
	}

	// 货物查询与舱位清单一致：G1 只在 C2，C3 已移除它，其他货物保持原状。
	g1, _ = r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C2" || g1.Weight != 30 {
		t.Fatalf("提交后 G1 应为 30 千克且只在 C2: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if !g2.Loaded || g2.CompartmentID != "C2" || g2.Weight != 20 {
		t.Fatalf("提交后 G2 应保持 20 千克在 C2: %+v", g2)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("C1 应保持另一笔调整后的空舱状态，不得再次扣减: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 50 || c2.RemainingWeight != 50 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
		t.Fatalf("提交后 C2 应含 G1、G2 共 50 千克、剩余 50 千克: %+v", c2)
	}
	c3, _ := r.Compartment("C3")
	if c3.UsedWeight != 0 || c3.RemainingWeight != 100 || len(c3.Cargo) != 0 {
		t.Fatalf("提交后 C3 应收回 30 千克、已清空: %+v", c3)
	}
	for _, cv := range c2.Cargo {
		if !cv.Loaded || cv.CompartmentID != "C2" {
			t.Fatalf("舱位清单中 %s 的位置与货物查询不一致: %+v", cv.ID, cv)
		}
	}

	// 后续移动与正式提交都不能改写之前保存的旧预览快照。
	assertStaleMoveSnapshot(t, old)
}

// TestStalePreviewMoveSubmitAfterUnloadRejected 保护不能移动的情况之一：
// 另一笔调整已把 G1 卸下，再提交原移动安排应返回“状态不符”错误并指出
// G1，不能把移动变成重新装载；失败不返回成功调整结果，错误携带本次提交
// 的调整编号，配载保留另一笔调整完成后的状态。
func TestStalePreviewMoveSubmitAfterUnloadRejected(t *testing.T) {
	r, ops, old := setupStaleMoveScenario(t)

	// 另一笔调整先把 G1 卸下。
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpUnload, CargoID: "G1"},
	}); err != nil {
		t.Fatalf("另一笔调整应成功: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("另一笔调整后 G1 应未装载: %+v", g1)
	}

	// 保留旧预览，直接以未使用过的编号提交原移动安排。
	res, err := r.Adjust("A2", ops)
	se := requireError(t, err, ErrStateMismatch)
	if res != nil {
		t.Fatalf("货物已卸下时必须拒绝，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("状态不符错误应指出货物 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A2" {
		t.Fatalf("状态不符错误应携带调整编号 A2，实际 %q", se.AdjustmentID)
	}
	if msg := se.Error(); !strings.Contains(msg, "G1") {
		t.Fatalf("状态不符说明应指出货物 G1: %s", msg)
	}

	// 配载保留另一笔调整完成后的状态：G1 未装载，移动没有变成重新装载。
	g1, _ = r.Cargo("G1")
	if g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("拒绝后 G1 应仍未装载: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if !g2.Loaded || g2.CompartmentID != "C2" || g2.Weight != 20 {
		t.Fatalf("拒绝后 G2 应保持 20 千克在 C2: %+v", g2)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("拒绝后 C1 应保持空舱: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 20 || c2.RemainingWeight != 80 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G2"}) {
		t.Fatalf("拒绝后 C2 应保持 G2 一件 20/80: %+v", c2)
	}
	c3, _ := r.Compartment("C3")
	if c3.UsedWeight != 0 || c3.RemainingWeight != 100 || len(c3.Cargo) != 0 {
		t.Fatalf("拒绝后 C3 应保持空舱: %+v", c3)
	}

	// 卸下与失败提交都不能改写之前保存的旧预览快照。
	assertStaleMoveSnapshot(t, old)
}

// TestStalePreviewMoveSubmitAfterMoveToTargetRejected 保护不能移动的情况
// 之二：另一笔调整已把 G1 移到原清单的目标 C2，再提交原移动安排应返回
// “重复操作”错误并指出 G1，不能当作一次成功移动，也不能重复占用重量；
// 失败不返回成功调整结果，错误携带本次提交的调整编号，配载保留另一笔
// 调整完成后的状态。
func TestStalePreviewMoveSubmitAfterMoveToTargetRejected(t *testing.T) {
	r, ops, old := setupStaleMoveScenario(t)

	// 另一笔调整先把 G1 移到原清单的目标 C2。
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
	}); err != nil {
		t.Fatalf("另一笔调整应成功: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C2" {
		t.Fatalf("另一笔调整后 G1 应在 C2: %+v", g1)
	}

	// 保留旧预览，直接以未使用过的编号提交原移动安排。
	res, err := r.Adjust("A2", ops)
	se := requireError(t, err, ErrDuplicateOp)
	if res != nil {
		t.Fatalf("货物已在目标舱位时必须拒绝，不应返回成功调整结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("重复操作错误应指出货物 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A2" {
		t.Fatalf("重复操作错误应携带调整编号 A2，实际 %q", se.AdjustmentID)
	}
	if msg := se.Error(); !strings.Contains(msg, "G1") || !strings.Contains(msg, "C2") {
		t.Fatalf("重复操作说明应指出货物 G1 与其所在舱位 C2: %s", msg)
	}

	// 配载保留另一笔调整完成后的状态：G1 在 C2，重量没有重复占用。
	g1, _ = r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C2" || g1.Weight != 30 {
		t.Fatalf("拒绝后 G1 应保持 30 千克在 C2: %+v", g1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 50 || c2.RemainingWeight != 50 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
		t.Fatalf("拒绝后 C2 应保持 G1、G2 共 50 千克，不得重复占用: %+v", c2)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("拒绝后 C1 应保持空舱: %+v", c1)
	}
	c3, _ := r.Compartment("C3")
	if c3.UsedWeight != 0 || c3.RemainingWeight != 100 || len(c3.Cargo) != 0 {
		t.Fatalf("拒绝后 C3 应保持空舱: %+v", c3)
	}

	// 后续移动与失败提交都不能改写之前保存的旧预览快照。
	assertStaleMoveSnapshot(t, old)
}
