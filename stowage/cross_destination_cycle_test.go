package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为批量配载调整补充“三个舱位循环换舱”的自动化回归保障，与已有的
// 两舱整舱互换（cross_destination_swap_test.go）互补：这里三件货物各自
// 依次换到下一舱（G1: C1->C2、G2: C2->C3、G3: C3->C1），并不组成两两
// 互换。合法性按整批操作全部完成后的最终配载判断：任何一件先移都会让
// 目标舱暂时超重并混入不同目的地，但完整安排完成后每个舱位仍只含同一
// 目的地的一件货物，最终配载合法，中间状态不得导致拒绝。
//
// 初始配载：C1、C2、C3 承重均为 100 千克（超重场景把 C1 改成 90）；
// G1 重 60 千克、目的地上海，在 C1；G2 重 80 千克、目的地天津，在 C2；
// G3 重 100 千克、目的地广州，在 C3；三件货物均不允许混装。

// newCrossDestinationCycleRegistry 建立循环换舱的初始配载。c1Max 决定
// C1 的承重（合法场景 100，超重场景 90）；C2、C3 始终为 100。初始装载
// 各舱一件货物，重量均不超过所在舱承重。
func newCrossDestinationCycleRegistry(t *testing.T, c1Max int64) *Registry {
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

// cycleMovePermutations 返回三条循环移动操作的全部 6 种先后次序，用于
// 保护“操作在清单中的先后次序不影响能否提交、最终配载与按编号排列的
// 变化记录”。
func cycleMovePermutations() [][]Op {
	base := []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C3"},
		{Kind: OpMove, CargoID: "G3", Target: "C1"},
	}
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

// cycleExpectedCargoChanges 是循环换舱按货物编号排列的变化记录。
func cycleExpectedCargoChanges() []CargoChange {
	return []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G2", From: "C2", To: "C3"},
		{CargoID: "G3", From: "C3", To: "C1"},
	}
}

// assertCycleCargoProfilesUnchanged 核对三件货物的重量、目的地与混装
// 许可始终保持登记原值，仅归属随调整改变。
func assertCycleCargoProfilesUnchanged(t *testing.T, r *Registry) {
	t.Helper()
	want := map[string]struct {
		weight      int64
		destination string
	}{
		"G1": {60, "上海"},
		"G2": {80, "天津"},
		"G3": {100, "广州"},
	}
	for id, w := range want {
		cv, _ := r.Cargo(id)
		if cv.Weight != w.weight || cv.Destination != w.destination || cv.AllowMixed {
			t.Fatalf("货物 %s 的登记资料被改变: %+v", id, cv)
		}
	}
}

// assertCycleActualStowage 核对登记处的实际配载：C1 只装 G1（已用 60、
// 剩余 c1Max-60），C2 只装 G2（80/20），C3 只装 G3（100/0），每件货物
// 的货物查询归属与所在舱清单一致，且三件货物在三舱清单中各出现一次，
// 重量、目的地与混装许可保持登记值。
func assertCycleActualStowage(t *testing.T, r *Registry, c1Max int64) {
	t.Helper()
	wantComps := []struct {
		id        string
		maxWeight int64
		used      int64
		remaining int64
		cargo     []string
	}{
		{"C1", c1Max, 60, c1Max - 60, []string{"G1"}},
		{"C2", 100, 80, 20, []string{"G2"}},
		{"C3", 100, 100, 0, []string{"G3"}},
	}
	seen := make(map[string]string)
	for _, wc := range wantComps {
		cv, err := r.Compartment(wc.id)
		if err != nil {
			t.Fatalf("查询舱位 %s 失败: %v", wc.id, err)
		}
		if cv.MaxWeight != wc.maxWeight || cv.UsedWeight != wc.used ||
			cv.RemainingWeight != wc.remaining ||
			!reflect.DeepEqual(cargoIDs(*cv), wc.cargo) {
			t.Fatalf("舱位 %s 实际配载错误: got=%+v want 承重=%d 已用=%d 剩余=%d 清单=%v",
				wc.id, cv, wc.maxWeight, wc.used, wc.remaining, wc.cargo)
		}
		for _, c := range cv.Cargo {
			if !c.Loaded || c.CompartmentID != wc.id {
				t.Fatalf("舱位 %s 清单中的 %s 位置标记错误: loaded=%v comp=%q",
					wc.id, c.ID, c.Loaded, c.CompartmentID)
			}
			if other, dup := seen[c.ID]; dup {
				t.Fatalf("货物 %s 同时出现在 %s 与 %s 清单中", c.ID, other, wc.id)
			}
			seen[c.ID] = wc.id
		}
	}
	if len(seen) != 3 {
		t.Fatalf("三舱清单应恰好含三件货物，实际: %+v", seen)
	}
	wantAt := map[string]string{"G1": "C1", "G2": "C2", "G3": "C3"}
	for id, wantComp := range wantAt {
		cv, _ := r.Cargo(id)
		if !cv.Loaded || cv.CompartmentID != wantComp {
			t.Fatalf("货物 %s 应在 %s，实际 %q（loaded=%v）",
				id, wantComp, cv.CompartmentID, cv.Loaded)
		}
		if seen[id] != wantComp {
			t.Fatalf("货物查询 %s 在 %s，但舱位清单显示在 %s", id, wantComp, seen[id])
		}
	}
	assertCycleCargoProfilesUnchanged(t, r)
}

