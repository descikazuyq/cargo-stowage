package stowage

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// 本文件保护已装载货物重量更正在接近 int64 上限时的回归：
// 更正是同一件货物的重量“替换”（先扣旧重量再计新重量），既不能把新
// 重量当成另一件货物追加，也不能因为旧重量与新重量本身相加放不进
// int64，就误拒更正后总重能够表示且没有超出承重的安排。
//
// 以下场景中两件货物目的地始终相同、混装许可始终保持原值，使每次更正
// 的成败只取决于重量规则。M 为 int64 可表示的最大正整数。

// nearLimitSetup 建立“重货 M-heavyGap + 轻货 10、同舱同目的地”的初始
// 配载，初始已用重量恒为 M-10。maxWeight 给出舱位承重。
func nearLimitSetup(t *testing.T, maxWeight int64) *Registry {
	t.Helper()
	const M = math.MaxInt64
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", maxWeight); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", M-20, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatalf("初始配载应合法: %v", err)
	}
	return r
}

// assertInitialStowage 校验失败更正后配载仍是初始状态：
// G1 重 M-20、G2 重 10，两件都在 C1，舱位已用 M-10，货物清单与编号数不变。
func assertInitialStowage(t *testing.T, r *Registry, maxWeight int64) {
	t.Helper()
	const M = math.MaxInt64
	g1, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if !g1.Loaded || g1.CompartmentID != "C1" || g1.Weight != M-20 {
		t.Fatalf("失败更正后重货旧重量与舱位应保留: %+v", g1)
	}
	g2, err := r.Cargo("G2")
	if err != nil {
		t.Fatal(err)
	}
	if !g2.Loaded || g2.CompartmentID != "C1" || g2.Weight != 10 {
		t.Fatalf("失败更正后轻货不应受影响: %+v", g2)
	}
	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if cpt.UsedWeight != M-10 || cpt.RemainingWeight != maxWeight-(M-10) {
		t.Fatalf("失败更正后舱位占用应保留初始值: %+v", cpt)
	}
	// 货物查询与舱位清单的重量、位置应一致，且每件只保留一条记录。
	assertCargoConsistent(t, r, cpt, "G1", M-20)
	assertCargoConsistent(t, r, cpt, "G2", 10)
	if len(cpt.Cargo) != 2 {
		t.Fatalf("舱位清单应仍是原来的两件货物，实际 %d 件: %+v", len(cpt.Cargo), cpt.Cargo)
	}
	if len(r.cargos) != 2 {
		t.Fatalf("失败更正不得新增货物记录，当前货物数=%d", len(r.cargos))
	}
}

