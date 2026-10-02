package stowage

import (
	"math"
	"reflect"
	"testing"
)

// loadAll 登记舱位与货物并按 ops 完成若干次正式调整的测试辅助。
func mustAdjust(t *testing.T, r *Registry, id string, ops []Op) *AdjustmentResult {
	t.Helper()
	res, err := r.Adjust(id, ops)
	if err != nil {
		t.Fatalf("调整 %s 意外失败: %v", id, err)
	}
	return res
}

// cargoIDs 取出舱位快照中的货物编号清单。
func cargoIDs(v CompartmentView) []string {
	ids := make([]string, 0, len(v.Cargo))
	for _, c := range v.Cargo {
		ids = append(ids, c.ID)
	}
	return ids
}

// ---------- 可提交预览 ----------

func TestPreviewLoadSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", false); err != nil {
		t.Fatal(err)
	}
	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable || len(res.Rejections) != 0 {
		t.Fatalf("合法预览应可提交且无拒绝原因: %+v", res)
	}
	// 货物原舱位为空编号，预计舱位为 C1。
	if !reflect.DeepEqual(res.CargoChanges, []CargoChange{{CargoID: "G1", From: "", To: "C1"}}) {
		t.Fatalf("货物变化错误: %+v", res.CargoChanges)
	}
	if len(res.Compartments) != 1 {
		t.Fatalf("应列出 1 个受影响舱位: %+v", res.Compartments)
	}
	cp := res.Compartments[0]
	if cp.ID != "C1" {
		t.Fatalf("舱位编号错误: %+v", cp)
	}
	if cp.Before.UsedWeight != 0 || cp.Before.RemainingWeight != 100 || len(cp.Before.Cargo) != 0 {
		t.Fatalf("调整前配载错误: %+v", cp.Before)
	}
	if cp.After.UsedWeight != 30 || cp.After.RemainingWeight != 70 {
		t.Fatalf("预计配载重量错误: %+v", cp.After)
	}
	if got := cargoIDs(cp.After); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("预计货物清单错误: %v", got)
	}
	// 预览不改变状态。
	cv, _ := r.Cargo("G1")
	if cv.Loaded || cv.CompartmentID != "" {
		t.Fatalf("预览不应改变货物状态: %+v", cv)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 0 || len(cpt.Cargo) != 0 {
		t.Fatalf("预览不应改变舱位占用: %+v", cpt)
	}
}

