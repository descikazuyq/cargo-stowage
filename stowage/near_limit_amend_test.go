package stowage

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// 本文件为“接近 int64 重量上限时更正已装载货物重量”补充自动化回归保障，
// 重点保护既有规则：AmendCargo 的新重量是对该货物现有重量的替换（所属
// 舱位先扣旧重量、再计新重量），货物编号与所属舱位保持不变，不能把新
// 重量当成另一件货物追加；即使旧重量与新重量（或旧已用重量与新重量）
// 相加放不进 int64，只要更正后的舱位总重仍能表示且没有超过承重，就
// 必须成功，不得因这种相加溢出而拒绝一个最终合法的更正。
//
// 沿用现有 AmendCargo 入口、重量错误分类（超重 / 重量溢出）与查询行为；
// 这些更正只改重量，目的地与混装许可保持原值，使结果只取决于重量规则。
//
// 记号：M = math.MaxInt64 = 9223372036854775807，重量单位千克。
// 初始 C1 承重由参数给定，装有重货 G1（M-20）与 G2（10），已用 M-10；
// 两件同目的地 X 且都允许混装。

// newNearLimitAmendRegistry 构造更正前配载：C1 承重 maxWeight，
// G1 重 M-20、G2 重 10 均已装入 C1（已用 M-10），两件同目的地 X 且
// 都允许混装（M-10 在两种被测承重 M、M-5 下都合法）。
func newNearLimitAmendRegistry(t *testing.T, maxWeight int64) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", maxWeight)
	mustRegisterCargo(t, r, "G1", math.MaxInt64-20, "X", true)
	mustRegisterCargo(t, r, "G2", 10, "X", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	return r
}

// assertNearLimitAmendLayout 全面核对一次重量更正后的配载一致性：
//   - 更正只是替换：G1 编号不变、仍在原舱位 C1、重量为 heavyWeight；
//     G2 始终留舱且仍重 10；登记处始终只有 G1、G2 两条记录（不追加货物）。
//   - 目的地与混装许可在这些只改重量的更正中保持原值（X / true）。
//   - C1 清单恰为字典序的 G1、G2 两条，承重 maxWeight、已用 heavyWeight+10、
//     剩余 maxWeight-(heavyWeight+10)。
//   - 货物查询与舱位清单中每件货物的重量、位置、资料逐件一致。
func assertNearLimitAmendLayout(t *testing.T, r *Registry, maxWeight, heavyWeight int64) {
	t.Helper()
	g1, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	g2, err := r.Cargo("G2")
	if err != nil {
		t.Fatal(err)
	}
	// 货物编号不变、仍在原舱位。
	if g1.ID != "G1" || !g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("G1 更正后编号不应变且应留在 C1: %+v", g1)
	}
	if g2.ID != "G2" || !g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("G2 应始终装载于 C1: %+v", g2)
	}
	// 重量是替换旧重量，不是累加；另一件货物重量不受影响。
	if g1.Weight != heavyWeight {
		t.Fatalf("G1 重量应被替换为 %d: %+v", heavyWeight, g1)
	}
	if g2.Weight != 10 {
		t.Fatalf("另一件货物 G2 应仍重 10: %+v", g2)
	}
	// 目的地与混装许可保持原值。
	if g1.Destination != "X" || !g1.AllowMixed {
		t.Fatalf("G1 目的地与混装许可应保持原值 X/true: %+v", g1)
	}
	if g2.Destination != "X" || !g2.AllowMixed {
		t.Fatalf("G2 的资料不应受更正影响: %+v", g2)
	}

	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	wantUsed := heavyWeight + 10
	if cpt.MaxWeight != maxWeight ||
		cpt.UsedWeight != wantUsed ||
		cpt.RemainingWeight != maxWeight-wantUsed {
		t.Fatalf("C1 应为 承重%d/已用%d/剩余%d: %+v",
			maxWeight, wantUsed, maxWeight-wantUsed, cpt)
	}
	// 舱位清单只保留原来的两条记录，更正不能追加或删除货物。
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("C1 货物清单应只保留原来的 G1、G2 两条: %v", got)
	}
	// 货物查询与舱位清单中的重量、位置、资料逐件一致。
	listed := make(map[string]CargoView, len(cpt.Cargo))
	for _, cv := range cpt.Cargo {
		listed[cv.ID] = cv
	}
	for _, q := range []CargoView{*g1, *g2} {
		cv, ok := listed[q.ID]
		if !ok {
			t.Fatalf("舱位清单缺少货物查询中的 %s", q.ID)
		}
		if cv.Weight != q.Weight ||
			cv.Loaded != q.Loaded ||
			cv.CompartmentID != q.CompartmentID ||
			cv.Destination != q.Destination ||
			cv.AllowMixed != q.AllowMixed {
			t.Fatalf("货物 %s 在货物查询与舱位清单中不一致: 查询=%+v 清单=%+v",
				q.ID, q, cv)
		}
	}

	// 登记处层面也不能多出记录：新重量不是另一件货物。
	if len(r.cargos) != 2 {
		t.Fatalf("更正不应追加货物记录，当前货物数=%d", len(r.cargos))
	}
}

// assertNearLimitAmendInitial 核对失败更正后配载仍为初始状态：
// G1 重 M-20、G2 重 10，C1 已用 M-10、剩余 maxWeight-(M-10)。
func assertNearLimitAmendInitial(t *testing.T, r *Registry, maxWeight int64) {
	t.Helper()
	assertNearLimitAmendLayout(t, r, maxWeight, math.MaxInt64-20)
}

