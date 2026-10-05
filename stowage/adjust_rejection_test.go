package stowage

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// 本文件为正式提交（Adjust）补充回归保障：一批安排在多个舱位同时触发
// 重量与混装限制时，正式提交只返回一个结构化错误，报告顺序固定为
// 先按舱位编号字典序选最靠前的违规舱位、同一舱位先重量后混装；被拒绝
// 后原配载可完整回查（整批原子性）；并保护 Preview（列全拒绝原因）与
// Adjust（只报一个）两种入口各自的用途。

// ---------- 测试辅助 ----------

func mustRegisterCompartment(t *testing.T, r *Registry, id string, maxWeight int64) {
	t.Helper()
	if err := r.RegisterCompartment(id, maxWeight); err != nil {
		t.Fatalf("登记舱位 %s 失败: %v", id, err)
	}
}

func mustRegisterCargo(t *testing.T, r *Registry, id string, weight int64, dest string, mixed bool) {
	t.Helper()
	if err := r.RegisterCargo(id, weight, dest, mixed); err != nil {
		t.Fatalf("登记货物 %s 失败: %v", id, err)
	}
}

// submitExpectReject 提交并断言调整被拒绝：err 为单条 *Error，且不返回
// 成功结果。返回该结构化错误供调用方核对 Kind、ID、AdjustmentID 与说明。
func submitExpectReject(t *testing.T, r *Registry, id string, ops []Op) *Error {
	t.Helper()
	res, err := r.Adjust(id, ops)
	if err == nil {
		t.Fatalf("调整 %s 应被拒绝，却返回成功: %+v", id, res)
	}
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("期望 *Error，实际为 %T: %v", err, err)
	}
	return se
}

// snapshotCompartments 对指定舱位做提交前快照（完整清单、已用与剩余重量）。
func snapshotCompartments(r *Registry, ids ...string) map[string]CompartmentView {
	snap := make(map[string]CompartmentView, len(ids))
	for _, id := range ids {
		v, err := r.Compartment(id)
		if err != nil {
			panic(err)
		}
		snap[id] = *v
	}
	return snap
}

// snapshotCargo 对给定货物做提交前快照（重量、目的地、混装许可、归属）。
func snapshotCargo(r *Registry, ids ...string) map[string]CargoView {
	snap := make(map[string]CargoView, len(ids))
	for _, id := range ids {
		v, err := r.Cargo(id)
		if err != nil {
			panic(err)
		}
		snap[id] = *v
	}
	return snap
}

// assertCompartmentsUnchanged 对比提交前快照与拒绝后查询结果。
func assertCompartmentsUnchanged(t *testing.T, r *Registry, before map[string]CompartmentView) {
	t.Helper()
	for id, want := range before {
		got, err := r.Compartment(id)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", id, err)
		}
		if got.MaxWeight != want.MaxWeight ||
			got.UsedWeight != want.UsedWeight ||
			got.RemainingWeight != want.RemainingWeight ||
			!reflect.DeepEqual(cargoIDs(*got), cargoIDs(want)) {
			t.Fatalf("舱位 %s 在拒绝后改变: 提交前 %+v, 提交后 %+v", id, want, got)
		}
	}
}

// assertCargoUnchanged 对比提交前货物快照与拒绝后查询结果（含登记资料
// 与装载状态）。
func assertCargoUnchanged(t *testing.T, r *Registry, before map[string]CargoView) {
	t.Helper()
	for id, want := range before {
		got, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if got.Weight != want.Weight ||
			got.Destination != want.Destination ||
			got.AllowMixed != want.AllowMixed ||
			got.Loaded != want.Loaded ||
			got.CompartmentID != want.CompartmentID {
			t.Fatalf("货物 %s 在拒绝后改变: 提交前 %+v, 提交后 %+v", id, want, got)
		}
	}
}