func TestPreviewMixedOperationsSortedWithFullLists(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 30, "Y", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G4", 5, "X", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	})

	// 故意打乱输入顺序：装 G4 到 C1、移 G2 到 C2、卸 G1。
	res, err := r.Preview([]Op{
		{Kind: OpMove, CargoID: "G2", Target: "C2"},
		{Kind: OpUnload, CargoID: "G1"},
		{Kind: OpLoad, CargoID: "G4", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("应可提交: %+v", res.Rejections)
	}
	// 货物变化按编号字典序，未装载用空编号。
	wantCargo := []CargoChange{
		{CargoID: "G1", From: "C1", To: ""},
		{CargoID: "G2", From: "C1", To: "C2"},
		{CargoID: "G4", From: "", To: "C1"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantCargo) {
		t.Fatalf("货物变化错误: %+v", res.CargoChanges)
	}
	// 受影响舱位按编号字典序。
	if len(res.Compartments) != 2 || res.Compartments[0].ID != "C1" || res.Compartments[1].ID != "C2" {
		t.Fatalf("舱位排列错误: %+v", res.Compartments)
	}
	c1 := res.Compartments[0]
	if c1.Before.UsedWeight != 30 || c1.Before.RemainingWeight != 70 {
		t.Fatalf("C1 调整前重量错误: %+v", c1.Before)
	}
	if got := cargoIDs(c1.Before); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("C1 调整前清单错误: %v", got)
	}
	if c1.After.UsedWeight != 5 || c1.After.RemainingWeight != 95 {
		t.Fatalf("C1 预计重量错误: %+v", c1.After)
	}
	if got := cargoIDs(c1.After); !reflect.DeepEqual(got, []string{"G4"}) {
		t.Fatalf("C1 预计清单错误: %v", got)
	}
	c2 := res.Compartments[1]
	if c2.Before.UsedWeight != 30 || c2.Before.RemainingWeight != 70 {
		t.Fatalf("C2 调整前重量错误: %+v", c2.Before)
	}
	if got := cargoIDs(c2.Before); !reflect.DeepEqual(got, []string{"G3"}) {
		t.Fatalf("C2 调整前清单错误: %v", got)
	}
	if c2.After.UsedWeight != 50 || c2.After.RemainingWeight != 50 {
		t.Fatalf("C2 预计重量错误: %+v", c2.After)
	}
	if got := cargoIDs(c2.After); !reflect.DeepEqual(got, []string{"G2", "G3"}) {
		t.Fatalf("C2 预计清单错误: %v", got)
	}
	// 状态不变。
	cv1, _ := r.Cargo("G1")
	cv4, _ := r.Cargo("G4")
	if cv1.CompartmentID != "C1" || cv4.Loaded {
		t.Fatalf("预览不应改变状态: %+v %+v", cv1, cv4)
	}
}

func TestPreviewSwapListsBothCompartmentsEvenWithSameWeight(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 30); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 30); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "X", false); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	})
	res, err := r.Preview([]Op{
		{Kind: OpMove, CargoID: "G2", Target: "C1"},
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("满载舱位交换最终合法，应可提交: %+v", res.Rejections)
	}
	if len(res.Compartments) != 2 {
		t.Fatalf("即使总重量不变，交换的两个舱位都要列出: %+v", res.Compartments)
	}
	wantCargo := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G2", From: "C2", To: "C1"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantCargo) {
		t.Fatalf("交换货物变化错误: %+v", res.CargoChanges)
	}
	c1, c2 := res.Compartments[0], res.Compartments[1]
	if c1.ID != "C1" || c2.ID != "C2" {
		t.Fatalf("舱位排序错误: %s %s", c1.ID, c2.ID)
	}
	if got := cargoIDs(c1.Before); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("C1 调整前清单错误: %v", got)
	}
	if got := cargoIDs(c1.After); !reflect.DeepEqual(got, []string{"G2"}) {
		t.Fatalf("C1 预计清单错误: %v", got)
	}
	if got := cargoIDs(c2.Before); !reflect.DeepEqual(got, []string{"G2"}) {
		t.Fatalf("C2 调整前清单错误: %v", got)
	}
	if got := cargoIDs(c2.After); !reflect.DeepEqual(got, []string{"G1"}) {
		t.Fatalf("C2 预计清单错误: %v", got)
	}
	for _, cp := range []CompartmentPreview{c1, c2} {
		if cp.Before.UsedWeight != 30 || cp.After.UsedWeight != 30 ||
			cp.Before.RemainingWeight != 0 || cp.After.RemainingWeight != 0 {
			t.Fatalf("交换前后重量应均为 30/0: %+v", cp)
		}
	}
}

// ---------- 拒绝原因 ----------

func TestPreviewOverweightStillReturnsChanges(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 50); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable || len(res.Rejections) != 1 {
		t.Fatalf("超重应不可提交且恰有一条原因: %+v", res)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrOverweight || rej.CompartmentID != "C1" ||
		rej.MaxWeight != 50 || rej.UsedWeight != 60 ||
		rej.RemainingWeight != -10 || rej.Overweight != 10 {
		t.Fatalf("超重原因内容错误: %+v", rej)
	}
	if len(rej.Destinations) != 0 || len(rej.OffendingCargo) != 0 {
		t.Fatalf("超重原因不应携带混装信息: %+v", rej)
	}
	// 预计变化照常返回。
	if len(res.CargoChanges) != 2 {
		t.Fatalf("仍应列出货物变化: %+v", res.CargoChanges)
	}
	after := res.Compartments[0].After
	if after.UsedWeight != 60 || after.RemainingWeight != -10 ||
		!reflect.DeepEqual(cargoIDs(after), []string{"G1", "G2"}) {
		t.Fatalf("预计配载应照常返回: %+v", after)
	}
	// 状态不变。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 0 || len(cpt.Cargo) != 0 {
		t.Fatalf("预览不应改变舱位: %+v", cpt)
	}
}

