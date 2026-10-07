package stowage

import (
	"reflect"
	"testing"
)

// 本文件为配载预览（Preview）补充回归保障，重点保护“每条操作本身合法、
// 但整批完成后触发混装冲突”时，拒绝原因对整批操作完成后舱内货物的描述：
//
// 主场景中一个承重充足的舱位已装着同一目的地的多件货物（两件不允许混装、
// 其余允许），一批安排同时卸下一件不允许混装的货物、装入另一目的地的两件
// 货物（一件允许、一件不允许）。Preview 必须成功返回完整结果，但判定不可
// 提交，唯一的拒绝原因是该舱位的混装冲突。混装说明按最终留在舱内的货物
// 生成：
//   - 目的地分组包含最终留舱的全部货物，允许混装的货物也不能省略；同一
//     目的地的多件货物归在同一组，每件只出现一次；
//   - 已安排卸下的货物不再属于任何预计分组，也不能继续被列为不允许混装
//     的货物；违规清单同时指出原来留舱与新装入的两件不允许混装货物；
//   - 分组与预计舱位货物清单逐件对应，目的地与混装许可沿用登记资料，
//     目的地按字典序、各分组货物与违规清单按货物编号字典序排列，改变
//     操作书写顺序不改变任何内容。
//
// 另保护一个边界：把原目的地货物全部卸下、最终只剩另一目的地的多件货物时，
// 即使其中有货物不允许混装，也应可提交且没有混装拒绝原因。两种预览都只读，
// 实际货物位置、舱位占用与货物资料仍为预览前的配载。

// 主场景初始配载：
// C1 承重 200 千克，装着三件去上海的货物：
//   - G1 20 千克、不允许混装（留舱）；
//   - G2 20 千克、不允许混装（本批卸下）；
//   - G3 10 千克、允许混装（留舱）。
//
// 另有两件尚未装载、去北京的货物：
//   - G4 10 千克、允许混装（本批装入 C1）；
//   - G5 10 千克、不允许混装（本批装入 C1）。
//
// C1 初始合计 50/200 千克。主批安排为“卸下 G2、装入 G4、装入 G5”，
// 每条操作本身合法；整批完成后 C1 仍为 50 千克（卸下 20、装入 20），
// 不超重，但同时含上海与北京货物且 G1、G5 不允许混装，故不可提交。
func setupBatchMixedPreviewRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 200)
	mustRegisterCargo(t, r, "G1", 20, "上海", false)
	mustRegisterCargo(t, r, "G2", 20, "上海", false)
	mustRegisterCargo(t, r, "G3", 10, "上海", true)
	mustRegisterCargo(t, r, "G4", 10, "北京", true)
	mustRegisterCargo(t, r, "G5", 10, "北京", false)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})

	// 初始配载固化为测试前提：C1 只含 G1、G2、G3 共 50 千克，G4、G5 未装载。
	c1, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.UsedWeight != 50 || c1.RemainingWeight != 150 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G2", "G3"}) {
		t.Fatalf("初始配载应为 G1、G2、G3 共 50/200 千克: %+v", c1)
	}
	for _, id := range []string{"G4", "G5"} {
		cv, _ := r.Cargo(id)
		if cv.Loaded || cv.CompartmentID != "" {
			t.Fatalf("货物 %s 初始应未装载: %+v", id, cv)
		}
	}
	return r
}

// batchMixedPreviewOps 是主批安排：卸下 G2，装入 G4、G5。
func batchMixedPreviewOps() []Op {
	return []Op{
		{Kind: OpUnload, CargoID: "G2"},
		{Kind: OpLoad, CargoID: "G4", Target: "C1"},
		{Kind: OpLoad, CargoID: "G5", Target: "C1"},
	}
}

// permuteOps 返回一组操作的全部排列，用于保护结果与操作书写顺序无关。
func permuteOps(base []Op) [][]Op {
	out := make([][]Op, 0)
	var perm func(int)
	perm = func(start int) {
		if start == len(base) {
			out = append(out, append([]Op(nil), base...))
			return
		}
		for i := start; i < len(base); i++ {
			base[start], base[i] = base[i], base[start]
			perm(start + 1)
			base[start], base[i] = base[i], base[start]
		}
	}
	perm(0)
	return out
}

