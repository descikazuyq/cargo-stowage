package stowage

import (
	"errors"
	"math"
	"testing"
)

// requireError 断言 err 是 *Error 且 Kind 匹配。
func requireError(t *testing.T, err error, kind ErrorKind) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际为 nil", kind)
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("期望 *Error，实际为 %T: %v", err, err)
	}
	if se.Kind != kind {
		t.Fatalf("期望错误 %s，实际为 %s（%v）", kind, se.Kind, err)
	}
	return se
}

// ---------- 登记 ----------

func TestRegisterCompartmentSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("  C1  ", 100); err != nil {
		t.Fatal(err)
	}
	view, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if view.MaxWeight != 100 || view.UsedWeight != 0 || view.RemainingWeight != 100 {
		t.Fatalf("舱位初始状态错误: %+v", view)
	}
	if len(view.Cargo) != 0 {
		t.Fatalf("新舱位不应有货物: %+v", view.Cargo)
	}
}

func TestRegisterCompartmentInvalid(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"", "   ", "\t\n"} {
		requireError(t, r.RegisterCompartment(id, 100), ErrInvalidID)
	}
	for _, w := range []int64{0, -1, -100} {
		requireError(t, r.RegisterCompartment("C1", w), ErrInvalidWeight)
	}
}

func TestRegisterCompartmentDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	err := r.RegisterCompartment("C1", 200)
	se := requireError(t, err, ErrAlreadyExists)
	if se.ID != "C1" {
		t.Fatalf("重复登记错误应指出舱位编号，实际 ID=%q", se.ID)
	}
	// 原记录保留。
	view, _ := r.Compartment("C1")
	if view.MaxWeight != 100 {
		t.Fatalf("重复登记后原记录被改变: %+v", view)
	}
}

func TestRegisterCargoSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("  G1  ", 20, "  Shanghai  ", true); err != nil {
		t.Fatal(err)
	}
	view, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Weight != 20 || view.Destination != "Shanghai" || !view.AllowMixed {
		t.Fatalf("货物登记信息错误: %+v", view)
	}
	if view.Loaded || view.CompartmentID != "" {
		t.Fatalf("货物登记后应处于未装载状态: %+v", view)
	}
}

func TestRegisterCargoInvalid(t *testing.T) {
	r := NewRegistry()
	requireError(t, r.RegisterCargo("", 10, "X", true), ErrInvalidID)
	requireError(t, r.RegisterCargo("G1", 0, "X", true), ErrInvalidWeight)
	requireError(t, r.RegisterCargo("G1", -5, "X", true), ErrInvalidWeight)
	requireError(t, r.RegisterCargo("G1", 10, "   ", true), ErrInvalidDestination)
}

func TestRegisterCargoDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCargo("G1", 20, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	err := r.RegisterCargo("G1", 99, "Beijing", false)
	se := requireError(t, err, ErrAlreadyExists)
	if se.ID != "G1" {
		t.Fatalf("重复登记错误应指出货物编号，实际 ID=%q", se.ID)
	}
	view, _ := r.Cargo("G1")
	if view.Weight != 20 || view.Destination != "Shanghai" || !view.AllowMixed {
		t.Fatalf("重复登记后原记录被改变: %+v", view)
	}
}