// assertAllEmpty 确认四件货物都未装载、两个舱位都是空舱。
func assertAllEmpty(t *testing.T, r *Registry) {
	t.Helper()
	for _, id := range []string{"G1", "G2", "G3", "G4"} {
		c, _ := r.Cargo(id)
		if c.Loaded || c.CompartmentID != "" {
			t.Fatalf("货物 %s 应仍未装载，实际在 %q", id, c.CompartmentID)
		}
	}
	for _, id := range []string{"C1", "C2"} {
		cpt, _ := r.Compartment(id)
		if cpt.UsedWeight != 0 || cpt.RemainingWeight != cpt.MaxWeight || len(cpt.Cargo) != 0 {
			t.Fatalf("舱位 %s 应仍为空舱，实际 %+v", id, cpt)
		}
	}
}

// ---------- 跨舱位报告对象选择 ----------

// C1 仅混装冲突、C2 仅超重时，必须报告字典序最靠前的 C1 的混装冲突，
// 错误对象为 C1 内不允许混装且编号最靠前的货物，说明指出冲突舱位。
func TestAdjustReportsLexicographicallyEarliestCompartment(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "C2", 50)
	// C1 最终：G1（上海，不许混装）+ G2（北京，允许）-> 仅混装冲突。
	mustRegisterCargo(t, r, "G1", 10, "上海", false)
	mustRegisterCargo(t, r, "G2", 10, "北京", true)
	// C2 最终：G3+G4 共 60 > 50 -> 仅超重，同目的地不构成混装冲突。
	mustRegisterCargo(t, r, "G3", 30, "X", true)
	mustRegisterCargo(t, r, "G4", 30, "X", true)

	ops := []Op{
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	}
	se := submitExpectReject(t, r, "  adj-1  ", ops)
	if se.Kind != ErrMixedLoading || se.ID != "G1" {
		t.Fatalf("应报告 C1 的混装冲突并指出 G1，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if !strings.Contains(se.Error(), "C1") {
		t.Fatalf("混装说明应指出冲突舱位 C1: %s", se.Error())
	}
	// 调整编号去掉首尾空白后携带。
	if se.AdjustmentID != "adj-1" {
		t.Fatalf("错误应携带去空白后的调整编号 adj-1，实际 %q", se.AdjustmentID)
	}
	assertAllEmpty(t, r)
}

