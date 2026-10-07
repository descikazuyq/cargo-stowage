package stowage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 本文件为配载调整中“先核对货物状态，再判断目标舱位”的诊断顺序补充
// 回归保障。调用方可能在货物已装载后再次提交装载，也可能把尚未装载的
// 货物安排移动；若此时目标舱位也填写错误（仅含空白或未登记），返回的
// 第一个原因决定用户先修正哪一项：既有行为先说明货物状态不符，本文件
// 锁定这一顺序，避免实现改成先解析目标而先报“编号为空/舱位不存在”。
//
// 同一份安排在相同状态下经提交前预览 Preview 与正式调整 Adjust 两个入口
// 得到的错误种类、涉及货物与中文说明必须一致；两者都不返回局部结果，
// 区别只在调整编号：正式调整的错误携带本次去掉首尾空白后、尚未被成功
// 占用的调整编号，预览错误的调整编号为空。

// ---------- 测试辅助 ----------

// stateOrderFixture 构造诊断顺序测试的固定状态：
// C1、C2 两个舱位；GL 已装载于 C1（供“已装载再装载”），GU 尚未装载
// （供“未装载移动”）。两件货物同目的地且允许混装，重量不会触发限制，
// 以便把拒绝原因隔离在状态与目标这两项检查上。
func stateOrderFixture(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 1000)
	mustRegisterCompartment(t, r, "C2", 1000)
	mustRegisterCargo(t, r, "GL", 10, "X", true)
	mustRegisterCargo(t, r, "GU", 10, "X", true)
	mustAdjust(t, r, "init", []Op{{Kind: OpLoad, CargoID: "GL", Target: "C1"}})
	return r
}

// previewExpectReject 预演并断言被拒绝：err 为单条 *Error 且不返回任何
// 局部预览结果。返回结构化错误，种类与对象由调用方核对。
func previewExpectReject(t *testing.T, r *Registry, ops []Op) *Error {
	t.Helper()
	res, err := r.Preview(ops)
	if err == nil {
		t.Fatalf("预览应被拒绝，却返回结果: %+v", res)
	}
	if res != nil {
		t.Fatalf("非法操作不应返回局部预览结果: %+v", res)
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("期望 *Error，实际为 %T: %v", err, err)
	}
	return se
}

// assertStateMismatchDiag 核对一条状态不符错误：种类、涉及货物（去首尾
// 空白后）、中文说明必须包含/不得包含的措辞。
func assertStateMismatchDiag(t *testing.T, se *Error, wantID string, wantContains, wantNotContains []string) {
	t.Helper()
	if se.Kind != ErrStateMismatch {
		t.Fatalf("应先报告货物状态不符 ErrStateMismatch，实际 Kind=%s（%s）", se.Kind, se.Error())
	}
	if se.ID != wantID {
		t.Fatalf("状态不符错误应指向货物 %q，实际 ID=%q", wantID, se.ID)
	}
	msg := se.Error()
	for _, s := range wantContains {
		if !strings.Contains(msg, s) {
			t.Fatalf("状态不符说明应包含 %q: %s", s, msg)
		}
	}
	for _, s := range wantNotContains {
		if strings.Contains(msg, s) {
			t.Fatalf("状态不符时不应先报告目标问题（说明不应包含 %q）: %s", s, msg)
		}
	}
}

// ---------- 状态不符先于目标错误：Preview 与 Adjust 一致 ----------