func TestPreviewMixedLoadingListsDestinationsAndOffenders(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	// G1 上海不允许混装，G2 北京允许，G3 广州不允许混装。
	if err := r.RegisterCargo("G1", 10, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 30, "Guangzhou", false); err != nil {
		t.Fatal(err)
	}
	res, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable || len(res.Rejections) != 1 {
		t.Fatalf("混装冲突应不可提交: %+v", res)
	}
	rej := res.Rejections[0]
	if rej.Kind != ErrMixedLoading || rej.CompartmentID != "C1" {
		t.Fatalf("拒绝原因错误: %+v", rej)
	}
	// 目的地按字典序，对应货物按编号字典序。
	wantDests := []MixedDestination{
		{Destination: "Beijing", CargoIDs: []string{"G2"}},
		{Destination: "Guangzhou", CargoIDs: []string{"G3"}},
		{Destination: "Shanghai", CargoIDs: []string{"G1"}},
	}
	if !reflect.DeepEqual(rej.Destinations, wantDests) {
		t.Fatalf("目的地分组错误: %+v", rej.Destinations)
	}
	if !reflect.DeepEqual(rej.OffendingCargo, []string{"G1", "G3"}) {
		t.Fatalf("应列出全部不允许混装的货物: %+v", rej.OffendingCargo)
	}
}

