package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为“三舱位循环换舱”补充自动化回归保障，与已有的两舱互换覆盖
// （cross_destination_swap_test.go 等）互补。
//
// 三个舱位 C1、C2、C3 分别装着 G1、G2、G3，同一次调整把 G1 移到 C2、
// G2 移到 C3、G3 移到 C1：三件货物依次换到下一舱，并不组成两两互换。
// 合法性按整批操作完成后的最终配载判断：单独先移任何一件都会让目标舱
// 暂时超重并混入不同目的地，这些中间状态不得导致拒绝；全部完成后每个
// 舱位仍只含同一目的地的一件货物。
//
// 前半部分覆盖三舱承重均为 100 千克时预览可提交、正式提交成功且结果与
// 操作次序无关；后半部分覆盖仅 C1 承重降为 90 千克时预览判为不可提交、
// 正式提交按 C1 超重拒绝且整批不生效。

// 初始配载：C2、C3 承重均为 100 千克，C1 承重为 c1Max；
// C1 装去上海的 G1（60），C2 装去天津的 G2（80），C3 装去广州的 G3（100），
// 三件货物均不允许混装。c1Max 取 100 时三舱初始已用 60、80、100；
// 取 90 时初始装载仍合法（C1 已用 60、剩余 30）。
func newCyclicRotationRegistry(t *testing.T, c1Max int64) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", c1Max)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCompartment(t, r, "C3", 100)
	mustRegisterCargo(t, r, "G1", 60, "上海", false)
	mustRegisterCargo(t, r, "G2", 80, "天津", false)
	mustRegisterCargo(t, r, "G3", 100, "广州", false)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C3"},
	})
	return r
}

// rotationOps 返回循环换舱的三条移动操作：G1 到 C2、G2 到 C3、G3 到 C1。
func rotationOps() []Op {
	return []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C3"},
		{Kind: OpMove, CargoID: "G3", Target: "C1"},
	}
}

// rotationOpPermutations 返回三条移动操作的全部 6 种先后次序，用于保护
// “操作在清单中的先后次序不影响能否提交、最终配载与变化记录”。
func rotationOpPermutations() [][]Op {
	base := rotationOps()
	var out [][]Op
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

// wantRotationCargoChanges 是按货物编号排列的循环换舱货物变化记录。
func wantRotationCargoChanges() []CargoChange {
	return []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G2", From: "C2", To: "C3"},
		{CargoID: "G3", From: "C3", To: "C1"},
	}
}

// wantRotationCompChanges 是按舱位编号排列的循环换舱重量变化记录。
func wantRotationCompChanges() []CompartmentChange {
	return []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 60, WeightAfter: 100},
		{CompartmentID: "C2", WeightBefore: 80, WeightAfter: 60},
		{CompartmentID: "C3", WeightBefore: 100, WeightAfter: 80},
	}
}

// assertRotationFinalStowage 核对循环换舱完成后的最终配载：C1 只装 G3
// （已用 100、剩余 0），C2 只装 G1（已用 60、剩余 40），C3 只装 G2
// （已用 80、剩余 20）；货物查询与舱位清单一一对应，每件货物恰好出现
// 一次，重量、目的地与混装许可保持登记值。
func assertRotationFinalStowage(t *testing.T, r *Registry) {
	t.Helper()
	want := map[string]struct {
		cargo     string
		used      int64
		remaining int64
	}{
		"C1": {"G3", 100, 0},
		"C2": {"G1", 60, 40},
		"C3": {"G2", 80, 20},
	}
	for id, w := range want {
		cv, err := r.Compartment(id)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", id, err)
		}
		if cv.UsedWeight != w.used || cv.RemainingWeight != w.remaining ||
			!reflect.DeepEqual(cargoIDs(*cv), []string{w.cargo}) {
			t.Fatalf("舱位 %s 最终配载错误: %+v", id, cv)
		}
		// 舱内清单与货物查询一致：清单中唯一货物的归属就是该舱位。
		if !cv.Cargo[0].Loaded || cv.Cargo[0].CompartmentID != id {
			t.Fatalf("舱位 %s 清单中货物归属错误: %+v", id, cv.Cargo[0])
		}
	}

	// 每件货物的归属与登记资料。
	wantCargo := map[string]struct {
		comp        string
		weight      int64
		destination string
	}{
		"G1": {"C2", 60, "上海"},
		"G2": {"C3", 80, "天津"},
		"G3": {"C1", 100, "广州"},
	}
	for id, w := range wantCargo {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !cv.Loaded || cv.CompartmentID != w.comp {
			t.Fatalf("货物 %s 应在 %s，实际 %q（loaded=%v）", id, w.comp, cv.CompartmentID, cv.Loaded)
		}
		if cv.Weight != w.weight || cv.Destination != w.destination || cv.AllowMixed {
			t.Fatalf("货物 %s 的登记资料被改变: %+v", id, cv)
		}
	}
}

