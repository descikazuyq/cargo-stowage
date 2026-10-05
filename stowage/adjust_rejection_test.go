package stowage

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// 本组回归测试保护“正式提交”（Adjust）在一批安排同时触发多个舱位的
// 承重或混装限制时的报告与回滚行为：
//
//	- 先按舱位编号的字典序选出最靠前的违规舱位，再在该舱位内决定原因；
//	- 同一舱位同时超重（或重量溢出）与混装冲突时，先报告重量原因；
//	- 仅混装冲突时，错误对象是不允许混装的货物中编号字典序最靠前的一件
//	  （可能是原来就在舱内的货物），说明中同时指出冲突舱位；
//	- 错误一律携带去掉首尾空白后的调整编号，且不返回成功调整结果；
//	- 拒绝后整次调整不生效：即使清单中含有本可成功的卸下或移动，也不能
//	  只执行其中一部分；提交后的查询与提交前完全一致。
//
// Preview 仍负责一次列全全部拒绝原因，两种入口用途不同，组内也保留一条
// 对照测试，避免把正式提交改成返回全部原因。

// assertCargoState 核对一件货物的登记资料与当前归属（comp 为空表示未装载）。
func assertCargoState(t *testing.T, r *Registry, id string, weight int64, dest string, allowMixed bool, comp string) {
	t.Helper()
	cv, err := r.Cargo(id)
	if err != nil {
		t.Fatalf("查询货物 %s: %v", id, err)
	}
	if cv.Weight != weight || cv.Destination != dest || cv.AllowMixed != allowMixed {
		t.Fatalf("货物 %s 的重量/目的地/混装许可被改变: %+v", id, cv)
	}
	if comp == "" {
		if cv.Loaded || cv.CompartmentID != "" {
			t.Fatalf("货物 %s 应仍未装载，实际位于 %q: %+v", id, cv.CompartmentID, cv)
		}
	} else if !cv.Loaded || cv.CompartmentID != comp {
		t.Fatalf("货物 %s 应仍在舱位 %s，实际 %q: %+v", id, comp, cv.CompartmentID, cv)
	}
}

