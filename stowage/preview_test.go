package stowage

import (
	"math"
	"testing"
)

// ---------- 基本预览 ----------

func TestPreviewSubmittable(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	// 预览：卸下 G1、装载 G2 到 C2。
	res, err := r.Preview([]Op{
		{Kind: OpUnload, CargoID: "G1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("合法预览应可提交: %+v", res.Rejections)
	}
	if len(res.Rejections) != 0 {
		t.Fatalf("合法预览不应有拒绝原因: %+v", res.Rejections)
	}

	// 货物变化按编号字典序排列。
	if len(res.CargoChanges) != 2 {
		t.Fatalf("应列出 2 条货物变化: %+v", res.CargoChanges)
	}
	if res.CargoChanges[0].CargoID != "G1" || res.CargoChanges[0].From != "C1" || res.CargoChanges[0].To != "" {
		t.Fatalf("G1 变化错误: %+v", res.CargoChanges[0])
	}
	if res.CargoChanges[1].CargoID != "G2" || res.CargoChanges[1].From != "" || res.CargoChanges[1].To != "C2" {
		t.Fatalf("G2 变化错误: %+v", res.CargoChanges[1])
	}

	// 受影响舱位按编号字典序排列。
	if len(res.CompartmentPreviews) != 2 {
		t.Fatalf("应列出 2 个受影响舱位: %+v", res.CompartmentPreviews)
	}
	cp1 := res.CompartmentPreviews[0]
	if cp1.CompartmentID != "C1" {
		t.Fatalf("舱位应按编号字典序排列: %+v", cp1)
	}
	if cp1.UsedWeightBefore != 30 || cp1.UsedWeightAfter != 0 ||
		cp1.RemainingBefore != 70 || cp1.RemainingAfter != 100 {
		t.Fatalf("C1 重量变化错误: %+v", cp1)
	}
	if len(cp1.CargoBefore) != 1 || cp1.CargoBefore[0].ID != "G1" {
		t.Fatalf("C1 调整前货物清单错误: %+v", cp1.CargoBefore)
	}
	if len(cp1.CargoAfter) != 0 {
		t.Fatalf("C1 调整后应为空: %+v", cp1.CargoAfter)
	}

	cp2 := res.CompartmentPreviews[1]
	if cp2.CompartmentID != "C2" {
		t.Fatalf("舱位应按编号字典序排列: %+v", cp2)
	}
	if cp2.UsedWeightBefore != 0 || cp2.UsedWeightAfter != 20 ||
		cp2.RemainingBefore != 100 || cp2.RemainingAfter != 80 {
		t.Fatalf("C2 重量变化错误: %+v", cp2)
	}
	if len(cp2.CargoBefore) != 0 || len(cp2.CargoAfter) != 1 || cp2.CargoAfter[0].ID != "G2" {
		t.Fatalf("C2 货物清单错误: before=%+v after=%+v", cp2.CargoBefore, cp2.CargoAfter)
	}

	// 配载未被预览改变。
	cv1, _ := r.Cargo("G1")
	cv2, _ := r.Cargo("G2")
	if cv1.CompartmentID != "C1" || cv2.Loaded {
		t.Fatalf("预览不应改变配载: %+v %+v", cv1, cv2)
	}
}

func TestPreviewEmpty(t *testing.T) {
	r := NewRegistry()
	se := requireError(t, func() error {
		_, err := r.Preview(nil)
		return err
	}(), ErrEmptyAdjustment)
	if se.AdjustmentID != "" {
		t.Fatalf("预览没有调整编号，AdjustmentID 应为空: %q", se.AdjustmentID)
	}
	requireError(t, func() error {
		_, err := r.Preview([]Op{})
		return err
	}(), ErrEmptyAdjustment)
}

func TestPreviewIllegalOpReturnsFirstError(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	// 第一条操作即不合法（G1 已装载，不能再次装载），返回该错误且无局部预览。
	se := requireError(t, func() error {
		_, err := r.Preview([]Op{
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
			{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		})
		return err
	}(), ErrStateMismatch)
	if se.ID != "G1" {
		t.Fatalf("应指出状态不符的货物，实际 ID=%q", se.ID)
	}

	// 第二条操作不合法（G2 不存在于... 实际 G2 存在但未装载，不能卸下）。
	se = requireError(t, func() error {
		_, err := r.Preview([]Op{
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpUnload, CargoID: "G2"},
		})
		return err
	}(), ErrStateMismatch)
	if se.ID != "G2" {
		t.Fatalf("应指出状态不符的货物，实际 ID=%q", se.ID)
	}

	// 重复操作。
	se = requireError(t, func() error {
		_, err := r.Preview([]Op{
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpMove, CargoID: "G1", Target: "C1"},
		})
		return err
	}(), ErrDuplicateOp)
	if se.ID != "G1" {
		t.Fatalf("应指出重复操作的货物，实际 ID=%q", se.ID)
	}

	// 不存在的货物。
	se = requireError(t, func() error {
		_, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "GX", Target: "C1"}})
		return err
	}(), ErrNotFound)
	if se.ID != "GX" {
		t.Fatalf("应指出不存在的货物，实际 ID=%q", se.ID)
	}
}

