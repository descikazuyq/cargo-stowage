package stowage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 本文件为“先核对货物状态，再判断目标舱位”的诊断顺序补充回归保障。
//
// 调用方可能在货物已装载后再次提交装载，也可能安排尚未装载的货物移动；
// 若此时目标舱位也填写错误（仅含空白或未登记），错误必须先报告货物状态
// 不符（ErrStateMismatch），而不是先报告目标为空或舱位不存在——否则会
// 误导用户先去修错误的那一项目标。同一份安排在相同状态下经 Preview 与
// Adjust 两个入口得到的错误种类、涉及货物与中文说明必须一致；非法操作
// 排在本来合法的其他货物操作之后时，前面的操作同样不得留下任何结果。

// ---------- 测试夹具 ----------

// newStateOrderFixture 建立固定配载：
//   - C1（已装 GL、GM），C2（已装 GU），全部货物同目的地、允许混装；
//   - GN、GQ 尚未装载；
//   - CX 刻意不登记，用作“未登记目标舱位”。
//
// 其中 GL 用于“已装载再装载”，GN 用于“未装载却移动”，GU/GM/GQ 用于
// 放在错误操作之前的本来合法的卸下/移动/装载。
func newStateOrderFixture(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "C2", 1000)
	mustRegisterCargo(t, r, "GL", 10, "X", true) // 已装载于 C1
	mustRegisterCargo(t, r, "GM", 10, "X", true) // 已装载于 C1
	mustRegisterCargo(t, r, "GU", 10, "X", true) // 已装载于 C2
	mustRegisterCargo(t, r, "GN", 10, "X", true) // 尚未装载
	mustRegisterCargo(t, r, "GQ", 10, "X", true) // 尚未装载
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "GL", Target: "C1"},
		{Kind: OpLoad, CargoID: "GM", Target: "C1"},
		{Kind: OpLoad, CargoID: "GU", Target: "C2"},
	})
	return r
}

// 两类同样错误的目标：仅含空白（去空白后为空）与未登记舱位。
var badStateOrderTargets = []struct {
	name   string
	target string
}{
	{"目标仅含空白", " \t\n "},
	{"目标未登记", "  CX  "},
}