// assertCompartmentState 核对舱位的完整清单、已用与剩余重量。
func assertCompartmentState(t *testing.T, r *Registry, id string, used, remaining int64, wantCargo []string) {
	t.Helper()
	cpt, err := r.Compartment(id)
	if err != nil {
		t.Fatalf("查询舱位 %s: %v", id, err)
	}
	if cpt.UsedWeight != used || cpt.RemainingWeight != remaining {
		t.Fatalf("舱位 %s 应为已用 %d 剩余 %d，实际 %d/%d: %+v",
			id, used, remaining, cpt.UsedWeight, cpt.RemainingWeight, cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, wantCargo) {
		t.Fatalf("舱位 %s 清单错误: got=%v want=%v", id, got, wantCargo)
	}
}

// ---------- 多个舱位同时违规：先按舱位编号字典序选舱 ----------

// newCrossViolationRegistry 构造两个舱位在同一批安排完成后各自违规的
// 初始配载：
//   - C1（承重 1000）：GA 重 500、目的地上海、不允许混装，已在舱内；
//     把 GB（60、北京、允许混装）装入 C1 后只有混装冲突（共 560 千克）。
//   - C2（承重 50）：GC 重 40、目的地 X、允许混装，已在舱内；
//     把 GD（20、X、允许混装）装入 C2 后只有超重（共 60 千克）。
//
// reversed 为 true 时以相反次序登记舱位与货物，操作清单也由调用方调换；
// 正式提交报告的违规舱位与原因不应受登记或清单次序影响。
func newCrossViolationRegistry(t *testing.T, reversed bool) (*Registry, []Op) {
	t.Helper()
	r := NewRegistry()
	c1 := func() {
		if err := r.RegisterCompartment("C1", 1000); err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterCargo("GA", 500, "上海", false); err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterCargo("GB", 60, "北京", true); err != nil {
			t.Fatal(err)
		}
	}
	c2 := func() {
		if err := r.RegisterCompartment("C2", 50); err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterCargo("GC", 40, "X", true); err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterCargo("GD", 20, "X", true); err != nil {
			t.Fatal(err)
		}
	}
	if reversed {
		c2()
		c1()
	} else {
		c1()
		c2()
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "GA", Target: "C1"},
		{Kind: OpLoad, CargoID: "GC", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		{Kind: OpLoad, CargoID: "GB", Target: "C1"}, // C1 混装
		{Kind: OpLoad, CargoID: "GD", Target: "C2"}, // C2 超重
	}
	return r, ops
}

// assertCrossStateUnchanged 核对双舱违规被拒绝后，原配载与登记资料原样保留。
func assertCrossStateUnchanged(t *testing.T, r *Registry, c1Name string) {
	t.Helper()
	assertCargoState(t, r, "GA", 500, "上海", false, c1Name)
	assertCargoState(t, r, "GB", 60, "北京", true, "")
	assertCargoState(t, r, "GC", 40, "X", true, "C2")
	assertCargoState(t, r, "GD", 20, "X", true, "")
	assertCompartmentState(t, r, c1Name, 500, 500, []string{"GA"})
	assertCompartmentState(t, r, "C2", 40, 10, []string{"GC"})
}

// TestAdjustCrossViolationsReportsEarliestCompartment 是核心规则：C1 只有
// 混装冲突、C2 超重时，正式提交只报告一个结构化错误——舱位编号最靠前的
// C1 的混装冲突，而不是超重。
func TestAdjustCrossViolationsReportsEarliestCompartment(t *testing.T) {
	r, ops := newCrossViolationRegistry(t, false)

	res, err := r.Adjust("A1", ops)
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "GA" {
		t.Fatalf("应报告 C1 混装冲突并指向不允许混装的 GA，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("错误应携带调整编号 A1，实际 %q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "GA") {
		t.Fatalf("混装说明应指出冲突舱位 C1 与货物 GA: %s", msg)
	}
	assertCrossStateUnchanged(t, r, "C1")
}

// TestAdjustCrossViolationsOrderIndependent 保护次序无关性：把触发 C2 超重
// 的操作放在清单前面、并以相反次序登记舱位与货物，报告结果仍为 C1 的
// 混装冲突；调整编号带首尾空白时错误中携带去空白后的编号。
func TestAdjustCrossViolationsOrderIndependent(t *testing.T) {
	r, ops := newCrossViolationRegistry(t, true)
	reversed := []Op{ops[1], ops[0]}

	res, err := r.Adjust("\tA1 \n", reversed)
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "GA" {
		t.Fatalf("调换清单与登记次序后仍应报告 C1 混装冲突（GA），实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("应携带去掉首尾空白后的调整编号 A1，实际 %q", se.AdjustmentID)
	}
	assertCrossStateUnchanged(t, r, "C1")
}

// TestAdjustLexicographicCompartmentIDs 明确保护“字典序”而非数值序：
// C10（混装）编号在字符串字典序上先于 C2（超重），应报告 C10；调换登记
// 与清单次序不改变结果。
func TestAdjustLexicographicCompartmentIDs(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		name := "正常次序"
		if reversed {
			name = "相反登记次序"
		}
		t.Run(name, func(t *testing.T) {
			r := NewRegistry()
			c10 := func() {
				if err := r.RegisterCompartment("C10", 1000); err != nil {
					t.Fatal(err)
				}
				if err := r.RegisterCargo("GA", 500, "上海", false); err != nil {
					t.Fatal(err)
				}
				if err := r.RegisterCargo("GB", 60, "北京", true); err != nil {
					t.Fatal(err)
				}
			}
			c2 := func() {
				if err := r.RegisterCompartment("C2", 50); err != nil {
					t.Fatal(err)
				}
				if err := r.RegisterCargo("GC", 40, "X", true); err != nil {
					t.Fatal(err)
				}
				if err := r.RegisterCargo("GD", 20, "X", true); err != nil {
					t.Fatal(err)
				}
			}
			if reversed {
				c2()
				c10()
			} else {
				c10()
				c2()
			}
			if _, err := r.Adjust("A0", []Op{
				{Kind: OpLoad, CargoID: "GA", Target: "C10"},
				{Kind: OpLoad, CargoID: "GC", Target: "C2"},
			}); err != nil {
				t.Fatal(err)
			}
			ops := []Op{
				{Kind: OpLoad, CargoID: "GB", Target: "C10"}, // C10 混装
				{Kind: OpLoad, CargoID: "GD", Target: "C2"},  // C2 超重
			}
			if reversed {
				ops = []Op{ops[1], ops[0]}
			}
			res, err := r.Adjust("A1", ops)
			se := requireError(t, err, ErrMixedLoading)
			if res != nil {
				t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
			}
			if se.ID != "GA" || se.AdjustmentID != "A1" {
				t.Fatalf("字典序应先报告 C10 的混装冲突（GA），实际 ID=%q 调整=%q",
					se.ID, se.AdjustmentID)
			}
			if !strings.Contains(se.Error(), "C10") {
				t.Fatalf("说明应指出冲突舱位 C10: %s", se.Error())
			}
			assertCrossStateUnchanged(t, r, "C10")
		})
	}
}

// TestPreviewStillListsAllReasonsContrastsAdjust 保留两种入口各自的用途：
// 同一批安排 Preview 一次列出 C1 混装与 C2 超重两条原因且只读，而正式
// 提交只返回 C1 混装这一个结构化错误。
func TestPreviewStillListsAllReasonsContrastsAdjust(t *testing.T) {
	r, ops := newCrossViolationRegistry(t, false)

	pv, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Submittable || len(pv.Rejections) != 2 {
		t.Fatalf("预览应列出全部两条拒绝原因: %+v", pv.Rejections)
	}
	if pv.Rejections[0].CompartmentID != "C1" || pv.Rejections[0].Kind != ErrMixedLoading ||
		!reflect.DeepEqual(pv.Rejections[0].OffendingCargo, []string{"GA"}) {
		t.Fatalf("第一条应为 C1 混装且祸首 GA: %+v", pv.Rejections[0])
	}
	if pv.Rejections[1].CompartmentID != "C2" || pv.Rejections[1].Kind != ErrOverweight ||
		pv.Rejections[1].UsedWeight != 60 || pv.Rejections[1].MaxWeight != 50 {
		t.Fatalf("第二条应为 C2 超重（60/50）: %+v", pv.Rejections[1])
	}
	// 预览只读：实际配载保持提交前状态。
	assertCrossStateUnchanged(t, r, "C1")

	// 同一批安排正式提交仍只报告一个原因。
	res, err := r.Adjust("A1", ops)
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("正式提交被拒绝时不应返回成功结果: %+v", res)
	}
	if se.ID != "GA" {
		t.Fatalf("正式提交应只报告 C1 混装，实际 ID=%q", se.ID)
	}
}

// ---------- 同一舱位多种违规：重量原因先于混装 ----------

// TestAdjustOverweightBeforeMixedReportsCompartment 保护同一舱位既超重又
// 混装时先报告重量原因：错误对象是舱位，说明含预计总重量与最大承重。
func TestAdjustOverweightBeforeMixedReportsCompartment(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	// G0 在上海、不允许混装；G1 去北京、允许混装：共舱确有混装冲突，
	// 同时合计 60+50=110 超过承重 100。
	if err := r.RegisterCargo("G0", 60, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 50, "北京", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G0", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Adjust("  A1  ", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	se := requireError(t, err, ErrOverweight)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "C1" {
		t.Fatalf("重量原因应指向舱位 C1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("应携带去空白后的调整编号 A1，实际 %q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "110") || !strings.Contains(msg, "100") {
		t.Fatalf("超重说明应含舱位 C1、预计总重量 110 与最大承重 100: %s", msg)
	}
	// 整次不生效：G1 未装载，C1 仍是 G0 一件 60 千克。
	assertCargoState(t, r, "G1", 50, "北京", true, "")
	assertCompartmentState(t, r, "C1", 60, 40, []string{"G0"})
}

// TestAdjustOverflowBeforeMixedReportsCompartment 保护重量原因的另一形态：
// 合计溢出 int64 与混装冲突并存时，先报告溢出且对象仍是舱位。
func TestAdjustOverflowBeforeMixedReportsCompartment(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	// G1 在上海、不允许混装，已在舱内；G2 去北京、允许混装：
	// 合计 MaxInt64-10+20 溢出 int64，且目的地不同存在混装冲突。
	if err := r.RegisterCargo("G1", math.MaxInt64-10, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "北京", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
	se := requireError(t, err, ErrOverflow)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "C1" {
		t.Fatalf("溢出应指向舱位 C1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("应携带调整编号 A1，实际 %q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") {
		t.Fatalf("溢出说明应指出舱位 C1: %s", msg)
	}
	if strings.Contains(msg, "-") {
		t.Fatalf("溢出时不应给出回绕后的负数重量: %s", msg)
	}
	// 整次不生效。
	assertCargoState(t, r, "G2", 20, "北京", true, "")
	assertCompartmentState(t, r, "C1", math.MaxInt64-10, 10, []string{"G1"})
}

// ---------- 仅混装冲突：祸首按货物编号字典序，不取最后加入者 ----------

// TestAdjustMixedReportsExistingSmallestOffender 保护祸首可能来自原来就在
// 舱内的货物：已在 C1 的 G1（上海、不允许混装）编号小于后装入的 G3
// （北京、不允许混装），错误必须指向 G1，而不是最后加入的 G3。
func TestAdjustMixedReportsExistingSmallestOffender(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "北京", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Adjust(" A1 ", []Op{{Kind: OpLoad, CargoID: "G3", Target: "C1"}})
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("应指向编号最靠前且不允许混装的 G1（原舱货物），实际 ID=%q", se.ID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "G1") {
		t.Fatalf("混装说明应指出冲突舱位 C1 与货物 G1: %s", msg)
	}
	assertCargoState(t, r, "G3", 10, "北京", false, "")
	assertCompartmentState(t, r, "C1", 10, 990, []string{"G1"})
}

// TestAdjustMixedReportsSmallestOffenderAmongExistingAndIncoming 保护纯字典序：
// 原舱 G2（上海、不允许混装），本批先装 G3（北京、不允许混装）再装 G1
// （广州、不允许混装）。按“最先加入”会指向 G3、按“最后加入”也不稳定，
// 正确结果是全部祸首中编号最靠前的 G1，说明仍指出舱位 C1。
func TestAdjustMixedReportsSmallestOffenderAmongExistingAndIncoming(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 10, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "北京", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "广州", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	se := requireError(t, err, ErrMixedLoading)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "G1" {
		t.Fatalf("应指向祸首中编号最靠前的 G1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("应携带调整编号 A1，实际 %q", se.AdjustmentID)
	}
	if !strings.Contains(se.Error(), "C1") {
		t.Fatalf("混装说明应指出冲突舱位 C1: %s", se.Error())
	}
	// G1、G3 都未装载，C1 仍只有 G2。
	assertCargoState(t, r, "G1", 10, "广州", false, "")
	assertCargoState(t, r, "G3", 10, "北京", false, "")
	assertCompartmentState(t, r, "C1", 10, 990, []string{"G2"})
}

// ---------- 拒绝后整次调整不生效：合法的卸下/移动不得部分生效 ----------

// newPartialEffectRegistry 构造含四个舱位的初始配载：
//   - C1（1000）：GK 500 上海不允许混装；GL 60 北京允许混装尚未装载。
//   - C2（50）：GC 40（X）已装载；GD 20（X）尚未装载。
//   - C3（1000）：GU 30、GM 20，均为上海且不允许混装。
//   - C4（1000）：空舱。
//
// 一批四条操作各自合法：卸下 GU、移动 GM 到 C4（这两项单独都能成功）、
// 装 GL 到 C1（仅混装冲突）、装 GD 到 C2（仅超重）。
func newPartialEffectRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, c := range []struct {
		id string
		mw int64
	}{
		{"C1", 1000}, {"C2", 50}, {"C3", 1000}, {"C4", 1000},
	} {
		if err := r.RegisterCompartment(c.id, c.mw); err != nil {
			t.Fatal(err)
		}
	}
	cargos := []struct {
		id          string
		w           int64
		dest        string
		mixed       bool
		compartment string
	}{
		{"GK", 500, "上海", false, "C1"},
		{"GC", 40, "X", true, "C2"},
		{"GU", 30, "上海", false, "C3"},
		{"GM", 20, "上海", false, "C3"},
		{"GL", 60, "北京", true, ""},
		{"GD", 20, "X", true, ""},
	}
	for _, c := range cargos {
		if err := r.RegisterCargo(c.id, c.w, c.dest, c.mixed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "GK", Target: "C1"},
		{Kind: OpLoad, CargoID: "GC", Target: "C2"},
		{Kind: OpLoad, CargoID: "GU", Target: "C3"},
		{Kind: OpLoad, CargoID: "GM", Target: "C3"},
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// partialEffectOps 是上述四条操作的清单；reverse 调换清单次序。
func partialEffectOps(reverse bool) []Op {
	ops := []Op{
		{Kind: OpUnload, CargoID: "GU"},             // 单独可成功
		{Kind: OpMove, CargoID: "GM", Target: "C4"}, // 单独可成功
		{Kind: OpLoad, CargoID: "GL", Target: "C1"}, // C1 混装
		{Kind: OpLoad, CargoID: "GD", Target: "C2"}, // C2 超重
	}
	if reverse {
		reversed := make([]Op, len(ops))
		for i, op := range ops {
			reversed[len(ops)-1-i] = op
		}
		return reversed
	}
	return ops
}

// TestAdjustRejectionAtomicWithValidUnloadAndMove 保护原子性：清单中混有
// 本可成功的卸下与移动时，拒绝不能让它们单独生效；报告仍是最靠前违规舱位
// C1 的混装冲突。清单次序不影响结论。
func TestAdjustRejectionAtomicWithValidUnloadAndMove(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "正常次序"
		if reverse {
			name = "清单反序"
		}
		t.Run(name, func(t *testing.T) {
			r := newPartialEffectRegistry(t)

			res, err := r.Adjust(" A1 ", partialEffectOps(reverse))
			se := requireError(t, err, ErrMixedLoading)
			if res != nil {
				t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
			}
			if se.ID != "GK" {
				t.Fatalf("应报告最靠前舱位 C1 的混装冲突（GK），实际 ID=%q", se.ID)
			}
			if se.AdjustmentID != "A1" {
				t.Fatalf("应携带去空白后的调整编号 A1，实际 %q", se.AdjustmentID)
			}

			// 已装载货物全部留在原舱位，未装载货物仍未装载；
			// 尤其 GU 未被卸下、GM 没有被移到 C4。
			assertCargoState(t, r, "GK", 500, "上海", false, "C1")
			assertCargoState(t, r, "GC", 40, "X", true, "C2")
			assertCargoState(t, r, "GU", 30, "上海", false, "C3")
			assertCargoState(t, r, "GM", 20, "上海", false, "C3")
			assertCargoState(t, r, "GL", 60, "北京", true, "")
			assertCargoState(t, r, "GD", 20, "X", true, "")

			// 各舱位完整清单、已用与剩余重量与提交前一致。
			assertCompartmentState(t, r, "C1", 500, 500, []string{"GK"})
			assertCompartmentState(t, r, "C2", 40, 10, []string{"GC"})
			assertCompartmentState(t, r, "C3", 50, 950, []string{"GM", "GU"})
			assertCompartmentState(t, r, "C4", 0, 1000, []string{})

			// 失败不占用调整编号：同一编号随后可用于一次合法调整。
			ok, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "GD", Target: "C4"}})
			if err != nil {
				t.Fatalf("失败调整不应占用编号，合法调整应能使用 A1: %v", err)
			}
			if ok.ID != "A1" {
				t.Fatalf("重试结果编号错误: %+v", ok)
			}
			assertCargoState(t, r, "GD", 20, "X", true, "C4")
			assertCompartmentState(t, r, "C4", 20, 980, []string{"GD"})
			// 重试不影响其他舱位。
			assertCompartmentState(t, r, "C3", 50, 950, []string{"GM", "GU"})
		})
	}
}

// ---------- 合法配载边界：同目的地共舱、恰好达到承重必须成功 ----------

// TestAdjustSameDestinationExactCapacitySucceeds 防止把合法配载一并挡住：
// 两件不允许混装但同一目的地的货物共舱、总重量恰好等于承重应成功；同批
// 中一项合法移动也应正常生效，查询结果与成功调整结果相互对应。
func TestAdjustSameDestinationExactCapacitySucceeds(t *testing.T) {
	r := NewRegistry()
	for _, c := range []struct {
		id string
		mw int64
	}{
		{"C1", 100}, {"C2", 100}, {"C3", 100},
	} {
		if err := r.RegisterCompartment(c.id, c.mw); err != nil {
			t.Fatal(err)
		}
	}
	// G1 60 上海不允许混装在 C1；G3 10 上海不允许混装在 C2；
	// G2 40 上海不允许混装尚未装载。
	if err := r.RegisterCargo("G1", 60, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 40, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "上海", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 装 G2 到 C1：同目的地共舱且恰好 100 千克；同时把 G3 从 C2 移到 C3。
	res, err := r.Adjust("A1", []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpMove, CargoID: "G3", Target: "C3"},
	})
	if err != nil {
		t.Fatalf("同目的地共舱且总重量恰好达到承重应成功: %v", err)
	}
	if res == nil || res.ID != "A1" {
		t.Fatalf("应返回编号为 A1 的成功调整结果: %+v", res)
	}
	wantChanges := []CargoChange{
		{CargoID: "G2", From: "", To: "C1"},
		{CargoID: "G3", From: "C2", To: "C3"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
		t.Fatalf("货物变化错误: %+v want %+v", res.CargoChanges, wantChanges)
	}
	wantCompChanges := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 60, WeightAfter: 100},
		{CompartmentID: "C2", WeightBefore: 10, WeightAfter: 0},
		{CompartmentID: "C3", WeightBefore: 0, WeightAfter: 10},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantCompChanges) {
		t.Fatalf("舱位变化错误: %+v want %+v", res.CompartmentChanges, wantCompChanges)
	}

	// 查询：C1 恰好满载、C2 清空、C3 含 G3；货物资料保持原样。
	assertCompartmentState(t, r, "C1", 100, 0, []string{"G1", "G2"})
	assertCompartmentState(t, r, "C2", 0, 100, []string{})
	assertCompartmentState(t, r, "C3", 10, 90, []string{"G3"})
	assertCargoState(t, r, "G1", 60, "上海", false, "C1")
	assertCargoState(t, r, "G2", 40, "上海", false, "C1")
	assertCargoState(t, r, "G3", 10, "上海", false, "C3")
}