// assertCyclePreviewCompartments 核对循环换舱预览中三个受影响舱位完整的
// 调整前/预计清单、承重、已用与剩余重量：Before 保留各舱调整前清单且货物
// 标记为原舱；After 中货物一律显示已装载于新舱；登记资料不变。
// c1Max 为 C1 承重（100 合法、90 超重）；C1 超重时其预计剩余为 -10。
func assertCyclePreviewCompartments(t *testing.T, res *PreviewResult, c1Max int64) {
	t.Helper()
	if len(res.Compartments) != 3 {
		t.Fatalf("预览应列出全部三个受影响舱位: %+v", res.Compartments)
	}
	want := []struct {
		id                      string
		maxWeight               int64
		usedBefore, remBefore   int64
		usedAfter, remAfter     int64
		cargoBefore, cargoAfter []string
	}{
		{"C1", c1Max, 60, c1Max - 60, 100, c1Max - 100, []string{"G1"}, []string{"G3"}},
		{"C2", 100, 80, 20, 60, 40, []string{"G2"}, []string{"G1"}},
		{"C3", 100, 100, 0, 80, 20, []string{"G3"}, []string{"G2"}},
	}
	for i, wc := range want {
		cp := res.Compartments[i]
		if cp.ID != wc.id {
			t.Fatalf("舱位应按编号字典序排列，第 %d 个应为 %s，实际 %s", i, wc.id, cp.ID)
		}
		b, a := cp.Before, cp.After
		if b.MaxWeight != wc.maxWeight || b.UsedWeight != wc.usedBefore ||
			b.RemainingWeight != wc.remBefore ||
			!reflect.DeepEqual(cargoIDs(b), wc.cargoBefore) {
			t.Fatalf("%s 调整前清单/重量错误: %+v", wc.id, b)
		}
		if a.MaxWeight != wc.maxWeight || a.UsedWeight != wc.usedAfter ||
			a.RemainingWeight != wc.remAfter ||
			!reflect.DeepEqual(cargoIDs(a), wc.cargoAfter) {
			t.Fatalf("%s 预计清单/重量错误: %+v", wc.id, a)
		}
		// Before 中货物保留原所属舱位。
		wantBeforeAt := map[string]string{"C1": "G1", "C2": "G2", "C3": "G3"}[wc.id]
		for _, c := range b.Cargo {
			if !c.Loaded || c.CompartmentID != wc.id || c.ID != wantBeforeAt {
				t.Fatalf("%s 调整前清单应保留 %s 原位置: %+v", wc.id, wantBeforeAt, c)
			}
		}
		// After 中货物一律显示已装载于该新舱。
		for _, c := range a.Cargo {
			if !c.Loaded || c.CompartmentID != wc.id {
				t.Fatalf("%s 预计清单中的 %s 应显示已装载于 %s: %+v",
					wc.id, c.ID, wc.id, c)
			}
		}
	}
	// 预计清单中的登记资料保持原值。
	wantProfile := map[string]struct {
		weight      int64
		destination string
	}{
		"G1": {60, "上海"},
		"G2": {80, "天津"},
		"G3": {100, "广州"},
	}
	for _, cp := range res.Compartments {
		for _, c := range cp.After.Cargo {
			wp := wantProfile[c.ID]
			if c.Weight != wp.weight || c.Destination != wp.destination || c.AllowMixed {
				t.Fatalf("预计清单中 %s 的登记资料被改变: %+v", c.ID, c)
			}
		}
	}
}