// ---------- 拒绝原因 ----------

func TestPreviewOverweight(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "X", true); err != nil {
		t.Fatal(err)
	}

	res, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable {
		t.Fatalf("超重预览不应可提交")
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("应有 1 条拒绝原因: %+v", res.Rejections)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrOverweight || rej.CompartmentID != "C1" {
		t.Fatalf("拒绝原因错误: %+v", rej)
	}
	if rej.MaxWeight != 50 || rej.TotalWeight != 60 || rej.OverWeight != 10 {
		t.Fatalf("超重数值错误: %+v", rej)
	}

	// 舱位预览仍返回预计重量与货物清单。
	cp := res.CompartmentPreviews[0]
	if cp.UsedWeightAfter != 60 || cp.RemainingAfter != -10 {
		t.Fatalf("舱位预计重量错误: %+v", cp)
	}
	if len(cp.CargoAfter) != 2 {
		t.Fatalf("应返回调整后货物清单: %+v", cp.CargoAfter)
	}
}

func TestPreviewOverflow(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", math.MaxInt64, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", math.MaxInt64, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable {
		t.Fatalf("溢出预览不应可提交")
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("应有 1 条拒绝原因: %+v", res.Rejections)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrOverflow || rej.CompartmentID != "C1" {
		t.Fatalf("应用重量溢出替代超重: %+v", rej)
	}
	// 溢出时不提供超重数值。
	if rej.MaxWeight != 0 || rej.TotalWeight != 0 || rej.OverWeight != 0 {
		t.Fatalf("溢出不应提供超重数值: %+v", rej)
	}

	cp := res.CompartmentPreviews[0]
	if !cp.Overflow {
		t.Fatalf("舱位应标记溢出: %+v", cp)
	}
	// 不提供预计已用重量、剩余重量。
	if cp.UsedWeightAfter != 0 || cp.RemainingAfter != 0 {
		t.Fatalf("溢出不应提供预计重量数值: %+v", cp)
	}
	// 调整前重量与货物清单照常返回。
	if cp.UsedWeightBefore != math.MaxInt64 {
		t.Fatalf("调整前重量应照常返回: %+v", cp)
	}
	if len(cp.CargoAfter) != 2 {
		t.Fatalf("溢出应照常返回货物清单: %+v", cp.CargoAfter)
	}
}

func TestPreviewMixedLoadingConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 30, "Guangzhou", false); err != nil {
		t.Fatal(err)
	}
	// C1 目前只有 G1（上海，不允许混装），单目的地合法。
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	// 预览装入 G2、G3：三个目的地，G1 与 G3 不允许混装。
	res, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable {
		t.Fatalf("混装冲突预览不应可提交")
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("应有 1 条拒绝原因: %+v", res.Rejections)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrMixedLoading || rej.CompartmentID != "C1" {
		t.Fatalf("拒绝原因错误: %+v", rej)
	}

	// 各目的地对应的货物按目的地字典序排列。
	if len(rej.Destinations) != 3 {
		t.Fatalf("应列出 3 个目的地: %+v", rej.Destinations)
	}
	wantDests := []string{"Beijing", "Guangzhou", "Shanghai"}
	for i, want := range wantDests {
		if rej.Destinations[i].Destination != want {
			t.Fatalf("目的地应按字典序排列: got=%s want=%s", rej.Destinations[i].Destination, want)
		}
	}
	// 每个目的地内货物按字典序排列。
	if rej.Destinations[0].CargoIDs[0] != "G2" {
		t.Fatalf("Beijing 货物错误: %+v", rej.Destinations[0])
	}
	if rej.Destinations[1].CargoIDs[0] != "G3" {
		t.Fatalf("Guangzhou 货物错误: %+v", rej.Destinations[1])
	}
	if rej.Destinations[2].CargoIDs[0] != "G1" {
		t.Fatalf("Shanghai 货物错误: %+v", rej.Destinations[2])
	}

	// 全部不允许混装的货物编号按字典序排列（G2 允许混装，不在其中）。
	if len(rej.UnmixedCargo) != 2 {
		t.Fatalf("应列出 2 件不允许混装的货物: %+v", rej.UnmixedCargo)
	}
	if rej.UnmixedCargo[0] != "G1" || rej.UnmixedCargo[1] != "G3" {
		t.Fatalf("不允许混装货物应按字典序排列: %+v", rej.UnmixedCargo)
	}
}