func TestIDTrimmedAndCaseSensitive(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("  C1  ", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("  g1  ", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	// 去空白后可查。
	if _, err := r.Compartment("C1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Cargo("g1"); err != nil {
		t.Fatal(err)
	}
	// 区分大小写。
	if _, err := r.Compartment("c1"); err == nil {
		t.Fatal("编号应区分大小写")
	}
	if _, err := r.Cargo("G1"); err == nil {
		t.Fatal("编号应区分大小写")
	}
}

// ---------- 装载 ----------

func TestLoadSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "A1" {
		t.Fatalf("结果编号错误: %s", res.ID)
	}
	if len(res.CargoChanges) != 1 {
		t.Fatalf("应列出 1 条货物变化: %+v", res.CargoChanges)
	}
	cc := res.CargoChanges[0]
	if cc.CargoID != "G1" || cc.From != "" || cc.To != "C1" {
		t.Fatalf("货物变化错误: %+v", cc)
	}
	if len(res.CompartmentChanges) != 1 {
		t.Fatalf("应列出 1 条舱位变化: %+v", res.CompartmentChanges)
	}
	cp := res.CompartmentChanges[0]
	if cp.CompartmentID != "C1" || cp.WeightBefore != 0 || cp.WeightAfter != 30 {
		t.Fatalf("舱位变化错误: %+v", cp)
	}
	cv, _ := r.Cargo("G1")
	if !cv.Loaded || cv.CompartmentID != "C1" {
		t.Fatalf("货物应已装载: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 30 || cpt.RemainingWeight != 70 {
		t.Fatalf("舱位重量错误: %+v", cpt)
	}
}

func TestLoadErrors(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	// 货物不存在。
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "GX", Target: "C1"}})
		return err
	}(), ErrNotFound)
	if se.ID != "GX" {
		t.Fatalf("应指出不存在的货物，实际 ID=%q", se.ID)
	}
	// 舱位不存在。
	se = requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G1", Target: "CX"}})
		return err
	}(), ErrNotFound)
	if se.ID != "CX" {
		t.Fatalf("应指出不存在的舱位，实际 ID=%q", se.ID)
	}
	// 正常装载。
	if _, err := r.Adjust("A3", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	// 已装载不能再次装载。
	se = requireError(t, func() error {
		_, err := r.Adjust("A4", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
		return err
	}(), ErrStateMismatch)
	if se.ID != "G1" {
		t.Fatalf("应指出状态不符的货物，实际 ID=%q", se.ID)
	}
	// 配载未被破坏。
	cv, _ := r.Cargo("G1")
	if cv.CompartmentID != "C1" {
		t.Fatalf("失败调整不应改变配载: %+v", cv)
	}
}

func TestLoadOverweight(t *testing.T) {
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
	_, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	se := requireError(t, err, ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重应指出舱位，实际 ID=%q", se.ID)
	}
	// 整次不生效。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 0 {
		t.Fatalf("失败调整不应改变舱位重量: %+v", cpt)
	}
}

func TestLoadMixedLoadingConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// G1 不允许混装（目的地 Shanghai），G2 允许（目的地 Beijing）。
	if err := r.RegisterCargo("G1", 10, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	// 先装 G1。
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	// 再装 G2：目的地不同，G1 不允许混装 -> 冲突。
	se := requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
		return err
	}(), ErrMixedLoading)
	if se.ID != "G1" {
		t.Fatalf("混装冲突应指出不允许混装的货物，实际 ID=%q", se.ID)
	}
	// 配载保持原样。
	cpt, _ := r.Compartment("C1")
	if len(cpt.Cargo) != 1 || cpt.Cargo[0].ID != "G1" {
		t.Fatalf("失败调整不应改变舱位货物: %+v", cpt.Cargo)
	}
}

func TestLoadSameDestinationNoMixedAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	// 同一目的地，即使都不允许混装也可以共舱。
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatalf("同一目的地应可共舱: %v", err)
	}
}

func TestLoadAllMixedAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "Shanghai", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 30, "Guangzhou", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	}); err != nil {
		t.Fatalf("全部允许混装时不同目的地应可共舱: %v", err)
	}
}

func TestWeightOverflow(t *testing.T) {
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
	// 再装一件会导致合计超过 int64 可表示范围。
	se := requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
		return err
	}(), ErrOverflow)
	if se.ID != "C1" {
		t.Fatalf("重量溢出应指出舱位，实际 ID=%q", se.ID)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64 {
		t.Fatalf("失败调整不应改变舱位重量: %+v", cpt)
	}
}

// ---------- 卸下 ----------

func TestUnloadSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A2", []Op{{Kind: OpUnload, CargoID: "G1"}})
	if err != nil {
		t.Fatal(err)
	}
	cc := res.CargoChanges[0]
	if cc.From != "C1" || cc.To != "" {
		t.Fatalf("卸下变化错误: %+v", cc)
	}
	cp := res.CompartmentChanges[0]
	if cp.WeightBefore != 30 || cp.WeightAfter != 0 {
		t.Fatalf("卸下舱位重量变化错误: %+v", cp)
	}
	// 货物记录保留，状态为未装载。
	cv, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if cv.Loaded || cv.CompartmentID != "" {
		t.Fatalf("卸下后货物应为未装载: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 0 || cpt.RemainingWeight != 100 {
		t.Fatalf("卸下后舱位重量应收回: %+v", cpt)
	}
}

func TestUnloadErrors(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	// 未装载不能卸下。
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpUnload, CargoID: "G1"}})
		return err
	}(), ErrStateMismatch)
	if se.ID != "G1" {
		t.Fatalf("应指出状态不符的货物，实际 ID=%q", se.ID)
	}
	// 不存在的货物。
	requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{{Kind: OpUnload, CargoID: "GX"}})
		return err
	}(), ErrNotFound)
}

