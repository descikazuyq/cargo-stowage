package stowage

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// 本文件为“接近 int64 重量上限时同舱换货”补充自动化回归保障，重点保护
// 既有规则：重量合法性（承重、溢出）只按整批操作完成后的最终配载判断，
// 原货物与新货物在操作序列中临时共舱、临时合计无法用 int64 表示都不得
// 拒绝一个最终合法的换货。换货不是新接口，仍是同一次 Adjust 里的
// “卸下 G2 + 装入 G3”，并继续使用既有 Preview 与 Adjust 入口。
//
// 记号：M = math.MaxInt64。初始 C1 装有 G1（M-20）与 G2（10），
// 已用 M-10；G3 尚未装载；三件货物目的地相同，资料本身合法。
// 本次只卸下 G2、装入 G3，G1 留在 C1。装入写在卸下之前或之后，
// 预览与正式提交结论必须一致。

// nearLimitSwapOps 返回两种书写顺序的换货清单：卸下 G2、装入 G3。
func nearLimitSwapOps() map[string][]Op {
	return map[string][]Op{
		"先卸后装": {
			{Kind: OpUnload, CargoID: "G2"},
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		},
		"先装后卸": {
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			{Kind: OpUnload, CargoID: "G2"},
		},
	}
}

// newNearLimitSwapRegistry 构造换货前配载：C1 承重由 maxWeight 指定，
// G1 重 M-20、G2 重 10 已装入 C1（已用 M-10），G3 重 g3Weight 尚未
// 装载；三件货物同目的地（X），资料合法。
func newNearLimitSwapRegistry(t *testing.T, maxWeight, g3Weight int64) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", maxWeight)
	mustRegisterCargo(t, r, "G1", math.MaxInt64-20, "X", true)
	mustRegisterCargo(t, r, "G2", 10, "X", true)
	mustRegisterCargo(t, r, "G3", g3Weight, "X", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	return r
}

// assertNearLimitBefore 核对换货前 C1：仍装 G1、G2，已用 M-10，
// 剩余 maxWeight-(M-10)；G3 未装载。
func assertNearLimitBefore(t *testing.T, r *Registry, maxWeight int64) {
	t.Helper()
	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if cpt.MaxWeight != maxWeight ||
		cpt.UsedWeight != math.MaxInt64-10 ||
		cpt.RemainingWeight != maxWeight-(math.MaxInt64-10) {
		t.Fatalf("换货前 C1 重量应为 承重%d/已用%d/剩余%d: %+v",
			maxWeight, math.MaxInt64-10, maxWeight-(math.MaxInt64-10), cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("换货前 C1 应仍装 G1、G2: %v", got)
	}
	g3, _ := r.Cargo("G3")
	if g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("换货前 G3 应未装载: %+v", g3)
	}
}

