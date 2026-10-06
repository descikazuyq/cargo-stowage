package stowage

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// 本文件为“接近 int64 重量上限时的同舱换货”补充自动化回归保障，重点
// 保护按整批操作完成后的最终配载判断重量这一规则。
//
// 换货不是新接口：一次调整中卸下原货物、装入另一件已登记但未装载的
// 货物，仍走现有的 Preview 与 Adjust 入口。初始形态为舱位 C1 装着
// G1（M−20）与 G2（10），G3 尚未装载；三件货物目的地相同、资料本身
// 合法，M = math.MaxInt64。本次安排只卸下 G2、把 G3 装入 C1，G1 留在
// 原舱。装入写在卸下之前时，三件货物临时共舱合计 (M−20)+10+20 = M+10，
// 已无法用 int64 表示；该中间状态不参与判断，两种顺序的预览与正式提交
// 结论必须一致。
//
// 同时区分两种最终拒绝：最终合计仍可表示但超过承重，按现有超重原因
// 拒绝；最终合计本身无法用 int64 表示，按现有重量溢出原因拒绝，数值
// 字段沿用零值约定，绝不返回回绕后的负数。

// newWeightLimitReplaceRegistry 建立上述初始配载：C1 承重由 maxWeight
// 指定（M 或 M−1），G1 重 M−20、G2 重 10 已装入 C1，G3 重 g3Weight
// 尚未装载，三件货物同目的地且都允许混装。
func newWeightLimitReplaceRegistry(t *testing.T, maxWeight, g3Weight int64) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", maxWeight)
	mustRegisterCargo(t, r, "G1", math.MaxInt64-20, "上海", true)
	mustRegisterCargo(t, r, "G2", 10, "上海", true)
	mustRegisterCargo(t, r, "G3", g3Weight, "上海", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	return r
}

// replaceNearLimitOps 返回“卸下 G2、把 G3 装入 C1”的两条操作，
// loadFirst 决定装入与卸下的先后。
func replaceNearLimitOps(loadFirst bool) []Op {
	unload := Op{Kind: OpUnload, CargoID: "G2"}
	load := Op{Kind: OpLoad, CargoID: "G3", Target: "C1"}
	if loadFirst {
		return []Op{load, unload}
	}
	return []Op{unload, load}
}

// assertInitialStowage 核对实际配载仍是调整前的 C1={G1,G2}、已用 M−10。
func assertInitialStowage(t *testing.T, r *Registry, maxWeight int64) {
	t.Helper()
	c1, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.MaxWeight != maxWeight ||
		c1.UsedWeight != math.MaxInt64-10 ||
		c1.RemainingWeight != maxWeight-(math.MaxInt64-10) ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G2"}) {
		t.Fatalf("实际配载应保持 C1={G1,G2}、已用 M−10: %+v", c1)
	}
	g1, _ := r.Cargo("G1")
	g2, _ := r.Cargo("G2")
	g3, _ := r.Cargo("G3")
	if g1.CompartmentID != "C1" || !g1.Loaded {
		t.Fatalf("G1 应留在 C1: %+v", g1)
	}
	if g2.CompartmentID != "C1" || !g2.Loaded {
		t.Fatalf("G2 应仍在 C1: %+v", g2)
	}
	if g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("G3 应仍未装载: %+v", g3)
	}
}

// ---------- 合法换货：最终恰好达到 M，临时共舱溢出不参与判断 ----------

