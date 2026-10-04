package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// setupMoveAndLoadBatch 构造回归场景共用的初始配载：C1、C2 承重均为
// 100 千克；G1（20 千克）已装入 C1，G2（60 千克）已装入 C2，
// G3（10 千克）尚未装载。三件货物目的地相同，均不允许混装。
// 返回的那批操作把 G1 移到 C2、同时把 G3 装入 C2。
func setupMoveAndLoadBatch(t *testing.T) (*Registry, []Op) {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCompartment("C2", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 20, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 60, "上海", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 10, "上海", false); err != nil {
		t.Fatal(err)
	}
	mustAdjust(t, r, "load-1", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C2"},
	})
	ops := []Op{
		{Kind: OpMove, CargoID: "G1", Target: "C2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C2"},
	}
	return r, ops
}

// checkMoveAndLoadPreviewOK 断言预览可提交，且 C2 预计装入三件货物、
// 总重量 90 千克；同时断言预览是只读的，实际配载保持原样。
func checkMoveAndLoadPreviewOK(t *testing.T, r *Registry, res *PreviewResult) {
	t.Helper()
	if !res.Submittable || len(res.Rejections) != 0 {
		t.Fatalf("预览应可提交且无拒绝原因: %+v", res.Rejections)
	}
	wantCargo := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G3", From: "", To: "C2"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantCargo) {
		t.Fatalf("预览货物变化错误: %+v", res.CargoChanges)
	}
	if len(res.Compartments) != 2 ||
		res.Compartments[0].ID != "C1" || res.Compartments[1].ID != "C2" {
		t.Fatalf("预览应列出 C1、C2 两个受影响舱位: %+v", res.Compartments)
	}
	c2 := res.Compartments[1]
	if c2.After.UsedWeight != 90 || c2.After.RemainingWeight != 10 {
		t.Fatalf("C2 预计总重量应为 90/100: %+v", c2.After)
	}
	if got := cargoIDs(c2.After); !reflect.DeepEqual(got, []string{"G1", "G2", "G3"}) {
		t.Fatalf("C2 预计清单应含三件货物: %v", got)
	}
	// 预览是只读的：实际配载保持原样。
	cv1, _ := r.Cargo("G1")
	cv3, _ := r.Cargo("G3")
	if cv1.CompartmentID != "C1" || cv3.Loaded {
		t.Fatalf("预览不应改变实际配载: G1=%+v G3=%+v", cv1, cv3)
	}
}

