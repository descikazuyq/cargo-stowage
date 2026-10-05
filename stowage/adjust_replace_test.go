package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// setupReplaceStowage 构造初始配载：C1 承重 100 千克，已装入去上海且都
// 不允许混装的 G1（20 千克）、G2（30 千克）；去北京的 G3（40 千克）
// 尚未装载，是否允许混装由 g3Mixed 决定。
func setupReplaceStowage(t *testing.T, g3Mixed bool) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := r.RegisterCompartment("C1", 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G1", 20, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G2", 30, "Shanghai", false); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterCargo("G3", 40, "Beijing", g3Mixed); err != nil {
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

// requireCargoProfile 核对货物的登记资料（重量、目的地、混装许可）未被改动。
func requireCargoProfile(t *testing.T, r *Registry, id string, weight int64, dest string, mixed bool) *CargoView {
	t.Helper()
	cv, err := r.Cargo(id)
	if err != nil {
		t.Fatal(err)
	}
	if cv.Weight != weight || cv.Destination != dest || cv.AllowMixed != mixed {
		t.Fatalf("货物 %s 登记资料不应被调整改动: %+v", id, cv)
	}
	return cv
}

// requireConsistent 核对货物归属与舱位清单相互对应：清单中的货物都归属
// 该舱位，归属该舱位的货物也都在清单中。
func requireConsistent(t *testing.T, r *Registry, compID string, cargoIDs ...string) {
	t.Helper()
	cpt, err := r.Compartment(compID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cpt.Cargo) != len(cargoIDs) {
		t.Fatalf("舱位 %s 清单应为 %v，实际 %+v", compID, cargoIDs, cpt.Cargo)
	}
	for i, id := range cargoIDs {
		if cpt.Cargo[i].ID != id {
			t.Fatalf("舱位 %s 清单应按编号排列为 %v，实际 %+v", compID, cargoIDs, cpt.Cargo)
		}
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatal(err)
		}
		if !cv.Loaded || cv.CompartmentID != compID {
			t.Fatalf("清单中的货物 %s 应归属舱位 %s: %+v", id, compID, cv)
		}
	}
}

// ---------- 一次调整中换掉舱位原有目的地的货物 ----------

// 同一次调整中卸下 G1、G2 并装入 G3：按最终配载判断，原货物尚未移出的
// 临时共舱不算混装冲突。装入写在卸货之前或之后，结论与最终查询一致。
func TestReplaceDestinationCargoSuccess(t *testing.T) {
	orders := map[string][]Op{
		"先卸后装": {
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpUnload, CargoID: "G2"},
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		},
		"先装后卸": {
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpUnload, CargoID: "G2"},
		},
	}
	var firstResult *AdjustmentResult
	for name, ops := range orders {
		t.Run(name, func(t *testing.T) {
			r := setupReplaceStowage(t, true)
			res, err := r.Adjust("A1", ops)
			if err != nil {
				t.Fatalf("换掉原有目的地货物应成功: %v", err)
			}
			if res.ID != "A1" {
				t.Fatalf("结果应保留此次调整编号: %+v", res)
			}
			// 货物变化按编号排列：两件卸下、一件装入，原、新舱位列清。
			wantChanges := []CargoChange{
				{CargoID: "G1", From: "C1", To: ""},
				{CargoID: "G2", From: "C1", To: ""},
				{CargoID: "G3", From: "", To: "C1"},
			}
			if !reflect.DeepEqual(res.CargoChanges, wantChanges) {
				t.Fatalf("货物变化错误: got=%+v want=%+v", res.CargoChanges, wantChanges)
			}
			// C1 的重量变化为 50 到 40 千克。
			wantComp := []CompartmentChange{{CompartmentID: "C1", WeightBefore: 50, WeightAfter: 40}}
			if !reflect.DeepEqual(res.CompartmentChanges, wantComp) {
				t.Fatalf("舱位变化错误: got=%+v want=%+v", res.CompartmentChanges, wantComp)
			}
			// 查询：剩余 60 千克，清单只含 G3。
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != 40 || cpt.RemainingWeight != 60 {
				t.Fatalf("舱位重量错误: %+v", cpt)
			}
			requireConsistent(t, r, "C1", "G3")
			// G1、G2 的记录仍存在且为未装载。
			for _, id := range []string{"G1", "G2"} {
				cv, err := r.Cargo(id)
				if err != nil {
					t.Fatalf("卸下的货物 %s 记录应仍存在: %v", id, err)
				}
				if cv.Loaded || cv.CompartmentID != "" {
					t.Fatalf("卸下的货物 %s 应为未装载: %+v", id, cv)
				}
			}
			// 三件货物的登记资料不被调整改动。
			requireCargoProfile(t, r, "G1", 20, "Shanghai", false)
			requireCargoProfile(t, r, "G2", 30, "Shanghai", false)
			requireCargoProfile(t, r, "G3", 40, "Beijing", true)
			// 两种操作顺序的成功结果应一致。
			if firstResult == nil {
				firstResult = res
			} else if !reflect.DeepEqual(res, firstResult) {
				t.Fatalf("操作顺序不应影响成功结果: got=%+v want=%+v", res, firstResult)
			}
		})
	}
}