// 主场景：Preview 成功返回完整结果但不可提交，混装说明完整描述整批完成后
// 的舱内货物。该保障对卸下与两次装入的全部 6 种书写次序成立，且次序不同时
// 整个 PreviewResult 必须逐字段一致。
func TestPreviewBatchUnloadAndLoadMixedExplanationComplete(t *testing.T) {
	var want *PreviewResult
	for i, ops := range permuteOps(batchMixedPreviewOps()) {
		r := setupBatchMixedPreviewRegistry(t)
		compBefore := snapshotCompartments(r, "C1")
		cargoBefore := snapshotCargo(r, "G1", "G2", "G3", "G4", "G5")

		res, err := r.Preview(ops)
		if err != nil {
			t.Fatalf("第 %d 种次序：每条操作本身合法，Preview 不应返回错误: %v", i, err)
		}
		if res == nil {
			t.Fatalf("第 %d 种次序：应返回完整预览结果", i)
		}

		// 不可提交，且唯一原因是 C1 的混装冲突（最终仍为 50/200，不超重）。
		if res.Submittable {
			t.Fatalf("第 %d 种次序：整批后上海/北京共舱且有货物禁混，应不可提交: %+v", i, res)
		}
		if len(res.Rejections) != 1 {
			t.Fatalf("第 %d 种次序：应只有 C1 混装一条拒绝原因: %+v", i, res.Rejections)
		}
		rej := res.Rejections[0]
		if rej.Kind != ErrMixedLoading || rej.CompartmentID != "C1" {
			t.Fatalf("第 %d 种次序：拒绝原因应为 C1 混装冲突: %+v", i, rej)
		}
		if rej.MaxWeight != 200 {
			t.Fatalf("第 %d 种次序：混装原因应沿用舱位承重 200: %+v", i, rej)
		}
		// 混装原因不携带重量数值。
		if rej.UsedWeight != 0 || rej.RemainingWeight != 0 || rej.Overweight != 0 {
			t.Fatalf("第 %d 种次序：混装原因不应携带重量数值: %+v", i, rej)
		}

		// 目的地分组按目的地字典序：先上海后北京；每组只含最终留在 C1 的
		// 货物，组内按货物编号字典序。已卸下的 G2 不属于任何分组；允许
		// 混装的 G3、G4 同样不能省略。
		wantDests := []MixedDestination{
			{Destination: "上海", CargoIDs: []string{"G1", "G3"}},
			{Destination: "北京", CargoIDs: []string{"G4", "G5"}},
		}
		if !reflect.DeepEqual(rej.Destinations, wantDests) {
			t.Fatalf("第 %d 种次序：目的地分组错误: got=%+v want=%+v",
				i, rej.Destinations, wantDests)
		}

		// 违规清单同时指出原舱留舱的 G1 与本批装入的 G5，按编号字典序；
		// 已卸下的 G2（虽不允许混装）与允许混装的 G3、G4 都不得列入。
		if !reflect.DeepEqual(rej.OffendingCargo, []string{"G1", "G5"}) {
			t.Fatalf("第 %d 种次序：不允许混装货物清单错误: got=%v want=[G1 G5]",
				i, rej.OffendingCargo)
		}

		// 只列出受影响舱位 C1。
		if len(res.Compartments) != 1 || res.Compartments[0].ID != "C1" {
			t.Fatalf("第 %d 种次序：应只列出受影响舱位 C1: %+v", i, res.Compartments)
		}
		cp := res.Compartments[0]

		// 调整前清单反映原配载：G1、G2、G3 共 50 千克，保持真实位置。
		if cp.Before.UsedWeight != 50 || cp.Before.RemainingWeight != 150 ||
			!reflect.DeepEqual(cargoIDs(cp.Before), []string{"G1", "G2", "G3"}) {
			t.Fatalf("第 %d 种次序：调整前清单应反映原配载: %+v", i, cp.Before)
		}
		for _, cv := range cp.Before.Cargo {
			if !cv.Loaded || cv.CompartmentID != "C1" {
				t.Fatalf("第 %d 种次序：调整前清单货物应保持真实位置: %+v", i, cv)
			}
		}

		// 预计清单反映卸下与装入后的安排：留舱的 G1、G3 与新装入的 G4、G5，
		// 卸下的 G2 不在其中；卸下 20、装入 20，总重量仍为 50/200。
		wantAfterIDs := []string{"G1", "G3", "G4", "G5"}
		if cp.After.UsedWeight != 50 || cp.After.RemainingWeight != 150 ||
			!reflect.DeepEqual(cargoIDs(cp.After), wantAfterIDs) {
			t.Fatalf("第 %d 种次序：预计清单应为 G1、G3、G4、G5 共 50 千克: %+v",
				i, cp.After)
		}

		// 预计清单逐件沿用登记资料（重量、目的地、混装许可），位置显示为
		// 已装载且属于 C1；混装分组与这份清单逐件对应。
		wantProfile := map[string]struct {
			weight int64
			dest   string
			mixed  bool
		}{
			"G1": {20, "上海", false},
			"G3": {10, "上海", true},
			"G4": {10, "北京", true},
			"G5": {10, "北京", false},
		}
		grouped := make(map[string]int)
		var flatIDs []string
		for _, d := range rej.Destinations {
			for _, id := range d.CargoIDs {
				grouped[id]++
				flatIDs = append(flatIDs, id)
			}
		}
		if len(cp.After.Cargo) != len(grouped) {
			t.Fatalf("第 %d 种次序：分组货物数 %d 与预计清单 %d 件不一致",
				i, len(grouped), len(cp.After.Cargo))
		}
		for _, cv := range cp.After.Cargo {
			p, ok := wantProfile[cv.ID]
			if !ok {
				t.Fatalf("第 %d 种次序：预计清单出现意外货物 %s", i, cv.ID)
			}
			if cv.Weight != p.weight || cv.Destination != p.dest || cv.AllowMixed != p.mixed {
				t.Fatalf("第 %d 种次序：货物 %s 的登记资料在预计清单中被改写: %+v",
					i, cv.ID, cv)
			}
			if !cv.Loaded || cv.CompartmentID != "C1" {
				t.Fatalf("第 %d 种次序：预计清单中 %s 应显示已装载于 C1: %+v",
					i, cv.ID, cv)
			}
			// 每件货物在分组中恰好出现一次。
			if grouped[cv.ID] != 1 {
				t.Fatalf("第 %d 种次序：货物 %s 在分组中应恰好出现一次，实际 %d 次",
					i, cv.ID, grouped[cv.ID])
			}
		}
		// 分组按目的字典序、组内按编号字典序摊平后，与预计清单顺序一致。
		if !reflect.DeepEqual(flatIDs, wantAfterIDs) {
			t.Fatalf("第 %d 种次序：混装分组与预计清单未逐件对应: got=%v want=%v",
				i, flatIDs, wantAfterIDs)
		}

		// 货物变化只涉及本批三件货物，按编号字典序；G1 留舱不出现。
		wantChanges := []CargoChange{
			{CargoID: "G2", From: "C1", To: ""},
			{CargoID: "G4", From: "", To: "C1"},
			{CargoID: "G5", From: "", To: "C1"},
		}
		if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
			t.Fatalf("第 %d 种次序：货物变化错误: got=%+v want=%+v",
				i, res.CargoChanges, wantChanges)
		}

		// 预览只读：实际货物位置、舱位占用与货物资料仍是预览前的配载。
		assertCompartmentsUnchanged(t, r, compBefore)
		assertCargoUnchanged(t, r, cargoBefore)

		if want == nil {
			want = res
		} else if !reflect.DeepEqual(res, want) {
			t.Fatalf("第 %d 种次序：改变操作书写顺序改变了预览内容:\ngot=%+v\nwant=%+v",
				i, res, want)
		}
	}
}