// 无论装入写在卸下之前还是之后，预览都应判定可以提交：Before 完整保留
// C1 调整前的 G1、G2 与已用重量 M−10；After 只含 G1、G3，已用 M、剩余
// 为零；货物变化正确显示 G2 由 C1 变为未装载、G3 由未装载进入 C1。
// 预览期间实际配载保持原样。
func TestReplaceNearWeightLimitPreviewSubmittable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loadFirst bool
	}{
		{name: "先卸后装", loadFirst: false},
		{name: "先装后卸（临时共舱合计 M+10 溢出）", loadFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWeightLimitReplaceRegistry(t, math.MaxInt64, 20)

			pv, err := r.Preview(replaceNearLimitOps(tc.loadFirst))
			if err != nil {
				t.Fatalf("逐条操作均合法时预览不应返回错误: %v", err)
			}
			if !pv.Submittable || len(pv.Rejections) != 0 {
				t.Fatalf("最终合计恰好为 M 时应判定可提交: %+v", pv.Rejections)
			}

			// 货物变化按编号字典序：G2 出舱、G3 入舱；留下的 G1 不列出。
			wantChanges := []CargoChange{
				{CargoID: "G2", From: "C1", To: ""},
				{CargoID: "G3", From: "", To: "C1"},
			}
			if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
				t.Fatalf("货物变化错误: got=%+v want=%+v", pv.CargoChanges, wantChanges)
			}

			if len(pv.Compartments) != 1 || pv.Compartments[0].ID != "C1" {
				t.Fatalf("应只列出受影响舱位 C1: %+v", pv.Compartments)
			}
			cp := pv.Compartments[0]

			// Before：完整保留调整前的两件货物、原位置与已用重量 M−10。
			if !reflect.DeepEqual(cargoIDs(cp.Before), []string{"G1", "G2"}) {
				t.Fatalf("Before 应保留 G1、G2: %+v", cp.Before.Cargo)
			}
			if cp.Before.MaxWeight != math.MaxInt64 ||
				cp.Before.UsedWeight != math.MaxInt64-10 || cp.Before.RemainingWeight != 10 {
				t.Fatalf("Before 重量应为承重 M、已用 M−10、剩余 10: %+v", cp.Before)
			}
			for _, c := range cp.Before.Cargo {
				if !c.Loaded || c.CompartmentID != "C1" {
					t.Fatalf("Before 清单中的 %s 应显示在 C1: %+v", c.ID, c)
				}
			}

			// After：预计清单只含 G1、G3，已用 M、剩余为零。
			if !reflect.DeepEqual(cargoIDs(cp.After), []string{"G1", "G3"}) {
				t.Fatalf("After 应只含 G1、G3: %+v", cp.After.Cargo)
			}
			afterWeight := map[string]int64{}
			for _, c := range cp.After.Cargo {
				afterWeight[c.ID] = c.Weight
				if !c.Loaded || c.CompartmentID != "C1" {
					t.Fatalf("After 清单中的 %s 应投影为已装载于 C1: %+v", c.ID, c)
				}
			}
			if afterWeight["G1"] != math.MaxInt64-20 || afterWeight["G3"] != 20 {
				t.Fatalf("After 清单应保留登记重量 G1=M−20、G3=20: %+v", afterWeight)
			}
			if cp.After.MaxWeight != math.MaxInt64 ||
				cp.After.UsedWeight != math.MaxInt64 || cp.After.RemainingWeight != 0 {
				t.Fatalf("After 重量应为承重 M、已用 M、剩余 0，不能出现回绕: %+v", cp.After)
			}

			// 预览只读：实际配载与各货物归属保持原样。
			assertInitialStowage(t, r, math.MaxInt64)
		})
	}
}

// 预览判为可提交的换货，用同一批操作正式提交也应成功，与操作先后无关；
// 返回的 C1 重量变化必须是 M−10 到 M（包含留下的 G1，不得回绕）。提交后
// 舱位清单与货物查询与预计归属一致，G2 记录仍可查询且不再占用重量。
func TestReplaceNearWeightLimitAdjustSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loadFirst bool
	}{
		{name: "先卸后装", loadFirst: false},
		{name: "先装后卸（临时共舱合计 M+10 溢出）", loadFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWeightLimitReplaceRegistry(t, math.MaxInt64, 20)
			ops := replaceNearLimitOps(tc.loadFirst)

			pv, err := r.Preview(ops)
			if err != nil {
				t.Fatal(err)
			}
			if !pv.Submittable {
				t.Fatalf("预览应判定可提交: %+v", pv.Rejections)
			}

			res, err := r.Adjust("  swap-limit  ", ops)
			if err != nil {
				t.Fatalf("最终合计恰好为 M 的换货应成功，不能因临时共舱溢出而拒绝: %v", err)
			}
			if res == nil || res.ID != "swap-limit" {
				t.Fatalf("成功结果应携带去空白后的编号: %+v", res)
			}
			wantChanges := []CargoChange{
				{CargoID: "G2", From: "C1", To: ""},
				{CargoID: "G3", From: "", To: "C1"},
			}
			if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
				t.Fatalf("货物变化错误: got=%+v want=%+v", res.CargoChanges, wantChanges)
			}
			if len(res.CompartmentChanges) != 1 {
				t.Fatalf("应只返回 C1 的重量变化: %+v", res.CompartmentChanges)
			}
			cc := res.CompartmentChanges[0]
			if cc.CompartmentID != "C1" ||
				cc.WeightBefore != math.MaxInt64-10 || cc.WeightAfter != math.MaxInt64 {
				t.Fatalf("C1 重量变化应是 M−10 到 M，不能回绕为负数或漏掉留下的 G1: %+v", cc)
			}

			// 提交后舱位查询与预计归属一致：C1={G1,G3}，已用 M、剩余 0。
			c1, _ := r.Compartment("C1")
			if c1.MaxWeight != math.MaxInt64 ||
				c1.UsedWeight != math.MaxInt64 || c1.RemainingWeight != 0 ||
				!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G3"}) {
				t.Fatalf("提交后 C1 应为 {G1,G3}、已用 M、剩余 0: %+v", c1)
			}

			// 货物查询：G1 留在 C1；G3 进入 C1；G2 记录仍可查询、已卸下、
			// 不再占用重量。
			g1, _ := r.Cargo("G1")
			g2, _ := r.Cargo("G2")
			g3, _ := r.Cargo("G3")
			if !g1.Loaded || g1.CompartmentID != "C1" || g1.Weight != math.MaxInt64-20 {
				t.Fatalf("G1 应留在 C1 且登记资料不变: %+v", g1)
			}
			if g2.Loaded || g2.CompartmentID != "" || g2.Weight != 10 {
				t.Fatalf("G2 记录应保留、为未装载且不再占用重量: %+v", g2)
			}
			if !g3.Loaded || g3.CompartmentID != "C1" || g3.Weight != 20 {
				t.Fatalf("G3 应已装入 C1: %+v", g3)
			}
		})
	}
}