// G3 登记为不允许混装时，最终舱位只剩这一件货物，仍应允许装入。
func TestReplaceDestinationCargoNoMixedSingleCargo(t *testing.T) {
	r := setupReplaceStowage(t, false)
	res, err := r.Adjust("A1", []Op{
		{Kind: OpUnload, CargoID: "G1"},
		{Kind: OpUnload, CargoID: "G2"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})
	if err != nil {
		t.Fatalf("最终只有一件货物时不允许混装也应可装入: %v", err)
	}
	if res.CompartmentChanges[0].WeightBefore != 50 || res.CompartmentChanges[0].WeightAfter != 40 {
		t.Fatalf("舱位重量变化错误: %+v", res.CompartmentChanges[0])
	}
	requireConsistent(t, r, "C1", "G3")
	cv := requireCargoProfile(t, r, "G3", 40, "Beijing", false)
	if !cv.Loaded || cv.CompartmentID != "C1" {
		t.Fatalf("G3 应已装入 C1: %+v", cv)
	}
}

// 只卸下 G1 就装入允许混装的 G3：最终留下不允许混装的 G2 与 G3 共舱，
// 总重量 70 千克虽未超重，也必须按混装冲突拒绝整次调整，配载保持原样。
// 先装后卸应得到同样的拒绝与未改变的配载。
func TestPartialUnloadMixedConflictRejected(t *testing.T) {
	orders := map[string][]Op{
		"先卸后装": {
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		},
		"先装后卸": {
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			{Kind: OpUnload, CargoID: "G1"},
		},
	}
	for name, ops := range orders {
		t.Run(name, func(t *testing.T) {
			r := setupReplaceStowage(t, true)
			res, err := r.Adjust("A1", ops)
			if res != nil {
				t.Fatalf("失败不应返回成功结果: %+v", res)
			}
			se := requireError(t, err, ErrMixedLoading)
			if se.ID != "G2" {
				t.Fatalf("混装冲突应指出不允许混装的 G2，实际 ID=%q", se.ID)
			}
			if se.AdjustmentID != "A1" {
				t.Fatalf("混装冲突应携带此次调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
			}
			if !strings.Contains(se.Error(), "C1") {
				t.Fatalf("混装说明应能看出涉及 C1: %s", se.Error())
			}
			// 整次调整不生效：G1 不能因卸货排在前面就被单独卸下，
			// G1、G2 仍在 C1，已用 50 千克、剩余 50 千克，G3 仍未装载。
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != 50 || cpt.RemainingWeight != 50 {
				t.Fatalf("拒绝后舱位重量应保持原样: %+v", cpt)
			}
			requireConsistent(t, r, "C1", "G1", "G2")
			cv3 := requireCargoProfile(t, r, "G3", 40, "Beijing", true)
			if cv3.Loaded || cv3.CompartmentID != "" {
				t.Fatalf("G3 应仍未装载: %+v", cv3)
			}
			// 三件货物的登记资料不被拒绝的调整改动。
			requireCargoProfile(t, r, "G1", 20, "Shanghai", false)
			requireCargoProfile(t, r, "G2", 30, "Shanghai", false)
		})
	}
}
