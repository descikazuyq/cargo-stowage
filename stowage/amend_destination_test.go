package stowage

import (
	"strings"
	"testing"
)

// 本文件为“更正已装载货物的目的地”补充自动化回归保障，重点保护混装限制
// 的判断范围与拒绝说明的指向：
//
// 混装限制必须按同舱全部货物判断。被更正的货物本身允许混装，并不能绕过
// 同舱其他货物的限制——它改去另一目的地后，舱内出现不同目的地，任一不
// 允许混装的同舱货物都会使更正按现有的结构化混装冲突错误 ErrMixedLoading
// 拒绝。错误涉及编号应指向真正不允许混装的同舱货物（多件时取编号字典序
// 最靠前的一件），而不是固定填入本次被更正货物的编号；中文说明同时指出
// 所属舱位与这件货物。
//
// 这些保障沿用 AmendCargo 的完整资料替换方式：即使只更改目的地，提交时
// 重量与混装许可也传入原值。被更正货物保持已装载，舱位当前配载合法且
// 重量在承重以内。所有观察都经由现有的登记（RegisterCargo/
// RegisterCompartment）、更正（AmendCargo）与查询（Cargo/Compartment）
// 入口完成，不把内部记录的组织方式当作公开约定。

// newDestinationAmendRegistry 建立“同舱货物原本同去上海”的初始配载。
// C1 承重 100 千克；G1（10）允许混装，G2（20）、G3（30）不允许混装，
// 三件货物都装入 C1（合计 60 千克，同一目的地共舱合法）。
//
// regOrder 给出登记次序，loadOrder 给出初始装载次序（每件在一次装载调整
// 中完成，调整编号随次序变化）；同一配载在不同登记、装载次序下建立后，
// 后续更正的结论与涉及编号都应相同。
func newDestinationAmendRegistry(t *testing.T, regOrder, loadOrder []string) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	profiles := map[string]struct {
		weight int64
		mixed  bool
	}{
		"G1": {10, true},
		"G2": {20, false},
		"G3": {30, false},
	}
	for _, id := range regOrder {
		p := profiles[id]
		mustRegisterCargo(t, r, id, p.weight, "上海", p.mixed)
	}
	ops := make([]Op, 0, len(loadOrder))
	for _, id := range loadOrder {
		ops = append(ops, Op{Kind: OpLoad, CargoID: id, Target: "C1"})
	}
	// 每次建库使用不同的调整编号，避免装载次序本身影响结论。
	mustAdjust(t, r, "load-"+strings.Join(loadOrder, "-"), ops)
	return r
}

// assertCargoProfile 经由货物查询入口核对一件货物的完整公开资料：
// 重量、目的地、混装许可、装载状态与所属舱位。
func assertCargoProfile(t *testing.T, r *Registry, id string, wantWeight int64, wantDest string, wantMixed bool, wantComp string) {
	t.Helper()
	cv, err := r.Cargo(id)
	if err != nil {
		t.Fatalf("查询货物 %s 失败: %v", id, err)
	}
	if cv.Weight != wantWeight || cv.Destination != wantDest || cv.AllowMixed != wantMixed {
		t.Fatalf("货物 %s 资料错误：got 重量=%d 目的地=%q 允许混装=%t，want 重量=%d 目的地=%q 允许混装=%t",
			id, cv.Weight, cv.Destination, cv.AllowMixed, wantWeight, wantDest, wantMixed)
	}
	wantLoaded := wantComp != ""
	if cv.Loaded != wantLoaded || cv.CompartmentID != wantComp {
		t.Fatalf("货物 %s 归属错误：got 已装载=%t 舱位=%q，want 已装载=%t 舱位=%q",
			id, cv.Loaded, cv.CompartmentID, wantLoaded, wantComp)
	}
}

// assertCompartmentCargo 经由舱位查询入口核对舱位清单中每件货物展示的
// 目的地与混装许可（清单按编号字典序排列），并核对清单完整、已用与剩余
// 重量。expect 的键为货物编号，值为该货物在清单中应展示的资料。
func assertCompartmentCargo(t *testing.T, r *Registry, comp string, wantUsed, wantRemaining int64, expect map[string]CargoView) {
	t.Helper()
	cpt, err := r.Compartment(comp)
	if err != nil {
		t.Fatalf("查询舱位 %s 失败: %v", comp, err)
	}
	if cpt.UsedWeight != wantUsed || cpt.RemainingWeight != wantRemaining {
		t.Fatalf("舱位 %s 重量错误：got 已用=%d 剩余=%d，want 已用=%d 剩余=%d",
			comp, cpt.UsedWeight, cpt.RemainingWeight, wantUsed, wantRemaining)
	}
	if len(cpt.Cargo) != len(expect) {
		t.Fatalf("舱位 %s 清单不完整：got %d 件 %v，want %d 件",
			comp, len(cpt.Cargo), cpt.Cargo, len(expect))
	}
	for i := 1; i < len(cpt.Cargo); i++ {
		if cpt.Cargo[i-1].ID >= cpt.Cargo[i].ID {
			t.Fatalf("舱位 %s 清单应按编号字典序排列：%v", comp, cpt.Cargo)
		}
	}
	for _, cv := range cpt.Cargo {
		want, ok := expect[cv.ID]
		if !ok {
			t.Fatalf("舱位 %s 清单出现意外货物 %s", comp, cv.ID)
		}
		if cv.Weight != want.Weight || cv.Destination != want.Destination ||
			cv.AllowMixed != want.AllowMixed || !cv.Loaded || cv.CompartmentID != comp {
			t.Fatalf("舱位 %s 清单中货物 %s 展示错误：got %+v，want 重量=%d 目的地=%q 允许混装=%t 已装载于 %s",
				comp, cv.ID, cv, want.Weight, want.Destination, want.AllowMixed, comp)
		}
	}
}

