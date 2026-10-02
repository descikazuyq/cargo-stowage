package stowage

import (
	"math"
	"testing"
)

// ---------- 修改未装载货物 ----------

func TestModifyCargoUnloadedSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 20, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	// 提交新的重量、目的地与混装许可。
	if err := r.ModifyCargo("G1", 30, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	view, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Weight != 30 || view.Destination != "Beijing" || view.AllowMixed {
		t.Fatalf("修改后货物资料错误: %+v", view)
	}
	// 编号与未装载状态保持原样。
	if view.ID != "G1" || view.Loaded || view.CompartmentID != "" {
		t.Fatalf("编号与舱位状态应保持原样: %+v", view)
	}
	// 舱位重量不受影响。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 0 || cpt.RemainingWeight != 100 {
		t.Fatalf("未装载货物修改不应影响舱位重量: %+v", cpt)
	}
}

func TestModifyCargoSameContentSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	// 提交与现有资料相同的内容也应成功，不新增货物。
	if err := r.ModifyCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatalf("相同内容修改应成功: %v", err)
	}
	view, _ := r.Cargo("G1")
	if view.Weight != 30 || view.Destination != "Shanghai" || !view.AllowMixed {
		t.Fatalf("资料应保持不变: %+v", view)
	}
}

func TestModifyCargoTrimsIDAndDestination(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("  G1  ", 20, "  Shanghai  ", true); err != nil {
		t.Fatal(err)
	}
	// 修改时编号与目的地沿用去首尾空白规则。
	if err := r.ModifyCargo("  G1  ", 30, "  Beijing  ", false); err != nil {
		t.Fatal(err)
	}
	view, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Weight != 30 || view.Destination != "Beijing" || view.AllowMixed {
		t.Fatalf("编号与目的地应去空白: %+v", view)
	}
	// 编号仍区分大小写。
	if _, err := r.Cargo("g1"); err == nil {
		t.Fatal("编号应区分大小写")
	}
}

// ---------- 校验错误 ----------

func TestModifyCargoValidation(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	requireError(t, r.ModifyCargo("", 10, "Y", true), ErrInvalidID)
	requireError(t, r.ModifyCargo("   ", 10, "Y", true), ErrInvalidID)
	requireError(t, r.ModifyCargo("GX", 10, "Y", true), ErrNotFound)
	requireError(t, r.ModifyCargo("G1", 0, "Y", true), ErrInvalidWeight)
	requireError(t, r.ModifyCargo("G1", -5, "Y", true), ErrInvalidWeight)
	requireError(t, r.ModifyCargo("G1", 10, "   ", true), ErrInvalidDestination)
}

func TestModifyCargoNotFoundNotRegistration(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	// 修改不存在的货物必须拒绝，不能当成登记。
	se := requireError(t, func() error {
		return r.ModifyCargo("GX", 10, "Y", true)
	}(), ErrNotFound)
	if se.ID != "GX" {
		t.Fatalf("应指出不存在的货物，实际 ID=%q", se.ID)
	}
	// 没有新增货物。
	if _, err := r.Cargo("GX"); err == nil {
		t.Fatal("修改不存在的货物不应新增货物")
	}
}

// ---------- 修改已装载货物：重量 ----------