// assertStateDiag 断言一条结构化错误先报告货物状态不符：种类、涉及货物
// （去空白后的货物编号）、调整编号与中文说明关键字都符合预期，且说明中
// 不泄漏目标层面的诊断（为空/不存在）。
func assertStateDiag(t *testing.T, se *Error, wantCargo string, wantAdjustment string, contains ...string) {
	t.Helper()
	if se.Kind != ErrStateMismatch {
		t.Fatalf("应先报告货物状态不符 ErrStateMismatch，实际为 %s（%v）", se.Kind, se)
	}
	if se.ID != wantCargo {
		t.Fatalf("状态不符错误应指向货物 %q，实际 ID=%q", wantCargo, se.ID)
	}
	if se.AdjustmentID != wantAdjustment {
		t.Fatalf("调整编号应为 %q，实际 %q", wantAdjustment, se.AdjustmentID)
	}
	msg := se.Error()
	for _, want := range contains {
		if !strings.Contains(msg, want) {
			t.Fatalf("状态不符说明应包含 %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "为空") || strings.Contains(msg, "不存在") {
		t.Fatalf("状态不符时不应抢先报告目标为空或舱位不存在: %s", msg)
	}
}

// checkPreviewAndAdjustAgree 让同一份安排分别经过 Preview 与 Adjust
// （Adjust 使用尚未成功占用过的调整编号，传入值带首尾空白），断言：
//   - 两者都拒绝且不返回任何结果（预览无局部预计配载，正式调整无变化记录）；
//   - 错误种类、涉及货物与中文说明逐字一致；
//   - Preview 错误的调整编号为空，Adjust 错误携带去首尾空白后的编号。
func checkPreviewAndAdjustAgree(t *testing.T, r *Registry, adjustmentID string, ops []Op, wantCargo string, contains ...string) {
	t.Helper()
	trimmed := trimID(adjustmentID)

	pv, perr := r.Preview(ops)
	if perr == nil {
		t.Fatalf("预览应被拒绝，却返回局部预计配载: %+v", pv)
	}
	if pv != nil {
		t.Fatalf("非法操作时预览不应返回局部预计配载: %+v", pv)
	}
	var pse *Error
	if !errors.As(perr, &pse) {
		t.Fatalf("预览错误应为 *Error，实际 %T: %v", perr, perr)
	}
	assertStateDiag(t, pse, wantCargo, "", contains...)

	res, aerr := r.Adjust(adjustmentID, ops)
	if aerr == nil {
		t.Fatalf("正式调整应被拒绝，却返回成功变化记录: %+v", res)
	}
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功变化记录: %+v", res)
	}
	var ase *Error
	if !errors.As(aerr, &ase) {
		t.Fatalf("调整错误应为 *Error，实际 %T: %v", aerr, aerr)
	}
	assertStateDiag(t, ase, wantCargo, trimmed, contains...)

	if pse.Error() != ase.Error() {
		t.Fatalf("预览与正式调整的诊断说明应一致:\n预览: %s\n调整: %s", pse.Error(), ase.Error())
	}
}

// ---------- 已装载货物再次装载：先报状态，不管目标如何错误 ----------

func TestReloadLoadedCargoReportsStateBeforeTarget(t *testing.T) {
	for _, bt := range badStateOrderTargets {
		t.Run(bt.name, func(t *testing.T) {
			r := newStateOrderFixture(t)
			// 货物编号带首尾空白仍须识别为已装载于 C1 的 GL。
			ops := []Op{{Kind: OpLoad, CargoID: "\t GL \n", Target: bt.target}}
			checkPreviewAndAdjustAgree(t, r, "  adj-reload-"+bt.name, ops, "GL",
				"GL", "已装载", "C1")

			// 拒绝后 GL 仍在 C1，配载与提交前一致。
			gl, _ := r.Cargo("GL")
			if !gl.Loaded || gl.CompartmentID != "C1" {
				t.Fatalf("GL 应仍装载于 C1: %+v", gl)
			}
		})
	}
}

// ---------- 未装载货物安排移动：先报状态，不管目标如何错误 ----------

func TestMoveUnloadedCargoReportsStateBeforeTarget(t *testing.T) {
	for _, bt := range badStateOrderTargets {
		t.Run(bt.name, func(t *testing.T) {
			r := newStateOrderFixture(t)
			ops := []Op{{Kind: OpMove, CargoID: "  GN  ", Target: bt.target}}
			checkPreviewAndAdjustAgree(t, r, "  adj-move-unloaded-"+bt.name, ops, "GN",
				"GN", "未装载", "不能移动")

			// 拒绝后 GN 仍未装载。
			gn, _ := r.Cargo("GN")
			if gn.Loaded || gn.CompartmentID != "" {
				t.Fatalf("GN 应仍未装载: %+v", gn)
			}
		})
	}
}

// 编号首尾空白不能把货物误当成另一个对象：若没有先按去空白编号识别，
// 这里会得到 ErrNotFound 而不是 ErrStateMismatch。
func TestWhitespaceAroundCargoIDResolvesSameCargo(t *testing.T) {
	r := newStateOrderFixture(t)
	ops := []Op{{Kind: OpLoad, CargoID: " \tGL\t ", Target: "   "}}
	se := submitExpectReject(t, r, "adj-ws-cargo", ops)
	if se.Kind != ErrStateMismatch || se.ID != "GL" {
		t.Fatalf("带空白编号应识别为 GL 并报状态不符，实际 Kind=%s ID=%q", se.Kind, se.ID)
	}
}

// ---------- 状态符合操作要求时，目标错误本身仍须被准确报告 ----------

// 货物状态没有问题时，空白目标返回 ErrInvalidID（ID 为空），未登记目标
// 返回 ErrNotFound（ID 为去空白后的目标编号）；Preview 与 Adjust 一致，
// 用户修正状态后才能继续得到准确的目标提示。
func TestBadTargetStillReportedWhenStateMatches(t *testing.T) {
	type expect struct {
		kind     ErrorKind
		targetID string
	}
	cases := []struct {
		name   string
		op     Op
		expect expect
	}{
		{"未装载货物装入空白目标", Op{Kind: OpLoad, CargoID: "GN", Target: " \t "}, expect{ErrInvalidID, ""}},
		{"未装载货物装入未登记目标", Op{Kind: OpLoad, CargoID: " GN ", Target: "  CX  "}, expect{ErrNotFound, "CX"}},
		{"已装载货物移动到空白目标", Op{Kind: OpMove, CargoID: "GL", Target: "\n\t "}, expect{ErrInvalidID, ""}},
		{"已装载货物移动到未登记目标", Op{Kind: OpMove, CargoID: " GL ", Target: "\tCX\t"}, expect{ErrNotFound, "CX"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newStateOrderFixture(t)
			ops := []Op{tc.op}

			pv, perr := r.Preview(ops)
			if pv != nil {
				t.Fatalf("目标非法时不应返回局部预览: %+v", pv)
			}
			ps := requireError(t, perr, tc.expect.kind)
			if ps.ID != tc.expect.targetID {
				t.Fatalf("预览应报告目标 %q，实际 ID=%q", tc.expect.targetID, ps.ID)
			}
			if ps.AdjustmentID != "" {
				t.Fatalf("预览错误不应携带调整编号，实际 %q", ps.AdjustmentID)
			}

			aid := "  adj-bad-target-" + tc.name + "  "
			res, aerr := r.Adjust(aid, ops)
			if res != nil {
				t.Fatalf("目标非法时不应返回成功变化记录: %+v", res)
			}
			as := requireError(t, aerr, tc.expect.kind)
			if as.ID != tc.expect.targetID {
				t.Fatalf("正式调整应报告目标 %q，实际 ID=%q", tc.expect.targetID, as.ID)
			}
			if as.AdjustmentID != trimID(aid) {
				t.Fatalf("正式调整错误应携带去空白后的编号，实际 %q", as.AdjustmentID)
			}
			if ps.Error() != as.Error() {
				t.Fatalf("预览与正式调整说明应一致: %q vs %q", ps.Error(), as.Error())
			}
		})
	}
}

// ---------- 错误操作排在合法操作之后：前面的操作也不能留下结果 ----------

// 一批安排中先放一条本来合法的其他货物操作，再放状态与目标同时有问题
// 的操作：整批必须被拒，货物资料与归属、舱位清单、已用及剩余重量全部
// 保持提交前状态；Preview（只读）与 Adjust（原子）之后都是如此。
func TestStateOrderRejectionAtomicAfterValidOp(t *testing.T) {
	cases := []struct {
		name      string
		aid       string
		ops       []Op
		cargoIDs  []string
		compIDs   []string
		stateWant []string // 错误说明关键字
		badCargo  string
	}{
		{
			name: "合法卸下后再装已装载货物到空白目标",
			aid:  "  adj-atomic-unload  ",
			ops: []Op{
				{Kind: OpUnload, CargoID: "GU"}, // 本可成功：GU 从 C2 卸下
				{Kind: OpLoad, CargoID: " GL ", Target: " \t "},
			},
			cargoIDs:  []string{"GL", "GM", "GU", "GN", "GQ"},
			compIDs:   []string{"C1", "C2"},
			stateWant: []string{"GL", "已装载", "C1"},
			badCargo:  "GL",
		},
		{
			name: "合法移动后移动未装载货物到未登记舱位",
			aid:  "  adj-atomic-move  ",
			ops: []Op{
				{Kind: OpMove, CargoID: "GM", Target: "C2"}, // 本可成功：C1 -> C2
				{Kind: OpMove, CargoID: " GN ", Target: " CX "},
			},
			cargoIDs:  []string{"GL", "GM", "GU", "GN", "GQ"},
			compIDs:   []string{"C1", "C2"},
			stateWant: []string{"GN", "未装载", "不能移动"},
			badCargo:  "GN",
		},
		{
			name: "合法装载后再装已装载货物到未登记舱位",
			aid:  "  adj-atomic-load  ",
			ops: []Op{
				{Kind: OpLoad, CargoID: "GQ", Target: "C2"}, // 本可成功
				{Kind: OpLoad, CargoID: "GL", Target: "CX"},
			},
			cargoIDs:  []string{"GL", "GM", "GU", "GN", "GQ"},
			compIDs:   []string{"C1", "C2"},
			stateWant: []string{"GL", "已装载", "C1"},
			badCargo:  "GL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newStateOrderFixture(t)
			cargoBefore := snapshotCargo(r, tc.cargoIDs...)
			compBefore := snapshotCompartments(r, tc.compIDs...)

			pv, perr := r.Preview(tc.ops)
			if pv != nil {
				t.Fatalf("预览不应返回局部预计配载: %+v", pv)
			}
			assertStateDiag(t, requireError(t, perr, ErrStateMismatch), tc.badCargo, "", tc.stateWant...)
			// 预览只读：快照应完全不变。
			assertCargoUnchanged(t, r, cargoBefore)
			assertCompartmentsUnchanged(t, r, compBefore)

			res, aerr := r.Adjust(tc.aid, tc.ops)
			if res != nil {
				t.Fatalf("被拒绝的调整不应返回成功变化记录: %+v", res)
			}
			assertStateDiag(t, requireError(t, aerr, ErrStateMismatch), tc.badCargo, trimID(tc.aid), tc.stateWant...)
			// 正式调整原子：前面合法操作同样不得生效，资料、归属、清单、重量不变。
			assertCargoUnchanged(t, r, cargoBefore)
			assertCompartmentsUnchanged(t, r, compBefore)
		})
	}
}

