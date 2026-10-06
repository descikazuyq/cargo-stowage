package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为正式提交（Adjust）的重复提交识别补充回归保障：货物、舱位与
// 调整编号只去掉首尾空白，编号内部的冒号、竖线、空格都是编号的一部分。
// 围绕正式提交的可观察结果保护两件事：
//
//  1. 两份编号“连在一起读”相同、实则货物与目标各不相同的安排，复用同一
//     调整编号时必须收到“调整编号冲突”，不返回成功结果、不改变任何配载；
//  2. 同一批含标点编号的安排，仅改变首尾空白或操作顺序时必须取回首次
//     成功的完整变化记录且不再装卸，返回的编号保留内部字符。
//
// 同时沿用既有业务行为：普通编号照常可用、卸下目标不参与内容比较、
// 变化记录按编号字典序返回、承重与混装规则不变。

// ---------- 连读后易混的两份安排必须判为不同内容 ----------

// 货物 “G|1” 装入 “C1” 与货物 “G” 装入 “1|C1” 是两份不同安排：把货物
// 与目标连在一起读都是 G|1C1，但四个对象互不相同且均已合法登记。
// 第一份成功后，第二份复用同一调整编号必须收到调整编号冲突，且第一件
// 货物仍在原舱、第二件仍未装载，两舱清单与重量保持冲突提交前的状态。
func TestAdjustPunctuatedIDConflictAcrossLookalikePlans(t *testing.T) {
	r := NewRegistry()
	// 两个舱位、两件货物均合法登记，重量与目的地不会导致拒绝。
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "1|C1", 1000)
	mustRegisterCargo(t, r, "G|1", 10, "X", true)
	mustRegisterCargo(t, r, "G", 10, "X", true)

	// 第一份安排：G|1 装入 C1。
	first := mustAdjust(t, r, "a|1", []Op{
		{Kind: OpLoad, CargoID: "G|1", Target: "C1"},
	})
	wantFirst := &AdjustmentResult{
		ID: "a|1",
		CargoChanges: []CargoChange{
			{CargoID: "G|1", From: "", To: "C1"},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 0, WeightAfter: 10},
		},
	}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("首次结果应原样保留含竖线的编号: %+v", first)
	}

	// 冲突提交前快照。
	compBefore := snapshotCompartments(r, "C1", "1|C1")
	cargoBefore := snapshotCargo(r, "G|1", "G")

	// 第二份安排：G 装入 1|C1。沿用同一调整编号（带首尾空白）提交，
	// 必须被拒绝且不返回成功结果——竖线是编号的一部分，不是分隔符，
	// 不能因为两份安排连读后相同就视为同一次调整。
	se := submitExpectReject(t, r, "  a|1  ", []Op{
		{Kind: OpLoad, CargoID: "G", Target: "1|C1"},
	})
	if se.Kind != ErrAdjustmentIDConflict {
		t.Fatalf("应报告调整编号冲突，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
	// 结构化错误按去首尾空白后的值说明涉及的调整编号；冲突错误不指向
	// 任何货物或舱位。
	if se.AdjustmentID != "a|1" || se.ID != "a|1" {
		t.Fatalf("错误应携带调整编号 a|1，实际 ID=%q AdjustmentID=%q",
			se.ID, se.AdjustmentID)
	}
	if !strings.Contains(se.Error(), "a|1") {
		t.Fatalf("错误说明应包含涉及的调整编号 a|1: %s", se.Error())
	}

	// 两舱清单、已用与剩余重量，以及两件货物的状态，均与提交前一致。
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)

	// 直接回查：第一件仍在原目标舱位，第二件仍未装载。
	g1, _ := r.Cargo("G|1")
	if !g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("G|1 应仍在 C1: %+v", g1)
	}
	g, _ := r.Cargo("G")
	if g.Loaded || g.CompartmentID != "" {
		t.Fatalf("G 应仍未装载: %+v", g)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 10 || c1.RemainingWeight != 990 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"G|1"}) {
		t.Fatalf("C1 应仍只装有 G|1、占用 10 千克: %+v", c1)
	}
	c1alt, _ := r.Compartment("1|C1")
	if c1alt.UsedWeight != 0 || c1alt.RemainingWeight != 1000 || len(c1alt.Cargo) != 0 {
		t.Fatalf("1|C1 应仍为空舱: %+v", c1alt)
	}

	// 第二份安排换一个未使用的调整编号即可正常成功：被拒的是内容识别，
	// 不是对象合法性；第一份的配载不受影响。
	second := mustAdjust(t, r, "a|2", []Op{
		{Kind: OpLoad, CargoID: "G", Target: "1|C1"},
	})
	if !reflect.DeepEqual(second.CargoChanges, []CargoChange{
		{CargoID: "G", From: "", To: "1|C1"},
	}) {
		t.Fatalf("第二份安排应按 G -> 1|C1 生效: %+v", second.CargoChanges)
	}
	g, _ = r.Cargo("G")
	if g.CompartmentID != "1|C1" {
		t.Fatalf("G 应已装入 1|C1: %+v", g)
	}
	g1, _ = r.Cargo("G|1")
	if g1.CompartmentID != "C1" {
		t.Fatalf("G|1 应仍在 C1: %+v", g1)
	}
	// 首次记录仍可按原编号原内容取回。
	again, err := r.Adjust("a|1", []Op{{Kind: OpLoad, CargoID: "G|1", Target: "C1"}})
	if err != nil {
		t.Fatalf("原编号原内容应仍返回首次结果: %v", err)
	}
	if !reflect.DeepEqual(again, wantFirst) {
		t.Fatalf("取回的应是首次记录: %+v", again)
	}
}

