package stowage

import (
	"reflect"
	"testing"
)

// 本文件为“已配载舱位再次登记”补充回归保障：舱位登记只用于新增舱位，
// 已有编号不能借再次登记改变最大承重，也不能把原舱位重建为空舱。
// 对已装货舱位的重复登记一律返回 ErrAlreadyExists（“对象已存在”），
// 错误对象编号为去首尾空白后的舱位编号；拒绝不得破坏货物归属与重量核对。
// 拒绝之后的查询、预览与正式提交（Adjust）必须继续按原舱位判断。

const (
	rerCompartmentID = "C1"
	rerMaxWeight     = int64(100)
	rerDestination   = "上海"
)

// setupReregisterScenario 建立主使用条件：C1 最大承重 100 千克，已装入
// G1（40 千克，不允许混装）与 G2（30 千克，允许混装），两件货物目的地
// 相同，当前配载合法（已用 70、剩余 30）；另有一件同目的地、40 千克的
// 未装载货物 G3，供后续配载判断使用。
func setupReregisterScenario(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, rerCompartmentID, rerMaxWeight)
	mustRegisterCargo(t, r, "G1", 40, rerDestination, false)
	mustRegisterCargo(t, r, "G2", 30, rerDestination, true)
	mustRegisterCargo(t, r, "G3", 40, rerDestination, true)
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: rerCompartmentID},
		{Kind: OpLoad, CargoID: "G2", Target: rerCompartmentID},
	})
	return r
}

// assertOriginalStowageIntact 查询确认拒绝后配载原样：舱位仍为承重 100、
// 已用 70、剩余 30，舱内清单恰为 G1、G2（不丢失也不重复），两件货物的
// 重量、目的地、混装许可与所属舱位保持原样，G3 仍未装载。
func assertOriginalStowageIntact(t *testing.T, r *Registry) {
	t.Helper()
	view, err := r.Compartment(rerCompartmentID)
	if err != nil {
		t.Fatalf("查询舱位失败: %v", err)
	}
	if view.MaxWeight != 100 || view.UsedWeight != 70 || view.RemainingWeight != 30 {
		t.Fatalf("舱位承重或占用被再次登记改变: %+v", view)
	}
	if !reflect.DeepEqual(cargoIDs(*view), []string{"G1", "G2"}) {
		t.Fatalf("舱内清单丢失或重复: %+v", view.Cargo)
	}

	g1, err := r.Cargo("G1")
	if err != nil {
		t.Fatal(err)
	}
	if g1.Weight != 40 || g1.Destination != rerDestination || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != rerCompartmentID {
		t.Fatalf("G1 资料或归属被改变: %+v", g1)
	}
	g2, err := r.Cargo("G2")
	if err != nil {
		t.Fatal(err)
	}
	if g2.Weight != 30 || g2.Destination != rerDestination || !g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != rerCompartmentID {
		t.Fatalf("G2 资料或归属被改变: %+v", g2)
	}
	g3, err := r.Cargo("G3")
	if err != nil {
		t.Fatal(err)
	}
	if g3.Weight != 40 || g3.Destination != rerDestination || !g3.AllowMixed ||
		g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("G3 本应保持未装载: %+v", g3)
	}
}

// TestReregisterLoadedCompartmentAlwaysAlreadyExists 用同一舱位编号再次
// 登记：承重调高到 200、压低到当前占用 70 以下的 50，以及与原承重相同
// 的 100，三种取值一律按“对象已存在”拒绝；首尾带空格或制表符仍指向
// 原舱位，拒绝类别不变，错误中的对象编号一律是去空白后的 C1。
func TestReregisterLoadedCompartmentAlwaysAlreadyExists(t *testing.T) {
	r := setupReregisterScenario(t)

	// 原编号直接重复：200 高于原承重、50 低于当前占用、100 与原承重相同。
	for _, w := range []int64{200, 50, 100} {
		err := r.RegisterCompartment(rerCompartmentID, w)
		se := requireError(t, err, ErrAlreadyExists)
		if se.Kind.String() != "对象已存在" {
			t.Fatalf("承重 %d 的重复登记错误类别说明异常: %s", w, se.Kind)
		}
		if se.ID != rerCompartmentID {
			t.Fatalf("承重 %d 的重复登记错误应指出舱位编号 %q，实际 %q",
				w, rerCompartmentID, se.ID)
		}
	}

	// 同一编号首尾带空白或制表符时仍指向原舱位，同样拒绝；即便再给的
	// 承重仍是原来的 100 千克，也是重复登记而不是更新成功。
	paddedIDs := []string{"  C1  ", "\tC1\t", " \t C1 \t "}
	for _, id := range paddedIDs {
		for _, w := range []int64{200, 50, 100} {
			err := r.RegisterCompartment(id, w)
			se := requireError(t, err, ErrAlreadyExists)
			if se.ID != rerCompartmentID {
				t.Fatalf("编号 %q、承重 %d 的重复登记错误对象应为去空白后的 %q，实际 %q",
					id, w, rerCompartmentID, se.ID)
			}
		}
	}

	assertOriginalStowageIntact(t, r)
}