// ---------- 最终超重：合计可表示但超过承重，按现有超重原因拒绝 ----------

// C1 承重改为 M−1 时，换货后合计 M 仍能用 int64 表示但超出承重 1 千克。
// 预览仍给出预计货物清单（After={G1,G3}）并标为不可提交，超重原因说明
// 舱位、预计重量 M 与承重 M−1；先装后卸顺序下临时共舱会溢出 int64，也
// 必须按最终超重而非溢出拒绝。正式提交不返回成功结果，G1、G2 留在 C1、
// G3 仍未装载，资料、舱位重量与剩余重量都不改变，且失败不占用编号。
func TestReplaceNearWeightLimitFinalOverweightRejected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loadFirst bool
	}{
		{name: "先卸后装", loadFirst: false},
		{name: "先装后卸（临时共舱溢出，最终仅超重）", loadFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const maxWeight = math.MaxInt64 - 1
			r := newWeightLimitReplaceRegistry(t, maxWeight, 20)
			ops := replaceNearLimitOps(tc.loadFirst)
			compBefore := snapshotCompartments(r, "C1")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			pv, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("逐条操作合法时应返回完整预览而非错误: %v", err)
			}
			if pv.Submittable {
				t.Fatalf("最终合计 M 超过承重 M−1，应标为不可提交")
			}
			if len(pv.Rejections) != 1 {
				t.Fatalf("应只有 C1 超重一条原因: %+v", pv.Rejections)
			}
			rej := pv.Rejections[0]
			if rej.Kind != ErrOverweight || rej.CompartmentID != "C1" {
				t.Fatalf("应以超重原因拒绝并指向 C1: %+v", rej)
			}
			if rej.MaxWeight != math.MaxInt64-1 ||
				rej.UsedWeight != math.MaxInt64 ||
				rej.RemainingWeight != -1 || rej.Overweight != 1 {
				t.Fatalf("超重原因应给出承重 M−1、预计重量 M、剩余 −1、超出 1: %+v", rej)
			}

			// 预览仍给出预计货物清单与调整前清单。
			if len(pv.Compartments) != 1 || pv.Compartments[0].ID != "C1" {
				t.Fatalf("应列出 C1 的前后配载: %+v", pv.Compartments)
			}
			cp := pv.Compartments[0]
			if !reflect.DeepEqual(cargoIDs(cp.Before), []string{"G1", "G2"}) ||
				cp.Before.UsedWeight != math.MaxInt64-10 {
				t.Fatalf("Before 应保留 G1、G2 与已用 M−10: %+v", cp.Before)
			}
			if !reflect.DeepEqual(cargoIDs(cp.After), []string{"G1", "G3"}) {
				t.Fatalf("超重时 After 仍应给出预计清单 G1、G3: %+v", cp.After.Cargo)
			}
			if cp.After.UsedWeight != math.MaxInt64 || cp.After.RemainingWeight != -1 {
				t.Fatalf("After 应给出预计已用 M 与剩余 −1: %+v", cp.After)
			}

			// 预览只读；正式提交以同一超重原因拒绝且不返回成功结果。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			se := submitExpectReject(t, r, "adj-ow", ops)
			if se.Kind != ErrOverweight || se.ID != "C1" {
				t.Fatalf("正式提交应按超重拒绝并指向 C1，实际 Kind=%s ID=%q", se.Kind, se.ID)
			}
			if se.AdjustmentID != "adj-ow" {
				t.Fatalf("错误应携带调整编号 adj-ow，实际 %q", se.AdjustmentID)
			}
			msg := se.Error()
			if !strings.Contains(msg, "C1") ||
				!strings.Contains(msg, "9223372036854775807") ||
				!strings.Contains(msg, "9223372036854775806") {
				t.Fatalf("超重说明应指出舱位 C1、预计重量 M 与承重 M−1: %s", msg)
			}

			// 整批原子性：G1、G2 留在 C1，G3 仍未装载，重量与资料不变。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertInitialStowage(t, r, maxWeight)

			// 失败不占用编号：同一编号提交合法的卸下 G2 应成功。
			mustAdjust(t, r, "adj-ow", []Op{{Kind: OpUnload, CargoID: "G2"}})
		})
	}
}