// ---------- 首尾空白与排列顺序不影响含标点编号的重复识别 ----------

// 含竖线、冒号、内部空格的同一批安排，仅改变货物、目标舱位与调整编号
// 的首尾空白、改变操作排列顺序，应返回首次成功保存的完整变化记录，
// 不再执行装载；结果编号沿用去首尾空白后的值，内部字符原样保留。
// 之后另有正常调整改变当前配载时，原内容再次提交仍只回放首次记录，
// 当前配载保持不变。
func TestAdjustPunctuatedIDIdempotentUnderWhitespaceAndOrder(t *testing.T) {
	r := NewRegistry()
	// 舱位编号含内部空格与冒号，互不相同；C3 供之后的正常调整使用。
	mustRegisterCompartment(t, r, "C 1", 100)
	mustRegisterCompartment(t, r, "C:2", 100)
	mustRegisterCompartment(t, r, "C3", 100)
	mustRegisterCargo(t, r, "G|1", 10, "X", true)
	mustRegisterCargo(t, r, "G:2", 10, "X", true)

	first := mustAdjust(t, r, "a:1|2", []Op{
		{Kind: OpLoad, CargoID: "G|1", Target: "C 1"},
		{Kind: OpLoad, CargoID: "G:2", Target: "C:2"},
	})
	// 完整首次记录：编号只去首尾空白、内部字符保留；货物与舱位变化均按
	// 编号字典序（字节序）排列——':'(0x3A) < '|'(0x7C)，故 G:2 在
	// G|1 前；' '(0x20) < ':'(0x3A)，故 C 1 在 C:2 前。
	wantFirst := &AdjustmentResult{
		ID: "a:1|2",
		CargoChanges: []CargoChange{
			{CargoID: "G:2", From: "", To: "C:2"},
			{CargoID: "G|1", From: "", To: "C 1"},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C 1", WeightBefore: 0, WeightAfter: 10},
			{CompartmentID: "C:2", WeightBefore: 0, WeightAfter: 10},
		},
	}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("首次结果应保留编号内部字符并按字典序排列: %+v", first)
	}

	// 仅改变各编号的首尾空白并倒置操作顺序：识别为同一批安排，返回首次
	// 保存的完整记录。注意 “ C 1 ” 去空白后必须仍是 “C 1”，不能被
	// 替换成 “C1”。
	again, err := r.Adjust("\t a:1|2 \n", []Op{
		{Kind: OpLoad, CargoID: " G:2 ", Target: "  C:2  "},
		{Kind: OpLoad, CargoID: " G|1 ", Target: " C 1 "},
	})
	if err != nil {
		t.Fatalf("首尾空白与顺序变化不应影响重复识别: %v", err)
	}
	if !reflect.DeepEqual(again, wantFirst) {
		t.Fatalf("应原样返回首次完整变化记录: %+v", again)
	}

	// 没有重新装载：两舱各只占用 10 千克，货物归属不变。
	cs1, _ := r.Compartment("C 1")
	if cs1.UsedWeight != 10 || cs1.RemainingWeight != 90 ||
		!reflect.DeepEqual(cargoIDs(*cs1), []string{"G|1"}) {
		t.Fatalf("重复提交不应再次装载，C 1 应仍只有 G|1: %+v", cs1)
	}
	cs2, _ := r.Compartment("C:2")
	if cs2.UsedWeight != 10 || cs2.RemainingWeight != 90 ||
		!reflect.DeepEqual(cargoIDs(*cs2), []string{"G:2"}) {
		t.Fatalf("重复提交不应再次装载，C:2 应仍只有 G:2: %+v", cs2)
	}

	// 之后一次正常调整改变当前配载：把 G|1 从 C 1 移到 C3。
	mustAdjust(t, r, "later", []Op{
		{Kind: OpMove, CargoID: "G|1", Target: "C3"},
	})
	gp, _ := r.Cargo("G|1")
	if gp.CompartmentID != "C3" {
		t.Fatalf("前置条件：G|1 应已移到 C3: %+v", gp)
	}

	// 原内容再次提交：仍取回首次记录（G|1 从未装载变为 C 1 的历史变化），
	// 但不重新装卸——G|1 必须留在 C3，空出来的 C 1 不应被重新占用。
	replay, err := r.Adjust("a:1|2", []Op{
		{Kind: OpLoad, CargoID: "G:2", Target: "C:2"},
		{Kind: OpLoad, CargoID: "G|1", Target: "C 1"},
	})
	if err != nil {
		t.Fatalf("当前配载改变后原内容重交仍应成功回放: %v", err)
	}
	if !reflect.DeepEqual(replay, wantFirst) {
		t.Fatalf("应继续取回首次记录，而非按当前配载重算: %+v", replay)
	}
	gp, _ = r.Cargo("G|1")
	if gp.CompartmentID != "C3" {
		t.Fatalf("回放不得把 G|1 移回 C 1: %+v", gp)
	}
	gc, _ := r.Cargo("G:2")
	if gc.CompartmentID != "C:2" {
		t.Fatalf("回放不得重复装载 G:2: %+v", gc)
	}
	cs1, _ = r.Compartment("C 1")
	if cs1.UsedWeight != 0 || cs1.RemainingWeight != 100 || len(cs1.Cargo) != 0 {
		t.Fatalf("C 1 应保持空舱: %+v", cs1)
	}
	cs3, _ := r.Compartment("C3")
	if cs3.UsedWeight != 10 || !reflect.DeepEqual(cargoIDs(*cs3), []string{"G|1"}) {
		t.Fatalf("C3 应保留当前配载 G|1: %+v", cs3)
	}
	cs2, _ = r.Compartment("C:2")
	if cs2.UsedWeight != 10 {
		t.Fatalf("C:2 应保持 10 千克: %+v", cs2)
	}
}