// TestAmendNearInt64LimitReplaceSucceeds 接近上限时，旧重量与新重量相加
// 本身会溢出 int64，但替换后的舱位总重能够表示且不超过承重，更正必须
// 成功：先把重货 M-20 更正为 M-10（合计恰好 M，剩余 0），再更正为
// M-30（合计 M-20，剩余 20）。两次更正都不得追加货物，编号、舱位与
// 另一件货物保持不变，货物查询与舱位清单的重量及位置必须一致。
func TestAmendNearInt64LimitReplaceSucceeds(t *testing.T) {
	const M = math.MaxInt64
	r := nearLimitSetup(t, M)

	// 第一次更正：M-20 -> M-10。注意旧重量 M-20 与新重量 M-10 直接相加
	// 会溢出 int64，但更正是替换而非累加，最终合计 (M-10)+10 = M 合法。
	if err := r.AmendCargo("G1", M-10, "X", true); err != nil {
		t.Fatalf("更正后合计恰好 M 应成功，误报: %v", err)
	}
	g1, _ := r.Cargo("G1")
	if !g1.Loaded || g1.CompartmentID != "C1" || g1.Weight != M-10 {
		t.Fatalf("重货应在原舱位替换为新重量: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 10 || g2.CompartmentID != "C1" {
		t.Fatalf("另一件货物应仍重 10 且舱位不变: %+v", g2)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != M || cpt.RemainingWeight != 0 {
		t.Fatalf("舱位应恰好占满: %+v", cpt)
	}
	assertCargoConsistent(t, r, cpt, "G1", M-10)
	assertCargoConsistent(t, r, cpt, "G2", 10)
	if len(cpt.Cargo) != 2 || len(r.cargos) != 2 {
		t.Fatalf("更正只能替换记录，不得追加货物: 清单 %d 件、登记 %d 件",
			len(cpt.Cargo), len(r.cargos))
	}

	// 第二次更正：M-10 -> M-30，最终合计 (M-30)+10 = M-20，剩余 20。
	if err := r.AmendCargo("G1", M-30, "X", true); err != nil {
		t.Fatalf("减重更正应成功，误报: %v", err)
	}
	g1, _ = r.Cargo("G1")
	if g1.Weight != M-30 || g1.CompartmentID != "C1" {
		t.Fatalf("重货应在原舱位更新为 M-30: %+v", g1)
	}
	g2, _ = r.Cargo("G2")
	if g2.Weight != 10 || g2.CompartmentID != "C1" {
		t.Fatalf("另一件货物仍应重 10: %+v", g2)
	}
	cpt, _ = r.Compartment("C1")
	if cpt.UsedWeight != M-20 || cpt.RemainingWeight != 20 {
		t.Fatalf("舱位应为已用 M-20、剩余 20: %+v", cpt)
	}
	assertCargoConsistent(t, r, cpt, "G1", M-30)
	assertCargoConsistent(t, r, cpt, "G2", 10)
	if len(cpt.Cargo) != 2 || len(r.cargos) != 2 {
		t.Fatalf("第二次更正仍只能替换记录: 清单 %d 件、登记 %d 件",
			len(cpt.Cargo), len(r.cargos))
	}
}

// assertCargoConsistent 双路比对：货物查询与舱位清单中同一件货物的重量、
// 位置一致，且清单中只出现一条记录。
func assertCargoConsistent(t *testing.T, r *Registry, cpt *CompartmentView, id string, weight int64) {
	t.Helper()
	cv, err := r.Cargo(id)
	if err != nil {
		t.Fatal(err)
	}
	if cv.Weight != weight {
		t.Fatalf("货物查询中 %s 重量应为 %d，实际 %d", id, weight, cv.Weight)
	}
	matches := 0
	for _, listed := range cpt.Cargo {
		if listed.ID != id {
			continue
		}
		matches++
		if listed.Weight != cv.Weight {
			t.Fatalf("%s 清单重量 %d 与货物查询 %d 不一致", id, listed.Weight, cv.Weight)
		}
		if listed.CompartmentID != cv.CompartmentID || listed.Loaded != cv.Loaded {
			t.Fatalf("%s 清单位置 %+v 与货物查询 %+v 不一致", id, listed, cv)
		}
	}
	if matches != 1 {
		t.Fatalf("%s 在舱位清单中应只有一条记录，实际 %d 条", id, matches)
	}
}

// TestAmendNearInt64LimitOverweightNotOverflow 合计仍可用 int64 表示、
// 只超过承重 1 千克时，必须归类为现有超重错误而非溢出错误：
// 承重 M-5，重货 M-20 更正为 M-14，预计总重 M-4，超出承重 1。
// 错误要指出舱位、预计总重与最大承重，且不得改动任何旧配载。
func TestAmendNearInt64LimitOverweightNotOverflow(t *testing.T) {
	const M = math.MaxInt64
	r := nearLimitSetup(t, M-5)

	se := requireError(t, r.AmendCargo("G1", M-14, "X", true), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重错误应指出舱位，实际 ID=%q", se.ID)
	}
	msg := se.Error()
	for _, want := range []string{"C1", strconv.FormatInt(M-4, 10), strconv.FormatInt(M-5, 10)} {
		if !strings.Contains(msg, want) {
			t.Fatalf("超重说明应包含舱位、预计总重 M-4 与最大承重 M-5: %s", msg)
		}
	}
	// 初始配载：已用 M-10、剩余 (M-5)-(M-10) = 5。
	assertInitialStowage(t, r, M-5)
}

// TestAmendNearInt64LimitOverflowRejected 预计总重超过 int64 上限时，
// 必须归类为现有重量溢出错误：承重 M，重货 M-20 更正为 M-9，
// 预计合计 (M-9)+10 = M+1 无法表示。错误指出舱位即可，说明中不得出现
// 回绕成负数的合计；失败后旧重量、舱位与占用全部保持初始配载。
func TestAmendNearInt64LimitOverflowRejected(t *testing.T) {
	const M = math.MaxInt64
	r := nearLimitSetup(t, M)

	se := requireError(t, r.AmendCargo("G1", M-9, "X", true), ErrOverflow)
	if se.ID != "C1" {
		t.Fatalf("溢出错误应指出舱位，实际 ID=%q", se.ID)
	}
	msg := se.Error()
	if strings.Contains(msg, "-") {
		t.Fatalf("溢出说明不得展示回绕成负数的合计: %s", msg)
	}
	if strings.Contains(msg, strconv.FormatInt(math.MinInt64, 10)) {
		t.Fatalf("溢出说明不得包含回绕后的合计值: %s", msg)
	}
	// 初始配载：已用 M-10、剩余 10。
	assertInitialStowage(t, r, M)
}