// ---------- 移动 ----------

func TestMoveSuccess(t *testing.T) {
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
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A2", []Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}})
	if err != nil {
		t.Fatal(err)
	}
	cc := res.CargoChanges[0]
	if cc.From != "C1" || cc.To != "C2" {
		t.Fatalf("移动变化错误: %+v", cc)
	}
	// 受影响舱位按编号字典序排列。
	if len(res.CompartmentChanges) != 2 {
		t.Fatalf("应列出 2 个舱位变化: %+v", res.CompartmentChanges)
	}
	if res.CompartmentChanges[0].CompartmentID != "C1" ||
		res.CompartmentChanges[0].WeightBefore != 30 || res.CompartmentChanges[0].WeightAfter != 0 {
		t.Fatalf("原舱位变化错误: %+v", res.CompartmentChanges[0])
	}
	if res.CompartmentChanges[1].CompartmentID != "C2" ||
		res.CompartmentChanges[1].WeightBefore != 0 || res.CompartmentChanges[1].WeightAfter != 30 {
		t.Fatalf("目标舱位变化错误: %+v", res.CompartmentChanges[1])
	}
	cv, _ := r.Cargo("G1")
	if cv.CompartmentID != "C2" {
		t.Fatalf("货物应已在新舱位: %+v", cv)
	}
}

func TestMoveErrors(t *testing.T) {
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
	// 未装载不能移动。
	requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}})
		return err
	}(), ErrStateMismatch)
	// 装载后移动到原舱位被拒绝。
	if _, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	se := requireError(t, func() error {
		_, err := r.Adjust("A3", []Op{{Kind: OpMove, CargoID: "G1", Target: "C1"}})
		return err
	}(), ErrDuplicateOp)
	if se.ID != "G1" {
		t.Fatalf("移动到原舱位应指出货物，实际 ID=%q", se.ID)
	}
	// 移动到不存在的舱位。
	se = requireError(t, func() error {
		_, err := r.Adjust("A4", []Op{{Kind: OpMove, CargoID: "G1", Target: "CX"}})
		return err
	}(), ErrNotFound)
	if se.ID != "CX" {
		t.Fatalf("应指出不存在的目标舱位，实际 ID=%q", se.ID)
	}
	// 配载保持原样。
	cv, _ := r.Cargo("G1")
	if cv.CompartmentID != "C1" {
		t.Fatalf("失败调整不应改变配载: %+v", cv)
	}
}

// ---------- 多操作调整与原子性 ----------

func TestSwapBetweenFullCompartments(t *testing.T) {
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
	// 两个满载舱位交换货物，最终状态合法即可成功。
	res, err := r.Adjust("A2", []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C1"},
	})
	if err != nil {
		t.Fatalf("满载舱位交换应成功: %v", err)
	}
	if len(res.CompartmentChanges) != 2 {
		t.Fatalf("应列出 2 个舱位变化: %+v", res.CompartmentChanges)
	}
	cv1, _ := r.Cargo("G1")
	cv2, _ := r.Cargo("G2")
	if cv1.CompartmentID != "C2" || cv2.CompartmentID != "C1" {
		t.Fatalf("交换后货物归属错误: %+v %+v", cv1, cv2)
	}
	cpt1, _ := r.Compartment("C1")
	cpt2, _ := r.Compartment("C2")
	if cpt1.UsedWeight != 30 || cpt2.UsedWeight != 30 {
		t.Fatalf("交换后舱位重量错误: %+v %+v", cpt1, cpt2)
	}
}

func TestBatchMixedOperations(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
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
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}
	// 一次调整：卸下 G1、移动 G2 到 C2、装载 G3 不动（未出现）。
	res, err := r.Adjust("A2", []Op{
		{Kind: OpUnload, CargoID: "G1"},
		{Kind: OpMove, CargoID: "G2", Target: "C2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 货物变化按编号字典序排列。
	if len(res.CargoChanges) != 2 {
		t.Fatalf("应列出 2 条货物变化: %+v", res.CargoChanges)
	}
	if res.CargoChanges[0].CargoID != "G1" || res.CargoChanges[0].From != "C1" || res.CargoChanges[0].To != "" {
		t.Fatalf("G1 变化错误: %+v", res.CargoChanges[0])
	}
	if res.CargoChanges[1].CargoID != "G2" || res.CargoChanges[1].From != "C1" || res.CargoChanges[1].To != "C2" {
		t.Fatalf("G2 变化错误: %+v", res.CargoChanges[1])
	}
	cpt1, _ := r.Compartment("C1")
	cpt2, _ := r.Compartment("C2")
	if cpt1.UsedWeight != 0 || cpt2.UsedWeight != 50 {
		t.Fatalf("舱位重量错误: %+v %+v", cpt1, cpt2)
	}
}

func TestEmptyAdjustmentRejected(t *testing.T) {
	r := NewRegistry()
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", nil)
		return err
	}(), ErrEmptyAdjustment)
	if se.AdjustmentID != "A1" {
		t.Fatalf("空调整应指出调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{})
		return err
	}(), ErrEmptyAdjustment)
}

