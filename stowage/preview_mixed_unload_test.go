package stowage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 本文件为 Preview 的混装拒绝说明补充回归保障，重点保护“同一舱位同一批
// 中既卸下又装入”时，拒绝原因对整批操作完成后舱内货物的描述：C1 原本
// 装着同一目的地（上海）的多件货物，其中两件不允许混装、其余允许；预览
// 卸下一件不允许混装的货物，同时装入另一目的地（北京）的两件货物（一件
// 允许、一件不允许）。每条操作本身合法、总重量也不超重，但最终舱内两个
// 目的地共舱且仍有不允许混装的货物，安排不可提交。
//
// 这时拒绝原因必须逐件描述最终留舱的货物：允许混装的货物也要列出，同一
// 目的地的多件货物归在同一组且每件只出现一次；已经卸下的货物既不属于
// 任何目的地分组，也不能继续被列为不允许混装的货物；不允许混装清单要
// 同时包含原来留舱与新装入的两件。另保护边界：原目的地货物全部卸下后，
// 即使新目的地中含不允许混装的货物，单一目的地不构成混装冲突，可提交。

// setupMixedUnloadScenario 建立共同的初始配载：
// C1 承重 1000 千克，已装四件各 10 千克、目的地均为上海的货物——
// G2、G5 不允许混装，G3、G7 允许混装（共 40 千克）；
// 另有两件尚未装载的北京货物：G1 不允许混装、G4 允许混装。
// 货物编号刻意打乱字典序，以暴露任何按遍历或书写顺序排列的错误。
func setupMixedUnloadScenario(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// 原舱的上海货物：两件禁混、两件允许。
	mustRegisterCargo(t, r, "G2", 10, "上海", false)
	mustRegisterCargo(t, r, "G3", 10, "上海", true)
	mustRegisterCargo(t, r, "G5", 10, "上海", false)
	mustRegisterCargo(t, r, "G7", 10, "上海", true)
	// 尚未装载的北京货物：G1 禁混、G4 允许。
	mustRegisterCargo(t, r, "G1", 10, "北京", false)
	mustRegisterCargo(t, r, "G4", 10, "北京", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		{Kind: OpLoad, CargoID: "G5", Target: "C1"},
		{Kind: OpLoad, CargoID: "G7", Target: "C1"},
	})
	return r
}