// 已装载货物再次装载、未装载货物安排移动时，无论目标是仅含空白的编号
// 还是未登记舱位，两个入口都必须先返回 ErrStateMismatch：ID 指向去掉
// 首尾空白后的货物编号，说明交代货物当前状态；不能先报目标为空或舱位
// 不存在。货物编号带首尾空白时仍须识别到原货物。
func TestStateCheckedBeforeTargetAcrossPreviewAndAdjust(t *testing.T) {
	cases := []struct {
		name            string
		aid             string // 正式调整使用的、尚未成功占用的编号（带首尾空白）
		ops             []Op
		wantID          string
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:         "已装载再装载-目标空白",
			aid:          "  adj-load-blank  ",
			ops:          []Op{{Kind: OpLoad, CargoID: "  GL  ", Target: "   "}},
			wantID:       "GL",
			wantContains: []string{"GL", "已装载", "C1"},
			// 不能先报告目标为空或舱位不存在。
			wantNotContains: []string{"目标舱位编号为空", "不存在"},
		},
		{
			name:            "已装载再装载-目标未登记",
			aid:             "  adj-load-missing  ",
			ops:             []Op{{Kind: OpLoad, CargoID: "\tGL\t", Target: "  CX  "}},
			wantID:          "GL",
			wantContains:    []string{"GL", "已装载", "C1"},
			wantNotContains: []string{"不存在"},
		},
		{
			name:            "未装载移动-目标空白",
			aid:             "  adj-move-blank  ",
			ops:             []Op{{Kind: OpMove, CargoID: "  GU  ", Target: "  "}},
			wantID:          "GU",
			wantContains:    []string{"GU", "未装载", "不能移动"},
			wantNotContains: []string{"目标舱位编号为空", "不存在"},
		},
		{
			name:            "未装载移动-目标未登记",
			aid:             "  adj-move-missing  ",
			ops:             []Op{{Kind: OpMove, CargoID: "\nGU\n", Target: "  CX  "}},
			wantID:          "GU",
			wantContains:    []string{"GU", "未装载", "不能移动"},
			wantNotContains: []string{"不存在"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := stateOrderFixture(t)

			// 预览：先返回状态不符，不带调整编号，不给局部预计配载。
			pv := previewExpectReject(t, r, tc.ops)
			assertStateMismatchDiag(t, pv, tc.wantID, tc.wantContains, tc.wantNotContains)
			if pv.AdjustmentID != "" {
				t.Fatalf("预览错误不应携带调整编号，实际 %q", pv.AdjustmentID)
			}

			// 预览只读：状态不变后用同一批操作正式调整，诊断必须一致。
			se := submitExpectReject(t, r, tc.aid, tc.ops)
			assertStateMismatchDiag(t, se, tc.wantID, tc.wantContains, tc.wantNotContains)
			if se.Error() != pv.Error() {
				t.Fatalf("两个入口的中文说明应一致：预览 %q，正式调整 %q", pv.Error(), se.Error())
			}
			wantAID := trimID(tc.aid)
			if se.AdjustmentID != wantAID {
				t.Fatalf("正式调整错误应携带去首尾空白后的编号 %q，实际 %q", wantAID, se.AdjustmentID)
			}

			// 两种拒绝都不得改变状态：GL 仍在 C1，GU 仍未装载。
			gl, _ := r.Cargo("GL")
			if !gl.Loaded || gl.CompartmentID != "C1" {
				t.Fatalf("GL 应仍装载于 C1: %+v", gl)
			}
			gu, _ := r.Cargo("GU")
			if gu.Loaded || gu.CompartmentID != "" {
				t.Fatalf("GU 应仍未装载: %+v", gu)
			}
		})
	}
}

// ---------- 错误操作排在合法操作之后：前序操作不得留下结果 ----------