func TestDuplicateCargoInAdjustment(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	// 同一件货物在一次调整中出现两次。
	se := requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
			{Kind: OpUnload, CargoID: "G1"},
		})
		return err
	}(), ErrDuplicateOp)
	if se.ID != "G1" {
		t.Fatalf("重复操作应指出货物，实际 ID=%q", se.ID)
	}
	// 配载保持原样。
	cv, _ := r.Cargo("G1")
	if cv.CompartmentID != "C1" {
		t.Fatalf("失败调整不应改变配载: %+v", cv)
	}
}

func TestAtomicFailureRollsBack(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 60, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 50, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 卸下 G3 合法；移动 G1 到 C2 会导致 C2 超重（50+60 > 100），整次失败。
	se := requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{
			{Kind: OpUnload, CargoID: "G3"},        // 合法
			{Kind: OpMove, CargoID: "G1", Target: "C2"}, // 超重
		})
		return err
	}(), ErrOverweight)
	if se.ID != "C2" {
		t.Fatalf("超重应指出舱位，实际 ID=%q", se.ID)
	}
	// 三件货物归属都不变。
	cv1, _ := r.Cargo("G1")
	cv2, _ := r.Cargo("G2")
	cv3, _ := r.Cargo("G3")
	if cv1.CompartmentID != "C1" || cv2.CompartmentID != "C2" || cv3.CompartmentID != "C1" {
		t.Fatalf("失败调整不应改变任何货物归属: %+v %+v %+v", cv1, cv2, cv3)
	}
	cpt1, _ := r.Compartment("C1")
	cpt2, _ := r.Compartment("C2")
	if cpt1.UsedWeight != 70 || cpt2.UsedWeight != 50 {
		t.Fatalf("失败调整不应改变舱位重量: %+v %+v", cpt1, cpt2)
	}
}

// ---------- 调整编号幂等 ----------

func TestAdjustmentIDIdempotent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	// 首次成功。
	res1, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	// 之后另有调整。
	if _, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G2", Target: "C2"}}); err != nil {
		t.Fatal(err)
	}
	// 以相同编号、相同内容（顺序不同）重复提交。
	res2, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.ID != res1.ID {
		t.Fatalf("重复提交应返回首次结果: %+v", res2)
	}
	if len(res2.CargoChanges) != len(res1.CargoChanges) {
		t.Fatalf("重复提交应返回首次结果的货物变化")
	}
	if res2.CargoChanges[0].From != "" || res2.CargoChanges[0].To != "C1" {
		t.Fatalf("重复提交应返回首次结果: %+v", res2.CargoChanges[0])
	}
	// 配载未被重复提交改变：G1 仍在 C1，G2 仍在 C2。
	cv1, _ := r.Cargo("G1")
	cv2, _ := r.Cargo("G2")
	if cv1.CompartmentID != "C1" || cv2.CompartmentID != "C2" {
		t.Fatalf("重复提交不应改变配载: %+v %+v", cv1, cv2)
	}
}

func TestAdjustmentIDConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	// 相同编号用于不同内容。
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
		return err
	}(), ErrAdjustmentIDConflict)
	if se.AdjustmentID != "A1" {
		t.Fatalf("编号冲突应指出调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	// 配载未改变。
	cv2, _ := r.Cargo("G2")
	if cv2.Loaded {
		t.Fatalf("冲突调整不应生效: %+v", cv2)
	}
}

func TestFailedAdjustmentDoesNotConsumeID(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 60, "X", true); err != nil {
		t.Fatal(err)
	}
	// 第一次失败（超重），编号不被占用。
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
		return err
	}(), ErrOverweight)
	if se.AdjustmentID != "A1" {
		t.Fatalf("失败应指出调整编号: %v", se)
	}
	// 修正登记（换一个舱位）后用相同编号重试成功。
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C2"}})
	if err != nil {
		t.Fatalf("失败调整不应占用编号，修正后应可重试: %v", err)
	}
	if res.ID != "A1" {
		t.Fatalf("重试结果编号错误: %+v", res)
	}
}