// TestStowageAfterReregisterJudgedByOriginalCompartment 重复登记被拒绝后，
// 配载判断必须继续使用原舱位（承重 100）：再装入同目的地 40 千克的 G3
// 时，预览应显示预计 110 千克、超重 10 千克且不可提交；正式提交应因
// 超重失败。既不能因填过 200 而放行，也不能按 50 把现有合法配载判成
// 超重（当前 70 本就高于 50）。失败后原两件货物仍在舱内，G3 仍未装载。
func TestStowageAfterReregisterJudgedByOriginalCompartment(t *testing.T) {
	r := setupReregisterScenario(t)
	requireError(t, r.RegisterCompartment(rerCompartmentID, 200), ErrAlreadyExists)
	requireError(t, r.RegisterCompartment(rerCompartmentID, 50), ErrAlreadyExists)

	loadG3 := []Op{{Kind: OpLoad, CargoID: "G3", Target: rerCompartmentID}}

	// 预览：预计 110、剩余 -10、超重 10，按原承重 100 判断，不可提交。
	res, err := r.Preview(loadG3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable || len(res.Rejections) != 1 {
		t.Fatalf("预计超重应不可提交且恰有一条拒绝原因: %+v", res)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrOverweight || rej.CompartmentID != rerCompartmentID ||
		rej.MaxWeight != 100 || rej.UsedWeight != 110 ||
		rej.RemainingWeight != -10 || rej.Overweight != 10 {
		t.Fatalf("超重预览内容错误: %+v", rej)
	}
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{
		{CargoID: "G3", From: "", To: rerCompartmentID},
	}) {
		t.Fatalf("预览变化记录错误: %+v", res.CargoChanges)
	}
	var after CompartmentView
	for _, cp := range res.Compartments {
		if cp.ID == rerCompartmentID {
			after = cp.After
		}
	}
	if after.ID != rerCompartmentID || after.MaxWeight != 100 ||
		after.UsedWeight != 110 || after.RemainingWeight != -10 ||
		!reflect.DeepEqual(cargoIDs(after), []string{"G1", "G2", "G3"}) {
		t.Fatalf("预计配载应按原承重 100 展示三件货物: %+v", after)
	}
	// 预览只读：原配载不变。
	assertOriginalStowageIntact(t, r)

	// 正式提交：因超重失败，返回单个结构化错误且没有成功结果。
	se := submitExpectReject(t, r, "A2", loadG3)
	if se.Kind != ErrOverweight || se.ID != rerCompartmentID || se.AdjustmentID != "A2" {
		t.Fatalf("正式提交应返回指向 C1、调整 A2 的超重错误: %+v", se)
	}

	// 失败后原两件货物仍在舱内，G3 仍未装载，重量核对仍为 100/70/30。
	assertOriginalStowageIntact(t, r)
}

// TestUnloadAfterReregisterReclaimsOriginalOccupancy 重复登记被拒绝后，
// 从原两件货物中卸下 G1（40 千克）应成功收回占用：舱位变为已用 30、
// 剩余 70，G2 保持原归属；被卸下的 G1 记录保留并显示未装载。
func TestUnloadAfterReregisterReclaimsOriginalOccupancy(t *testing.T) {
	r := setupReregisterScenario(t)
	requireError(t, r.RegisterCompartment("  C1  ", 200), ErrAlreadyExists)
	requireError(t, r.RegisterCompartment("\tC1\t", 50), ErrAlreadyExists)
	requireError(t, r.RegisterCompartment(rerCompartmentID, 100), ErrAlreadyExists)

	mustAdjust(t, r, "A2", []Op{{Kind: OpUnload, CargoID: "G1"}})

	view, err := r.Compartment(rerCompartmentID)
	if err != nil {
		t.Fatal(err)
	}
	if view.MaxWeight != 100 || view.UsedWeight != 30 || view.RemainingWeight != 70 {
		t.Fatalf("卸下后舱位核对错误: %+v", view)
	}
	if !reflect.DeepEqual(cargoIDs(*view), []string{"G2"}) {
		t.Fatalf("卸下后舱内应只剩 G2: %+v", view.Cargo)
	}

	// G2 保持原归属与登记资料。
	g2, err := r.Cargo("G2")
	if err != nil {
		t.Fatal(err)
	}
	if g2.Weight != 30 || g2.Destination != rerDestination || !g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != rerCompartmentID {
		t.Fatalf("G2 应保持原归属: %+v", g2)
	}

	// G1 记录保留（重量、目的地、混装许可不变），显示未装载。
	g1, err := r.Cargo("G1")
	if err != nil {
		t.Fatalf("卸下后货物记录应保留，查询却失败: %v", err)
	}
	if g1.Weight != 40 || g1.Destination != rerDestination || g1.AllowMixed ||
		g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("G1 卸下后应为未装载且资料保留: %+v", g1)
	}
}