// 主场景的正式提交行为保持不变：同一批安排用 Adjust 提交按 C1 的混装冲突
// 拒绝（Preview 列全原因、Adjust 只报一个），整批不生效、不占用编号。
func TestPreviewBatchMixedArrangementAdjustStillRejected(t *testing.T) {
	r := setupBatchMixedPreviewRegistry(t)
	compBefore := snapshotCompartments(r, "C1")
	cargoBefore := snapshotCargo(r, "G1", "G2", "G3", "G4", "G5")

	se := submitExpectReject(t, r, "batch-mixed", batchMixedPreviewOps())
	if se.Kind != ErrMixedLoading {
		t.Fatalf("正式提交应按混装冲突拒绝，实际 Kind=%s", se.Kind)
	}
	// 违规货物为 G1、G5，编号字典序最靠前的是原舱留舱的 G1。
	if se.ID != "G1" {
		t.Fatalf("混装错误应指出编号最靠前的 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "batch-mixed" {
		t.Fatalf("错误应携带调整编号 batch-mixed，实际 %q", se.AdjustmentID)
	}
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)

	// 失败不占用编号：仅卸下 G2 的合法修正内容可用同一编号提交成功。
	res, err := r.Adjust("batch-mixed", []Op{{Kind: OpUnload, CargoID: "G2"}})
	if err != nil {
		t.Fatalf("被拒编号不应被占用，修正后应成功: %v", err)
	}
	if res == nil || len(res.CargoChanges) != 1 || res.CargoChanges[0].CargoID != "G2" ||
		res.CargoChanges[0].From != "C1" || res.CargoChanges[0].To != "" {
		t.Fatalf("修正后调整结果异常: %+v", res)
	}
}