func TestAdjustmentIDInvalid(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	requireError(t, func() error {
		_, err := r.Adjust("   ", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
		return err
	}(), ErrInvalidID)
}

// 编号允许任意非空字节，内容判断必须按原始字节区分登记处能区分的编号：
// 含不同无效 UTF-8 字节的货物编号不能因显示为同一替代字符而被混为一件。
func TestAdjustmentContentDistinguishesInvalidUTF8Cargo(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G\x80", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G\x81", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	res1, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G\x80", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	// 同一调整编号用于另一件字节不同的货物：按编号冲突拒绝。
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G\x81", Target: "C1"}})
		return err
	}(), ErrAdjustmentIDConflict)
	if se.AdjustmentID != "A1" {
		t.Fatalf("冲突应指出调整编号 A1，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	// 配载保持提交前状态：第一件仍在 C1，第二件未装载，舱位只占用 10 千克。
	cv1, _ := r.Cargo("G\x80")
	cv2, _ := r.Cargo("G\x81")
	if !cv1.Loaded || cv1.CompartmentID != "C1" {
		t.Fatalf("第一件货物应仍在 C1: %+v", cv1)
	}
	if cv2.Loaded {
		t.Fatalf("第二件货物不应被装载: %+v", cv2)
	}
	comp, _ := r.Compartment("C1")
	if comp.UsedWeight != 10 || len(comp.Cargo) != 1 {
		t.Fatalf("舱位应只占用 10 千克且只有一件货物: %+v", comp)
	}
	// 换一个未使用的调整编号，第二件仍能正常装入：两件货物始终是独立记录。
	res2, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G\x81", Target: "C1"}})
	if err != nil {
		t.Fatalf("新编号应能装入第二件货物: %v", err)
	}
	if res2.ID != "A2" || len(res2.CargoChanges) != 1 || res2.CargoChanges[0].CargoID != "G\x81" {
		t.Fatalf("第二次调整结果错误: %+v", res2)
	}
	comp, _ = r.Compartment("C1")
	if comp.UsedWeight != 20 || len(comp.Cargo) != 2 {
		t.Fatalf("两件货物应都在舱位中: %+v", comp)
	}
	// 首次保存的结果不受冲突提交影响，仍可按原编号原内容取回。
	again, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G\x80", Target: "C1"}})
	if err != nil {
		t.Fatalf("原编号原内容应仍返回首次结果: %v", err)
	}
	if again.ID != res1.ID || len(again.CargoChanges) != 1 || again.CargoChanges[0].CargoID != "G\x80" {
		t.Fatalf("首次保存的结果应原样返回: %+v", again)
	}
}

// 目标舱位编号同样按原始字节区分：同一调整编号指向字节不同的舱位
// 不能视为相同内容。
func TestAdjustmentContentDistinguishesInvalidUTF8Target(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C\x80", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C\x81", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C\x80"}}); err != nil {
		t.Fatal(err)
	}
	requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C\x81"}})
		return err
	}(), ErrAdjustmentIDConflict)
	cv, _ := r.Cargo("G1")
	if !cv.Loaded || cv.CompartmentID != "C\x80" {
		t.Fatalf("货物应仍在原舱位: %+v", cv)
	}
}

// 含无效 UTF-8 字节的编号与真正包含 Unicode 替代字符 U+FFFD 的编号
// 是不同的编号，内容判断中也不能混同。
func TestAdjustmentContentDistinguishesReplacementChar(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G\x80", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G�", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G\x80", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G�", Target: "C1"}})
		return err
	}(), ErrAdjustmentIDConflict)
	// 反向同样区分。
	if _, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G�", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	requireError(t, func() error {
		_, err := r.Adjust("A2", []Op{{Kind: OpLoad, CargoID: "G\x80", Target: "C1"}})
		return err
	}(), ErrAdjustmentIDConflict)
}