// 因状态不符被拒绝的调整不占用编号：用同一编号（去空白后相同）提交修正
// 后的合法安排应当成功，而不是被当作重复或冲突。
func TestStateMismatchFailureDoesNotOccupyAdjustmentID(t *testing.T) {
	r := newStateOrderFixture(t)
	aid := "  adj-state-retry  "
	bad := []Op{{Kind: OpMove, CargoID: " GN ", Target: " CX "}}
	se := submitExpectReject(t, r, aid, bad)
	if se.Kind != ErrStateMismatch || se.AdjustmentID != "adj-state-retry" {
		t.Fatalf("前置拒绝应携带编号 adj-state-retry: %+v", se)
	}

	res, err := r.Adjust(aid, []Op{{Kind: OpUnload, CargoID: "GU"}})
	if err != nil {
		t.Fatalf("失败不应占用编号，修正后用同一编号应成功: %v", err)
	}
	if res.ID != "adj-state-retry" || len(res.CargoChanges) != 1 ||
		res.CargoChanges[0].CargoID != "GU" || res.CargoChanges[0].From != "C2" ||
		res.CargoChanges[0].To != "" {
		t.Fatalf("复用编号的成功结果异常: %+v", res)
	}
	gu, _ := r.Cargo("GU")
	if gu.Loaded || gu.CompartmentID != "" {
		t.Fatalf("GU 应已卸下: %+v", gu)
	}
}