// ---------- 最终溢出：合计无法用 int64 表示，按现有重量溢出原因拒绝 ----------

// C1 承重为 M、G3 重 21 时，换货后合计 (M−20)+21 = M+1 无法用 int64
// 表示。预览仍给出预计货物清单并标为不可提交，溢出原因只指出舱位 C1，
// 已用、剩余与超出重量沿用零值约定，After 视图同样不给重量数值；不能
// 当作普通超重，也不能返回回绕后的负数。正式提交不返回成功结果，G1、
// G2 留在 C1、G3 仍未装载，资料、舱位重量与剩余重量都不改变。
func TestReplaceNearWeightLimitFinalOverflowRejected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loadFirst bool
	}{
		{name: "先卸后装", loadFirst: false},
		{name: "先装后卸", loadFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWeightLimitReplaceRegistry(t, math.MaxInt64, 21)
			ops := replaceNearLimitOps(tc.loadFirst)
			compBefore := snapshotCompartments(r, "C1")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			pv, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("逐条操作合法时应返回完整预览而非错误: %v", err)
			}
			if pv.Submittable {
				t.Fatalf("最终合计 M+1 无法表示，应标为不可提交")
			}
			if len(pv.Rejections) != 1 {
				t.Fatalf("应只有 C1 溢出一条原因: %+v", pv.Rejections)
			}
			rej := pv.Rejections[0]
			if rej.Kind != ErrOverflow || rej.CompartmentID != "C1" {
				t.Fatalf("应以重量溢出原因拒绝并指向 C1，不能当作普通超重: %+v", rej)
			}
			if rej.MaxWeight != math.MaxInt64 {
				t.Fatalf("溢出原因仍应提供承重 M: %+v", rej)
			}
			if rej.UsedWeight != 0 || rej.RemainingWeight != 0 || rej.Overweight != 0 {
				t.Fatalf("溢出时预计已用、剩余与超出重量应沿用零值约定: %+v", rej)
			}

			// 预览仍给出预计货物清单；After 重量数值保持零值，Before 不变。
			if len(pv.Compartments) != 1 || pv.Compartments[0].ID != "C1" {
				t.Fatalf("应列出 C1 的前后配载: %+v", pv.Compartments)
			}
			cp := pv.Compartments[0]
			if !reflect.DeepEqual(cargoIDs(cp.Before), []string{"G1", "G2"}) ||
				cp.Before.UsedWeight != math.MaxInt64-10 || cp.Before.RemainingWeight != 10 {
				t.Fatalf("Before 应保留 G1、G2、已用 M−10、剩余 10: %+v", cp.Before)
			}
			if !reflect.DeepEqual(cargoIDs(cp.After), []string{"G1", "G3"}) {
				t.Fatalf("溢出时 After 仍应给出预计清单 G1、G3: %+v", cp.After.Cargo)
			}
			if cp.After.UsedWeight != 0 || cp.After.RemainingWeight != 0 {
				t.Fatalf("溢出时 After 已用与剩余应保持零值，不能返回回绕数值: %+v", cp.After)
			}

			// 预览只读；正式提交按溢出拒绝，不返回成功结果，说明中不得出现
			// 回绕后的负总重量。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			se := submitExpectReject(t, r, "adj-of", ops)
			if se.Kind != ErrOverflow || se.ID != "C1" {
				t.Fatalf("正式提交应按重量溢出拒绝并指向 C1，实际 Kind=%s ID=%q", se.Kind, se.ID)
			}
			if se.AdjustmentID != "adj-of" {
				t.Fatalf("错误应携带调整编号 adj-of，实际 %q", se.AdjustmentID)
			}
			msg := se.Error()
			if !strings.Contains(msg, "C1") || !strings.Contains(msg, "int64") {
				t.Fatalf("溢出说明应指出舱位 C1 与 int64 范围: %s", msg)
			}
			if strings.Contains(msg, "-9223372036854775808") {
				t.Fatalf("溢出说明不得返回回绕后的负总重量: %s", msg)
			}

			// 整批原子性：G1、G2 留在 C1，G3 仍未装载，重量与资料不变。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertInitialStowage(t, r, math.MaxInt64)
			g3, _ := r.Cargo("G3")
			if g3.Weight != 21 || g3.Loaded || g3.CompartmentID != "" {
				t.Fatalf("G3 应保持未装载且登记重量 21 不变: %+v", g3)
			}

			// 失败不占用编号：同一编号提交合法的卸下 G2 应成功。
			mustAdjust(t, r, "adj-of", []Op{{Kind: OpUnload, CargoID: "G2"}})
		})
	}
}