// TestAmendDestinationMixedConflictPointsAtOtherCargo 是核心保障：同舱货物
// 原本都去同一目的地，其中 G1 允许混装、G2/G3 不允许混装。只把 G1 的
// 目的地改为北京（重量与混装许可提交原值），即使 G1 自己允许混装，也不能
// 绕过 G2、G3 的限制，必须返回现有的结构化混装冲突错误，涉及编号指向
// 字典序最靠前的不允许混装货物 G2，而不是被更正的 G1。
func TestAmendDestinationMixedConflictPointsAtOtherCargo(t *testing.T) {
	r := newDestinationAmendRegistry(t,
		[]string{"G1", "G2", "G3"},
		[]string{"G1", "G2", "G3"})

	err := r.AmendCargo("G1", 10, "北京", true)
	se := requireError(t, err, ErrMixedLoading)
	if se.ID != "G2" {
		t.Fatalf("混装冲突应指出真正不允许混装的同舱货物 G2，而非被更正货物，实际 ID=%q", se.ID)
	}
	// 更正不携带调整编号。
	if se.AdjustmentID != "" {
		t.Fatalf("更正的混装冲突不应携带调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	// 中文说明同时指出所属舱位与真正不允许混装的货物，且不点名被更正货物。
	if want := "舱位 C1 存在不同目的地货物，但货物 G2 不允许混装"; se.Error() != want {
		t.Fatalf("混装说明应指出舱位 C1 与货物 G2，got %q want %q", se.Error(), want)
	}

	// 被拒绝后重新查询：G1 保留原目的地、原重量、原混装许可，仍在原舱位。
	assertCargoProfile(t, r, "G1", 10, "上海", true, "C1")
	// 同舱其他货物的资料与位置不变。
	assertCargoProfile(t, r, "G2", 20, "上海", false, "C1")
	assertCargoProfile(t, r, "G3", 30, "上海", false, "C1")
	// 舱位清单继续完整列出原有货物，全部显示原目的地，已用/剩余保持
	// 更正前的 60/40——不能货物查询仍旧、舱位清单却写入新目的地。
	assertCompartmentCargo(t, r, "C1", 60, 40, map[string]CargoView{
		"G1": {Weight: 10, Destination: "上海", AllowMixed: true},
		"G2": {Weight: 20, Destination: "上海", AllowMixed: false},
		"G3": {Weight: 30, Destination: "上海", AllowMixed: false},
	})
}

// TestAmendDestinationMixedConflictStableAcrossOrdering 改变这些货物的登记
// 次序与初始装载次序，不应改变同一配载下更正的结论或涉及编号：无论先登记
// 、先装载哪一件，被更正的都是 G1，祸首始终是字典序最靠前的 G2，失败后
// 的查询结果也一致。
func TestAmendDestinationMixedConflictStableAcrossOrdering(t *testing.T) {
	registrations := [][]string{
		{"G1", "G2", "G3"},
		{"G2", "G1", "G3"},
		{"G3", "G2", "G1"},
	}
	loadings := [][]string{
		{"G1", "G2", "G3"},
		{"G3", "G2", "G1"},
		{"G2", "G1", "G3"},
	}
	for _, regOrder := range registrations {
		for _, loadOrder := range loadings {
			r := newDestinationAmendRegistry(t, regOrder, loadOrder)
			se := requireError(t, r.AmendCargo("G1", 10, "北京", true), ErrMixedLoading)
			if se.ID != "G2" {
				t.Fatalf("登记次序%v、装载次序%v：涉及编号应为 G2，实际 %q",
					regOrder, loadOrder, se.ID)
			}
			if !strings.Contains(se.Error(), "C1") || !strings.Contains(se.Error(), "G2") {
				t.Fatalf("登记次序%v、装载次序%v：说明应指出 C1 与 G2：%s",
					regOrder, loadOrder, se.Error())
			}
			assertCargoProfile(t, r, "G1", 10, "上海", true, "C1")
			assertCargoProfile(t, r, "G2", 20, "上海", false, "C1")
			assertCargoProfile(t, r, "G3", 30, "上海", false, "C1")
			assertCompartmentCargo(t, r, "C1", 60, 40, map[string]CargoView{
				"G1": {Weight: 10, Destination: "上海", AllowMixed: true},
				"G2": {Weight: 20, Destination: "上海", AllowMixed: false},
				"G3": {Weight: 30, Destination: "上海", AllowMixed: false},
			})
		}
	}
}

// TestAmendDestinationConflictWithMultipleOffendersPicksEarliest 多件不允许
// 混装的同舱货物中，涉及编号沿用编号字典序最靠前的选择：把不允许混装的
// 货物设为 G2、G7、G3 时，祸首仍是 G2（而不是装载或登记次序中的某一件）。
func TestAmendDestinationConflictWithMultipleOffendersPicksEarliest(t *testing.T) {
	for _, loadOrder := range [][]string{
		{"G1", "G7", "G3", "G2"},
		{"G2", "G3", "G7", "G1"},
	} {
		r := NewRegistry()
		mustRegisterCompartment(t, r, "C1", 1000)
		mustRegisterCargo(t, r, "G1", 10, "上海", true)
		mustRegisterCargo(t, r, "G7", 10, "上海", false)
		mustRegisterCargo(t, r, "G3", 10, "上海", false)
		mustRegisterCargo(t, r, "G2", 10, "上海", false)
		ops := make([]Op, 0, len(loadOrder))
		for _, id := range loadOrder {
			ops = append(ops, Op{Kind: OpLoad, CargoID: id, Target: "C1"})
		}
		mustAdjust(t, r, "load-"+strings.Join(loadOrder, "-"), ops)

		se := requireError(t, r.AmendCargo("G1", 10, "北京", true), ErrMixedLoading)
		if se.ID != "G2" {
			t.Fatalf("装载次序%v：多件不允许混装时应取字典序最靠前的 G2，实际 ID=%q",
				loadOrder, se.ID)
		}
	}
}

// TestAmendDestinationSucceedsWhenAllCargoAllowMixed 保护同一规则允许的
// 更正：同舱全部货物都允许混装时，把其中一件改去另一目的地应成功。新查询
// 中的货物资料与舱位清单都显示新目的地，货物归属与重量占用不变。
func TestAmendDestinationSucceedsWhenAllCargoAllowMixed(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCargo(t, r, "H1", 10, "上海", true)
	mustRegisterCargo(t, r, "H2", 20, "上海", true)
	mustRegisterCargo(t, r, "H3", 30, "上海", true)
	mustAdjust(t, r, "load-1", []Op{
		{Kind: OpLoad, CargoID: "H1", Target: "C1"},
		{Kind: OpLoad, CargoID: "H2", Target: "C1"},
		{Kind: OpLoad, CargoID: "H3", Target: "C1"},
	})

	// 只改 H1 的目的地，重量与混装许可提交原值。
	if err := r.AmendCargo("H1", 10, "北京", true); err != nil {
		t.Fatalf("同舱全部允许混装时改去另一目的地应成功: %v", err)
	}
	// H1 显示新目的地，仍装载于 C1，重量与混装许可不变。
	assertCargoProfile(t, r, "H1", 10, "北京", true, "C1")
	// 同舱其他货物资料与位置不变。
	assertCargoProfile(t, r, "H2", 20, "上海", true, "C1")
	assertCargoProfile(t, r, "H3", 30, "上海", true, "C1")
	// 舱位清单同样显示 H1 的新目的地，重量占用维持 60/40。
	assertCompartmentCargo(t, r, "C1", 60, 40, map[string]CargoView{
		"H1": {Weight: 10, Destination: "北京", AllowMixed: true},
		"H2": {Weight: 20, Destination: "上海", AllowMixed: true},
		"H3": {Weight: 30, Destination: "上海", AllowMixed: true},
	})
}

// TestAmendDestinationWhitespaceOnlyChangeAllowedWithNonMixedCargo 提交的
// 目的地只是原值增加首尾空白，去空白后仍与同舱货物一致时，即使其他货物
// 不允许混装也应成功：不存在不同目的地共舱。保存的目的地沿用现有去空白
// 规则（去掉首尾空白），重量占用与归属不变。
func TestAmendDestinationWhitespaceOnlyChangeAllowedWithNonMixedCargo(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCargo(t, r, "W1", 10, "上海", true)
	mustRegisterCargo(t, r, "W2", 20, "上海", false)
	mustAdjust(t, r, "load-1", []Op{
		{Kind: OpLoad, CargoID: "W1", Target: "C1"},
		{Kind: OpLoad, CargoID: "W2", Target: "C1"},
	})

	if err := r.AmendCargo("W1", 10, "  上海\t", true); err != nil {
		t.Fatalf("去空白后目的地与同舱一致时应成功，即使 W2 不允许混装: %v", err)
	}
	// 保存值沿用去空白规则，不带首尾空白。
	assertCargoProfile(t, r, "W1", 10, "上海", true, "C1")
	assertCargoProfile(t, r, "W2", 20, "上海", false, "C1")
	assertCompartmentCargo(t, r, "C1", 30, 70, map[string]CargoView{
		"W1": {Weight: 10, Destination: "上海", AllowMixed: true},
		"W2": {Weight: 20, Destination: "上海", AllowMixed: false},
	})
}