func TestPreviewRejectionsOrderedAcrossCompartmentsAndCauses(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 50); err != nil {
		t.Fatal(err)
	}
	// C1：G1（上海，不许混装）+ G2（北京）共 120 千克，同时超重与混装。
	if err := r.RegisterCargo("G1", 60, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 60, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	// C2：G3+G4 共 60 千克，仅超重。
	if err := r.RegisterCargo("G3", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G4", 30, "X", true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Preview([]Op{
		{Kind: OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable {
		t.Fatal("应不可提交")
	}
	got := make([]struct {
		comp string
		kind ErrorKind
	}, len(res.Rejections))
	for i, x := range res.Rejections {
		got[i] = struct {
			comp string
			kind ErrorKind
		}{x.CompartmentID, x.Kind}
	}
	want := []struct {
		comp string
		kind ErrorKind
	}{
		{"C1", ErrOverweight},
		{"C1", ErrMixedLoading},
		{"C2", ErrOverweight},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("拒绝原因次序错误: %+v", got)
	}
	// 两个受影响舱位都给出完整前后配载。
	if len(res.Compartments) != 2 ||
		res.Compartments[0].ID != "C1" || res.Compartments[1].ID != "C2" {
		t.Fatalf("舱位排列错误: %+v", res.Compartments)
	}
}

func TestPreviewOverflowReplacesOverweightAndOmitsNumbers(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", math.MaxInt64, "X", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", math.MaxInt64, "Y", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
	res, err := r.Preview([]Op{{Kind: OpLoad, CargoID: "G2", Target: "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Submittable || len(res.Rejections) != 2 {
		t.Fatalf("溢出与混装应同时报告: %+v", res)
	}
	ov := res.Rejections[0]
	if ov.Kind != ErrOverflow || ov.CompartmentID != "C1" || ov.MaxWeight != math.MaxInt64 {
		t.Fatalf("首条应为重量溢出: %+v", ov)
	}
	if ov.UsedWeight != 0 || ov.RemainingWeight != 0 || ov.Overweight != 0 {
		t.Fatalf("溢出舱位不应提供重量数值: %+v", ov)
	}
	// 混装原因照常返回。
	mx := res.Rejections[1]
	if mx.Kind != ErrMixedLoading || !reflect.DeepEqual(mx.OffendingCargo, []string{"G1"}) {
		t.Fatalf("混装原因应照常返回: %+v", mx)
	}
	// 舱位货物清单照常返回，但预计已用/剩余重量不给数值。
	after := res.Compartments[0].After
	if !reflect.DeepEqual(cargoIDs(after), []string{"G1", "G2"}) {
		t.Fatalf("预计货物清单应照常返回: %v", cargoIDs(after))
	}
	if after.UsedWeight != 0 || after.RemainingWeight != 0 {
		t.Fatalf("溢出时预计重量数值应为零值: %+v", after)
	}
	// 状态不变。
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != math.MaxInt64 || !reflect.DeepEqual(cargoIDs(*cpt), []string{"G1"}) {
		t.Fatalf("预览不应改变配载: %+v", cpt)
	}
}

// ---------- 空清单与非法操作 ----------

func TestPreviewEmptyList(t *testing.T) {
	r := NewRegistry()
	for _, ops := range [][]Op{nil, {}} {
		se := requireError(t, func() error {
			_, err := r.Preview(ops)
			return err
		}(), ErrEmptyAdjustment)
		if se.ID != "" || se.AdjustmentID != "" {
			t.Fatalf("空清单错误不应携带对象编号: %+v", se)
		}
	}
}

func TestPreviewInvalidOpFirstInInputOrder(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 20, "X", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})

	check := func(name string, ops []Op, kind ErrorKind, id string) {
		t.Helper()
		res, err := r.Preview(ops)
		se := requireError(t, err, kind)
		if se.ID != id {
			t.Fatalf("%s：错误应指向 %q，实际 %q", name, id, se.ID)
		}
		if res != nil {
			t.Fatalf("%s：非法操作不应返回局部预览", name)
		}
	}

	check("货物不存在", []Op{{Kind: OpLoad, CargoID: "GX", Target: "C1"}}, ErrNotFound, "GX")
	check("舱位不存在", []Op{{Kind: OpLoad, CargoID: "G2", Target: "CX"}}, ErrNotFound, "CX")
	check("已装载再装载", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C2"}}, ErrStateMismatch, "G1")
	check("未装载卸下", []Op{{Kind: OpUnload, CargoID: "G2"}}, ErrStateMismatch, "G2")
	check("未装载移动", []Op{{Kind: OpMove, CargoID: "G2", Target: "C2"}}, ErrStateMismatch, "G2")
	check("移动到原舱位", []Op{{Kind: OpMove, CargoID: "G1", Target: "C1"}}, ErrDuplicateOp, "G1")
	check("货物重复", []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpUnload, CargoID: "G1"},
	}, ErrDuplicateOp, "G1")
	check("操作种类无效", []Op{{Kind: OpKind(99), CargoID: "G1", Target: "C2"}}, ErrInvalidOp, "G1")
	check("货物编号为空", []Op{{Kind: OpUnload, CargoID: "  "}}, ErrInvalidID, "")
	check("目标舱位为空", []Op{{Kind: OpLoad, CargoID: "G2", Target: "  "}}, ErrInvalidID, "")

	// 按输入顺序返回第一个非法操作：调换次序后指向不同货物。
	check("第一条非法优先-1", []Op{
		{Kind: OpUnload, CargoID: "G2"},             // 非法（未装载）
		{Kind: OpLoad, CargoID: "GX", Target: "C1"}, // 也非法
	}, ErrStateMismatch, "G2")
	check("第一条非法优先-2", []Op{
		{Kind: OpLoad, CargoID: "GX", Target: "C1"}, // 非法
		{Kind: OpUnload, CargoID: "G2"},             // 也非法
	}, ErrNotFound, "GX")
}

// ---------- 一致性、确定性、快照独立 ----------

func TestPreviewConsistentWithAdjust(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 40, "X", true); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 70, "X", true); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})

	ops := []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}
	res, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Submittable {
		t.Fatalf("应可提交: %+v", res.Rejections)
	}
	mustAdjust(t, r, "A2", ops)
	// 正式提交后的实际配载与预览 After 一致。
	for _, cp := range res.Compartments {
		got, err := r.Compartment(cp.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.UsedWeight != cp.After.UsedWeight ||
			got.RemainingWeight != cp.After.RemainingWeight ||
			!reflect.DeepEqual(cargoIDs(*got), cargoIDs(cp.After)) {
			t.Fatalf("舱位 %s 提交后与预览不一致: got=%+v want=%+v", cp.ID, got, cp.After)
		}
	}
	for _, cc := range res.CargoChanges {
		got, err := r.Cargo(cc.CargoID)
		if err != nil {
			t.Fatal(err)
		}
		if got.CompartmentID != cc.To {
			t.Fatalf("货物 %s 提交后位置与预览不一致: %s", cc.CargoID, got.CompartmentID)
		}
	}

	// 超重场景：预览拒绝原因种类与正式提交错误一致。
	if err := r.RegisterCargo("G3", 80, "X", true); err != nil {
		t.Fatal(err)
	}
	badOps := []Op{{Kind: OpLoad, CargoID: "G3", Target: "C2"}}
	pv, err := r.Preview(badOps)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Submittable || pv.Rejections[0].Kind != ErrOverweight {
		t.Fatalf("预览应判超重: %+v", pv)
	}
	requireError(t, func() error {
		_, e := r.Adjust("A3", badOps)
		return e
	}(), ErrOverweight)
}