// assertNearLimitProfilesUnchanged 核对三件货物的登记重量、目的地与
// 混装许可始终不随换货结果改变。
func assertNearLimitProfilesUnchanged(t *testing.T, r *Registry, g3Weight int64) {
	t.Helper()
	g1, _ := r.Cargo("G1")
	if g1.Weight != math.MaxInt64-20 || g1.Destination != "X" || !g1.AllowMixed {
		t.Fatalf("G1 登记资料被改变: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 10 || g2.Destination != "X" || !g2.AllowMixed {
		t.Fatalf("G2 登记资料被改变: %+v", g2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Weight != g3Weight || g3.Destination != "X" || !g3.AllowMixed {
		t.Fatalf("G3 登记资料被改变: %+v", g3)
	}
}

// 成功路径（C1 承重 M、G3 重 20）：无论装入写在卸下之前还是之后，
// 预览都应判定可提交。先装后卸时 G3 与尚未移出的 G1、G2 临时共舱，
// 合计 (M-20)+10+20 = M+10 无法用 int64 表示；合法性只看最终配载
// （G1+G3 = M），故不得因临时溢出拒绝。
func TestNearLimitSwapPreviewSubmittable(t *testing.T) {
	for name, ops := range nearLimitSwapOps() {
		t.Run(name, func(t *testing.T) {
			r := newNearLimitSwapRegistry(t, math.MaxInt64, 20)
			pv, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("换货最终合计 M 合法，预览不应返回错误: %v", err)
			}
			if !pv.Submittable || len(pv.Rejections) != 0 {
				t.Fatalf("换货应可提交，实际拒绝原因: %+v", pv.Rejections)
			}

			// 货物变化按编号字典序：G2 由 C1 变为未装载，G3 由未装载进入 C1。
			wantChanges := []CargoChange{
				{CargoID: "G2", From: "C1", To: ""},
				{CargoID: "G3", From: "", To: "C1"},
			}
			if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
				t.Fatalf("货物变化错误: got=%+v want=%+v", pv.CargoChanges, wantChanges)
			}

			// 只有 C1 受影响。
			if len(pv.Compartments) != 1 || pv.Compartments[0].ID != "C1" {
				t.Fatalf("应只列出 C1 的前后配载: %+v", pv.Compartments)
			}
			cp := pv.Compartments[0]

			// 调整前完整保留 G1、G2 与已用重量 M-10。
			before := cp.Before
			if got := cargoIDs(before); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
				t.Fatalf("调整前清单应保留 G1、G2: %v", got)
			}
			if before.MaxWeight != math.MaxInt64 ||
				before.UsedWeight != math.MaxInt64-10 ||
				before.RemainingWeight != 10 {
				t.Fatalf("调整前重量应为 承重M/已用M-10/剩余10: %+v", before)
			}
			// 调整前清单中的货物保持原所属舱位。
			for _, cv := range before.Cargo {
				if !cv.Loaded || cv.CompartmentID != "C1" {
					t.Fatalf("调整前清单中的 %s 应仍显示装载于 C1: %+v", cv.ID, cv)
				}
			}

			// 预计清单只含 G1、G3，已用 M、剩余 0，且显示为属于 C1。
			after := cp.After
			if got := cargoIDs(after); !reflect.DeepEqual(got, []string{"G1", "G3"}) {
				t.Fatalf("预计清单应只含 G1、G3: %v", got)
			}
			if after.MaxWeight != math.MaxInt64 ||
				after.UsedWeight != math.MaxInt64 ||
				after.RemainingWeight != 0 {
				t.Fatalf("预计重量应为 承重M/已用M/剩余0: %+v", after)
			}
			for _, cv := range after.Cargo {
				if !cv.Loaded || cv.CompartmentID != "C1" {
					t.Fatalf("预计清单中的 %s 应显示装载于 C1: %+v", cv.ID, cv)
				}
			}

			// 预览期间实际配载保持原样：C1 仍是 G1、G2，G3 仍未装载。
			assertNearLimitBefore(t, r, math.MaxInt64)
			assertNearLimitProfilesUnchanged(t, r, 20)
		})
	}
}