func TestModifyCargoLoadedWeight(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	// 加重到 50，舱位已用重量与剩余重量随之变化。
	if err := r.ModifyCargo("G1", 50, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 || cpt.RemainingWeight != 50 {
		t.Fatalf("舱位重量应按新重量计算: %+v", cpt)
	}
	// 卸下收回的是更正后的重量。
	mustAdjust(t, r, "A2", []Op{{Kind: OpUnload, CargoID: "G1"}})
	cpt, _ = r.Compartment("C1")
	if cpt.UsedWeight != 0 || cpt.RemainingWeight != 100 {
		t.Fatalf("卸下后应收回全部重量: %+v", cpt)
	}
}

func TestModifyCargoExactlyAtMaxWeight(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	// 恰好达到承重允许保存。
	if err := r.ModifyCargo("G1", 50, "X", true); err != nil {
		t.Fatalf("恰好达到承重应允许保存: %v", err)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 || cpt.RemainingWeight != 0 {
		t.Fatalf("舱位重量错误: %+v", cpt)
	}
}

func TestModifyCargoOverweight(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	// 加重到 60 会超过承重。
	se := requireError(t, func() error {
		return r.ModifyCargo("G1", 60, "X", true)
	}(), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重应指出所属舱位，实际 ID=%q", se.ID)
	}
	// 旧资料保留。
	view, _ := r.Cargo("G1")
	if view.Weight != 30 {
		t.Fatalf("失败修改不应改变重量: %+v", view)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 30 || cpt.RemainingWeight != 20 {
		t.Fatalf("失败修改不应改变舱位重量: %+v", cpt)
	}
}

func TestModifyCargoOverweightWithOtherCargo(t *testing.T) {
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
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// G1 加重到 90：扣除旧重量后总重量 110，超过承重。
	se := requireError(t, func() error {
		return r.ModifyCargo("G1", 90, "X", true)
	}(), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重应指出所属舱位，实际 ID=%q", se.ID)
	}
	// 两件货物的旧资料都保留。
	v1, _ := r.Cargo("G1")
	v2, _ := r.Cargo("G2")
	if v1.Weight != 30 || v2.Weight != 20 {
		t.Fatalf("失败修改不应改变任何货物重量: %+v %+v", v1, v2)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 {
		t.Fatalf("失败修改不应改变舱位重量: %+v", cpt)
	}
}

func TestModifyCargoOverflow(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", math.MaxInt64-10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// G1 加重 1 千克会导致合计溢出 int64。
	se := requireError(t, func() error {
		return r.ModifyCargo("G1", math.MaxInt64-9, "X", true)
	}(), ErrOverflow)
	if se.ID != "C1" {
		t.Fatalf("溢出应指出所属舱位，实际 ID=%q", se.ID)
	}
	// 旧资料保留，且不提供回绕后的总重量。
	view, _ := r.Cargo("G1")
	if view.Weight != math.MaxInt64-10 {
		t.Fatalf("失败修改不应改变重量: %+v", view)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64 {
		t.Fatalf("失败修改不应改变舱位重量: %+v", cpt)
	}
}

// ---------- 修改已装载货物：混装 ----------

func TestModifyCargoMixedLoadingConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// G1 上海、G2 北京，初始都允许混装才能共舱。
	if err := r.RegisterCargo("G1", 10, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// G1 目的地改成广州（与 G2 不同）并关闭混装许可 -> 冲突。
	se := requireError(t, func() error {
		return r.ModifyCargo("G1", 10, "Guangzhou", false)
	}(), ErrMixedLoading)
	if se.ID != "G1" {
		t.Fatalf("混装冲突应指出不允许混装的货物，实际 ID=%q", se.ID)
	}
	// 旧资料保留。
	view, _ := r.Cargo("G1")
	if view.Destination != "Shanghai" {
		t.Fatalf("失败修改不应改变目的地: %+v", view)
	}
}

func TestModifyCargoSameDestinationNoMixedAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// G1 上海、G2 北京，初始都允许混装才能共舱。
	if err := r.RegisterCargo("G1", 10, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// 把 G2 的目的地改成上海（与 G1 相同），同时关闭 G2 的混装许可，
	// 只要最终重量合法就应成功。
	if err := r.ModifyCargo("G2", 20, "Shanghai", false); err != nil {
		t.Fatalf("同目的地即使不许混装也应成功: %v", err)
	}
	view, _ := r.Cargo("G2")
	if view.Destination != "Shanghai" || view.AllowMixed {
		t.Fatalf("修改后资料错误: %+v", view)
	}
	// 舱位重量不变。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 30 {
		t.Fatalf("舱位重量错误: %+v", cpt)
	}
}

func TestModifyCargoEnableMixedResolvesConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// G1 上海、G2 北京，初始都允许混装才能共舱。
	if err := r.RegisterCargo("G1", 10, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// G1 改去广州并保持混装许可，最终合法应成功。
	if err := r.ModifyCargo("G1", 10, "Guangzhou", true); err != nil {
		t.Fatalf("开启混装许可后不同目的地应可共舱: %v", err)
	}
	view, _ := r.Cargo("G1")
	if view.Destination != "Guangzhou" || !view.AllowMixed {
		t.Fatalf("修改后资料错误: %+v", view)
	}
}

// ---------- 重量与混装同时不合法：重量优先 ----------

func TestModifyCargoWeightBeforeMixed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	// G1 上海、G2 北京，初始都允许混装才能共舱。
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// G1 加重到 60（超重）且目的地改成广州并关闭混装许可（混装冲突），
	// 重量原因优先。
	se := requireError(t, func() error {
		return r.ModifyCargo("G1", 60, "Guangzhou", false)
	}(), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("重量与混装同时不合法应先报告重量: %+v", se)
	}
	// 旧资料保留。
	view, _ := r.Cargo("G1")
	if view.Weight != 30 || view.Destination != "Shanghai" {
		t.Fatalf("失败修改不应改变资料: %+v", view)
	}
}

// ---------- 原子性：失败时全部保留 ----------

func TestModifyCargoFailurePreservesAll(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	// G1 上海、G2 北京，初始都允许混装才能共舱。
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	// G1 改成超重且混装冲突的资料，整次失败。
	se := requireError(t, func() error {
		return r.ModifyCargo("G1", 200, "Guangzhou", false)
	}(), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("应指出所属舱位: %+v", se)
	}
	// 两件货物的旧资料都保留。
	v1, _ := r.Cargo("G1")
	v2, _ := r.Cargo("G2")
	if v1.Weight != 30 || v1.Destination != "Shanghai" || !v1.AllowMixed {
		t.Fatalf("G1 旧资料应保留: %+v", v1)
	}
	if v2.Weight != 20 || v2.Destination != "Beijing" || !v2.AllowMixed {
		t.Fatalf("G2 资料应保留: %+v", v2)
	}
	// 舱位重量保留。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 || cpt.RemainingWeight != 50 {
		t.Fatalf("舱位重量应保留: %+v", cpt)
	}
}

// ---------- 不触碰调整编号与已保存调整结果 ----------

func TestModifyCargoDoesNotTouchAdjustments(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	first := mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	// 修改货物资料。
	if err := r.ModifyCargo("G1", 50, "X", true); err != nil {
		t.Fatal(err)
	}
	// 以原编号和原内容重复提交，仍返回首次成功时的变化与重量，不重新执行。
	replay, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID {
		t.Fatalf("重复提交应返回首次结果: %+v", replay)
	}
	if replay.CompartmentChanges[0].WeightAfter != first.CompartmentChanges[0].WeightAfter {
		t.Fatalf("重复提交应返回首次成功时的重量，不重新执行: %+v", replay.CompartmentChanges[0])
	}
	// 配载未被重复提交改变。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 50 {
		t.Fatalf("重复提交不应改变配载: %+v", cpt)
	}
}

func TestModifyCargoDoesNotOccupyAdjustmentID(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	// 修改不占用调整编号：A1 仍可用于一次正式调整。
	if err := r.ModifyCargo("G1", 40, "X", true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatalf("修改不应占用调整编号: %v", err)
	}
	if res.ID != "A1" {
		t.Fatalf("调整编号错误: %+v", res)
	}
}

// ---------- 快照独立性 ----------

func TestModifyCargoSnapshotsRetainOldContent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	// 修改前返回的查询快照保留原内容。
	old, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	oldCpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	// 修改资料。
	if err := r.ModifyCargo("G1", 50, "Beijing", false); err != nil {
		t.Fatal(err)
	}
	// 旧快照不受影响。
	if old.Weight != 30 || old.Destination != "Shanghai" || !old.AllowMixed {
		t.Fatalf("修改前的查询快照应保留原内容: %+v", old)
	}
	if oldCpt.UsedWeight != 30 || oldCpt.RemainingWeight != 70 {
		t.Fatalf("修改前的舱位快照应保留原内容: %+v", oldCpt)
	}
	// 调用方修改旧快照也不影响登记资料。
	old.Weight = 9999
	old.Destination = "HACKED"
	view, _ := r.Cargo("G1")
	if view.Weight != 50 || view.Destination != "Beijing" || view.AllowMixed {
		t.Fatalf("修改返回快照不应影响登记资料: %+v", view)
	}
}

// ---------- 登记入口不被覆盖 ----------

func TestModifyCargoDoesNotAllowRegisterOverwrite(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	// 原有登记入口遇到重复编号仍拒绝，不能借它隐式覆盖货物。
	se := requireError(t, func() error {
		return r.RegisterCargo("G1", 99, "Y", false)
	}(), ErrAlreadyExists)
	if se.ID != "G1" {
		t.Fatalf("重复登记应指出货物: %+v", se)
	}
	view, _ := r.Cargo("G1")
	if view.Weight != 30 || view.Destination != "X" || !view.AllowMixed {
		t.Fatalf("重复登记不应覆盖货物: %+v", view)
	}
}

// ---------- 修改后预览按新资料计算 ----------

func TestModifyCargoPreviewUsesNewInfo(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	// 修改重量后，预览反映新重量。
	if err := r.ModifyCargo("G1", 50, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Preview([]Op{{Kind: OpUnload, CargoID: "G1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("卸下应可提交: %+v", res.Rejections)
	}
	after := res.Compartments[0].After
	if after.UsedWeight != 0 || after.RemainingWeight != 100 {
		t.Fatalf("预览卸下后应收回新重量: %+v", after)
	}
}