// 三舱循环换舱的预览应判为可提交且没有拒绝原因：虽然逐条先移任何一件都会
// 让目标舱暂时超重（目标舱原货尚未移出）并混入不同目的地，最终配载却是
// C1=G3（广州）、C2=G1（上海）、C3=G2（天津），各舱一件、同一目的地。
// 预览完整保留各舱调整前清单，预计清单中的货物显示已装载于新舱；预览只读，
// 预览之后查询三件货物仍在原舱。该结论对三条操作的全部先后次序成立。
func TestPreviewCrossDestinationCycleSucceedsAndDoesNotApply(t *testing.T) {
	for i, ops := range cycleMovePermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCrossDestinationCycleRegistry(t, 100)

			res, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("第 %d 种次序：预览不应出错: %v", i, err)
			}
			if !res.Submittable {
				t.Fatalf("第 %d 种次序：循环换舱按最终配载应可提交: %+v", i, res.Rejections)
			}
			if len(res.Rejections) != 0 {
				t.Fatalf("第 %d 种次序：可提交预览不应有拒绝原因: %+v", i, res.Rejections)
			}
			if !reflect.DeepEqual(res.CargoChanges, cycleExpectedCargoChanges()) {
				t.Fatalf("第 %d 种次序：货物变化错误: got=%+v want=%+v",
					i, res.CargoChanges, cycleExpectedCargoChanges())
			}
			assertCyclePreviewCompartments(t, res, 100)

			// 预览只读：三件货物仍在原舱，三舱清单、已用与剩余重量不变。
			assertCycleActualStowage(t, r, 100)
		})
	}
}

// 正式提交循环换舱应成功。变化记录反映三件货物各自的原舱与新舱，以及三个
// 舱位准确的前后重量，均按编号排列；最终 C1、C2、C3 分别只装 G3、G1、G2，
// 已用 100、60、80，剩余 0、40、20。货物查询与舱位清单严格一致，每件只
// 出现一次，重量、目的地和混装许可保持登记值。三条操作的全部先后次序都
// 得到同一结论。
func TestAdjustCrossDestinationCycleSucceeds(t *testing.T) {
	for i, ops := range cycleMovePermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCrossDestinationCycleRegistry(t, 100)

			res, err := r.Adjust("cycle-full", ops)
			if err != nil {
				t.Fatalf("第 %d 种次序：循环换舱按最终配载应合法，却被拒绝: %v", i, err)
			}
			if res == nil || res.ID != "cycle-full" {
				t.Fatalf("第 %d 种次序：成功结果异常: %+v", i, res)
			}
			if !reflect.DeepEqual(res.CargoChanges, cycleExpectedCargoChanges()) {
				t.Fatalf("第 %d 种次序：货物变化错误: got=%+v want=%+v",
					i, res.CargoChanges, cycleExpectedCargoChanges())
			}
			// 三个舱位准确的前后重量，按舱位编号排列；即使没有舱位重量
			// 相等也逐一核对。
			wantCompChanges := []CompartmentChange{
				{CompartmentID: "C1", WeightBefore: 60, WeightAfter: 100},
				{CompartmentID: "C2", WeightBefore: 80, WeightAfter: 60},
				{CompartmentID: "C3", WeightBefore: 100, WeightAfter: 80},
			}
			if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
				t.Fatalf("第 %d 种次序：舱位重量变化错误: got=%+v want=%+v",
					i, res.CompartmentChanges, wantCompChanges)
			}

			// 最终配载：C1=G3（100/0）、C2=G1（60/40）、C3=G2（80/20）。
			wantFinal := []struct {
				id              string
				used, remaining int64
				cargo           []string
			}{
				{"C1", 100, 0, []string{"G3"}},
				{"C2", 60, 40, []string{"G1"}},
				{"C3", 80, 20, []string{"G2"}},
			}
			for _, wc := range wantFinal {
				cv, _ := r.Compartment(wc.id)
				if cv.MaxWeight != 100 || cv.UsedWeight != wc.used ||
					cv.RemainingWeight != wc.remaining ||
					!reflect.DeepEqual(cargoIDs(*cv), wc.cargo) {
					t.Fatalf("第 %d 种次序：%s 最终配载错误: %+v", i, wc.id, cv)
				}
			}

			// 货物查询与舱位清单一致：每件只出现一次、只在一个舱。
			wantAt := map[string]string{"G1": "C2", "G2": "C3", "G3": "C1"}
			counts := make(map[string]int)
			for _, id := range []string{"C1", "C2", "C3"} {
				cv, _ := r.Compartment(id)
				if len(cv.Cargo) != 1 {
					t.Fatalf("第 %d 种次序：%s 最终应只装一件货物: %+v", i, id, cv.Cargo)
				}
				counts[cv.Cargo[0].ID]++
			}
			for id, wantComp := range wantAt {
				cv, _ := r.Cargo(id)
				if !cv.Loaded || cv.CompartmentID != wantComp {
					t.Fatalf("第 %d 种次序：货物 %s 应在 %s，实际 %q（loaded=%v）",
						i, id, wantComp, cv.CompartmentID, cv.Loaded)
				}
				if counts[id] != 1 {
					t.Fatalf("第 %d 种次序：货物 %s 应在舱位清单中恰好出现一次，实际 %d 次",
						i, id, counts[id])
				}
			}
			assertCycleCargoProfilesUnchanged(t, r)
		})
	}
}