// 成功路径正式提交：两种书写顺序都应成功，返回的 C1 重量变化必须是
// M-10 -> M（不能回绕成负数，也不能漏掉留在 C1 的 G1）；提交后舱位
// 清单与货物查询与预计归属一致，G2 记录仍可查询且不再占用重量。
func TestNearLimitSwapAdjustSucceeds(t *testing.T) {
	for name, ops := range nearLimitSwapOps() {
		t.Run(name, func(t *testing.T) {
			r := newNearLimitSwapRegistry(t, math.MaxInt64, 20)
			res, err := r.Adjust("swap-1", ops)
			if err != nil {
				t.Fatalf("最终合计 M 恰好达承重，正式提交应成功: %v", err)
			}
			if res == nil || res.ID != "swap-1" {
				t.Fatalf("成功结果应携带编号 swap-1: %+v", res)
			}

			// 货物变化：G2 卸下、G3 装入，按编号字典序。
			wantChanges := []CargoChange{
				{CargoID: "G2", From: "C1", To: ""},
				{CargoID: "G3", From: "", To: "C1"},
			}
			if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
				t.Fatalf("货物变化错误: got=%+v want=%+v", res.CargoChanges, wantChanges)
			}

			// 舱位重量变化 M-10 -> M：先装后卸时若按中间状态或发生整数
			// 回绕，这里会得到错误/负值；只按最终配载才是 M。
			if len(res.CompartmentChanges) != 1 {
				t.Fatalf("应只列出 C1 的重量变化: %+v", res.CompartmentChanges)
			}
			cp := res.CompartmentChanges[0]
			if cp.CompartmentID != "C1" ||
				cp.WeightBefore != math.MaxInt64-10 ||
				cp.WeightAfter != math.MaxInt64 {
				t.Fatalf("C1 重量变化应为 M-10 -> M，不能回绕或漏掉 G1: %+v", cp)
			}

			// 提交后舱位查询：清单只含 G1、G3，已用 M、剩余 0。
			cpt, _ := r.Compartment("C1")
			if cpt.MaxWeight != math.MaxInt64 ||
				cpt.UsedWeight != math.MaxInt64 ||
				cpt.RemainingWeight != 0 {
				t.Fatalf("提交后 C1 重量应为 承重M/已用M/剩余0: %+v", cpt)
			}
			if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G3"}) {
				t.Fatalf("提交后 C1 应只含 G1、G3: %v", got)
			}

			// 货物查询与预计归属一致：G1 留在 C1，G3 进入 C1。
			g1, _ := r.Cargo("G1")
			if !g1.Loaded || g1.CompartmentID != "C1" {
				t.Fatalf("G1 应留在 C1: %+v", g1)
			}
			g3, _ := r.Cargo("G3")
			if !g3.Loaded || g3.CompartmentID != "C1" {
				t.Fatalf("G3 应已装入 C1: %+v", g3)
			}
			// G2 记录仍可查询、为未装载，且不再占用 C1 重量。
			g2, _ := r.Cargo("G2")
			if g2.Loaded || g2.CompartmentID != "" {
				t.Fatalf("G2 应已卸下且记录保留（未装载）: %+v", g2)
			}
			assertNearLimitProfilesUnchanged(t, r, 20)
		})
	}
}

// 预览可提交的换货，用同一批操作正式提交后，舱位清单/重量应与预览的
// 预计配载逐字段一致（两种顺序都核对）。
func TestNearLimitSwapPreviewThenAdjustConsistent(t *testing.T) {
	for name, ops := range nearLimitSwapOps() {
		t.Run(name, func(t *testing.T) {
			r := newNearLimitSwapRegistry(t, math.MaxInt64, 20)
			pv, err := r.Preview(ops)
			if err != nil || !pv.Submittable {
				t.Fatalf("预览应可提交: err=%v rejections=%+v", err, pv)
			}
			if _, err := r.Adjust("swap-1", ops); err != nil {
				t.Fatalf("正式提交应成功: %v", err)
			}
			cpt, _ := r.Compartment("C1")
			after := pv.Compartments[0].After
			if cpt.UsedWeight != after.UsedWeight ||
				cpt.RemainingWeight != after.RemainingWeight ||
				!reflect.DeepEqual(cargoIDs(*cpt), cargoIDs(after)) {
				t.Fatalf("提交后配载应与预计一致: got=%+v want=%+v", cpt, after)
			}
		})
	}
}

