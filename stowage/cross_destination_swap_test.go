package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为正式提交（Adjust）补充“不同目的地货物整舱互换”的回归保障。
//
// 已有满载互换的覆盖使用相同目的地；这里覆盖两个舱位分别装着不同目的地、
// 且货物全部不允许混装的情况。合法性按整次调整全部完成后的最终配载判断：
// 交换过程中若逐条移动会出现暂时超重（目的地货物尚未全部移出）与暂时的
// 不同目的地共舱，这些中间状态都不得导致拒绝；整体交换完成后两个舱位仍
// 各自只含同一目的地货物、总重量不变，必须成功。
//
// 同一场景下只互换一件货物时，最终两舱都同时含上海与北京货物，总重量并
// 未超限，但全部货物不允许混装，整次调整必须按 C1 的混装冲突拒绝，且四件
// 货物都留在原舱，不能先完成其中一次移动。

// 初始配载：C1、C2 承重均为 100 千克；
// C1 装去上海的 G1（40）、G2（60），C2 装去北京的 G3（40）、G4（60），
// 四件货物均不允许混装。两舱都恰好满载。
func newCrossDestinationSwapRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "C2", 100)
	mustRegisterCargo(t, r, "G1", 40, "上海", false)
	mustRegisterCargo(t, r, "G2", 60, "上海", false)
	mustRegisterCargo(t, r, "G3", 40, "北京", false)
	mustRegisterCargo(t, r, "G4", 60, "北京", false)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
	})
	return r
}

// swapOpPermutations 返回整舱互换四条移动操作的全部 24 种先后次序，
// 用于保护“操作在清单中的先后次序不影响能否成功、最终配载与变化记录”。
func swapOpPermutations() [][]Op {
	base := []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpMove, CargoID: "G2", Target: "C2"},
		{Kind: OpMove, CargoID: "G3", Target: "C1"},
		{Kind: OpMove, CargoID: "G4", Target: "C1"},
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

// 不同目的地、全部不允许混装的两舱整舱互换应成功。交换若按逐条移动执行，
// 中间会出现暂时超重（如先把 G1、G2 移入仍装着 G3、G4 的 C2：200 > 100）
// 与暂时的不同目的地共舱；这些中间状态都不能导致拒绝。该保障对四条移动
// 的全部先后次序成立。
func TestAdjustCrossDestinationFullCabinSwapSucceeds(t *testing.T) {
	for i, ops := range swapOpPermutations() {
		t.Run("次序", func(t *testing.T) {
			r := newCrossDestinationSwapRegistry(t)

			res, err := r.Adjust("swap-full", ops)
			if err != nil {
				t.Fatalf("第 %d 种次序：整舱互换按最终配载应合法，却被拒绝: %v", i, err)
			}
			if res == nil || res.ID != "swap-full" {
				t.Fatalf("第 %d 种次序：成功结果异常: %+v", i, res)
			}

			// 货物变化保留四件货物各自的原舱位与新舱位，按货物编号排列。
			wantCargoChanges := []CargoChange{
				{CargoID: "G1", From: "C1", To: "C2"},
				{CargoID: "G2", From: "C1", To: "C2"},
				{CargoID: "G3", From: "C2", To: "C1"},
				{CargoID: "G4", From: "C2", To: "C1"},
			}
			if !reflect.DeepEqual(res.CargoChanges, wantCargoChanges) {
				t.Fatalf("第 %d 种次序：货物变化错误: got=%+v want=%+v",
					i, res.CargoChanges, wantCargoChanges)
			}

			// 两个舱位都应有 100 -> 100 的重量变化记录，按舱位编号排列；
			// 不能因前后重量相等就省略。
			wantCompChanges := []CompartmentChange{
				{CompartmentID: "C1", WeightBefore: 100, WeightAfter: 100},
				{CompartmentID: "C2", WeightBefore: 100, WeightAfter: 100},
			}
			if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
				t.Fatalf("第 %d 种次序：舱位重量变化错误: got=%+v want=%+v",
					i, res.CompartmentChanges, wantCompChanges)
			}

			// 最终配载：C1 只含 G3、G4（北京），C2 只含 G1、G2（上海），
			// 两舱已用 100、剩余 0。
			c1, _ := r.Compartment("C1")
			if c1.UsedWeight != 100 || c1.RemainingWeight != 0 ||
				!reflect.DeepEqual(cargoIDs(*c1), []string{"G3", "G4"}) {
				t.Fatalf("第 %d 种次序：C1 最终配载错误: %+v", i, c1)
			}
			c2, _ := r.Compartment("C2")
			if c2.UsedWeight != 100 || c2.RemainingWeight != 0 ||
				!reflect.DeepEqual(cargoIDs(*c2), []string{"G1", "G2"}) {
				t.Fatalf("第 %d 种次序：C2 最终配载错误: %+v", i, c2)
			}

			// 每件货物的归属与舱内清单一致；货物不能遗漏，也不能同时留在
			// 原舱和目标舱。
			wantAt := map[string]string{
				"G1": "C2", "G2": "C2", "G3": "C1", "G4": "C1",
			}
			for id, wantComp := range wantAt {
				cv, _ := r.Cargo(id)
				if !cv.Loaded || cv.CompartmentID != wantComp {
					t.Fatalf("第 %d 种次序：货物 %s 应在 %s，实际 %q（loaded=%v）",
						i, id, wantComp, cv.CompartmentID, cv.Loaded)
				}
			}

			// 重量、目的地和混装许可保持原值。
			assertSwapCargoProfilesUnchanged(t, r)

			// 两舱清单与货物归属严格一一对应：每个舱位恰好两件，四件货物
			// 各出现一次。
			if len(c1.Cargo) != 2 || len(c2.Cargo) != 2 {
				t.Fatalf("第 %d 种次序：交换后不应遗漏或重复货物: C1=%d 件 C2=%d 件",
					i, len(c1.Cargo), len(c2.Cargo))
			}
		})
	}
}