// assertRotationInitialStowage 核对初始配载保持原样：G1、G2、G3 分别仍在
// C1、C2、C3，三舱已用 60、80、100，剩余为承重减去已用。
func assertRotationInitialStowage(t *testing.T, r *Registry, c1Max int64) {
	t.Helper()
	want := map[string]struct {
		cargo string
		used  int64
		max   int64
	}{
		"C1": {"G1", 60, c1Max},
		"C2": {"G2", 80, 100},
		"C3": {"G3", 100, 100},
	}
	for id, w := range want {
		cv, err := r.Compartment(id)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", id, err)
		}
		if cv.UsedWeight != w.used || cv.RemainingWeight != w.max-w.used ||
			!reflect.DeepEqual(cargoIDs(*cv), []string{w.cargo}) {
			t.Fatalf("舱位 %s 应保持调整前状态: %+v", id, cv)
		}
	}
	wantAt := map[string]string{"G1": "C1", "G2": "C2", "G3": "C3"}
	for id, wantComp := range wantAt {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !cv.Loaded || cv.CompartmentID != wantComp {
			t.Fatalf("货物 %s 应仍在 %s，实际 %q（loaded=%v）", id, wantComp, cv.CompartmentID, cv.Loaded)
		}
	}
}

// ---------- 三舱承重均为 100 千克：循环换舱合法 ----------

// 预览循环换舱：虽然单独先移任何一件都会让目标舱暂时超重并混入不同目的地，
// 但按整批完成后的最终配载判断应可提交且没有拒绝原因；预览完整保留各舱
// 调整前清单，预计清单中的货物显示为已装载于新舱；预览之后三件货物仍在
// 原舱。
func TestPreviewCyclicRotationSubmittable(t *testing.T) {
	r := newCyclicRotationRegistry(t, 100)

	pv, err := r.Preview(rotationOps())
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Submittable {
		t.Fatalf("循环换舱按最终配载应可提交，拒绝原因: %+v", pv.Rejections)
	}
	if len(pv.Rejections) != 0 {
		t.Fatalf("可提交的预览不应有拒绝原因: %+v", pv.Rejections)
	}

	// 货物变化记录三件货物各自的原舱与新舱，按货物编号排列。
	if !reflect.DeepEqual(pv.CargoChanges, wantRotationCargoChanges()) {
		t.Fatalf("预览货物变化错误: got=%+v want=%+v", pv.CargoChanges, wantRotationCargoChanges())
	}

	// 三个舱位都列出调整前后的完整配载，按舱位编号排列。
	if len(pv.Compartments) != 3 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" || pv.Compartments[2].ID != "C3" {
		t.Fatalf("预览应依次列出 C1、C2、C3: %+v", pv.Compartments)
	}
	wantBefore := map[string]string{"C1": "G1", "C2": "G2", "C3": "G3"}
	wantAfter := map[string]struct {
		cargo     string
		used      int64
		remaining int64
	}{
		"C1": {"G3", 100, 0},
		"C2": {"G1", 60, 40},
		"C3": {"G2", 80, 20},
	}
	for _, cp := range pv.Compartments {
		// 调整前清单完整保留，货物保持原所属舱位。
		if len(cp.Before.Cargo) != 1 || cp.Before.Cargo[0].ID != wantBefore[cp.ID] ||
			!cp.Before.Cargo[0].Loaded || cp.Before.Cargo[0].CompartmentID != cp.ID {
			t.Fatalf("舱位 %s 调整前清单错误: %+v", cp.ID, cp.Before.Cargo)
		}
		// 预计清单中的货物显示为已装载于新舱，重量与剩余重量对应最终配载。
		w := wantAfter[cp.ID]
		if cp.After.UsedWeight != w.used || cp.After.RemainingWeight != w.remaining ||
			len(cp.After.Cargo) != 1 || cp.After.Cargo[0].ID != w.cargo {
			t.Fatalf("舱位 %s 预计配载错误: %+v", cp.ID, cp.After)
		}
		if !cp.After.Cargo[0].Loaded || cp.After.Cargo[0].CompartmentID != cp.ID {
			t.Fatalf("舱位 %s 预计清单中的货物应显示已装载于该舱: %+v", cp.ID, cp.After.Cargo[0])
		}
	}

	// 预览是只读的：三件货物仍在原舱，各舱重量保持调整前状态。
	assertRotationInitialStowage(t, r, 100)
}