// 把触发状态不符的操作放在一条本来合法的其他货物操作之后：预览不返回
// 局部预计配载，正式调整不返回成功变化记录；货物资料与归属、舱位清单、
// 已用及剩余重量都保持提交前状态。
func TestStateMismatchAfterLegalOpLeavesNoPartialEffect(t *testing.T) {
	// 形态一：先把未装载的 GU 合法装入 C2，再对已装载于 C1 的 GL 重复
	// 装载（目标仅空白）。第二条状态不符，第一条装载也不得生效。
	r := stateOrderFixture(t)
	compBefore := snapshotCompartments(r, "C1", "C2")
	cargoBefore := snapshotCargo(r, "GL", "GU")
	opsLoad := []Op{
		{Kind: OpLoad, CargoID: "GU", Target: "C2"},  // 单独看合法
		{Kind: OpLoad, CargoID: " GL ", Target: " "}, // 已装载再装载 + 空白目标
	}

	pv := previewExpectReject(t, r, opsLoad)
	assertStateMismatchDiag(t, pv, "GL",
		[]string{"GL", "已装载", "C1"}, []string{"目标舱位编号为空", "不存在"})
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)

	se := submitExpectReject(t, r, "  adj-atomic-load  ", opsLoad)
	assertStateMismatchDiag(t, se, "GL",
		[]string{"GL", "已装载", "C1"}, []string{"目标舱位编号为空", "不存在"})
	if se.AdjustmentID != "adj-atomic-load" {
		t.Fatalf("错误应携带去空白后的编号，实际 %q", se.AdjustmentID)
	}
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
	// 直接回查：GU 没被装进 C2，GL 仍在 C1。
	gu, _ := r.Cargo("GU")
	if gu.Loaded || gu.CompartmentID != "" {
		t.Fatalf("前序合法装载不应留下结果，GU 应仍未装载: %+v", gu)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 0 || c2.RemainingWeight != 1000 || len(c2.Cargo) != 0 {
		t.Fatalf("C2 应仍为空舱: %+v", c2)
	}

	// 形态二：先把 GL 从 C1 合法移动到 C2，再安排未装载的 GU 移动到一个
	// 未登记舱位。第二条状态不符，第一条移动也不得生效。
	opsMove := []Op{
		{Kind: OpMove, CargoID: "GL", Target: "C2"},     // 单独看合法
		{Kind: OpMove, CargoID: " GU ", Target: " CX "}, // 未装载移动 + 未登记目标
	}

	pv2 := previewExpectReject(t, r, opsMove)
	assertStateMismatchDiag(t, pv2, "GU",
		[]string{"GU", "未装载", "不能移动"}, []string{"不存在"})
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)

	se2 := submitExpectReject(t, r, "  adj-atomic-move  ", opsMove)
	assertStateMismatchDiag(t, se2, "GU",
		[]string{"GU", "未装载", "不能移动"}, []string{"不存在"})
	if se2.Error() != pv2.Error() {
		t.Fatalf("两个入口的中文说明应一致：预览 %q，正式调整 %q", pv2.Error(), se2.Error())
	}
	if se2.AdjustmentID != "adj-atomic-move" {
		t.Fatalf("错误应携带去空白后的编号，实际 %q", se2.AdjustmentID)
	}
	assertCompartmentsUnchanged(t, r, compBefore)
	assertCargoUnchanged(t, r, cargoBefore)
	gl, _ := r.Cargo("GL")
	if gl.CompartmentID != "C1" {
		t.Fatalf("前序合法移动不应留下结果，GL 应仍在 C1: %+v", gl)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 10 || c1.RemainingWeight != 990 ||
		!reflect.DeepEqual(cargoIDs(*c1), []string{"GL"}) {
		t.Fatalf("C1 应仍只装有 GL、占用 10 千克: %+v", c1)
	}

	// 失败不占用编号：形态一被拒的编号用于修正后的合法内容应能成功，
	// 生效的是修正内容（GU 装入 C2），与此前拒绝无关。
	res := mustAdjust(t, r, "adj-atomic-load", []Op{
		{Kind: OpLoad, CargoID: "GU", Target: "C2"},
	})
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{{CargoID: "GU", From: "", To: "C2"}}) {
		t.Fatalf("失败不应占用编号，修正后应按新内容成功: %+v", res.CargoChanges)
	}
	gu, _ = r.Cargo("GU")
	if gu.CompartmentID != "C2" {
		t.Fatalf("修正提交后 GU 应在 C2: %+v", gu)
	}
	gl, _ = r.Cargo("GL")
	if gl.CompartmentID != "C1" {
		t.Fatalf("GL 应继续留在 C1: %+v", gl)
	}
}

// ---------- 状态符合要求时，目标错误本身仍须被准确报告 ----------