// 保持同样的初始货物与换舱安排，只把 C1 承重设为 90：初始装载仍合法
// （G1 仅 60），但循环后 C1 将装 G3（100），超重 10 千克。预览应判为
// 不可提交，仍给出完整预计配载与货物变化，拒绝原因指出 C1 超重 10 千克、
// 预计已用 100、预计剩余 -10，且不夹带混装原因；预览不落地，三件货物仍在
// 原舱。三条操作的全部先后次序结论一致。
func TestPreviewCrossDestinationCycleOverweightRejected(t *testing.T) {
	for i, ops := range cycleMovePermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCrossDestinationCycleRegistry(t, 90)
			compBefore := snapshotCompartments(r, "C1", "C2", "C3")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			res, err := r.Preview(ops)
			if err != nil {
				t.Fatalf("第 %d 种次序：预览不应出错: %v", i, err)
			}
			if res.Submittable || len(res.Rejections) != 1 {
				t.Fatalf("第 %d 种次序：应不可提交且恰有一条拒绝原因: %+v", i, res)
			}
			rej := res.Rejections[0]
			if rej.Kind != ErrOverweight || rej.CompartmentID != "C1" ||
				rej.MaxWeight != 90 || rej.UsedWeight != 100 ||
				rej.RemainingWeight != -10 || rej.Overweight != 10 {
				t.Fatalf("第 %d 种次序：C1 超重原因内容错误: %+v", i, rej)
			}
			if len(rej.Destinations) != 0 || len(rej.OffendingCargo) != 0 {
				t.Fatalf("第 %d 种次序：超重原因不应携带混装信息: %+v", i, rej)
			}

			// 即使不可提交，完整预计配载与货物变化仍照常返回。
			if !reflect.DeepEqual(res.CargoChanges, cycleExpectedCargoChanges()) {
				t.Fatalf("第 %d 种次序：超重时仍应给出货物变化: got=%+v", i, res.CargoChanges)
			}
			assertCyclePreviewCompartments(t, res, 90)

			// 预览只读：三舱实际清单、已用与剩余重量及货物归属保持调整前状态。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertCycleActualStowage(t, r, 90)
		})
	}
}

// C1 承重为 90 时正式提交必须按 C1 超重整次拒绝：结构化错误为
// ErrOverweight、指向 C1、携带调整编号，说明给出预计总重 100 千克与
// 承重 90 千克，不返回成功结果。整批原子：三条操作的任何先后次序下，
// 三件货物的位置、三舱实际清单、已用与剩余重量都完全保留调整前状态，
// 不能先完成部分换舱。失败不占用编号：以同一编号重提仍得到超重拒绝
// 而非编号冲突。
func TestAdjustCrossDestinationCycleOverweightRejectedAtomically(t *testing.T) {
	for i, ops := range cycleMovePermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCrossDestinationCycleRegistry(t, 90)
			compBefore := snapshotCompartments(r, "C1", "C2", "C3")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3")

			se := submitExpectReject(t, r, "cycle-over", ops)
			if se.Kind != ErrOverweight {
				t.Fatalf("第 %d 种次序：应按超重拒绝，实际 Kind=%s", i, se.Kind)
			}
			if se.ID != "C1" {
				t.Fatalf("第 %d 种次序：超重原因应指向舱位 C1，实际 ID=%q", i, se.ID)
			}
			if se.AdjustmentID != "cycle-over" {
				t.Fatalf("第 %d 种次序：错误应携带调整编号 cycle-over，实际 %q",
					i, se.AdjustmentID)
			}
			msg := se.Error()
			if !strings.Contains(msg, "C1") ||
				!strings.Contains(msg, "100") || !strings.Contains(msg, "90") {
				t.Fatalf("第 %d 种次序：超重说明应指出 C1、预计总重 100 与承重 90: %s",
					i, msg)
			}

			// 整次调整不生效：三件货物仍在原舱，三舱重量不变。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertCycleActualStowage(t, r, 90)

			// 失败的调整不占用编号：以同一编号重提仍是超重拒绝而不是编号冲突，
			// 状态继续保持原样，证明任何一种操作次序都没有先完成部分换舱。
			again := submitExpectReject(t, r, "cycle-over", ops)
			if again.Kind != ErrOverweight || again.ID != "C1" ||
				again.AdjustmentID != "cycle-over" {
				t.Fatalf("第 %d 种次序：同编号重提应仍为超重拒绝: %+v", i, again)
			}
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)
			assertCycleActualStowage(t, r, 90)
		})
	}
}