// ---------- 冒号与内部空格同属编号内容 ----------

// 冒号同样是编号的一部分；编号内部保留空格的对象必须与删除该空格后的
// 对象区分。这些内容识别问题必须先于货物状态检查报告为调整编号冲突，
// 在承重与混装都合法的前提下不能误报成货物状态不符，也不能误判为同一
// 内容而回放首次记录。
func TestAdjustPunctuatedIDColonAndInternalSpaceAreContent(t *testing.T) {
	r := NewRegistry()
	// 冒号、内部空格都属于编号：三组舱位、三件货物互不相同，全部合法登记。
	for _, id := range []string{"C:1", "C1", "C 1"} {
		mustRegisterCompartment(t, r, id, 1000)
	}
	for _, id := range []string{"G:1", "G1", "G 1"} {
		mustRegisterCargo(t, r, id, 10, "X", true)
	}

	// G:1 装入 C:1，调整编号本身也含冒号。
	mustAdjust(t, r, "a:1", []Op{
		{Kind: OpLoad, CargoID: "G:1", Target: "C:1"},
	})
	// 同一调整编号提交 G1 -> C1：货物与目标都不同（冒号属于编号），
	// 必须报告调整编号冲突；若错误地删除冒号，两份内容会被混为一谈。
	se := submitExpectReject(t, r, "a:1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if se.Kind != ErrAdjustmentIDConflict || se.AdjustmentID != "a:1" {
		t.Fatalf("冒号编号应按内容冲突拒绝，实际 Kind=%s AdjustmentID=%q",
			se.Kind, se.AdjustmentID)
	}

	// G 1（内部空格）装入 C 1（内部空格），调整编号同样含内部空格。
	first := mustAdjust(t, r, "a 2", []Op{
		{Kind: OpLoad, CargoID: "G 1", Target: "C 1"},
	})
	wantFirst := &AdjustmentResult{
		ID: "a 2",
		CargoChanges: []CargoChange{
			{CargoID: "G 1", From: "", To: "C 1"},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C 1", WeightBefore: 0, WeightAfter: 10},
		},
	}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("首次结果应保留编号内部空格: %+v", first)
	}

	// 仅改变各编号的首尾空白：取回首次记录，结果编号是去首尾空白后
	// 仍含内部空格的值，不能被替换成去掉空格的 G1 / C1 / a2。
	repadded, err := r.Adjust("  a 2 \t", []Op{
		{Kind: OpLoad, CargoID: "  G 1  ", Target: "\tC 1\t"},
	})
	if err != nil {
		t.Fatalf("首尾空白变化应识别为原调整: %v", err)
	}
	if !reflect.DeepEqual(repadded, wantFirst) {
		t.Fatalf("应返回含内部空格的首次记录: %+v", repadded)
	}

	// 同一编号改交 G1 -> C 1：内容不同（内部空格属于编号）。若先按
	// 当前状态校验再做内容比较、或在编号识别时删除内部空格，这里会把
	// G1 误认成已装载的 G 1 而误报“状态不符”；正确行为是调整编号冲突。
	se = submitExpectReject(t, r, "a 2", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C 1"},
	})
	if se.Kind != ErrAdjustmentIDConflict {
		t.Fatalf("内部空格差异应报调整编号冲突，而非 %s（%s）", se.Kind, se.Error())
	}
	if se.AdjustmentID != "a 2" {
		t.Fatalf("错误应携带含内部空格的调整编号，实际 %q", se.AdjustmentID)
	}

	// 三个对象始终各自独立：G1 从未被装载，G:1 在 C:1，G 1 在 C 1。
	gPlain, _ := r.Cargo("G1")
	if gPlain.Loaded || gPlain.CompartmentID != "" {
		t.Fatalf("G1 不应因编号混淆被装载: %+v", gPlain)
	}
	gColon, _ := r.Cargo("G:1")
	if gColon.CompartmentID != "C:1" {
		t.Fatalf("G:1 应仍在 C:1: %+v", gColon)
	}
	gSpace, _ := r.Cargo("G 1")
	if gSpace.CompartmentID != "C 1" {
		t.Fatalf("G 1 应仍在 C 1: %+v", gSpace)
	}
	// G1 用新编号装入 C1 是一份独立的合法安排。
	mustAdjust(t, r, "a3", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	cPlain, _ := r.Compartment("C1")
	if !reflect.DeepEqual(cargoIDs(*cPlain), []string{"G1"}) || cPlain.UsedWeight != 10 {
		t.Fatalf("C1 应只装有独立对象 G1: %+v", cPlain)
	}
	cSpace, _ := r.Compartment("C 1")
	if !reflect.DeepEqual(cargoIDs(*cSpace), []string{"G 1"}) {
		t.Fatalf("C 1 应仍只装有 G 1: %+v", cSpace)
	}
}