// assertSwapCargoProfilesUnchanged 核对四件货物的重量、目的地与混装许可
// 始终保持登记原值，仅归属随调整改变。
func assertSwapCargoProfilesUnchanged(t *testing.T, r *Registry) {
	t.Helper()
	want := map[string]struct {
		weight      int64
		destination string
	}{
		"G1": {40, "上海"},
		"G2": {60, "上海"},
		"G3": {40, "北京"},
		"G4": {60, "北京"},
	}
	for id, w := range want {
		cv, _ := r.Cargo(id)
		if cv.Weight != w.weight || cv.Destination != w.destination || cv.AllowMixed {
			t.Fatalf("货物 %s 的登记资料被改变: %+v", id, cv)
		}
	}
}

// 同一场景下只交换 G1 与 G3：最终 C1 含 G2（上海）、G3（北京），
// C2 含 G1（上海）、G4（北京），两舱总重量仍是 100，没有超重，但全部
// 货物不允许混装，整次调整必须失败，不返回成功结果。结构化错误报告
// 字典序最靠前的违规舱位 C1 的混装冲突，并指出其中编号最靠前的不允许
// 混装货物 G2，说明中能看出涉及 C1。
func TestAdjustCrossDestinationPartialSwapMixedConflictRejected(t *testing.T) {
	// 两种先后次序都覆盖到：不能先完成其中一次移动再拒绝另一次。
	for _, tc := range []struct {
		name string
		ops  []Op
	}{
		{name: "G1先行", ops: []Op{
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
			{Kind: OpMove, CargoID: "G3", Target: "C1"},
		}},
		{name: "G3先行", ops: []Op{
			{Kind: OpMove, CargoID: "G3", Target: "C1"},
			{Kind: OpMove, CargoID: "G1", Target: "C2"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newCrossDestinationSwapRegistry(t)

			compBefore := snapshotCompartments(r, "C1", "C2")
			cargoBefore := snapshotCargo(r, "G1", "G2", "G3", "G4")

			se := submitExpectReject(t, r, "swap-partial", tc.ops)
			if se.Kind != ErrMixedLoading {
				t.Fatalf("应按混装冲突拒绝，实际 Kind=%s", se.Kind)
			}
			// C1 最终含 G2（上海，禁混）与 G3（北京，禁混），违规货物为
			// {G2, G3}，编号最靠前的是 G2（而不是移入的 G3）。
			if se.ID != "G2" {
				t.Fatalf("应指出 C1 内编号最靠前的不允许混装货物 G2，实际 ID=%q", se.ID)
			}
			if !strings.Contains(se.Error(), "C1") {
				t.Fatalf("混装说明应指出冲突舱位 C1: %s", se.Error())
			}
			if se.AdjustmentID != "swap-partial" {
				t.Fatalf("错误应携带调整编号 swap-partial，实际 %q", se.AdjustmentID)
			}

			// 整次调整不生效：四件货物仍在各自原舱，不能先完成其中一次移动。
			assertCompartmentsUnchanged(t, r, compBefore)
			assertCargoUnchanged(t, r, cargoBefore)

			// 再明确核对两个舱位的完整清单、已用与剩余重量保持原样。
			c1, _ := r.Compartment("C1")
			if c1.UsedWeight != 100 || c1.RemainingWeight != 0 ||
				!reflect.DeepEqual(cargoIDs(*c1), []string{"G1", "G2"}) {
				t.Fatalf("拒绝后 C1 应仍为 G1、G2 且满载: %+v", c1)
			}
			c2, _ := r.Compartment("C2")
			if c2.UsedWeight != 100 || c2.RemainingWeight != 0 ||
				!reflect.DeepEqual(cargoIDs(*c2), []string{"G3", "G4"}) {
				t.Fatalf("拒绝后 C2 应仍为 G3、G4 且满载: %+v", c2)
			}
			assertSwapCargoProfilesUnchanged(t, r)
		})
	}
}