func TestPreviewSameCompartmentWeightAndMixed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 40, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	// 装入 G2：总重 70 > 50 超重；目的地不同且 G1 不允许混装。
	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable {
		t.Fatalf("超重且混装冲突不应可提交")
	}
	if len(res.Rejections) != 2 {
		t.Fatalf("同一舱位应有 2 条拒绝原因: %+v", res.Rejections)
	}
	// 同舱先重量后混装。
	if res.Rejections[0].Kind != ErrOverweight || res.Rejections[1].Kind != ErrMixedLoading {
		t.Fatalf("同舱应先重量后混装: %+v", res.Rejections)
	}
	if res.Rejections[0].CompartmentID != "C1" || res.Rejections[1].CompartmentID != "C1" {
		t.Fatalf("两条原因都应指向 C1: %+v", res.Rejections)
	}
}

func TestPreviewRejectionsSortedByCompartment(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 60, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 60, "X", true); err != nil {
		t.Fatal(err)
	}

	// 两个舱位都超重，输入顺序为 C2 先、C1 后。
	res, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rejections) != 2 {
		t.Fatalf("应有 2 条拒绝原因: %+v", res.Rejections)
	}
	if res.Rejections[0].CompartmentID != "C1" || res.Rejections[1].CompartmentID != "C2" {
		t.Fatalf("拒绝原因应按舱位编号字典序排列: %+v", res.Rejections)
	}
}

// ---------- 最终配载判断 ----------

func TestPreviewSwapIntermediateTemporarilyOverweight(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 30); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 30); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 交换两件满载货物：中间状态会超重，但最终配载合法。
	res, err := r.Preview([]Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("最终合法的交换不应被中间状态拒绝: %+v", res.Rejections)
	}
	// 即使总重量没有变化，也要列出两个舱位。
	if len(res.CompartmentPreviews) != 2 {
		t.Fatalf("应列出 2 个受影响舱位: %+v", res.CompartmentPreviews)
	}
	for _, cp := range res.CompartmentPreviews {
		if cp.UsedWeightBefore != 30 || cp.UsedWeightAfter != 30 {
			t.Fatalf("交换前后重量都应为 30: %+v", cp)
		}
	}
}

// ---------- 排序 ----------