// 边界：把原目的地（上海）的货物全部安排卸下（G1、G2、G3），最终 C1 只剩
// 北京的 G4、G5——目的地单一，即使 G5 不允许混装也应可提交、没有混装拒绝
// 原因；允许混装与否在单一目的地共舱时都不构成冲突。该保障对三件卸下与
// 两件装入共 5 条操作的全部 120 种书写次序成立。
func TestPreviewUnloadAllOriginCargoLeavesSingleDestinationSubmittable(t *testing.T) {
	ops := []Op{
		{Kind: OpUnload, CargoID: "G1"},
		{Kind: OpUnload, CargoID: "G2"},
		{Kind: OpUnload, CargoID: "G3"},
		{Kind: OpLoad, CargoID: "G4", Target: "C1"},
		{Kind: OpLoad, CargoID: "G5", Target: "C1"},
	}
	var want *PreviewResult
	for i, order := range permuteOps(ops) {
		r := setupBatchMixedPreviewRegistry(t)
		compBefore := snapshotCompartments(r, "C1")
		cargoBefore := snapshotCargo(r, "G1", "G2", "G3", "G4", "G5")

		res, err := r.Preview(order)
		if err != nil {
			t.Fatalf("第 %d 种次序：Preview 不应返回错误: %v", i, err)
		}
		if !res.Submittable || len(res.Rejections) != 0 {
			t.Fatalf("第 %d 种次序：最终只剩北京货物应可提交且无拒绝原因: %+v",
				i, res.Rejections)
		}

		if len(res.Compartments) != 1 || res.Compartments[0].ID != "C1" {
			t.Fatalf("第 %d 种次序：应只列出 C1: %+v", i, res.Compartments)
		}
		cp := res.Compartments[0]
		// 调整前仍是上海三件。
		if !reflect.DeepEqual(cargoIDs(cp.Before), []string{"G1", "G2", "G3"}) ||
			cp.Before.UsedWeight != 50 || cp.Before.RemainingWeight != 150 {
			t.Fatalf("第 %d 种次序：调整前清单应反映原配载: %+v", i, cp.Before)
		}
		// 预计：上海的 G1、G2、G3 全部卸下，北京的 G4、G5 装入；两件货物
		// 目的地均为北京，合计 20/200。
		if !reflect.DeepEqual(cargoIDs(cp.After), []string{"G4", "G5"}) ||
			cp.After.UsedWeight != 20 || cp.After.RemainingWeight != 180 {
			t.Fatalf("第 %d 种次序：预计清单应为 G4、G5 共 20 千克: %+v",
				i, cp.After)
		}
		wantChanges := []CargoChange{
			{CargoID: "G1", From: "C1", To: ""},
			{CargoID: "G2", From: "C1", To: ""},
			{CargoID: "G3", From: "C1", To: ""},
			{CargoID: "G4", From: "", To: "C1"},
			{CargoID: "G5", From: "", To: "C1"},
		}
		if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
			t.Fatalf("第 %d 种次序：货物变化错误: got=%+v want=%+v",
				i, res.CargoChanges, wantChanges)
		}

		assertCompartmentsUnchanged(t, r, compBefore)
		assertCargoUnchanged(t, r, cargoBefore)

		if want == nil {
			want = res
		} else if !reflect.DeepEqual(res, want) {
			t.Fatalf("第 %d 种次序：改变操作书写顺序改变了预览内容:\ngot=%+v\nwant=%+v",
				i, res, want)
		}
	}

	// 同一批合法安排正式提交应成功，提交后查询与预计配载一致：
	// C1 只剩北京的 G4、G5，G1、G2、G3 全部卸下但货物记录保留。
	r := setupBatchMixedPreviewRegistry(t)
	res, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("边界安排应可提交: %+v", res.Rejections)
	}
	adj, err := r.Adjust("single-dest", ops)
	if err != nil {
		t.Fatalf("最终单一目的地的安排正式提交应成功: %v", err)
	}
	if adj.ID != "single-dest" || len(adj.CargoChanges) != 5 {
		t.Fatalf("成功调整结果异常: %+v", adj)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 20 || c1.RemainingWeight != 180 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G4", "G5"}) {
		t.Fatalf("提交后 C1 应只剩 G4、G5 共 20 千克: %+v", c1)
	}
	for _, id := range []string{"G1", "G2", "G3"} {
		cv, _ := r.Cargo(id)
		if cv.Loaded || cv.CompartmentID != "" {
			t.Fatalf("货物 %s 应已卸下: %+v", id, cv)
		}
	}
	for _, id := range []string{"G4", "G5"} {
		cv, _ := r.Cargo(id)
		if !cv.Loaded || cv.CompartmentID != "C1" || cv.Destination != "北京" {
			t.Fatalf("货物 %s 应在北京、装于 C1: %+v", id, cv)
		}
	}
}