// ---------- 已有正常装载、移动、卸下行为不受影响 ----------

// 同一批安排包含合法装载、移动与卸下时：Preview 判为可提交且给出与正式
// 调整逐件一致的变化记录；Adjust 成功并落为预计配载，未涉及货物不受影响。
func TestNormalLoadMoveUnloadStillSucceed(t *testing.T) {
	r := newStateOrderFixture(t)
	ops := []Op{
		{Kind: OpUnload, CargoID: "GU"},            // C2 -> 未装载
		{Kind: OpMove, CargoID: "GM", Target: "C2"}, // C1 -> C2
		{Kind: OpLoad, CargoID: "GN", Target: "C2"}, // 未装载 -> C2
	}

	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatalf("合法安排预览不应报错: %v", err)
	}
	if !pv.Submittable || len(pv.Rejections) != 0 {
		t.Fatalf("合法安排应可提交: %+v", pv.Rejections)
	}
	wantChanges := []CargoChange{
		{CargoID: "GM", From: "C1", To: "C2"},
		{CargoID: "GN", From: "", To: "C2"},
		{CargoID: "GU", From: "C2", To: ""},
	}
	if !reflect.DeepEqual(pv.CargoChanges, wantChanges) {
		t.Fatalf("预览变化记录异常: got=%+v want=%+v", pv.CargoChanges, wantChanges)
	}

	res, err := r.Adjust("  adj-normal  ", ops)
	if err != nil {
		t.Fatalf("合法安排正式提交应成功: %v", err)
	}
	if res.ID != "adj-normal" || !reflect.DeepEqual(res.CargoChanges, wantChanges) {
		t.Fatalf("正式调整结果应与预览变化一致: %+v", res)
	}

	// 最终配载：C1 只剩未涉及的 GL（10）；C2 为 GM、GN（20）；GU 已卸下。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 10 || c1.RemainingWeight != 990 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"GL"}) {
		t.Fatalf("C1 应只剩 GL: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 20 || c2.RemainingWeight != 980 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"GM", "GN"}) {
		t.Fatalf("C2 应为 GM、GN: %+v", c2)
	}
	gu, _ := r.Cargo("GU")
	if gu.Loaded || gu.CompartmentID != "" {
		t.Fatalf("GU 应已卸下: %+v", gu)
	}
	// 未涉及货物 GQ 仍未装载，登记资料不变。
	gq, _ := r.Cargo("GQ")
	if gq.Loaded || gq.Weight != 10 || gq.Destination != "X" || !gq.AllowMixed {
		t.Fatalf("GQ 应保持未装载且资料不变: %+v", gq)
	}
}