// 字节完全相同的重复提交（含无效 UTF-8 编号）仍是幂等的：
// 返回首次成功结果，不重新装卸。
func TestAdjustmentIdempotentWithInvalidUTF8(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C\x80", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G\x80", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	res1, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: " G\x80 ", Target: " C\x80 "}})
	if err != nil {
		t.Fatal(err)
	}
	// 忽略首尾空白、操作内容字节相同：返回首次结果。
	res2, err := r.Adjust(" A1 ", []Op{{Kind: OpLoad, CargoID: "G\x80", Target: "C\x80"}})
	if err != nil {
		t.Fatalf("相同内容重复提交应成功: %v", err)
	}
	if res2.ID != res1.ID || len(res2.CargoChanges) != 1 || res2.CargoChanges[0].To != "C\x80" {
		t.Fatalf("应返回首次成功结果: %+v", res2)
	}
	comp, _ := r.Compartment("C\x80")
	if comp.UsedWeight != 10 || len(comp.Cargo) != 1 {
		t.Fatalf("重复提交不应改变配载: %+v", comp)
	}
}

// ---------- 查询 ----------

func TestQueryNotFound(t *testing.T) {
	r := NewRegistry()
	se := requireError(t, func() error {
		_, err := r.Compartment("CX")
		return err
	}(), ErrNotFound)
	if se.ID != "CX" {
		t.Fatalf("应指出不存在的舱位，实际 ID=%q", se.ID)
	}
	se = requireError(t, func() error {
		_, err := r.Cargo("GX")
		return err
	}(), ErrNotFound)
	if se.ID != "GX" {
		t.Fatalf("应指出不存在的货物，实际 ID=%q", se.ID)
	}
}

func TestCompartmentViewSorted(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"G3", "G1", "G2"} {
		if err := r.RegisterCargo(id, 10, "X", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	view, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if view.UsedWeight != 30 || view.RemainingWeight != 970 {
		t.Fatalf("舱位重量错误: %+v", view)
	}
	got := []string{view.Cargo[0].ID, view.Cargo[1].ID, view.Cargo[2].ID}
	want := []string{"G1", "G2", "G3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("货物清单应按编号字典序排列: got=%v want=%v", got, want)
		}
	}
}

// ---------- 快照独立性 ----------

func TestQuerySnapshotIndependent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}
	view, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	// 调用方修改返回内容。
	view.UsedWeight = 9999
	view.Cargo[0].Weight = 9999
	view.Cargo[0].ID = "HACKED"

	// 系统记录不受影响。
	view2, _ := r.Compartment("C1")
	if view2.UsedWeight != 10 || view2.Cargo[0].Weight != 10 || view2.Cargo[0].ID != "G1" {
		t.Fatalf("查询快照不应影响系统记录: %+v", view2)
	}
	cv, _ := r.Cargo("G1")
	if cv.Weight != 10 {
		t.Fatalf("货物快照不应受影响: %+v", cv)
	}
}

func TestAdjustmentResultSnapshotIndependent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	// 调用方修改返回结果。
	res.CargoChanges[0].CargoID = "HACKED"
	res.CompartmentChanges[0].WeightAfter = 9999

	// 以相同编号重复提交，返回的首次结果未被污染。
	res2, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res2.CargoChanges[0].CargoID != "G1" {
		t.Fatalf("已保存的调整结果不应被调用方修改: %+v", res2.CargoChanges[0])
	}
	if res2.CompartmentChanges[0].WeightAfter != 10 {
		t.Fatalf("已保存的调整结果不应被调用方修改: %+v", res2.CompartmentChanges[0])
	}
}

// ---------- 其他边界 ----------

func TestUnknownOpKind(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	se := requireError(t, func() error {
		_, err := r.Adjust("A1", []Op{{Kind: OpKind(99), CargoID: "G1", Target: "C1"}})
		return err
	}(), ErrInvalidOp)
	if se.ID != "G1" {
		t.Fatalf("无效操作种类应指出货物，实际 ID=%q", se.ID)
	}
}

func TestMoveCargoIntoSameDestinationMixedCompartment(t *testing.T) {
	// 综合场景：移动后目标舱室混装合法、原舱室重量收回。
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}
	// G1 与 G2 目的地相同，移动 G1 到 C2 合法。
	if _, err := r.Adjust("A2", []Op{{Kind: OpMove, CargoID: "G1", Target: "C2"}}); err != nil {
		t.Fatalf("相同目的地混装应合法: %v", err)
	}
	cpt1, _ := r.Compartment("C1")
	cpt2, _ := r.Compartment("C2")
	if cpt1.UsedWeight != 0 || cpt2.UsedWeight != 50 {
		t.Fatalf("舱位重量错误: %+v %+v", cpt1, cpt2)
	}
}