// 成功路径：C1 承重 M，连续两次只改重量的更正。
//
// 第一次把 G1 由 M-20 更正为 M-10：舱位合计 (M-10)+10 = M，恰好等于
// 承重 M，应成功且剩余 0。旧重量 M-20 与新重量 M-10 相加（以及旧已用
// M-10 与新重量 M-10 相加）都会超过 int64；正确语义是“扣旧计新”的
// 替换，最终合计 M 可表示且不超承重，故不得拒绝。
//
// 第二次把 G1 由 M-10 更正为 M-30：合计 (M-30)+10 = M-20，剩余 20。
// 两次更正后清单都只能各保留原来的 G1、G2 两条记录。
func TestNearLimitAmendToExactCapacityThenReduce(t *testing.T) {
	r := newNearLimitAmendRegistry(t, math.MaxInt64)

	// 第一次更正：恰好占满。
	if err := r.AmendCargo("G1", math.MaxInt64-10, "X", true); err != nil {
		t.Fatalf("更正后合计 M 恰好达承重应成功（不得因旧+新溢出而拒绝）: %v", err)
	}
	assertNearLimitAmendLayout(t, r, math.MaxInt64, math.MaxInt64-10)
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64 || cpt.RemainingWeight != 0 {
		t.Fatalf("第一次更正后应恰好占满、剩余 0: %+v", cpt)
	}

	// 第二次更正：减重，已用 M-20、剩余 20。
	if err := r.AmendCargo("G1", math.MaxInt64-30, "X", true); err != nil {
		t.Fatalf("减重更正应成功: %v", err)
	}
	assertNearLimitAmendLayout(t, r, math.MaxInt64, math.MaxInt64-30)
	cpt, _ = r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64-20 || cpt.RemainingWeight != 20 {
		t.Fatalf("第二次更正后应 已用M-20/剩余20: %+v", cpt)
	}
}

// 拒绝路径一（可表示但超重）：C1 承重 M-5，把 G1 由 M-20 更正为 M-14，
// 预计合计 (M-14)+10 = M-4 仍能用 int64 表示，却超过承重 M-5 恰好
// 1 千克。必须沿用现有“超重”错误，指出舱位 C1、预计总重 M-4 与最大
// 承重 M-5，不能误判成重量溢出。失败不改动旧重量、舱位与原有占用。
func TestNearLimitAmendRepresentableOverweightRejected(t *testing.T) {
	const maxWeight = math.MaxInt64 - 5
	r := newNearLimitAmendRegistry(t, maxWeight)
	compBefore := snapshotCompartments(r, "C1")
	cargoBefore := snapshotCargo(r, "G1", "G2")

	se := requireError(t, r.AmendCargo("G1", math.MaxInt64-14, "X", true), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重应指出装不下的舱位 C1，实际 ID=%q", se.ID)
	}
	msg := se.Error()
	// 说明须含舱位 C1、预计总重 M-4 与承重 M-5（十进制），证明这是按
	// “最终超重（可表示）”而不是溢出或回绕数值拒绝。
	if !strings.Contains(msg, "C1") ||
		!strings.Contains(msg, "9223372036854775803") || // 预计总重 M-4
		!strings.Contains(msg, "9223372036854775802") { // 最大承重 M-5
		t.Fatalf("超重说明应指出舱位、预计总重 M-4 与最大承重 M-5: %s", msg)
	}

	// 失败原子性：G1 旧重量 M-20、两件货物舱位与原有占用全部保留。
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
	assertNearLimitAmendInitial(t, r, maxWeight)
}

// 拒绝路径二（不可表示的溢出）：C1 承重 M，把 G1 由 M-20 更正为 M-9，
// 预计合计 (M-9)+10 = M+1 超过 int64 上限。必须沿用现有“重量溢出”
// 错误并指出舱位 C1，说明中不得展示回绕成负数的合计；不能误判成超重。
// 失败不改动旧重量、舱位与原有占用。
func TestNearLimitAmendOverflowRejected(t *testing.T) {
	r := newNearLimitAmendRegistry(t, math.MaxInt64)
	compBefore := snapshotCompartments(r, "C1")
	cargoBefore := snapshotCargo(r, "G1", "G2")

	se := requireError(t, r.AmendCargo("G1", math.MaxInt64-9, "X", true), ErrOverflow)
	if se.ID != "C1" {
		t.Fatalf("重量溢出应指出所属舱位 C1，实际 ID=%q", se.ID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") {
		t.Fatalf("溢出说明应指出舱位 C1: %s", msg)
	}
	// 无法表示的合计（回绕为 math.MinInt64）不得出现在说明中。
	if strings.Contains(msg, "-") {
		t.Fatalf("溢出说明不得展示回绕成负数的合计重量: %s", msg)
	}

	// 失败原子性：仍是初始配载——G1 重 M-20、G2 重 10，C1 已用 M-10、
	// 剩余 10，两件货物都仍在 C1。
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
	assertNearLimitAmendInitial(t, r, math.MaxInt64)
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64-10 || cpt.RemainingWeight != 10 {
		t.Fatalf("溢出被拒后 C1 应仍为 已用M-10/剩余10: %+v", cpt)
	}
}