// 无论 C2 的操作放在清单前面还是后面，报告结果都是 C1 的混装冲突。
func TestAdjustReportStableAcrossOpOrder(t *testing.T) {
	build := func() (*Registry, []Op) {
		r := NewRegistry()
		mustRegisterCompartment(t, r, "C1", 1000)
		mustRegisterCompartment(t, r, "C2", 50)
		mustRegisterCargo(t, r, "G1", 10, "上海", false)
		mustRegisterCargo(t, r, "G2", 10, "北京", true)
		mustRegisterCargo(t, r, "G3", 30, "X", true)
		mustRegisterCargo(t, r, "G4", 30, "X", true)
		ops := []Op{
			{Kind: OpLoad, CargoID: "G4", Target: "C2"},
			{Kind: OpLoad, CargoID: "G3", Target: "C2"},
			{Kind: OpLoad, CargoID: "G2", Target: "C1"},
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		}
		return r, ops
	}

	// C2 的操作在前。
	r, _ := build()
	se := submitExpectReject(t, r, "adj-1", []Op{
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G1" {
		t.Fatalf("C2 操作在前时仍应报告 C1 混装冲突，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	assertAllEmpty(t, r)

	// C1 的操作在前。
	r, _ = build()
	se = submitExpectReject(t, r, "adj-1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G1" {
		t.Fatalf("C1 操作在前时仍应报告 C1 混装冲突，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	assertAllEmpty(t, r)
}

// 改变舱位与货物的登记次序，不能改变按编号字典序得到的报告结果。
func TestAdjustReportStableAcrossRegistrationOrder(t *testing.T) {
	// 登记次序：先 C2 后 C1；货物先登记涉及 C2 的 G4、G3，再 C1 的 G2、G1。
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C2", 50)
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCargo(t, r, "G4", 30, "X", true)
	mustRegisterCargo(t, r, "G3", 30, "X", true)
	mustRegisterCargo(t, r, "G2", 10, "北京", true)
	mustRegisterCargo(t, r, "G1", 10, "上海", false)

	se := submitExpectReject(t, r, "adj-x", []Op{
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G1" {
		t.Fatalf("改变登记次序后仍应报告 C1 混装冲突（G1），实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	assertAllEmpty(t, r)
}

// 除 C1、C2 外还有一个字典序更靠后、同样违规的舱位时，仍只报 C1。
func TestAdjustEarliestCompartmentAmongThree(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "C2", 50)
	mustRegisterCompartment(t, r, "C3", 50)
	mustRegisterCargo(t, r, "G1", 10, "上海", false)
	mustRegisterCargo(t, r, "G2", 10, "北京", true) // C1：仅混装
	mustRegisterCargo(t, r, "G3", 60, "X", true)  // C2：超重
	mustRegisterCargo(t, r, "G5", 60, "X", true)  // C3：超重
	se := submitExpectReject(t, r, "adj-3", []Op{
		{Kind: OpLoad, CargoID: "G5", Target: "C3"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G1" {
		t.Fatalf("三个舱位同时违规时仍应只报 C1 混装冲突，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	for _, c := range []string{"C1", "C2", "C3"} {
		v, _ := r.Compartment(c)
		if v.UsedWeight != 0 || len(v.Cargo) != 0 {
			t.Fatalf("舱位 %s 应仍为空舱: %+v", c, v)
		}
	}
}

// ---------- 同一舱位两种原因的取舍 ----------

// 同一舱位同时超重与混装冲突时，先报告重量原因：错误对象是舱位本身，
// 说明里能看到预计总重量与最大承重。
func TestAdjustSameCompartmentWeightBeforeMixed(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 50)
	// G1（上海，不许混装）+ G2（北京，允许）最终共 100 > 50，且混装冲突。
	mustRegisterCargo(t, r, "G1", 60, "上海", false)
	mustRegisterCargo(t, r, "G2", 40, "北京", true)

	se := submitExpectReject(t, r, "adj-wm", []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if se.Kind != ErrOverweight {
		t.Fatalf("同舱超重与混装并存时应先报重量，实际 Kind=%s", se.Kind)
	}
	// 错误对象编号是该舱位，而不是任何一件货物。
	if se.ID != "C1" {
		t.Fatalf("重量原因应指向舱位 C1，实际 ID=%q", se.ID)
	}
	// 说明里能看到预计总重量与最大承重。
	if !strings.Contains(se.Error(), "100") || !strings.Contains(se.Error(), "50") {
		t.Fatalf("超重说明应包含预计总重量 100 与最大承重 50: %s", se.Error())
	}
	if se.AdjustmentID != "adj-wm" {
		t.Fatalf("错误应携带调整编号 adj-wm，实际 %q", se.AdjustmentID)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 0 || cpt.RemainingWeight != 50 || len(cpt.Cargo) != 0 {
		t.Fatalf("C1 应仍为空舱: %+v", cpt)
	}
}

// 预计合计溢出 int64 的舱位同时存在混装冲突时，仍先报告重量（溢出）原因，
// 对象为该舱位而非货物。
func TestAdjustOverflowReportedBeforeMixed(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", math.MaxInt64)
	mustRegisterCargo(t, r, "G1", math.MaxInt64, "上海", false)
	mustRegisterCargo(t, r, "G2", math.MaxInt64, "北京", true)
	mustAdjust(t, r, "init", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})

	se := submitExpectReject(t, r, "adj-ov", []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	if se.Kind != ErrOverflow {
		t.Fatalf("溢出与混装并存时应先报溢出，实际 Kind=%s", se.Kind)
	}
	if se.ID != "C1" {
		t.Fatalf("溢出原因应指向舱位 C1，实际 %q", se.ID)
	}
	if se.AdjustmentID != "adj-ov" {
		t.Fatalf("错误应携带编号 adj-ov，实际 %q", se.AdjustmentID)
	}
	// 拒绝后 G2 仍未装载，C1 仍只有 G1。
	g2, _ := r.Cargo("G2")
	if g2.Loaded || g2.CompartmentID != "" {
		t.Fatalf("G2 应仍未装载: %+v", g2)
	}
	c1, _ := r.Compartment("C1")
	if !reflect.DeepEqual(cargoIDs(*c1), []string{"G1"}) {
		t.Fatalf("C1 应仍只有 G1: %+v", c1.Cargo)
	}
}

// ---------- 混装冲突的对象选择 ----------

// 重量合法、只有混装冲突时，错误对象是不允许混装的货物中编号字典序
// 最靠前的一件（含原舱货物），而不是最后加入舱内的那件。
func TestAdjustMixedConflictOffenderMayPrecedeIncomingCargo(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCargo(t, r, "G2", 10, "上海", false) // 原舱，禁混
	mustRegisterCargo(t, r, "G7", 10, "上海", false) // 原舱，禁混
	mustRegisterCargo(t, r, "G1", 10, "北京", true)  // 未装载，许混，本批加入
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G7", Target: "C1"},
	})

	compBefore := snapshotCompartments(r, "C1")
	cargoBefore := snapshotCargo(r, "G1", "G2", "G7")

	// 最终 C1 = {G2, G7}(上海,禁混) + G1(北京,许混)，重量 30 合法，
	// 只有混装冲突。违规货物集合是原舱的 {G2, G7}，字典序最靠前为 G2；
	// 最后加入的 G1 允许混装，根本不在违规集合内。
	se := submitExpectReject(t, r, "adj-m2", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G2" {
		t.Fatalf("应指出原舱内字典序最靠前的违规货物 G2，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if !strings.Contains(se.Error(), "C1") {
		t.Fatalf("说明应指出冲突舱位 C1: %s", se.Error())
	}
	if se.AdjustmentID != "adj-m2" {
		t.Fatalf("错误应携带调整编号 adj-m2，实际 %q", se.AdjustmentID)
	}

	// 拒绝后回查：G2、G7 仍在 C1，G1 仍未装载；重量、目的地、混装许可、
	// 舱位清单、已用与剩余重量都与提交前一致。
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
}

// 违规货物既包括原舱货物也包括本批移入货物时，仍按全部违规货物编号的
// 字典序取最靠前的一件，与谁最后进舱无关。
func TestAdjustMixedConflictOffenderAcrossStayingAndIncoming(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "C2", 1000)
	// C1 原舱：G5（上海，禁混）。C2：G0（广州，禁混）。未装载：
	// G9（北京，许混）。初始各舱均同目的地，初始装载本身合法。
	mustRegisterCargo(t, r, "G5", 10, "上海", false)
	mustRegisterCargo(t, r, "G0", 10, "广州", false)
	mustRegisterCargo(t, r, "G9", 10, "北京", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G5", Target: "C1"},
		{Kind: OpLoad, CargoID: "G0", Target: "C2"},
	})

	compBefore := snapshotCompartments(r, "C1", "C2")
	cargoBefore := snapshotCargo(r, "G0", "G5", "G9")

	// 把 G0 移入 C1、把 G9 装入 C1：最终 C1 = G5(上海,禁混)、
	// G0(广州,禁混)、G9(北京,许混)，重量 30 合法，只有混装冲突。
	// 违规集合 {G0(本批移入), G5(原舱)}，字典序最靠前为 G0。
	se := submitExpectReject(t, r, "adj-m3", []Op{
		{Kind: OpMove, CargoID: "G0", Target: "C1"},
		{Kind: OpLoad, CargoID: "G9", Target: "C1"},
	})
	if se.Kind != ErrMixedLoading || se.ID != "G0" {
		t.Fatalf("应指出全部违规货物中编号最靠前的 G0，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if !strings.Contains(se.Error(), "C1") {
		t.Fatalf("说明应指出冲突舱位 C1: %s", se.Error())
	}
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
}

// ---------- 拒绝后仍可查询到原配载（整批原子性）----------

// 清单里包含一项本来能够成功的卸下或移动时，不能留下那一项已生效的
// 状态；同批另一项操作导致整批被拒后，全部状态与提交前一致。
func TestAdjustRejectionAtomicAcrossUnloadAndMove(t *testing.T) {
	// 形态一：本可成功的卸下 + 引发超重的移动。报告 C2 超重，但卸下不生效。
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCargo(t, r, "GU", 40, "X", true) // 将被卸下（单独看合法）
	mustRegisterCargo(t, r, "GM", 60, "X", true) // 将被移动到 C2
	mustRegisterCargo(t, r, "GX", 90, "X", true) // 留在 C2
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "GU", Target: "C1"},
		{Kind: OpLoad, CargoID: "GM", Target: "C1"},
		{Kind: OpLoad, CargoID: "GX", Target: "C2"},
	})

	compBefore := snapshotCompartments(r, "C1", "C2")
	cargoBefore := snapshotCargo(r, "GU", "GM", "GX")

	se := submitExpectReject(t, r, "adj-atomic1", []Op{
		{Kind: OpUnload, CargoID: "GU"},             // 本可成功：C1 40 -> 0
		{Kind: OpMove, CargoID: "GM", Target: "C2"}, // C2: 90+60=150 超重
	})
	if se.Kind != ErrOverweight || se.ID != "C2" {
		t.Fatalf("应报告 C2 超重，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if !strings.Contains(se.Error(), "150") || !strings.Contains(se.Error(), "100") {
		t.Fatalf("超重说明应含预计总重 150 与承重 100: %s", se.Error())
	}
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)

	// 形态二：本可成功的移动 + 引发混装冲突的装载。报告 C1 混装，但移动
	// 不生效。C1 原舱为 GM（北京，许混），把另一舱的 GS（同为北京、许混）
	// 移入 C1 本身合法；同批把未装载的 GN（深圳，禁混）装入 C1 才造成冲突。
	r2 := NewRegistry()
	mustRegisterCompartment(t, r2, "C1", 1000)
	mustRegisterCompartment(t, r2, "C2", 1000)
	mustRegisterCargo(t, r2, "GM", 10, "北京", true)
	mustRegisterCargo(t, r2, "GS", 10, "北京", true)
	mustRegisterCargo(t, r2, "GN", 10, "深圳", false)
	mustAdjust(t, r2, "init", []Op{
		{Kind: OpLoad, CargoID: "GM", Target: "C1"},
		{Kind: OpLoad, CargoID: "GS", Target: "C2"},
	})

	compBefore2 := snapshotCompartments(r2, "C1", "C2")
	cargoBefore2 := snapshotCargo(r2, "GM", "GS", "GN")

	se2 := submitExpectReject(t, r2, "adj-atomic2", []Op{
		{Kind: OpMove, CargoID: "GS", Target: "C1"}, // 本可成功：同目的地共舱
		{Kind: OpLoad, CargoID: "GN", Target: "C1"}, // 深圳 vs 北京，GN 禁混
	})
	if se2.Kind != ErrMixedLoading || se2.ID != "GN" {
		t.Fatalf("应报告 C1 混装冲突并指出 GN，实际 Kind=%s ID=%q", se2.Kind, se2.ID)
	}
	if !strings.Contains(se2.Error(), "C1") {
		t.Fatalf("说明应指出冲突舱位 C1: %s", se2.Error())
	}
	assertCompartmentsUnchanged(t, r2, compBefore2)
	assertCargoUnchanged(t, r2, cargoBefore2)
}

// 被拒绝后，原本已装载货物仍在原舱位、原本未装载货物仍未装载；失败不
// 占用编号，用同一编号提交修正后的合法安排可以成功，随后回查到的是新
// 配载，而被拒当刻回查到的是原配载。
func TestAdjustRejectionLeavesOriginalStowageQueryable(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCargo(t, r, "G1", 60, "X", true)
	mustRegisterCargo(t, r, "G2", 50, "X", true)
	mustRegisterCargo(t, r, "G3", 10, "X", true)
	mustAdjust(t, r, "a0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})

	compBefore := snapshotCompartments(r, "C1", "C2")
	cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

	// 卸下 G3 本可成功；移动 G1 到 C2 导致 C2 超重（50+60>100），整批拒绝。
	se := submitExpectReject(t, r, "  bad-batch  ", []Op{
		{Kind: OpUnload, CargoID: "G3"},
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
	})
	if se.Kind != ErrOverweight || se.ID != "C2" {
		t.Fatalf("应报告 C2 超重，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if se.AdjustmentID != "bad-batch" {
		t.Fatalf("错误应携带去空白后的编号 bad-batch，实际 %q", se.AdjustmentID)
	}
	// 被拒当刻即可查询到原配载。
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)

	// 失败不占用编号：同一编号用于修正后的合法内容应成功。
	res, err := r.Adjust("bad-batch", []Op{{Kind: OpUnload, CargoID: "G3"}})
	if err != nil {
		t.Fatalf("失败不应占用编号，修正后应成功: %v", err)
	}
	if res == nil || len(res.CargoChanges) != 1 ||
		res.CargoChanges[0].CargoID != "G3" || res.CargoChanges[0].From != "C1" ||
		res.CargoChanges[0].To != "" {
		t.Fatalf("修正后调整结果异常: %+v", res)
	}
	g3, _ := r.Cargo("G3")
	if g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("G3 应已卸下: %+v", g3)
	}
	// 同批被拒涉及的 G1、G2 归属保持初始终态。
	g1, _ := r.Cargo("G1")
	g2, _ := r.Cargo("G2")
	if g1.CompartmentID != "C1" || g2.CompartmentID != "C2" {
		t.Fatalf("G1、G2 归属不应改变: %+v %+v", g1, g2)
	}
}

// ---------- 两种入口各自的用途 ----------

// Preview 列出全部拒绝原因，正式提交只返回一个结构化错误；两者对首个
// 原因的判定一致，而 Preview 额外保留其余原因与受影响舱位的前后清单。
func TestPreviewListsAllReasonsAdjustReturnsOne(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 50)
	mustRegisterCompartment(t, r, "C2", 50)
	// C1：上海(禁混)+北京(允许) 合计 80 > 50，超重且混装；C2：仅超重。
	mustRegisterCargo(t, r, "G1", 40, "上海", false)
	mustRegisterCargo(t, r, "G2", 40, "北京", true)
	mustRegisterCargo(t, r, "G3", 30, "X", true)
	mustRegisterCargo(t, r, "G4", 30, "X", true)
	ops := []Op{
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	}

	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Submittable {
		t.Fatal("预览应判定不可提交")
	}
	got := make([][2]string, 0, len(pv.Rejections))
	for _, x := range pv.Rejections {
		got = append(got, [2]string{x.CompartmentID, x.Kind.String()})
	}
	want := [][2]string{
		{"C1", "超重"},
		{"C1", "混装冲突"},
		{"C2", "超重"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Preview 应列全全部拒绝原因: %+v", got)
	}
	// Preview 给出全部受影响舱位的完整前后清单，这是 Adjust 的单条错误
	// 不提供的信息。
	if len(pv.Compartments) != 2 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" {
		t.Fatalf("Preview 应列出两个受影响舱位: %+v", pv.Compartments)
	}
	assertAllEmpty(t, r)

	// 正式提交只返回一个结构化错误，首个原因与预览第一条一致。
	se := submitExpectReject(t, r, "p-1", ops)
	if se.Kind != ErrOverweight || se.ID != "C1" {
		t.Fatalf("Adjust 应只报首个原因 C1 超重，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	if se.AdjustmentID != "p-1" {
		t.Fatalf("Adjust 错误应携带编号 p-1，实际 %q", se.AdjustmentID)
	}
	// *Error 只承载单条原因；调用方需要全部原因时应使用 Preview。
	// 提交被拒后原配载仍可回查，且该编号未被占用。
	assertAllEmpty(t, r)
	if _, err := r.Adjust("p-1", []Op{{Kind: OpLoad, CargoID: "G3", Target: "C2"}}); err != nil {
		t.Fatalf("被拒编号不应被占用: %v", err)
	}
}

// Preview 判为可提交的安排，用同一批操作正式提交应成功，提交后查询结果
// 与预计配载对应——保留两种入口在成功路径上的配合。
func TestPreviewSubmittableThenAdjustSucceeds(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 30)
	mustRegisterCompartment(t, r, "C2", 30)
	mustRegisterCargo(t, r, "G1", 30, "上海", false)
	mustRegisterCargo(t, r, "G2", 30, "上海", false)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	})
	ops := []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C1"},
	}
	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Submittable || len(pv.Rejections) != 0 {
		t.Fatalf("满载互换应可提交: %+v", pv.Rejections)
	}
	res, err := r.Adjust("swap-1", ops)
	if err != nil {
		t.Fatalf("预览可提交的安排正式提交应成功: %v", err)
	}
	if res == nil || res.ID != "swap-1" {
		t.Fatalf("成功结果异常: %+v", res)
	}
	g1, _ := r.Cargo("G1")
	g2, _ := r.Cargo("G2")
	if g1.CompartmentID != "C2" || g2.CompartmentID != "C1" {
		t.Fatalf("提交后配载应与预览一致: G1=%s G2=%s", g1.CompartmentID, g2.CompartmentID)
	}
}

// ---------- 合法配载不得被误伤 ----------

// 同一目的地货物共舱、总重量恰好达到承重的批处理应正常成功并返回完整
// 变化结果，避免把合法配载也挡住。
func TestAdjustSameDestinationExactCapacitySucceeds(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 50)
	mustRegisterCargo(t, r, "G1", 60, "上海", false)
	mustRegisterCargo(t, r, "G2", 40, "上海", false) // C1 恰好 100，同目的地可共舱
	mustRegisterCargo(t, r, "G3", 50, "北京", true)  // C2 恰好 50

	res, err := r.Adjust(" ok-2 ", []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	})
	if err != nil {
		t.Fatalf("同目的地共舱且恰好满载应成功: %v", err)
	}
	if res == nil || res.ID != "ok-2" {
		t.Fatalf("成功结果应携带去空白后的编号 ok-2: %+v", res)
	}
	if len(res.CargoChanges) != 3 || len(res.CompartmentChanges) != 2 {
		t.Fatalf("应返回三件货物与两个舱位的变化: %+v", res)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 100 || c1.RemainingWeight != 0 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G2"}) {
		t.Fatalf("C1 应恰好满载且清单为 G1、G2: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 50 || c2.RemainingWeight != 0 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"G3"}) {
		t.Fatalf("C2 应恰好满载且清单为 G3: %+v", c2)
	}
	// 货物登记资料保持原样。
	g1, _ := r.Cargo("G1")
	if g1.Weight != 60 || g1.Destination != "上海" || g1.AllowMixed {
		t.Fatalf("G1 资料不应改变: %+v", g1)
	}
}
