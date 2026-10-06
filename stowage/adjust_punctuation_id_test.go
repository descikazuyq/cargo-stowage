package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为正式提交（Adjust）补充回归保障：货物、舱位与调整编号内部的
// 冒号、竖线、空格都是编号的一部分，只去掉首尾空白。重点保护两类被
// 看错的情形——
//
//  1. 两份安排的货物编号与目标舱位各不相同，但把“货物+目标”连在一起
//     阅读时极易误认为相同（如 "G|1"->"C1" 与 "G"->"1|C1"）：同一调整
//     编号先后提交必须得到“调整编号冲突”，且冲突提交不改变任何配载；
//  2. 同一批含标点编号的安排，仅改首尾空白或操作顺序时必须取回首次
//     成功的完整变化记录、不再装卸；之后另有正常调整改变了当前配载，
//     原内容回放仍返回首次记录并保留当前配载。
//
// 同时保护冒号与编号内部空格：内部保留空格的对象与删掉该空格的对象
// 是两件独立登记的货物/两个独立舱位，内容识别问题必须报“调整编号
// 冲突”，不得误报成货物状态不符。结果中的编号一律沿用去首尾空白后
// 的原值，不能被替换成删掉内部字符后的编号；普通编号、卸下目标忽略、
// 按编号字典序返回变化等既有行为保持不变。

// newPunctuatedFixture 登记两件货物与两个舱位：货物 "G|1"、"G"，
// 舱位 "C1"、"1|C1"。四者均合法且彼此独立，重量与目的地（同目的地、
// 允许混装、承重充足）不会导致任何装载被拒。
func newPunctuatedFixture(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "1|C1", 1000)
	mustRegisterCargo(t, r, "G|1", 10, "X", true)
	mustRegisterCargo(t, r, "G", 10, "X", true)
	return r
}

// 断言含标点的舱位查询结果（清单、已用与剩余重量）。
func assertCompartmentState(t *testing.T, r *Registry, id string, wantUsed int64, wantCargo []string) {
	t.Helper()
	got, err := r.Compartment(id)
	if err != nil {
		t.Fatalf("查询舱位 %q 失败: %v", id, err)
	}
	if got.UsedWeight != wantUsed || got.RemainingWeight != got.MaxWeight-wantUsed {
		t.Fatalf("舱位 %q 重量错误: 已用 %d 剩余 %d（承重 %d），期望已用 %d",
			id, got.UsedWeight, got.RemainingWeight, got.MaxWeight, wantUsed)
	}
	if ids := cargoIDs(*got); !reflect.DeepEqual(ids, wantCargo) {
		t.Fatalf("舱位 %q 货物清单错误: got=%v want=%v", id, ids, wantCargo)
	}
}

// ---------- 竖线落在编号内部：两份易混安排必须判为不同内容 ----------