// 正式提交循环换舱应成功：C1、C2、C3 分别只装 G3、G1、G2，已用重量
// 100、60、80，剩余 0、40、20，与预览对应；变化记录反映三件货物各自的
// 原舱与新舱以及三个舱位准确的前后重量。三条操作的全部先后次序都得到
// 相同的能否提交、最终配载与按编号排列的变化记录。
func TestAdjustCyclicRotationSucceeds(t *testing.T) {
	for i, ops := range rotationOpPermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCyclicRotationRegistry(t, 100)

			res, err := r.Adjust("rotate", ops)
			if err != nil {
				t.Fatalf("第 %d 种次序：循环换舱按最终配载应合法，却被拒绝: %v", i, err)
			}
			if res == nil || res.ID != "rotate" {
				t.Fatalf("第 %d 种次序：成功结果异常: %+v", i, res)
			}

			// 变化记录按编号排列，与操作先后次序无关。
			if !reflect.DeepEqual(res.CargoChanges, wantRotationCargoChanges()) {
				t.Fatalf("第 %d 种次序：货物变化错误: got=%+v want=%+v",
					i, res.CargoChanges, wantRotationCargoChanges())
			}
			if !reflect.DeepEqual(res.CompartmentChanges, wantRotationCompChanges()) {
				t.Fatalf("第 %d 种次序：舱位重量变化错误: got=%+v want=%+v",
					i, res.CompartmentChanges, wantRotationCompChanges())
			}

			// 最终配载与预览对应，货物查询与舱位清单一致，每件只出现一次。
			assertRotationFinalStowage(t, r)
		})
	}
}

// ---------- C1 承重降为 90 千克：循环换舱后 C1 超重 ----------

// 同样的初始货物与换舱安排，只把 C1 承重设为 90 千克：初始装载仍合法，
// 但换舱后 C1 将装 100 千克。预览应判为不可提交，仍给出完整预计配载，
// 并指出 C1 超重 10 千克、预计剩余重量为负 10 千克。
func TestPreviewCyclicRotationOverweight(t *testing.T) {
	r := newCyclicRotationRegistry(t, 90)

	// 初始装载合法：C1 已用 60、剩余 30。
	assertRotationInitialStowage(t, r, 90)

	pv, err := r.Preview(rotationOps())
	if err != nil {
		t.Fatal(err)
	}
	if pv.Submittable {
		t.Fatal("C1 承重 90 时循环换舱应判为不可提交")
	}

	// 唯一的拒绝原因是 C1 超重：预计总重 100、超重 10、预计剩余 -10。
	wantRej := []Rejection{{
		CompartmentID:   "C1",
		Kind:            ErrOverweight,
		MaxWeight:       90,
		UsedWeight:      100,
		RemainingWeight: -10,
		Overweight:      10,
	}}
	if !reflect.DeepEqual(pv.Rejections, wantRej) {
		t.Fatalf("拒绝原因错误: got=%+v want=%+v", pv.Rejections, wantRej)
	}

	// 不可提交时仍给出完整预计配载：三个舱位的前后清单都在，
	// C1 预计装 G3、已用 100、剩余 -10。
	if len(pv.Compartments) != 3 ||
		pv.Compartments[0].ID != "C1" || pv.Compartments[1].ID != "C2" || pv.Compartments[2].ID != "C3" {
		t.Fatalf("预览应依次列出 C1、C2、C3: %+v", pv.Compartments)
	}
	c1 := pv.Compartments[0]
	if len(c1.Before.Cargo) != 1 || c1.Before.Cargo[0].ID != "G1" {
		t.Fatalf("C1 调整前清单应保留 G1: %+v", c1.Before.Cargo)
	}
	if c1.After.UsedWeight != 100 || c1.After.RemainingWeight != -10 ||
		len(c1.After.Cargo) != 1 || c1.After.Cargo[0].ID != "G3" {
		t.Fatalf("C1 预计配载应为 G3 且超重: %+v", c1.After)
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantRotationCargoChanges()) {
		t.Fatalf("预览货物变化错误: got=%+v want=%+v", pv.CargoChanges, wantRotationCargoChanges())
	}

	// 预览不改变状态。
	assertRotationInitialStowage(t, r, 90)
}

// 正式提交必须报告 C1 超重，说明预计总重 100 千克与承重 90 千克，不返回
// 成功结果；三件货物的位置和三个舱位的实际清单、已用及剩余重量都保留
// 调整前的状态，不能先完成部分换舱。
func TestAdjustCyclicRotationOverweightRejected(t *testing.T) {
	for i, ops := range rotationOpPermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCyclicRotationRegistry(t, 90)

			compBefore := snapshotCompartments(r, "C1", "C2", "C3")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			se := submitExpectReject(t, r, "rotate", ops)
			if se.Kind != ErrOverweight {
				t.Fatalf("第 %d 种次序：应按超重拒绝，实际 Kind=%s", i, se.Kind)
			}
			if se.ID != "C1" {
				t.Fatalf("第 %d 种次序：超重应指出舱位 C1，实际 ID=%q", i, se.ID)
			}
			if !strings.Contains(se.Error(), "100") || !strings.Contains(se.Error(), "90") {
				t.Fatalf("第 %d 种次序：超重说明应含预计总重 100 与承重 90: %s", i, se.Error())
			}
			if se.AdjustmentID != "rotate" {
				t.Fatalf("第 %d 种次序：错误应携带调整编号 rotate，实际 %q", i, se.AdjustmentID)
			}

			// 整批不生效：货物位置、舱位清单与重量都保持调整前状态，
			// 不能先完成部分换舱。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertRotationInitialStowage(t, r, 90)
		})
	}
}