// mixedUnloadOps 是核心清单：卸下一件原舱禁混货物 G2，装入两件北京
// 货物（G1 禁混、G4 允许）。最终 C1 = 上海 G3(许混)、G5(禁混)、
// G7(许混) + 北京 G1(禁混)、G4(许混)，共 50 千克，不超重但存在混装冲突。
func mixedUnloadOps() []Op {
	return []Op{
		{Kind: OpUnload, CargoID: "G2"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G4", Target: "C1"},
	}
}

// wantMixedUnloadDests 是最终配载的目的地分组：按目的地字典序（上海的
// 首字节小于北京），同组货物按编号字典序；已卸下的 G2 不在任何一组。
func wantMixedUnloadDests() []MixedDestination {
	return []MixedDestination{
		{Destination: "上海", CargoIDs: []string{"G3", "G5", "G7"}},
		{Destination: "北京", CargoIDs: []string{"G1", "G4"}},
	}
}

// findCompartmentPreview 从预览结果中取出指定舱位的前后配载。
func findCompartmentPreview(t *testing.T, res *PreviewResult, id string) CompartmentPreview {
	t.Helper()
	for _, cp := range res.Compartments {
		if cp.ID == id {
			return cp
		}
	}
	t.Fatalf("预览结果缺少舱位 %s: %+v", id, res.Compartments)
	return CompartmentPreview{}
}

// TestPreviewMixedUnloadRejectionDescribesFinalCargo 覆盖核心回归：
// 每条操作合法、预览调用成功并返回完整结果，但安排不可提交，唯一的拒绝
// 原因是 C1 的混装冲突；目的地分组与不允许混装清单都按最终留舱货物逐件
// 描述。
func TestPreviewMixedUnloadRejectionDescribesFinalCargo(t *testing.T) {
	r := setupMixedUnloadScenario(t)
	res, err := r.Preview(mixedUnloadOps())
	if err != nil {
		t.Fatalf("每条操作本身合法，预览不应返回错误: %v", err)
	}
	if res.Submittable || len(res.Rejections) != 1 {
		t.Fatalf("最终两个目的地共舱且有禁混货物，应不可提交且恰有一条原因: %+v", res)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrMixedLoading || rej.CompartmentID != "C1" {
		t.Fatalf("拒绝原因应为 C1 的混装冲突: %+v", rej)
	}
	if rej.MaxWeight != 1000 {
		t.Fatalf("混装原因应保留舱位承重: %+v", rej)
	}
	// 混装原因不提供重量数值，对应字段保持零值。
	if rej.UsedWeight != 0 || rej.RemainingWeight != 0 || rej.Overweight != 0 {
		t.Fatalf("混装原因不应携带重量数值: %+v", rej)
	}

	// 目的地分组：覆盖最终留舱的全部五件货物，允许混装的 G3、G4、G7
	// 不能省略；同目的地多件归在同一组；已卸下的 G2 不属于任何分组。
	if !reflect.DeepEqual(rej.Destinations, wantMixedUnloadDests()) {
		t.Fatalf("目的地分组应逐件描述最终留舱货物: %+v want %+v",
			rej.Destinations, wantMixedUnloadDests())
	}
	for _, d := range rej.Destinations {
		for _, id := range d.CargoIDs {
			if id == "G2" {
				t.Fatalf("已安排卸下的 G2 不应再出现在目的地分组 %s 中", d.Destination)
			}
		}
	}
	// 每件货物在全部分组中只出现一次。
	seen := make(map[string]int)
	for _, d := range rej.Destinations {
		for _, id := range d.CargoIDs {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("货物 %s 在目的地分组中出现 %d 次，应只出现一次", id, n)
		}
	}

	// 不允许混装清单：同时包含原来留舱的 G5 与新装入的 G1，按编号字典序；
	// 已卸下的禁混货物 G2 不能继续被列出，允许混装的 G3、G4、G7 也不在内。
	if !reflect.DeepEqual(rej.OffendingCargo, []string{"G1", "G5"}) {
		t.Fatalf("禁混清单应同时指出留舱的 G5 与新装的 G1（不含已卸下的 G2）: %+v",
			rej.OffendingCargo)
	}
}

// TestPreviewMixedUnloadGroupsCorrespondToAfterList 保护分组与预计舱位
// 货物清单逐件对应：分组中出现的货物集合、目的地与混装许可都与 C1 的
// After 清单一致且沿用登记资料；Before 仍反映原配载。
func TestPreviewMixedUnloadGroupsCorrespondToAfterList(t *testing.T) {
	r := setupMixedUnloadScenario(t)
	res, err := r.Preview(mixedUnloadOps())
	if err != nil {
		t.Fatal(err)
	}

	cp := findCompartmentPreview(t, res, "C1")

	// 调整前清单反映原配载：四件上海货物共 40 千克，保持真实位置。
	if cp.Before.UsedWeight != 40 || cp.Before.RemainingWeight != 960 {
		t.Fatalf("C1 调整前应为 40/1000 千克: %+v", cp.Before)
	}
	if got := cargoIDs(cp.Before); !reflect.DeepEqual(got, []string{"G2", "G3", "G5", "G7"}) {
		t.Fatalf("C1 调整前清单应反映原配载: %v", got)
	}
	for _, cv := range cp.Before.Cargo {
		if !cv.Loaded || cv.CompartmentID != "C1" {
			t.Fatalf("调整前清单中的货物应保持原所属舱位: %+v", cv)
		}
	}

	// 预计清单反映卸下与装入后的安排：五件货物共 50 千克，一律显示为
	// 已装载且属于 C1；G2 已不在舱内。
	if cp.After.UsedWeight != 50 || cp.After.RemainingWeight != 950 {
		t.Fatalf("C1 预计应为 50/1000 千克: %+v", cp.After)
	}
	afterIDs := cargoIDs(cp.After)
	wantAfter := []string{"G1", "G3", "G4", "G5", "G7"}
	if !reflect.DeepEqual(afterIDs, wantAfter) {
		t.Fatalf("C1 预计清单应只含最终留舱的五件货物: %v want %v", afterIDs, wantAfter)
	}
	for _, cv := range cp.After.Cargo {
		if !cv.Loaded || cv.CompartmentID != "C1" {
			t.Fatalf("预计清单中的货物应显示已装载于 C1: %+v", cv)
		}
	}

	// 预计清单中每件货物的目的地与混装许可沿用登记资料。
	wantCargo := map[string]struct {
		dest  string
		mixed bool
	}{
		"G1": {"北京", false},
		"G3": {"上海", true},
		"G4": {"北京", true},
		"G5": {"上海", false},
		"G7": {"上海", true},
	}
	for _, cv := range cp.After.Cargo {
		want, ok := wantCargo[cv.ID]
		if !ok {
			t.Fatalf("预计清单出现意外货物 %s", cv.ID)
		}
		if cv.Destination != want.dest || cv.AllowMixed != want.mixed || cv.Weight != 10 {
			t.Fatalf("货物 %s 的登记资料不应被改写: %+v want %+v", cv.ID, cv, want)
		}
	}

	// 分组展平后与预计清单逐件对应：同一组货物集合、每件恰好一次（分组按
	// 目的地字典序组织，清单按编号全局排序，故比较集合而非排列），目的地
	// 与许可也一致。
	rej := res.Rejections[0]
	groupedSet := make(map[string]bool)
	groupDest := make(map[string]string)
	for _, d := range rej.Destinations {
		for _, id := range d.CargoIDs {
			if groupedSet[id] {
				t.Fatalf("货物 %s 在目的地分组中重复出现", id)
			}
			groupedSet[id] = true
			groupDest[id] = d.Destination
		}
	}
	if len(groupedSet) != len(afterIDs) {
		t.Fatalf("分组货物数 %d 与预计清单货物数 %d 不一致", len(groupedSet), len(afterIDs))
	}
	for _, id := range afterIDs {
		if !groupedSet[id] {
			t.Fatalf("预计清单中的货物 %s 未出现在目的地分组中", id)
		}
	}
	for _, cv := range cp.After.Cargo {
		if groupDest[cv.ID] != cv.Destination {
			t.Fatalf("货物 %s 的分组目的地 %q 与登记资料 %q 不一致",
				cv.ID, groupDest[cv.ID], cv.Destination)
		}
	}
	offenders := make(map[string]bool, len(rej.OffendingCargo))
	for _, id := range rej.OffendingCargo {
		offenders[id] = true
	}
	for _, cv := range cp.After.Cargo {
		if offenders[cv.ID] != !cv.AllowMixed {
			t.Fatalf("货物 %s 是否列入禁混清单应与登记的混装许可一致: %+v", cv.ID, cv)
		}
	}

	// 货物变化按编号字典序，卸下以空舱位表示。
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "", To: "C1"},
		{CargoID: "G2", From: "C1", To: ""},
		{CargoID: "G4", From: "", To: "C1"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
		t.Fatalf("货物变化错误: %+v want %+v", res.CargoChanges, wantChanges)
	}
	// 全部操作只涉及 C1。
	if len(res.Compartments) != 1 || res.Compartments[0].ID != "C1" {
		t.Fatalf("受影响舱位应只有 C1: %+v", res.Compartments)
	}
}

// TestPreviewMixedUnloadIndependentOfOpOrder 保护确定性：改变操作书写
// 顺序不能改变可提交性、目的地分组、禁混清单、前后货物清单与变化记录。
func TestPreviewMixedUnloadIndependentOfOpOrder(t *testing.T) {
	orders := [][]Op{
		{
			{Kind: OpUnload, CargoID: "G2"},
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
			{Kind: OpLoad, CargoID: "G4", Target: "C1"},
		},
		{
			{Kind: OpLoad, CargoID: "G4", Target: "C1"},
			{Kind: OpUnload, CargoID: "G2"},
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		},
		{
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
			{Kind: OpLoad, CargoID: "G4", Target: "C1"},
			{Kind: OpUnload, CargoID: "G2"},
		},
	}
	var first *PreviewResult
	for i, ops := range orders {
		r := setupMixedUnloadScenario(t)
		res, err := r.Preview(ops)
		if err != nil {
			t.Fatalf("第 %d 种顺序预览出错: %v", i, err)
		}
		if i == 0 {
			first = res
			continue
		}
		if !reflect.DeepEqual(res, first) {
			t.Fatalf("第 %d 种顺序的预览结果与第一种不一致:\n%+v\n%+v", i, res, first)
		}
	}

	// 直接核对排序契约，避免 DeepEqual 本身依赖错误排列时漏检。
	res := first
	if got := res.Rejections[0].Destinations; !reflect.DeepEqual(got, wantMixedUnloadDests()) {
		t.Fatalf("目的地分组应按目的地字典序、货物按编号字典序: %+v", got)
	}
	if got := res.Rejections[0].OffendingCargo; !reflect.DeepEqual(got, []string{"G1", "G5"}) {
		t.Fatalf("禁混清单应按货物编号字典序: %+v", got)
	}
}

// TestPreviewMixedUnloadReadOnlyAndAdjustRejected 保护两种预览（被拒的
// 混装预览）不改变任何实际状态，并沿用现有正式调整行为：同一批操作正式
// 提交被拒、整批不生效，失败不占用调整编号。
func TestPreviewMixedUnloadReadOnlyAndAdjustRejected(t *testing.T) {
	r := setupMixedUnloadScenario(t)
	ops := mixedUnloadOps()

	// 以两种书写顺序各预览一次，都不应改变状态。
	if _, err := r.Preview(ops); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G4", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpUnload, CargoID: "G2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 查询仍得到预览前的配载：C1 仍是四件上海货物共 40 千克。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 40 || c1.RemainingWeight != 960 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G2", "G3", "G5", "G7"}) {
		t.Fatalf("预览后 C1 应保持原配载: %+v", c1)
	}
	// 北京两件货物仍未装载；全部货物资料沿用登记。
	for _, want := range []struct {
		id          string
		dest        string
		mixed       bool
		compartment string
	}{
		{"G1", "北京", false, ""},
		{"G2", "上海", false, "C1"},
		{"G3", "上海", true, "C1"},
		{"G4", "北京", true, ""},
		{"G5", "上海", false, "C1"},
		{"G7", "上海", true, "C1"},
	} {
		cv, _ := r.Cargo(want.id)
		if cv.Destination != want.dest || cv.AllowMixed != want.mixed ||
			cv.Weight != 10 || cv.CompartmentID != want.compartment ||
			cv.Loaded != (want.compartment != "") {
			t.Fatalf("预览后货物 %s 状态/资料应不变: %+v", want.id, cv)
		}
	}

	// 正式提交同一批安排：被拒，错误为结构化混装冲突，对象是禁混货物中
	// 编号最靠前的 G1（新装入者），说明指出冲突舱位 C1。
	res, err := r.Adjust("pm-1", ops)
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("正式提交应返回 *Error，实际 %T: %v", err, err)
	}
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.Kind != ErrMixedLoading || se.ID != "G1" {
		t.Fatalf("应拒绝于 C1 混装冲突并指出 G1，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if se.AdjustmentID != "pm-1" {
		t.Fatalf("错误应携带调整编号 pm-1，实际 %q", se.AdjustmentID)
	}
	if msg := se.Error(); !strings.Contains(msg, "C1") || !strings.Contains(msg, "G1") {
		t.Fatalf("混装说明应指出 C1 与 G1: %s", msg)
	}

	// 整批原子：拒绝后配载与货物资料仍保持预览前状态。
	c1, _ = r.Compartment("C1")
	if c1.UsedWeight != 40 || !reflect.DeepEqual(cargoIDs(*c1), []string{"G2", "G3", "G5", "G7"}) {
		t.Fatalf("拒绝后 C1 应保持原配载: %+v", c1)
	}
	g1, _ := r.Cargo("G1")
	if g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("拒绝后 G1 应仍未装载（不能只执行装入）: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if !g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("拒绝后 G2 应仍在 C1（不能只执行卸下）: %+v", g2)
	}

	// 失败不占用编号：同一编号用于合法内容（只卸下 G2，最终仅上海货物）
	// 应成功。
	ok, err := r.Adjust("pm-1", []Op{{Kind: OpUnload, CargoID: "G2"}})
	if err != nil {
		t.Fatalf("被拒编号不应被占用，合法内容应能提交: %v", err)
	}
	if ok == nil || len(ok.CargoChanges) != 1 || ok.CargoChanges[0].CargoID != "G2" {
		t.Fatalf("复用编号的调整结果异常: %+v", ok)
	}
}