// "G|1" 装入 "C1" 与 "G" 装入 "1|C1" 是两份不同内容：竖线既可能出现在
// 货物编号里，也可能出现在舱位编号里，连读两份安排的“货物+目标”时
// 字符序列仅竖线位置不同（"G|1C1" 与 "G1|C1"）。若把竖线当作操作间
// 分隔、或为比较而删除/替换竖线，就会把两份内容误判为同一份。
func TestAdjustConflictAcrossPunctuationBoundaryIDs(t *testing.T) {
	r := newPunctuatedFixture(t)
	const aid = "adj|boundary"

	// 第一份安排：货物 "G|1" -> 舱位 "C1"，编号带首尾空白也应按原值识别。
	res1, err := r.Adjust("  "+aid+"  ", []Op{
		{Kind: OpLoad, CargoID: " G|1 ", Target: " C1 "},
	})
	if err != nil {
		t.Fatalf("第一份安排应成功: %v", err)
	}
	// 完整变化记录沿用去首尾空白后的原值，竖线保留，只有 C1 受影响。
	want1 := &AdjustmentResult{
		ID:           aid,
		CargoChanges: []CargoChange{{CargoID: "G|1", From: "", To: "C1"}},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 0, WeightAfter: 10},
		},
	}
	if !reflect.DeepEqual(res1, want1) {
		t.Fatalf("第一份安排结果错误: got=%+v want=%+v", res1, want1)
	}

	// 冲突提交前对两个舱位留快照。
	before := snapshotCompartments(r, "C1", "1|C1")

	// 第二份安排：货物 "G" -> 舱位 "1|C1"，沿用同一调整编号。
	// submitExpectReject 同时断言不返回成功结果，且错误为单条 *Error。
	structured := submitExpectReject(t, r, aid, []Op{
		{Kind: OpLoad, CargoID: "G", Target: "1|C1"},
	})
	if structured.Kind != ErrAdjustmentIDConflict {
		t.Fatalf("应报调整编号冲突，实际为 %s（%v）", structured.Kind, err)
	}
	// 结构化错误按现有方式说明涉及的调整编号（去首尾空白后的值）。
	if structured.AdjustmentID != aid || structured.ID != aid {
		t.Fatalf("错误应指出调整编号 %q，实际 AdjustmentID=%q ID=%q",
			aid, structured.AdjustmentID, structured.ID)
	}
	if !strings.Contains(structured.Error(), aid) {
		t.Fatalf("错误说明应包含调整编号 %q: %s", aid, structured.Error())
	}

	// 冲突提交后再次查询：第一件货物仍在原目标舱位，第二件仍未装载，
	// 两舱的货物清单、已用重量和剩余重量均保持冲突提交前的状态。
	assertCompartmentsUnchanged(t, r, before)
	gPipe, _ := r.Cargo("G|1")
	if !gPipe.Loaded || gPipe.CompartmentID != "C1" {
		t.Fatalf("第一件货物应仍在 C1: %+v", gPipe)
	}
	gPlain, _ := r.Cargo("G")
	if gPlain.Loaded || gPlain.CompartmentID != "" {
		t.Fatalf("第二件货物应仍未装载: %+v", gPlain)
	}
	assertCompartmentState(t, r, "C1", 10, []string{"G|1"})
	assertCompartmentState(t, r, "1|C1", 0, []string{})

	// 第二份安排本身完全合法（两件货物、两个舱位均独立登记，承重与
	// 混装均无问题）：换一个新的调整编号即可成功，两份安排彼此独立。
	res3 := mustAdjust(t, r, "adj-2", []Op{
		{Kind: OpLoad, CargoID: "G", Target: "1|C1"},
	})
	if len(res3.CargoChanges) != 1 || res3.CargoChanges[0].CargoID != "G" ||
		res3.CargoChanges[0].To != "1|C1" {
		t.Fatalf("第二份安排以新编号提交的结果错误: %+v", res3)
	}
	gPlain, _ = r.Cargo("G")
	if !gPlain.Loaded || gPlain.CompartmentID != "1|C1" {
		t.Fatalf("第二件货物应已装入 1|C1: %+v", gPlain)
	}
	gPipe, _ = r.Cargo("G|1")
	if gPipe.CompartmentID != "C1" {
		t.Fatalf("第一件货物不应受影响: %+v", gPipe)
	}
	assertCompartmentState(t, r, "C1", 10, []string{"G|1"})
	assertCompartmentState(t, r, "1|C1", 10, []string{"G"})
}

// ---------- 仅首尾空白与排列顺序不同：取回首次完整记录 ----------

