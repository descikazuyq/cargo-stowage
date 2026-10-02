package stowage

import (
	"math"
	"strings"
	"testing"
)

// ---------- 未装载货物的更正 ----------

func TestAmendUnloadedCargoSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("  G1  ", 20, "  Shanghai  ", true); err != nil {
		t.Fatal(err)
	}
	if err := r.AmendCargo(" G1 ", 35, "  Beijing ", false); err != nil {
		t.Fatal(err)
	}
	cv, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if cv.Weight != 35 || cv.Destination != "Beijing" || cv.AllowMixed {
		t.Fatalf("三项资料应整体替换: %+v", cv)
	}
	if cv.Loaded || cv.CompartmentID != "" {
		t.Fatalf("未装载货物更正后仍应未装载: %+v", cv)
	}
}

func TestAmendUnloadedSameContentSucceeds(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 20, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	// 与现有资料完全相同也应成功。
	if err := r.AmendCargo("G1", 20, " Shanghai ", false); err != nil {
		t.Fatal(err)
	}
	cv, _ := r.Cargo("G1")
	if cv.Weight != 20 || cv.Destination != "Shanghai" || cv.AllowMixed {
		t.Fatalf("相同内容更正后资料不应变化: %+v", cv)
	}
	if len(r.cargos) != 1 {
		t.Fatalf("不应新增货物，当前货物数=%d", len(r.cargos))
	}
}

func TestAmendUnloadedValidation(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 20, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	// 空编号。
	requireError(t, r.AmendCargo("  ", 30, "Beijing", true), ErrInvalidID)
	// 找不到货物：不是登记。
	se := requireError(t, r.AmendCargo("GX", 30, "Beijing", true), ErrNotFound)
	if se.ID != "GX" {
		t.Fatalf("应指出不存在的货物，实际 ID=%q", se.ID)
	}
	if _, err := r.Cargo("GX"); err == nil {
		t.Fatal("修改不存在的货物不能变成登记")
	}
	// 非正重量。
	requireError(t, r.AmendCargo("G1", 0, "Beijing", true), ErrInvalidWeight)
	requireError(t, r.AmendCargo("G1", -1, "Beijing", true), ErrInvalidWeight)
	// 去空白后为空的目的地。
	requireError(t, r.AmendCargo("G1", 30, "   ", true), ErrInvalidDestination)
	// 全部失败后旧资料保留。
	cv, _ := r.Cargo("G1")
	if cv.Weight != 20 || cv.Destination != "Shanghai" || !cv.AllowMixed {
		t.Fatalf("更正失败不应改变旧资料: %+v", cv)
	}
}

func TestAmendIDCasingAndTrim(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("g1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.AmendCargo("  g1  ", 20, "Y", false); err != nil {
		t.Fatal(err)
	}
	cv, _ := r.Cargo("g1")
	if cv.Weight != 20 || cv.Destination != "Y" || cv.AllowMixed {
		t.Fatalf("去空白编号应命中: %+v", cv)
	}
	requireError(t, r.AmendCargo("G1", 30, "Z", true), ErrNotFound)
}