// 用户修正货物状态后应继续得到准确的目标提示：空白目标返回
// ErrInvalidID 且 ID 为空；未登记目标返回 ErrNotFound，ID 为去掉首尾
// 空白后的目标编号。两个入口种类、对象与说明一致，编号携带规则不变。
func TestTargetErrorsReportedWhenStateSatisfiesOp(t *testing.T) {
	cases := []struct {
		name     string
		aid      string
		ops      []Op
		wantKind ErrorKind
		wantID   string
	}{
		{"装载-空白目标", "  adj-t-load-blank  ",
			[]Op{{Kind: OpLoad, CargoID: "GU", Target: "   "}}, ErrInvalidID, ""},
		{"装载-未登记目标", "  adj-t-load-missing  ",
			[]Op{{Kind: OpLoad, CargoID: " GU ", Target: "  CX  "}}, ErrNotFound, "CX"},
		{"移动-空白目标", "  adj-t-move-blank  ",
			[]Op{{Kind: OpMove, CargoID: "GL", Target: "\t "}}, ErrInvalidID, ""},
		{"移动-未登记目标", "  adj-t-move-missing  ",
			[]Op{{Kind: OpMove, CargoID: " GL ", Target: "  CX  "}}, ErrNotFound, "CX"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := stateOrderFixture(t)

			pv, perr := r.Preview(tc.ops)
			if perr == nil {
				t.Fatalf("目标错误应拒绝预览，却返回: %+v", pv)
			}
			if pv != nil {
				t.Fatalf("目标错误时不应返回局部预览: %+v", pv)
			}
			pse := requireError(t, perr, tc.wantKind)
			if pse.ID != tc.wantID {
				t.Fatalf("预览应指向目标 %q，实际 ID=%q", tc.wantID, pse.ID)
			}
			if pse.AdjustmentID != "" {
				t.Fatalf("预览错误不应携带调整编号，实际 %q", pse.AdjustmentID)
			}

			se := submitExpectReject(t, r, tc.aid, tc.ops)
			if se.Kind != tc.wantKind {
				t.Fatalf("正式调整应返回 %s，实际 %s", tc.wantKind, se.Kind)
			}
			if se.ID != tc.wantID {
				t.Fatalf("正式调整应指向目标 %q，实际 ID=%q", tc.wantID, se.ID)
			}
			if se.Error() != pse.Error() {
				t.Fatalf("两个入口的中文说明应一致：预览 %q，正式调整 %q", pse.Error(), se.Error())
			}
			if se.AdjustmentID != trimID(tc.aid) {
				t.Fatalf("正式调整错误应携带去空白后的编号 %q，实际 %q", trimID(tc.aid), se.AdjustmentID)
			}

			// 目标错误同样不得改变配载。
			gl, _ := r.Cargo("GL")
			if gl.CompartmentID != "C1" {
				t.Fatalf("GL 应仍在 C1: %+v", gl)
			}
			gu, _ := r.Cargo("GU")
			if gu.Loaded || gu.CompartmentID != "" {
				t.Fatalf("GU 应仍未装载: %+v", gu)
			}
		})
	}
}

// ---------- 正常装载、移动、卸下行为不受影响 ----------

// 保护诊断顺序的同时，合法的装载、移动、卸下仍按原规则成功并返回变化
// 记录，避免为保护错误路径误伤正常路径。
func TestNormalLoadMoveUnloadStillSucceed(t *testing.T) {
	r := stateOrderFixture(t)

	// 合法装载：GU -> C2。
	load := mustAdjust(t, r, "  normal-load  ", []Op{
		{Kind: OpLoad, CargoID: " GU ", Target: " C2 "},
	})
	if load.ID != "normal-load" {
		t.Fatalf("成功结果编号应为去空白后的值: %+v", load.ID)
	}
	if !reflect.DeepEqual(load.CargoChanges, []CargoChange{{CargoID: "GU", From: "", To: "C2"}}) {
		t.Fatalf("装载变化记录异常: %+v", load.CargoChanges)
	}

	// 合法移动：GL C1 -> C2（与 GU 同目的地、允许混装，重量合法）。
	move := mustAdjust(t, r, "normal-move", []Op{
		{Kind: OpMove, CargoID: "GL", Target: "C2"},
	})
	if !reflect.DeepEqual(move.CargoChanges, []CargoChange{{CargoID: "GL", From: "C1", To: "C2"}}) {
		t.Fatalf("移动变化记录异常: %+v", move.CargoChanges)
	}

	// 合法卸下：GL 从 C2 卸下。
	unload := mustAdjust(t, r, "normal-unload", []Op{
		{Kind: OpUnload, CargoID: " GL ", Target: "随便填什么都忽略"},
	})
	if !reflect.DeepEqual(unload.CargoChanges, []CargoChange{{CargoID: "GL", From: "C2", To: ""}}) {
		t.Fatalf("卸下变化记录异常: %+v", unload.CargoChanges)
	}

	gl, _ := r.Cargo("GL")
	if gl.Loaded || gl.CompartmentID != "" {
		t.Fatalf("卸下后 GL 应未装载: %+v", gl)
	}
	gu, _ := r.Cargo("GU")
	if gu.CompartmentID != "C2" {
		t.Fatalf("GU 应留在 C2: %+v", gu)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || len(c1.Cargo) != 0 {
		t.Fatalf("C1 应为空舱: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 10 || c2.RemainingWeight != 990 ||
		!reflect.DeepEqual(cargoIDs(*c2), []string{"GU"}) {
		t.Fatalf("C2 应只装有 GU、占用 10 千克: %+v", c2)
	}
}