// 含竖线、冒号的同一批安排，只改变货物、目标舱位与调整编号的首尾
// 空白，或改变操作排列顺序，应返回首次成功保存的完整变化记录，不再
// 执行装载。结果编号沿用去首尾空白后的值；变化仍按编号字典序返回。
func TestAdjustIdempotentPunctuationIDsWhitespaceAndOrderOnly(t *testing.T) {
	r := newPunctuatedFixture(t)
	mustRegisterCompartment(t, r, "C2", 1000)
	const aid = "a:1|2"

	// 首次：货物与目标编号均带首尾空白，"G|1" 在前、"G" 在后。
	res1 := mustAdjust(t, r, "  "+aid+"\t", []Op{
		{Kind: OpLoad, CargoID: "\tG|1 ", Target: "\nC1 "},
		{Kind: OpLoad, CargoID: " G ", Target: " 1|C1"},
	})
	// 货物变化按编号字典序："G" 是 "G|1" 的前缀，故 "G" 在前；
	// 舱位变化按编号字典序：'1'(0x31) < 'C'(0x43)，故 "1|C1" 在前。
	want1 := &AdjustmentResult{
		ID: aid,
		CargoChanges: []CargoChange{
			{CargoID: "G", From: "", To: "1|C1"},
			{CargoID: "G|1", From: "", To: "C1"},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "1|C1", WeightBefore: 0, WeightAfter: 10},
			{CompartmentID: "C1", WeightBefore: 0, WeightAfter: 10},
		},
	}
	if !reflect.DeepEqual(res1, want1) {
		t.Fatalf("首次结果错误: got=%+v want=%+v", res1, want1)
	}

	// 变体一：操作顺序倒置，编号不带空白。
	variants := [][]Op{
		{
			{Kind: OpLoad, CargoID: "G", Target: "1|C1"},
			{Kind: OpLoad, CargoID: "G|1", Target: "C1"},
		},
		// 变体二：顺序倒置且三类编号各自换了首尾空白（空格/制表符/换行）。
		{
			{Kind: OpLoad, CargoID: "\n G \t", Target: "\t1|C1 \n"},
			{Kind: OpLoad, CargoID: " G|1\n", Target: "  C1  "},
		},
	}
	for i, ops := range variants {
		res, err := r.Adjust(" "+aid+" ", ops)
		if err != nil {
			t.Fatalf("变体 %d 应识别为原安排并返回首次结果: %v", i+1, err)
		}
		if !reflect.DeepEqual(res, res1) {
			t.Fatalf("变体 %d 应返回首次成功保存的完整记录: got=%+v want=%+v",
				i+1, res, res1)
		}
	}

	// 回放不重复执行装载：两舱各只占用 10 千克，两件货物各在自己的舱位。
	gPipe, _ := r.Cargo("G|1")
	gPlain, _ := r.Cargo("G")
	if gPipe.CompartmentID != "C1" || gPlain.CompartmentID != "1|C1" {
		t.Fatalf("回放不应改变配载: G|1=%q G=%q", gPipe.CompartmentID, gPlain.CompartmentID)
	}
	assertCompartmentState(t, r, "C1", 10, []string{"G|1"})
	assertCompartmentState(t, r, "1|C1", 10, []string{"G"})

	// 之后另有一次正常调整改变当前配载：把 "G" 从 "1|C1" 移到 C2。
	mustAdjust(t, r, "normal-move", []Op{
		{Kind: OpMove, CargoID: "G", Target: "C2"},
	})
	gPlain, _ = r.Cargo("G")
	if gPlain.CompartmentID != "C2" {
		t.Fatalf("前置：G 应已移到 C2: %+v", gPlain)
	}

	// 原内容再次提交：继续取回首次记录（记录里 G 的去向仍是 "1|C1"），
	// 不按当前配载重算、不再执行装载，当前配载保持不变。
	res2, err := r.Adjust(aid, []Op{
		{Kind: OpLoad, CargoID: " G ", Target: " 1|C1 "},
		{Kind: OpLoad, CargoID: "G|1", Target: "C1"},
	})
	if err != nil {
		t.Fatalf("当前配载改变后原内容回放仍应返回首次记录: %v", err)
	}
	if !reflect.DeepEqual(res2, res1) {
		t.Fatalf("应原样取回首次记录: got=%+v want=%+v", res2, res1)
	}
	gPipe, _ = r.Cargo("G|1")
	gPlain, _ = r.Cargo("G")
	if gPipe.CompartmentID != "C1" || gPlain.CompartmentID != "C2" {
		t.Fatalf("回放应保留当前配载: G|1=%q G=%q", gPipe.CompartmentID, gPlain.CompartmentID)
	}
	assertCompartmentState(t, r, "C1", 10, []string{"G|1"})
	assertCompartmentState(t, r, "1|C1", 0, []string{})
	assertCompartmentState(t, r, "C2", 10, []string{"G"})
}

// ---------- 冒号与内部空格：删掉内部字符后的对象是另一份内容 ----------