func TestAmendUnloadedThenLoadUsesNewProfile(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 90, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	// 原重量装不进与 G2 的组合，更正为较小重量后按新资料装载。
	if err := r.RegisterCargo("G2", 80, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if err := r.AmendCargo("G1", 20, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatalf("更正后应按新资料装载: %v", err)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 100 || cpt.RemainingWeight != 0 {
		t.Fatalf("舱位应按更正后重量计: %+v", cpt)
	}
}

// ---------- 已装载货物的更正：重量 ----------

func TestAmendLoadedWeightRecomputed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 先扣旧重量再计新重量：30 -> 40，合计 60。
	if err := r.AmendCargo("G1", 40, "X", true); err != nil {
		t.Fatal(err)
	}
	cv, _ := r.Cargo("G1")
	if cv.Weight != 40 || cv.CompartmentID != "C1" || !cv.Loaded {
		t.Fatalf("更正后货物应留在原舱位: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 60 || cpt.RemainingWeight != 40 {
		t.Fatalf("舱位应扣除旧重量计入新重量: %+v", cpt)
	}
}

func TestAmendLoadedExactCapacityAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 20 -> 30，合计恰好 50。
	if err := r.AmendCargo("G1", 30, "X", true); err != nil {
		t.Fatalf("恰好达到承重应允许保存: %v", err)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 || cpt.RemainingWeight != 0 {
		t.Fatalf("舱位重量错误: %+v", cpt)
	}
}

func TestAmendLoadedOverweightRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 30 -> 90：预计合计 110 超过承重 100。
	se := requireError(t, r.AmendCargo("G1", 90, "X", true), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重说明应指出所属舱位，实际 ID=%q", se.ID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "110") || !strings.Contains(msg, "100") {
		t.Fatalf("超重说明应给出舱位、预计总重量和承重: %s", msg)
	}
	// 旧资料、舱位重量与其他货物全部保留。
	cv, _ := r.Cargo("G1")
	if cv.Weight != 30 {
		t.Fatalf("失败更正不应改变货物重量: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 || cpt.RemainingWeight != 50 {
		t.Fatalf("失败更正不应改变舱位重量: %+v", cpt)
	}
}

func TestAmendLoadedOverflowRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", math.MaxInt64-10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 5, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 5 -> 11：合计 MaxInt64+1，无法用 int64 表示。
	se := requireError(t, r.AmendCargo("G2", 11, "X", true), ErrOverflow)
	if se.ID != "C1" {
		t.Fatalf("溢出应指出所属舱位，实际 ID=%q", se.ID)
	}
	if strings.Contains(se.Error(), "-") {
		t.Fatalf("溢出时不应提供回绕后的总重量: %s", se.Error())
	}
	cv, _ := r.Cargo("G2")
	if cv.Weight != 5 {
		t.Fatalf("失败更正不应改变货物重量: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64-5 {
		t.Fatalf("失败更正不应改变舱位重量: %+v", cpt)
	}
}

// ---------- 已装载货物的更正：目的地与混装 ----------

func TestAmendLoadedMixedConflictRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// 不同目的地、全员允许混装，当前合法。
	if err := r.RegisterCargo("G1", 10, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 关闭 G1 的混装许可而目的地仍不同 -> 冲突，祸首是 G1。
	se := requireError(t, r.AmendCargo("G1", 10, "Shanghai", false), ErrMixedLoading)
	if se.ID != "G1" {
		t.Fatalf("混装说明应指出不允许混装的货物，实际 ID=%q", se.ID)
	}
	if !strings.Contains(se.Error(), "C1") || !strings.Contains(se.Error(), "G1") {
		t.Fatalf("混装说明应指出舱位及不允许混装的货物: %s", se.Error())
	}
	cv, _ := r.Cargo("G1")
	if !cv.AllowMixed || cv.Destination != "Shanghai" {
		t.Fatalf("失败更正不应改变旧资料: %+v", cv)
	}
}

func TestAmendToSameDestinationDisableMixedSucceeds(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// G1、G2 目的地不同但都允许混装，当前合法共舱。
	if err := r.RegisterCargo("G1", 10, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// G2 改成与 G1 相同目的地并关闭混装，最终合法即应成功。
	if err := r.AmendCargo("G2", 10, "Shanghai", false); err != nil {
		t.Fatalf("目的地改成一致后关闭混装应成功: %v", err)
	}
	cv, _ := r.Cargo("G2")
	if cv.Destination != "Shanghai" || cv.AllowMixed || cv.CompartmentID != "C1" {
		t.Fatalf("更正资料错误: %+v", cv)
	}
}

func TestAmendDisableMixedBeforeDestinationWouldReject(t *testing.T) {
	// 模拟"逐字段修改"会先落入非法中间状态：先关闭混装时目的地仍不同，
	// 但三项共同生效后目的地一致，最终合法必须成功。
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	// G1 上海允许混装、G2 北京允许混装，当前靠全员允许混装共舱。
	if err := r.RegisterCargo("G1", 60, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// G2 减重到 10、目的地改为上海、关闭混装：最终 60+10=70 且目的地一致。
	if err := r.AmendCargo("G2", 10, "Shanghai", false); err != nil {
		t.Fatalf("按最终配载合法的更正不应被中间状态拒绝: %v", err)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 70 || cpt.RemainingWeight != 30 {
		t.Fatalf("舱位应按新重量计算: %+v", cpt)
	}
}

func TestAmendWeightAndMixedBothInvalidReportsWeightFirst(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 60, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// G2 增重到 50（合计 110 超重）同时改新目的地并关闭混装（混装也冲突）。
	se := requireError(t, r.AmendCargo("G2", 50, "Guangzhou", false), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("重量与混装同时不合法应先报告重量原因，实际 ID=%q", se.ID)
	}
}

func TestAmendLoadedFailureAtomic(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// G1 改去北京且不混装：与 G2 目的地不同，冲突。
	requireError(t, r.AmendCargo("G1", 30, "Beijing", false), ErrMixedLoading)
	cv1, _ := r.Cargo("G1")
	cv2, _ := r.Cargo("G2")
	if cv1.Destination != "Shanghai" || cv1.AllowMixed {
		t.Fatalf("G1 旧资料应保留: %+v", cv1)
	}
	if cv2.Destination != "Shanghai" || cv2.AllowMixed || cv2.Weight != 20 {
		t.Fatalf("其他货物不应受影响: %+v", cv2)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 {
		t.Fatalf("舱位重量应保留: %+v", cpt)
	}
}

// ---------- 更正与调整编号、快照、后续操作 ----------

func TestAmendLoadedSameContentSucceeds(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 已装载时提交完全相同的资料：成功、重量不重复计入、舱位不变。
	if err := r.AmendCargo("G1", 30, " Shanghai ", false); err != nil {
		t.Fatal(err)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 || cpt.RemainingWeight != 50 {
		t.Fatalf("相同内容更正不应再次占用重量: %+v", cpt)
	}
	cv, _ := r.Cargo("G1")
	if cv.CompartmentID != "C1" {
		t.Fatalf("舱位应保持原样: %+v", cv)
	}
}

func TestAmendDoesNotConsumeAdjustmentID(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("A1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	// 货物编号与调整编号同名互不干扰；更正不占用调整编号。
	if err := r.AmendCargo("A1", 20, "Y", true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "A1", Target: "C1"}})
	if err != nil {
		t.Fatalf("更正不应占用调整编号: %v", err)
	}
	if res.ID != "A1" {
		t.Fatalf("调整编号错误: %+v", res)
	}
}

func TestAmendDoesNotRewriteSavedAdjustment(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	// 首次成功调整：G1 30 装入 C1，舱位 0 -> 30。
	res1, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	// 之后更正货物重量与目的地。
	if err := r.AmendCargo("G1", 40, "Z", false); err != nil {
		t.Fatal(err)
	}
	// 以原编号、原内容重复提交，仍返回首次成功时的变化与重量。
	res2, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.CargoChanges[0] != res1.CargoChanges[0] {
		t.Fatalf("重复提交应返回首次变化: %+v vs %+v", res2.CargoChanges[0], res1.CargoChanges[0])
	}
	if res2.CompartmentChanges[0].WeightAfter != 30 {
		t.Fatalf("重复提交应返回首次重量 30，实际 %+v", res2.CompartmentChanges[0])
	}
}

func TestAmendSnapshotIndependence(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	oldCargo, _ := r.Cargo("G1")
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	oldComp, _ := r.Compartment("C1")
	// 更正前的快照保留原内容。
	if err := r.AmendCargo("G1", 40, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	if oldCargo.Weight != 30 || oldCargo.Destination != "Shanghai" {
		t.Fatalf("更正前的货物快照应保留原内容: %+v", oldCargo)
	}
	if oldComp.UsedWeight != 30 || oldComp.Cargo[0].Destination != "Shanghai" {
		t.Fatalf("更正前的舱位快照应保留原内容: %+v", oldComp)
	}
	// 调用方篡改旧快照不影响登记资料。
	oldCargo.Weight = 9999
	oldComp.Cargo[0].Weight = 9999
	cv, _ := r.Cargo("G1")
	if cv.Weight != 40 || cv.Destination != "Beijing" || cv.AllowMixed {
		t.Fatalf("登记资料不应受旧快照篡改影响: %+v", cv)
	}
}

func TestPreviewAfterAmendUsesNewProfile(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 15); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	oldPreview, err := r.Preview([]Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}})
	if err != nil {
		t.Fatal(err)
	}
	if oldPreview.Submittable {
		t.Fatal("旧重量 30 移入承重 15 的 C2 应不可提交")
	}
	// 更正为 10 后，预览按新资料计算。
	if err := r.AmendCargo("G1", 10, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	newPreview, err := r.Preview([]Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}})
	if err != nil {
		t.Fatal(err)
	}
	if !newPreview.Submittable {
		t.Fatalf("更正后预览应可提交: %+v", newPreview.Rejections)
	}
	if newPreview.Compartments[0].After.UsedWeight != 0 {
		t.Fatalf("预览应按新重量计算原舱位: %+v", newPreview.Compartments[0])
	}
	// 旧预览快照仍是旧内容。
	if oldPreview.Rejections[0].UsedWeight != 30 {
		t.Fatalf("旧预览应保留更正前内容: %+v", oldPreview.Rejections[0])
	}
}

func TestMoveAndUnloadAfterAmendUseNewData(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.AmendCargo("G1", 40, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	// 移动按新重量计。
	res, err := r.Adjust("A1", []Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompartmentChanges[0].WeightBefore != 40 || res.CompartmentChanges[0].WeightAfter != 0 {
		t.Fatalf("C1 变化错误: %+v", res.CompartmentChanges[0])
	}
	if res.CompartmentChanges[1].WeightBefore != 0 || res.CompartmentChanges[1].WeightAfter != 40 {
		t.Fatalf("C2 应按更正后重量计入: %+v", res.CompartmentChanges[1])
	}
	// 卸下收回更正后的重量。
	res2, err := r.Adjust("A2", []Op{{Kind: OpUnload, CargoID: "G1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.CompartmentChanges[0].WeightBefore != 40 || res2.CompartmentChanges[0].WeightAfter != 0 {
		t.Fatalf("卸下应收回更正后的重量 40: %+v", res2.CompartmentChanges[0])
	}
	cv, _ := r.Cargo("G1")
	if cv.Loaded || cv.Weight != 40 || cv.Destination != "Beijing" {
		t.Fatalf("卸下后货物资料错误: %+v", cv)
	}
}

func TestRegisterCargoStillRejectsDuplicateAfterAmend(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 20, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.AmendCargo("G1", 40, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	requireError(t, r.RegisterCargo("G1", 50, "X", true), ErrAlreadyExists)
	cv, _ := r.Cargo("G1")
	if cv.Weight != 40 || cv.Destination != "Beijing" || cv.AllowMixed {
		t.Fatalf("登记入口不能隐式覆盖货物: %+v", cv)
	}
}