func TestPreviewOrderingIndependentOfInput(t *testing.T) {
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
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C3"},
	}); err != nil {
		t.Fatal(err)
	}

	// 输入顺序打乱：移动 G3、卸下 G1、装载... G2 已装载，改为移动 G2 到 C3。
	res, err := r.Preview([]Op{
		{Kind: OpMove, CargoID: "G3", Target: "C1"},
		{Kind: OpUnload, CargoID: "G1"},
		{Kind: OpMove, CargoID: "G2", Target: "C3"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 货物变化按编号字典序排列。
	wantCargo := []string{"G1", "G2", "G3"}
	for i, want := range wantCargo {
		if res.CargoChanges[i].CargoID != want {
			t.Fatalf("货物变化应按编号字典序排列: got=%s want=%s", res.CargoChanges[i].CargoID, want)
		}
	}

	// 受影响舱位按编号字典序排列。
	wantComps := []string{"C1", "C2", "C3"}
	for i, want := range wantComps {
		if res.CompartmentPreviews[i].CompartmentID != want {
			t.Fatalf("舱位应按编号字典序排列: got=%s want=%s", res.CompartmentPreviews[i].CompartmentID, want)
		}
	}

	// 舱位内货物清单按编号字典序排列。
	for _, cp := range res.CompartmentPreviews {
		assertSortedCargoIDs(t, cp.CargoBefore)
		assertSortedCargoIDs(t, cp.CargoAfter)
	}
}

func assertSortedCargoIDs(t *testing.T, views []CargoView) {
	t.Helper()
	for i := 1; i < len(views); i++ {
		if views[i-1].ID >= views[i].ID {
			t.Fatalf("货物清单应按编号字典序排列: %+v", views)
		}
	}
}

// ---------- 快照独立性与幂等 ----------

func TestPreviewSnapshotIndependent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}

	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}

	// 调用方修改返回内容。
	res.CargoChanges[0].CargoID = "HACKED"
	res.CompartmentPreviews[0].UsedWeightAfter = 9999
	res.CompartmentPreviews[0].CargoAfter[0].ID = "HACKED"

	// 登记处不受影响。
	cv, _ := r.Cargo("G1")
	if cv.Loaded {
		t.Fatalf("预览不应改变货物状态: %+v", cv)
	}

	// 之后的预览不受影响。
	res2, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.CargoChanges[0].CargoID != "G1" {
		t.Fatalf("后续预览不应被之前的调用方修改影响: %+v", res2.CargoChanges[0])
	}
	if res2.CompartmentPreviews[0].UsedWeightAfter != 10 {
		t.Fatalf("后续预览重量不应被影响: %+v", res2.CompartmentPreviews[0])
	}
	if res2.CompartmentPreviews[0].CargoAfter[0].ID != "G1" {
		t.Fatalf("后续预览货物清单不应被影响: %+v", res2.CompartmentPreviews[0].CargoAfter)
	}
}

func TestPreviewIdempotentWhenStateUnchanged(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}

	ops := []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}
	res1, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}

	if res1.Submittable != res2.Submittable {
		t.Fatalf("连续预览结果应一致")
	}
	if len(res1.CargoChanges) != len(res2.CargoChanges) {
		t.Fatalf("连续预览货物变化应一致")
	}
	if len(res1.CompartmentPreviews) != len(res2.CompartmentPreviews) {
		t.Fatalf("连续预览舱位变化应一致")
	}
	if res1.CompartmentPreviews[0].UsedWeightAfter != res2.CompartmentPreviews[0].UsedWeightAfter {
		t.Fatalf("连续预览重量应一致")
	}
}

func TestPreviewDoesNotConsumeAdjustmentID(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}

	// 预览不占用调整编号。
	if _, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	// 用相同编号正式提交应成功。
	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatalf("预览不应占用调整编号: %v", err)
	}
	if res.ID != "A1" {
		t.Fatalf("正式提交编号错误: %+v", res)
	}
}

func TestPreviewDoesNotReflectConcurrentAdjustment(t *testing.T) {
	// 预览反映同一个时刻的登记和配载。
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}

	// 预览时 G1 未装载，预计装载到 C1。
	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("预览时应可提交")
	}
	if res.CargoChanges[0].From != "" || res.CargoChanges[0].To != "C1" {
		t.Fatalf("预览应反映预览时刻的状态: %+v", res.CargoChanges[0])
	}

	// 正式提交后状态改变。
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	// 旧预览结果不变（仍是未装载 -> C1），但新预览反映新状态。
	res2, err := r.Preview([]Op{{Kind: OpUnload, CargoID: "G1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.CargoChanges[0].From != "C1" || res2.CargoChanges[0].To != "" {
		t.Fatalf("新预览应反映提交后的状态: %+v", res2.CargoChanges[0])
	}
}

func TestFormalSubmissionStillValidates(t *testing.T) {
	// 正式提交仍按提交时状态判断，不能直接使用旧预览放行。
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}

	// 预览时装载 G1 合法。
	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("预览应可提交")
	}

	// 在正式提交前，G1 已被另一次调整装载到 C1。
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	// 用旧预览的操作正式提交：G1 已装载，应被拒绝。
	se := requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
		return err
	}(), ErrStateMismatch)
	if se.ID != "G1" {
		t.Fatalf("正式提交应按提交时状态判断: %+v", se)
	}
}