// 编号内部的冒号、空格同样参与区分：内部保留空格的对象与删掉该空格
// 的对象是两件独立登记的货物/两个独立舱位。同调整编号先后提交时必须
// 报调整编号冲突——内容识别在状态检查之前完成，不得把内容识别问题
// 误报成货物状态不符。
func TestAdjustConflictDistinguishesColonAndInternalSpaceIDs(t *testing.T) {
	cases := []struct {
		name           string
		cargoA, cargoB string
		compA, compB   string
	}{
		// 与竖线同形："G:1"->"C1" 连读为 "G:1C1"，"G"->"1:C1" 连读为 "G1:C1"。
		{"colon", "G:1", "G", "C1", "1:C1"},
		// 编号内部保留空格："G 1" 与 "G1"、"C 1" 与 "C1" 是不同对象。
		{"internal-space", "G 1", "G1", "C 1", "C1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			mustRegisterCompartment(t, r, tc.compA, 1000)
			mustRegisterCompartment(t, r, tc.compB, 1000)
			mustRegisterCargo(t, r, tc.cargoA, 10, "X", true)
			mustRegisterCargo(t, r, tc.cargoB, 10, "X", true)

			// 两组编号在登记处彼此独立、均可分别查询。
			for _, id := range []string{tc.cargoA, tc.cargoB} {
				if _, err := r.Cargo(id); err != nil {
					t.Fatalf("货物 %q 应可独立查询: %v", id, err)
				}
			}
			for _, id := range []string{tc.compA, tc.compB} {
				if _, err := r.Compartment(id); err != nil {
					t.Fatalf("舱位 %q 应可独立查询: %v", id, err)
				}
			}

			// 第一份安排成功。
			res1 := mustAdjust(t, r, "adj-x", []Op{
				{Kind: OpLoad, CargoID: tc.cargoA, Target: tc.compA},
			})
			if res1.CargoChanges[0].CargoID != tc.cargoA ||
				res1.CargoChanges[0].To != tc.compA {
				t.Fatalf("首次结果应保留编号内部字符: %+v", res1.CargoChanges[0])
			}
			before := snapshotCompartments(r, tc.compA, tc.compB)

			// 同一调整编号提交“删掉内部字符后”的另一份安排。
			se := submitExpectReject(t, r, "adj-x", []Op{
				{Kind: OpLoad, CargoID: tc.cargoB, Target: tc.compB},
			})
			if se.Kind != ErrAdjustmentIDConflict {
				t.Fatalf("应识别为不同内容并报调整编号冲突，实际为 %s；"+
					"内容识别问题不得误报成货物状态不符（%v）", se.Kind, se)
			}
			if se.AdjustmentID != "adj-x" {
				t.Fatalf("错误应携带调整编号 adj-x，实际 %q", se.AdjustmentID)
			}

			// 配载保持冲突前状态：第一件在原舱，第二件仍未装载。
			assertCompartmentsUnchanged(t, r, before)
			cA, _ := r.Cargo(tc.cargoA)
			if !cA.Loaded || cA.CompartmentID != tc.compA {
				t.Fatalf("第一件货物应仍在 %q: %+v", tc.compA, cA)
			}
			cB, _ := r.Cargo(tc.cargoB)
			if cB.Loaded || cB.CompartmentID != "" {
				t.Fatalf("第二件货物应仍未装载: %+v", cB)
			}

			// 新编号下第二份安排同样合法成功：承重与混装均不是问题，
			// 两组对象自始至终独立。
			mustAdjust(t, r, "adj-y", []Op{
				{Kind: OpLoad, CargoID: tc.cargoB, Target: tc.compB},
			})
			cA, _ = r.Cargo(tc.cargoA)
			cB, _ = r.Cargo(tc.cargoB)
			if cA.CompartmentID != tc.compA || cB.CompartmentID != tc.compB {
				t.Fatalf("两份安排应各自生效: %q 在 %q、%q 在 %q",
					tc.cargoA, cA.CompartmentID, tc.cargoB, cB.CompartmentID)
			}
			assertCompartmentState(t, r, tc.compA, 10, []string{tc.cargoA})
			assertCompartmentState(t, r, tc.compB, 10, []string{tc.cargoB})
		})
	}
}

// ---------- 含标点编号与“卸下目标忽略”既有行为的组合 ----------

// 卸下操作的目标值本来就不参与内容比较；目标里即使含竖线、冒号等
// 字符，删除或更换目标仍识别为同一次卸下，返回首次结果且不再次卸下。
func TestAdjustIdempotentUnloadTargetWithPunctuationIgnored(t *testing.T) {
	r := newPunctuatedFixture(t)
	mustAdjust(t, r, "load", []Op{
		{Kind: OpLoad, CargoID: "G|1", Target: "C1"},
	})

	// 首次卸下，目标填了含竖线与冒号的无效值，应被忽略。
	res1 := mustAdjust(t, r, "u:1", []Op{
		{Kind: OpUnload, CargoID: " G|1 ", Target: "C1|junk:2"},
	})
	want1 := &AdjustmentResult{
		ID:           "u:1",
		CargoChanges: []CargoChange{{CargoID: "G|1", From: "C1", To: ""}},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 10, WeightAfter: 0},
		},
	}
	if !reflect.DeepEqual(res1, want1) {
		t.Fatalf("卸下结果错误: got=%+v want=%+v", res1, want1)
	}

	// 更换或删除卸下目标（含仅空白、含标点的已登记/未登记舱位形式），
	// 连同调整编号的首尾空白，都不影响重复识别。
	for _, target := range []string{"", "   ", "C1", " 1|C1:? "} {
		res, err := r.Adjust("\tu:1\n", []Op{
			{Kind: OpUnload, CargoID: "G|1", Target: target},
		})
		if err != nil {
			t.Fatalf("卸下目标 %q 不应影响重复识别: %v", target, err)
		}
		if !reflect.DeepEqual(res, res1) {
			t.Fatalf("卸下目标 %q 变化时应返回首次记录: got=%+v want=%+v",
				target, res, res1)
		}
	}

	// 不再次卸下：货物保持未装载，C1 重量不发生第二次收回。
	g, _ := r.Cargo("G|1")
	if g.Loaded || g.CompartmentID != "" {
		t.Fatalf("重复提交不应再次改变货物状态: %+v", g)
	}
	assertCompartmentState(t, r, "C1", 0, []string{})
}