// 最终超重（可表示）：C1 承重改为 M-1，G3 仍重 20，换货后合计 M
// 仍能用 int64 表示但超过承重 1 千克。必须以既有的“超重”原因拒绝，
// 说明指出舱位、预计重量 M 与承重 M-1，不能当成溢出，也不能回绕。
func TestNearLimitSwapFinalOverweightRejected(t *testing.T) {
	for name, ops := range nearLimitSwapOps() {
		t.Run(name, func(t *testing.T) {
			r := newNearLimitSwapRegistry(t, math.MaxInt64-1, 20)
			assertNearLimitBefore(t, r, math.MaxInt64-1)
			compBefore := snapshotCompartments(r, "C1")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			// 预览：仍给出完整预计货物清单，但标为不可提交，原因为超重
			// （不是溢出），携带预计重量 M、承重 M-1、超出 1。
			pv, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("逐条合法时预览应返回完整结果而非错误: %v", err)
			}
			if pv.Submittable || len(pv.Rejections) != 1 {
				t.Fatalf("最终超重应不可提交且恰有一条原因: %+v", pv)
			}
			rej := pv.Rejections[0]
			if rej.Kind != ErrOverweight || rej.CompartmentID != "C1" {
				t.Fatalf("应以超重原因指出 C1，实际 %+v", rej)
			}
			if rej.MaxWeight != math.MaxInt64-1 ||
				rej.UsedWeight != math.MaxInt64 ||
				rej.RemainingWeight != -1 ||
				rej.Overweight != 1 {
				t.Fatalf("超重原因应给出 承重M-1/预计M/剩余-1/超出1: %+v", rej)
			}
			// 预计清单仍只含 G1、G3（被拒安排也展示预计位置）。
			after := pv.Compartments[0].After
			if got := cargoIDs(after); !reflect.DeepEqual(got, []string{"G1", "G3"}) {
				t.Fatalf("超重时预计货物清单仍应给出: %v", got)
			}
			if after.UsedWeight != math.MaxInt64 || after.RemainingWeight != -1 {
				t.Fatalf("超重（可表示）时预计重量应照常给出 M 与 -1: %+v", after)
			}
			// 预览不改实际配载。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)

			// 正式提交：返回现有超重结构化错误，说明含舱位、预计重量与承重，
			// 不返回成功结果。
			se := submitExpectReject(t, r, "swap-bad", ops)
			if se.Kind != ErrOverweight || se.ID != "C1" {
				t.Fatalf("正式提交应报 C1 超重，实际 Kind=%s ID=%q", se.Kind, se.ID)
			}
			if se.AdjustmentID != "swap-bad" {
				t.Fatalf("错误应携带编号 swap-bad，实际 %q", se.AdjustmentID)
			}
			msg := se.Error()
			// 说明应指出舱位 C1，并含预计总重量 M 与最大承重 M-1（十进制），
			// 证明这是按“最终超重”而不是溢出或回绕数值拒绝。
			if !strings.Contains(msg, "C1") {
				t.Fatalf("超重说明应指出舱位 C1: %s", msg)
			}
			if !strings.Contains(msg, "9223372036854775807") ||
				!strings.Contains(msg, "9223372036854775806") {
				t.Fatalf("超重说明应含预计重量 M 与承重 M-1: %s", msg)
			}

			// 整批不生效：G1、G2 留在 C1，G3 仍未装载，资料与舱位重量不变。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertNearLimitProfilesUnchanged(t, r, 20)
		})
	}
}