// 预览通过后更正了货物重量，保留旧预览并直接提交原安排：正式提交必须
// 按提交时的最新资料重新判断，拒绝整次调整，不能被旧的可提交结果放行。
func TestStalePreviewDoesNotMaskOverweightAfterAmend(t *testing.T) {
	r, ops := setupMoveAndLoadBatch(t)

	preview, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	checkMoveAndLoadPreviewOK(t, r, preview)

	// 预览结束后更正 G1 的重量为 40 千克，目的地与混装许可保持原值。
	if err := r.AmendCargo("G1", 40, "上海", false); err != nil {
		t.Fatalf("更正应成功: %v", err)
	}
	cv, _ := r.Cargo("G1")
	if cv.Weight != 40 || cv.CompartmentID != "C1" {
		t.Fatalf("更正后 G1 应为 40 千克且仍在 C1: %+v", cv)
	}

	// 不重新预览，直接用一个未使用的调整编号提交原来那批操作：
	// C2 预计 40+60+10=110 千克，超过承重 100，整次调整必须被拒绝。
	res, err := r.Adjust("adj-1", ops)
	se := requireError(t, err, ErrOverweight)
	if res != nil {
		t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
	}
	if se.ID != "C2" {
		t.Fatalf("超重错误应指出舱位 C2，实际 ID=%q", se.ID)
	}
	if se.AdjustmentID != "adj-1" {
		t.Fatalf("错误应携带本次调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C2") || !strings.Contains(msg, "110") || !strings.Contains(msg, "100") {
		t.Fatalf("超重说明应给出舱位、预计总重量 110 与最大承重 100: %s", msg)
	}

	// 整次调整不生效：不能只执行其中一项操作，也不能撤回资料更正。
	cv1, _ := r.Cargo("G1")
	if cv1.Weight != 40 || !cv1.Loaded || cv1.CompartmentID != "C1" {
		t.Fatalf("G1 应仍在 C1 且保留更正后的 40 千克: %+v", cv1)
	}
	cv2, _ := r.Cargo("G2")
	if !cv2.Loaded || cv2.CompartmentID != "C2" || cv2.Weight != 60 {
		t.Fatalf("G2 应仍在 C2: %+v", cv2)
	}
	cv3, _ := r.Cargo("G3")
	if cv3.Loaded || cv3.CompartmentID != "" {
		t.Fatalf("G3 应仍未装载: %+v", cv3)
	}
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 40 || c1.RemainingWeight != 60 {
		t.Fatalf("C1 已用 40、剩余 60: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 60 || c2.RemainingWeight != 40 {
		t.Fatalf("C2 已用 60、剩余 40: %+v", c2)
	}

	// 之前保存的预览快照不被更正与失败提交改写：仍保留 G1 重 20 千克、
	// C2 预计 90 千克的内容。
	if !preview.Submittable {
		t.Fatalf("旧预览应仍为可提交: %+v", preview.Rejections)
	}
	c2After := preview.Compartments[1].After
	if c2After.UsedWeight != 90 || c2After.RemainingWeight != 10 {
		t.Fatalf("旧预览中 C2 预计应仍为 90/100: %+v", c2After)
	}
	g1 := c2After.Cargo[0]
	if g1.ID != "G1" || g1.Weight != 20 {
		t.Fatalf("旧预览中 G1 应仍为重 20 千克: %+v", g1)
	}
}

// 承重恰好相等的边界：预览可提交后把 G1 更正为 30 千克，直接提交原
// 安排应成功，C2 恰好装满 100 千克；返回的舱位重量变化依据提交时的
// 实际重量，而非旧预览。
func TestStalePreviewExactCapacityAdjustSucceeds(t *testing.T) {
	r, ops := setupMoveAndLoadBatch(t)

	preview, err := r.Preview(ops)
	if err != nil {
		t.Fatal(err)
	}
	checkMoveAndLoadPreviewOK(t, r, preview)

	// 更正 G1 为 30 千克：提交后 C2 恰好 30+60+10=100 千克。
	if err := r.AmendCargo("G1", 30, "上海", false); err != nil {
		t.Fatalf("更正应成功: %v", err)
	}

	res, err := r.Adjust("adj-1", ops)
	if err != nil {
		t.Fatalf("恰好达到承重应提交成功: %v", err)
	}
	if res.ID != "adj-1" {
		t.Fatalf("调整编号错误: %+v", res)
	}
	wantCargo := []CargoChange{
		{CargoID: "G1", From: "C1", To: "C2"},
		{CargoID: "G3", From: "", To: "C2"},
	}
	if !reflect.DeepEqual(res.CargoChanges, wantCargo) {
		t.Fatalf("货物变化错误: %+v", res.CargoChanges)
	}
	// 舱位重量变化依据提交时的实际重量（G1 已更正为 30），而非旧预览
	// 中的 20 千克：C1 由 30 清空，C2 由 60 增至 100。
	wantComp := []CompartmentChange{
		{CompartmentID: "C1", WeightBefore: 30, WeightAfter: 0},
		{CompartmentID: "C2", WeightBefore: 60, WeightAfter: 100},
	}
	if !reflect.DeepEqual(res.CompartmentChanges, wantComp) {
		t.Fatalf("舱位重量变化应按提交时实际重量计算: %+v", res.CompartmentChanges)
	}

	// 货物查询与舱位清单一致：C1 清空，C2 同时包含三件货物，
	// 已用 100 千克、剩余 0 千克。
	c1, _ := r.Compartment("C1")
	if c1.UsedWeight != 0 || c1.RemainingWeight != 100 || len(c1.Cargo) != 0 {
		t.Fatalf("C1 应已清空: %+v", c1)
	}
	c2, _ := r.Compartment("C2")
	if c2.UsedWeight != 100 || c2.RemainingWeight != 0 {
		t.Fatalf("C2 应恰好装满 100/100: %+v", c2)
	}
	if got := cargoIDs(*c2); !reflect.DeepEqual(got, []string{"G1", "G2", "G3"}) {
		t.Fatalf("C2 应同时包含三件货物: %v", got)
	}
	for id, wantWeight := range map[string]int64{"G1": 30, "G2": 60, "G3": 10} {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatal(err)
		}
		if !cv.Loaded || cv.CompartmentID != "C2" || cv.Weight != wantWeight {
			t.Fatalf("货物 %s 应在 C2 且重 %d 千克: %+v", id, wantWeight, cv)
		}
	}
	// 舱位清单中各货物的重量与货物查询一致。
	for _, cv := range c2.Cargo {
		want, err := r.Cargo(cv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cv.Weight != want.Weight || cv.CompartmentID != want.CompartmentID {
			t.Fatalf("舱位清单与货物查询不一致: 清单=%+v 查询=%+v", cv, want)
		}
	}
}