func TestPreviewDeterministicInputOrderIndependent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 600, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 600, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	ops1 := []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
	}
	ops2 := []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	}
	res1, err := r.Preview(ops1)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := r.Preview(ops2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res1, res2) {
		t.Fatalf("预览结果不应受输入顺序影响:\n%+v\n%+v", res1, res2)
	}
	res3, err := r.Preview(ops1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res1, res3) {
		t.Fatal("状态未变时连续预览结果应一致")
	}
}

func TestPreviewSnapshotIndependent(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "Beijing", true); err != nil {
		t.Fatal(err)
	}
	ops := []Op{
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
	}
	res1, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	// 调用方任意修改返回内容。
	res1.Submittable = true
	res1.CargoChanges[0].CargoID = "HACKED"
	res1.Compartments[0].After.UsedWeight = 9999
	res1.Compartments[0].After.Cargo[0].ID = "HACKED"
	res1.Rejections[0].CompartmentID = "HACKED"
	res1.Rejections[0].OffendingCargo[0] = "HACKED"
	res1.Rejections[0].Destinations[0].CargoIDs[0] = "HACKED"

	res2, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Submittable {
		t.Fatal("新预览不应被污染，仍应不可提交")
	}
	if res2.CargoChanges[0].CargoID == "HACKED" ||
		res2.Compartments[0].After.UsedWeight == 9999 ||
		res2.Compartments[0].After.Cargo[0].ID == "HACKED" ||
		res2.Rejections[0].CompartmentID == "HACKED" ||
		res2.Rejections[0].OffendingCargo[0] == "HACKED" ||
		res2.Rejections[0].Destinations[0].CargoIDs[0] == "HACKED" {
		t.Fatalf("调用方修改返回内容污染了后续预览: %+v", res2)
	}
	// 登记处本身也未受影响。
	cv, _ := r.Cargo("G1")
	if cv.Loaded {
		t.Fatalf("货物仍应未装载: %+v", cv)
	}
}

func TestPreviewDoesNotTouchAdjustmentRecords(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	ops := []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}
	for i := 0; i < 3; i++ {
		if _, err := r.Preview(ops); err != nil {
			t.Fatal(err)
		}
	}
	// 预览不占用编号：A1 首次正式提交即成功，而非编号冲突。
	res := mustAdjust(t, r, "A1", ops)
	if res.ID != "A1" || res.CargoChanges[0].To != "C1" {
		t.Fatalf("正式提交应正常成功: %+v", res)
	}
	// 再次预览反映的是 A1 之后的同一时刻配载：G1 已装载，再装为状态不符。
	requireError(t, func() error {
		_, e := r.Preview(ops)
		return e
	}(), ErrStateMismatch)
}

func TestPreviewConcurrentWithAdjust(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 1000); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 10, "X", true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_, _ = r.Preview([]Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}})
			_, _ = r.Compartment("C1")
			_, _ = r.Cargo("G1")
		}
	}()
	// 与预览并发的正式调整：相同编号与内容走幂等，均返回成功且不报错、不崩溃，
	// 但配载只真正变化一次。
	var ok int
	for i := 0; i < 100; i++ {
		if _, err := r.Adjust("A1", []Op{{Kind: OpLoad, CargoID: "G1", Target: "C1"}}); err == nil {
			ok++
		}
	}
	<-done
	if ok != 100 {
		t.Fatalf("幂等重提应全部返回首次成功，实际成功 %d 次", ok)
	}
	cpt, _ := r.Compartment("C1")
	if cpt.UsedWeight != 10 || !reflect.DeepEqual(cargoIDs(*cpt), []string{"G1"}) {
		t.Fatalf("并发后最终配载错误: %+v", cpt)
	}
}