// 最终溢出（不可表示）：C1 承重为 M，G3 重 21，换货后合计 (M-20)+21 =
// M+1 无法用 int64 表示。必须以既有的“重量溢出”原因拒绝，不能当作
// 普通超重，也不能返回回绕后的负数。
func TestNearLimitSwapFinalOverflowRejected(t *testing.T) {
	for name, ops := range nearLimitSwapOps() {
		t.Run(name, func(t *testing.T) {
			r := newNearLimitSwapRegistry(t, math.MaxInt64, 21)
			assertNearLimitBefore(t, r, math.MaxInt64)
			compBefore := snapshotCompartments(r, "C1")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			// 预览：给出预计货物清单并标为不可提交，原因为溢出；溢出时
			// 预计已用、剩余与超出量沿用现有零值约定，仅给承重 M。
			pv, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("逐条合法时预览应返回完整结果而非错误: %v", err)
			}
			if pv.Submittable || len(pv.Rejections) != 1 {
				t.Fatalf("最终溢出应不可提交且恰有一条原因: %+v", pv)
			}
			rej := pv.Rejections[0]
			if rej.Kind != ErrOverflow || rej.CompartmentID != "C1" {
				t.Fatalf("应以重量溢出原因指出 C1，实际 %+v", rej)
			}
			if rej.MaxWeight != math.MaxInt64 {
				t.Fatalf("溢出原因应给出承重 M: %+v", rej)
			}
			if rej.UsedWeight != 0 || rej.RemainingWeight != 0 || rej.Overweight != 0 {
				t.Fatalf("溢出时预计已用/剩余/超出应沿用零值约定: %+v", rej)
			}
			// 预计货物清单照常返回，但预计已用/剩余重量为零值。
			after := pv.Compartments[0].After
			if got := cargoIDs(after); !reflect.DeepEqual(got, []string{"G1", "G3"}) {
				t.Fatalf("溢出时预计货物清单仍应给出: %v", got)
			}
			if after.UsedWeight != 0 || after.RemainingWeight != 0 {
				t.Fatalf("溢出时预计已用与剩余重量应为零值: %+v", after)
			}
			// 预览不改实际配载。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)

			// 正式提交：返回现有溢出结构化错误，不返回成功结果，也不报超重。
			se := submitExpectReject(t, r, "swap-overflow", ops)
			if se.Kind != ErrOverflow || se.ID != "C1" {
				t.Fatalf("正式提交应报 C1 重量溢出，实际 Kind=%s ID=%q", se.Kind, se.ID)
			}
			if se.AdjustmentID != "swap-overflow" {
				t.Fatalf("错误应携带编号 swap-overflow，实际 %q", se.AdjustmentID)
			}
			if !strings.Contains(se.Error(), "C1") {
				t.Fatalf("溢出说明应指出舱位 C1: %s", se.Error())
			}

			// 整批不生效：G1、G2 留在 C1，G3 仍未装载，资料、舱位重量与
			// 剩余重量（10）都不改变。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertNearLimitProfilesUnchanged(t, r, 21)
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != math.MaxInt64-10 || cpt.RemainingWeight != 10 {
				t.Fatalf("拒绝后 C1 应仍为 已用M-10/剩余10: %+v", cpt)
			}
		})
	}
}

// 被最终超重/溢出拒绝的换货不占用调整编号：同一编号改用合法的“仅卸下
// G2”应能成功，确保拒绝路径既没生效也没污染编号记录。
func TestNearLimitSwapRejectionDoesNotConsumeID(t *testing.T) {
	// 最终超重形态。
	r := newNearLimitSwapRegistry(t, math.MaxInt64-1, 20)
	submitExpectReject(t, r, "swap-retry", []Op{
		{Kind: OpUnload, CargoID: "G2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})
	if _, err := r.Adjust("swap-retry", []Op{{Kind: OpUnload, CargoID: "G2"}}); err != nil {
		t.Fatalf("被拒编号不应被占用，合法的仅卸下 G2 应成功: %v", err)
	}
	g2, _ := r.Cargo("G2")
	if g2.Loaded || g2.CompartmentID != "" {
		t.Fatalf("重试成功后 G2 应已卸下: %+v", g2)
	}

	// 最终溢出形态。
	r2 := newNearLimitSwapRegistry(t, math.MaxInt64, 21)
	submitExpectReject(t, r2, "swap-retry2", []Op{
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		{Kind: OpUnload, CargoID: "G2"},
	})
	if _, err := r2.Adjust("swap-retry2", []Op{{Kind: OpUnload, CargoID: "G2"}}); err != nil {
		t.Fatalf("溢出被拒编号不应被占用: %v", err)
	}
}