// ---------- 含标点编号下卸下目标仍被忽略 ----------

// 既有行为保持：卸下操作不参与内容比较的目标值，在编号含竖线、冒号时
// 同样不影响重复识别——填另一个含竖线的已登记舱位或无法解析的值，都只
// 回放首次记录，不报冲突、不再次装卸。
func TestAdjustPunctuatedIDUnloadTargetStillIgnored(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCompartment(t, r, "1|C1", 100)
	mustRegisterCargo(t, r, "G|1", 10, "X", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G|1", Target: "C1"},
	})

	first := mustAdjust(t, r, "u:1", []Op{
		{Kind: OpUnload, CargoID: " G|1 "},
	})
	wantFirst := &AdjustmentResult{
		ID: "u:1",
		CargoChanges: []CargoChange{
			{CargoID: "G|1", From: "C1", To: ""},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 10, WeightAfter: 0},
		},
	}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("首次卸下结果异常: %+v", first)
	}

	for _, target := range []string{"1|C1", "x|y:z", "  "} {
		got, err := r.Adjust("  u:1 ", []Op{
			{Kind: OpUnload, CargoID: "G|1", Target: target},
		})
		if err != nil {
			t.Fatalf("卸下目标 %q 不应影响重复识别: %v", target, err)
		}
		if !reflect.DeepEqual(got, wantFirst) {
			t.Fatalf("卸下目标 %q 变化时应返回首次记录: %+v", target, got)
		}
	}
	g, _ := r.Cargo("G|1")
	if g.Loaded || g.CompartmentID != "" {
		t.Fatalf("重复提交不应再次装卸，G|1 应保持未装载: %+v", g)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("C1 应保持空舱: %+v", c1)
	}
}