// TestPreviewUnloadAllOriginalDestinationLeavesSingleDestination 保护容易
// 混淆的边界：把原目的地（上海）的货物全部卸下，最终舱内只剩另一目的地
// （北京）的多件货物，即使其中 G1 不允许混装，单一目的地也不构成混装
// 冲突——预览可提交、没有任何拒绝原因；同批操作正式提交成功，提交后
// 查询与预计清单对应。
func TestPreviewUnloadAllOriginalDestinationLeavesSingleDestination(t *testing.T) {
	r := setupMixedUnloadScenario(t)
	ops := []Op{
		{Kind: OpUnload, CargoID: "G2"},
		{Kind: OpUnload, CargoID: "G3"},
		{Kind: OpUnload, CargoID: "G5"},
		{Kind: OpUnload, CargoID: "G7"},
		{Kind: OpLoad, CargoID: "G4", Target: "C1"}, // 北京，允许混装
		{Kind: OpLoad, CargoID: "G1", Target: "C1"}, // 北京，不允许混装
	}
	res, err := r.Preview(ops)
	if err != nil {
		t.Fatalf("每条操作合法，预览不应返回错误: %v", err)
	}
	if !res.Submittable || len(res.Rejections) != 0 {
		t.Fatalf("最终仅北京一个目的地，即使含禁混货物也应可提交且无拒绝原因: %+v", res)
	}

	cp := findCompartmentPreview(t, res, "C1")
	// 调整前仍是四件上海货物，预计清单只剩两件北京货物。
	if got := cargoIDs(cp.Before); !reflect.DeepEqual(got, []string{"G2", "G3", "G5", "G7"}) {
		t.Fatalf("调整前清单应反映原配载: %v", got)
	}
	if cp.After.UsedWeight != 20 || cp.After.RemainingWeight != 980 {
		t.Fatalf("C1 预计应为 20/1000 千克: %+v", cp.After)
	}
	if got := cargoIDs(cp.After); !reflect.DeepEqual(got, []string{"G1", "G4"}) {
		t.Fatalf("C1 预计清单应只剩北京的 G1、G4: %v", got)
	}
	// 许可与目的地沿用登记：G1 仍为不允许混装，但单一目的地不触发冲突。
	g1After := cp.After.Cargo[0]
	if g1After.ID != "G1" || g1After.Destination != "北京" || g1After.AllowMixed {
		t.Fatalf("预计清单中 G1 应为北京、不允许混装: %+v", g1After)
	}

	// 预览只读：实际配载不变。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 40 || !reflect.DeepEqual(cargoIDs(*c1), []string{"G2", "G3", "G5", "G7"}) {
		t.Fatalf("预览后 C1 应保持原配载: %+v", c1)
	}
	for _, id := range []string{"G1", "G4"} {
		cv, _ := r.Cargo(id)
		if cv.Loaded || cv.CompartmentID != "" {
			t.Fatalf("预览后 %s 应仍未装载: %+v", id, cv)
		}
	}

	// 可提交的安排用同一批操作正式提交应成功，提交后与预计清单逐件对应。
	adj, err := r.Adjust("pm-ok", ops)
	if err != nil {
		t.Fatalf("单一目的地安排应正式提交成功: %v", err)
	}
	if adj == nil || adj.ID != "pm-ok" || len(adj.CargoChanges) != 6 {
		t.Fatalf("成功调整应含六件货物的变化: %+v", adj)
	}
	wantChanges := []CargoChange{
		{CargoID: "G1", From: "", To: "C1"},
		{CargoID: "G2", From: "C1", To: ""},
		{CargoID: "G3", From: "C1", To: ""},
		{CargoID: "G4", From: "", To: "C1"},
		{CargoID: "G5", From: "C1", To: ""},
		{CargoID: "G7", From: "C1", To: ""},
	}
	if !reflect.DeepEqual(adj.CargoChanges, wantChanges) {
		t.Fatalf("货物变化错误: %+v want %+v", adj.CargoChanges, wantChanges)
	}
	c1, _ = r.Compartment("C1")
	if c1.UsedWeight != 20 || c1.RemainingWeight != 980 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G4"}) {
		t.Fatalf("提交后 C1 应与预计清单一致: %+v", c1)
	}
	for _, id := range []string{"G2", "G3", "G5", "G7"} {
		cv, _ := r.Cargo(id)
		if cv.Loaded || cv.CompartmentID != "" {
			t.Fatalf("提交后 %s 应已卸下: %+v", id, cv)
		}
	}
}
