package stowage

import (
	"reflect"
	"testing"
)

// 本文件固定“已装货舱位再次登记”的回归行为：舱位登记只用于新增舱位，
// 同一编号（去首尾空白后识别）再次登记一律返回 ErrAlreadyExists，
// 既不能借再次登记改变最大承重，也不能把原舱位重建为空舱；拒绝不得破坏
// 既有货物归属与重量核对，随后的预览与正式配载调整必须继续按原舱位
// （最大承重 100 千克）判断。
//
// 主要使用条件：C1 最大承重 100 千克；G1（40 千克）、G2（30 千克）
// 已装入 C1，两件货物同目的地、当前配载合法（已用 70、剩余 30）；
// G3（40 千克，同目的地）尚未装载。

// newReregisterRegistry 构造已装货舱位场景：C1 承重 100，G1、G2 已装入，
// G3 尚未装载。舱位首次登记时编号带首尾空白，验证登记本身按去空白识别。
func newReregisterRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("  C1  ", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 40, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 40, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Adjust("A0", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// assertOriginalStowageIntact 核对重复登记被拒绝后，舱位与三件货物的
// 全部状态仍与首次合法配载一致：承重 100、已用 70、剩余 30，舱内恰为
// G1、G2 两件（不丢失、不重复、不被清空），三件货物的重量、目的地、
// 混装许可与所属舱位均保持原样。
func assertOriginalStowageIntact(t *testing.T, r *Registry) {
	t.Helper()
	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatal(err)
	}
	if cpt.MaxWeight != 100 || cpt.UsedWeight != 70 || cpt.RemainingWeight != 30 {
		t.Fatalf("重复登记不得改变舱位承重与占用，应为 100/70/30: %+v", cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("舱内清单应仍是 G1、G2（不丢失不重复）: %v", got)
	}
	g1, _ := r.Cargo("G1")
	if g1.Weight != 40 || g1.Destination != "Shanghai" || g1.AllowMixed ||
		!g1.Loaded || g1.CompartmentID != "C1" {
		t.Fatalf("G1 资料与归属应保持原样: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 30 || g2.Destination != "Shanghai" || g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("G2 资料与归属应保持原样: %+v", g2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Weight != 40 || g3.Destination != "Shanghai" || g3.AllowMixed ||
		g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("未装载的 G3 不应被重复登记波及: %+v", g3)
	}
}

// 已装货舱位用同一编号再次登记：无论新承重比原承重大（200）、低于当前
// 占用（50）还是与原承重相同（100），也无论编号是否带首尾空格或制表符，
// 都返回“对象已存在”的结构化错误，错误中的对象编号是去空白后的编号，
// 且每次拒绝后原配载都完好无损。
func TestReregisterLoadedCompartmentAlwaysRejected(t *testing.T) {
	r := newReregisterRegistry(t)
	cases := []struct {
		name      string
		id        string
		maxWeight int64
	}{
		{"更大承重200", "C1", 200},
		{"低于当前占用50", "C1", 50},
		{"与原承重相同100", "C1", 100},
		{"更大承重且编号带空格", "  C1  ", 200},
		{"低于占用且编号带制表符", "\tC1\t", 50},
		{"相同承重且编号首尾空白", " \tC1\n ", 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.RegisterCompartment(tc.id, tc.maxWeight)
			se := requireError(t, err, ErrAlreadyExists)
			if se.Kind.String() != "对象已存在" {
				t.Fatalf("错误类别说明应为对象已存在，实际为 %q", se.Kind.String())
			}
			// 对象编号一律使用去首尾空白后的值，登记错误不涉及调整编号。
			if se.ID != "C1" {
				t.Fatalf("错误应指出去空白后的舱位编号 C1，实际 ID=%q", se.ID)
			}
			if se.AdjustmentID != "" {
				t.Fatalf("登记错误不应携带调整编号: %+v", se)
			}
			// 每次拒绝后立即核对：承重、占用、清单与货物归属都原样保留。
			assertOriginalStowageIntact(t, r)
		})
	}

	// 带空白的编号仍指向原舱位：按带空白编号查到的仍是原舱位原配载，
	// 并没有因重复登记被重建为空舱或另建舱位。
	cpt, err := r.Compartment("\t C1 \t")
	if err != nil {
		t.Fatal(err)
	}
	if cpt.MaxWeight != 100 || cpt.UsedWeight != 70 || cpt.RemainingWeight != 30 {
		t.Fatalf("带空白编号查询应仍指向原舱位: %+v", cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("原舱位不应被重建为空舱: %v", got)
	}
}

// 重复登记被拒绝后，配载判断必须继续使用原舱位的 100 千克承重：
// 再装入一件 40 千克、同目的地的 G3 时，预览应显示预计 110 千克、
// 超重 10 千克且不可提交；正式提交应因超重失败，失败后原两件货物仍在
// 舱内、G3 仍未装载。既不能因填过 200 千克而放行，也不能按 50 千克
// 把当前合法配载误判为超重。
func TestStowageJudgedByOriginalCapacityAfterReregister(t *testing.T) {
	r := newReregisterRegistry(t)
	// 先尝试放大到 200、再压低到 50，均被拒绝。
	requireError(t, r.RegisterCompartment("C1", 200), ErrAlreadyExists)
	requireError(t, r.RegisterCompartment("  C1  ", 50), ErrAlreadyExists)

	// 预览按原承重 100 判断：70 + 40 = 110，超重 10，不可提交。
	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G3", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable || len(res.Rejections) != 1 {
		t.Fatalf("预计超重应不可提交且恰有一条拒绝原因: %+v", res)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrOverweight || rej.CompartmentID != "C1" ||
		rej.MaxWeight != 100 || rej.UsedWeight != 110 ||
		rej.RemainingWeight != -10 || rej.Overweight != 10 {
		t.Fatalf("超重原因应按原承重 100 计为 110/超 10: %+v", rej)
	}
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{{CargoID: "G3", From: "", To: "C1"}}) {
		t.Fatalf("应列出 G3 的预计变化: %+v", res.CargoChanges)
	}
	if len(res.Compartments) != 1 || res.Compartments[0].ID != "C1" {
		t.Fatalf("应只列出受影响的 C1: %+v", res.Compartments)
	}
	cp := res.Compartments[0]
	if cp.Before.MaxWeight != 100 || cp.Before.UsedWeight != 70 || cp.Before.RemainingWeight != 30 ||
		!reflect.DeepEqual(cargoIDs(cp.Before), []string{"G1", "G2"}) {
		t.Fatalf("预览 Before 应为原配载 100/70/30: %+v", cp.Before)
	}
	if cp.After.MaxWeight != 100 || cp.After.UsedWeight != 110 || cp.After.RemainingWeight != -10 {
		t.Fatalf("预览 After 应显示预计 110、剩余 -10: %+v", cp.After)
	}
	if got := cargoIDs(cp.After); !reflect.DeepEqual(got, []string{"G1", "G2", "G3"}) {
		t.Fatalf("预计清单应含三件货物: %v", got)
	}
	for _, cv := range cp.After.Cargo {
		if !cv.Loaded || cv.CompartmentID != "C1" {
			t.Fatalf("预计清单中的货物应显示已装载于 C1: %+v", cv)
		}
	}
	// 预览只读，配载不变。
	assertOriginalStowageIntact(t, r)

	// 正式提交同样按原承重拒绝：整次不生效。
	se := requireError(t, func() error {
		_, e := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G3", Target: "C1"}})
		return e
	}(), ErrOverweight)
	if se.ID != "C1" {
		t.Fatalf("超重应指出原舱位 C1，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "A1" {
		t.Fatalf("应携带调整编号 A1，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	// 失败后原两件货物仍在舱内，新货物仍未装载，重量核对仍是 100/70/30。
	assertOriginalStowageIntact(t, r)
}

// 重复登记被拒绝、超重装入也失败后，原舱位仍可正常调整：卸下 40 千克的
// G1 应成功收回占用，舱位变为已用 30、剩余 70；G2 保持原归属，G1 与
// G3 的货物记录保留且显示未装载。
func TestUnloadAfterReregisterRejectionReclaimsWeight(t *testing.T) {
	r := newReregisterRegistry(t)
	requireError(t, r.RegisterCompartment("\tC1\t", 200), ErrAlreadyExists)
	requireError(t, r.RegisterCompartment("C1", 50), ErrAlreadyExists)
	requireError(t, func() error {
		_, e := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G3", Target: "C1"}})
		return e
	}(), ErrOverweight)

	// 从原两件货物中卸下 40 千克的 G1，正常成功。
	res, err := r.Adjust("A2", []Op{{Kind: OpUnload, CargoID: "G1"}})
	if err != nil {
		t.Fatalf("原舱位卸下货物应成功: %v", err)
	}
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{{CargoID: "G1", From: "C1", To: ""}}) {
		t.Fatalf("应列出 G1 从 C1 卸下的变化: %+v", res.CargoChanges)
	}
	if !reflect.DeepEqual(res.CompartmentChanges, []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 70, WeightAfter: 30},
	}) {
		t.Fatalf("C1 重量应 70 -> 30: %+v", res.CompartmentChanges)
	}

	// 查询：承重仍为 100，已用 30、剩余 70，舱内只剩 G2。
	cpt, _ := r.Compartment("C1")
	if cpt.MaxWeight != 100 || cpt.UsedWeight != 30 || cpt.RemainingWeight != 70 {
		t.Fatalf("卸下后应为 100/30/70: %+v", cpt)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G2"}) {
		t.Fatalf("舱内应只剩 G2: %v", got)
	}
	// G2 保持原归属与登记资料。
	g2, _ := r.Cargo("G2")
	if g2.Weight != 30 || g2.Destination != "Shanghai" || g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("G2 应保持原归属与资料: %+v", g2)
	}
	// 被卸下的 G1 记录保留，显示未装载，登记资料不变。
	g1, _ := r.Cargo("G1")
	if g1.Weight != 40 || g1.Destination != "Shanghai" || g1.AllowMixed ||
		g1.Loaded || g1.CompartmentID != "" {
		t.Fatalf("G1 卸下后记录应保留且为未装载: %+v", g1)
	}
	// 未能装入的 G3 依旧未装载。
	g3, _ := r.Cargo("G3")
	if g3.Loaded || g3.CompartmentID != "" {
		t.Fatalf("G3 应仍未装载: %+v", g3)
	}
}
