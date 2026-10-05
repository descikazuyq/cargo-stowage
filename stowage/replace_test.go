package stowage

import (
	"strings"
	"testing"
)

// newReplaceRegistry 构造同舱更换目的地货物的初始配载：
// C1 承重 100；G1（20，上海，不允许混装）、G2（30，上海，不允许混装）
// 已装入 C1；G3（40，北京，混装许可由参数指定）尚未装载。
func newReplaceRegistry(t *testing.T, g3AllowMixed bool) *Registry {
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
	if err := r.RegisterCargo("G3", 40, "Beijing", g3AllowMixed); err != nil {
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

// assertReplaceProfilesUnchanged 核对三件货物登记的重量、目的地与混装许可
// 始终不变，仅归属随调整结果改变。
func assertReplaceProfilesUnchanged(t *testing.T, r *Registry, g3AllowMixed bool) {
	t.Helper()
	g1, _ := r.Cargo("G1")
	if g1.Weight != 20 || g1.Destination != "Shanghai" || g1.AllowMixed {
		t.Fatalf("G1 登记资料被改变: %+v", g1)
	}
	g2, _ := r.Cargo("G2")
	if g2.Weight != 30 || g2.Destination != "Shanghai" || g2.AllowMixed {
		t.Fatalf("G2 登记资料被改变: %+v", g2)
	}
	g3, _ := r.Cargo("G3")
	if g3.Weight != 40 || g3.Destination != "Beijing" || g3.AllowMixed != g3AllowMixed {
		t.Fatalf("G3 登记资料被改变: %+v", g3)
	}
}

// 同一次调整中卸下原目的地的 G1、G2 并装入新目的地的 G3：合法性以全部
// 操作完成后的最终配载判断，原货物尚未移出的临时共舱不算混装冲突。
// 装入写在卸货之前或之后，结论与最终查询结果都应一致。
func TestReplaceDestinationCargoSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		ops  []Op
	}{
		{
			name: "先卸后装",
			ops: []Op{
				{Kind: OpUnload, CargoID: "G1"},
				{Kind: OpUnload, CargoID: "G2"},
				{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			},
		},
		{
			name: "先装后卸",
			ops: []Op{
				{Kind: OpLoad, CargoID: "G3", Target: "C1"},
				{Kind: OpUnload, CargoID: "G1"},
				{Kind: OpUnload, CargoID: "G2"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReplaceRegistry(t, true)
			res, err := r.Adjust("A1", tc.ops)
			if err != nil {
				t.Fatalf("原货物全部移出后只剩 G3，应成功: %v", err)
			}
			// 成功结果保留此次调整编号。
			if res.ID != "A1" {
				t.Fatalf("结果编号错误: %+v", res)
			}
			// 两件卸下、一件装入，货物变化按编号字典序排列。
			if len(res.CargoChanges) != 3 {
				t.Fatalf("应列出 3 条货物变化: %+v", res.CargoChanges)
			}
			wantChanges := []CargoChange{
				{CargoID: "G1", From: "C1", To: ""},
				{CargoID: "G2", From: "C1", To: ""},
				{CargoID: "G3", From: "", To: "C1"},
			}
			for i, want := range wantChanges {
				if res.CargoChanges[i] != want {
					t.Fatalf("第 %d 条货物变化错误: got=%+v want=%+v", i, res.CargoChanges[i], want)
				}
			}
			// C1 重量 50 -> 40。
			if len(res.CompartmentChanges) != 1 {
				t.Fatalf("应只列出 C1 的变化: %+v", res.CompartmentChanges)
			}
			cp := res.CompartmentChanges[0]
			if cp.CompartmentID != "C1" || cp.WeightBefore != 50 || cp.WeightAfter != 40 {
				t.Fatalf("C1 重量变化错误: %+v", cp)
			}
			// 查询：剩余 60，清单只含 G3。
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != 40 || cpt.RemainingWeight != 60 {
				t.Fatalf("C1 重量错误: %+v", cpt)
			}
			if len(cpt.Cargo) != 1 || cpt.Cargo[0].ID != "G3" {
				t.Fatalf("C1 清单应只含 G3: %+v", cpt.Cargo)
			}
			// 货物归属与舱位清单相互对应。
			g3, _ := r.Cargo("G3")
			if !g3.Loaded || g3.CompartmentID != "C1" {
				t.Fatalf("G3 应已装入 C1: %+v", g3)
			}
			for _, id := range []string{"G1", "G2"} {
				cv, _ := r.Cargo(id)
				if cv.Loaded || cv.CompartmentID != "" {
					t.Fatalf("%s 记录应保留且为未装载: %+v", id, cv)
				}
			}
			assertReplaceProfilesUnchanged(t, r, true)
		})
	}
}

// G3 登记为不允许混装时，最终舱位只有它一件货物，仍应允许装入
// （没有不同目的地货物共舱，混装许可无关）。
func TestReplaceWithNonMixedIncomingCargoSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		ops  []Op
	}{
		{name: "先卸后装", ops: []Op{
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpUnload, CargoID: "G2"},
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		}},
		{name: "先装后卸", ops: []Op{
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			{Kind: OpUnload, CargoID: "G1"},
			{Kind: OpUnload, CargoID: "G2"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReplaceRegistry(t, false)
			res, err := r.Adjust("A1", tc.ops)
			if err != nil {
				t.Fatalf("最终只有不允许混装的 G3 一件时应允许装入: %v", err)
			}
			if res.ID != "A1" {
				t.Fatalf("结果编号错误: %+v", res)
			}
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != 40 || cpt.RemainingWeight != 60 || len(cpt.Cargo) != 1 {
				t.Fatalf("C1 最终配载错误: %+v", cpt)
			}
			if cpt.Cargo[0].ID != "G3" || cpt.Cargo[0].AllowMixed {
				t.Fatalf("C1 应只剩 G3 且保留其不允许混装登记: %+v", cpt.Cargo[0])
			}
			assertReplaceProfilesUnchanged(t, r, false)
		})
	}
}

// 只卸下 G1、装入允许混装的 G3：最终留下去上海且不允许混装的 G2，
// 与去北京的 G3 共舱。总重量 70 未超重，仍必须按混装冲突拒绝整次调整，
// 且不能因卸货操作排在前面就把 G1 单独卸下。先卸后装与先装后卸结论一致。
func TestPartialReplaceMixedConflictRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		ops  []Op
	}{
		{
			name: "先卸后装",
			ops: []Op{
				{Kind: OpUnload, CargoID: "G1"},
				{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			},
		},
		{
			name: "先装后卸",
			ops: []Op{
				{Kind: OpLoad, CargoID: "G3", Target: "C1"},
				{Kind: OpUnload, CargoID: "G1"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReplaceRegistry(t, true)
			res, err := r.Adjust("A1", tc.ops)
			if err == nil {
				t.Fatalf("留有不允许混装的 G2 时应拒绝整次调整，却返回成功: %+v", res)
			}
			if res != nil {
				t.Fatalf("被拒绝的调整不应返回成功结果: %+v", res)
			}
			se := requireError(t, err, ErrMixedLoading)
			// 结构化原因指出 G2 并携带此次调整编号。
			if se.ID != "G2" {
				t.Fatalf("混装冲突应指出不允许混装的 G2，实际 ID=%q", se.ID)
			}
			if se.AdjustmentID != "A1" {
				t.Fatalf("混装冲突应携带调整编号 A1，实际 AdjustmentID=%q", se.AdjustmentID)
			}
			// 说明中能看出涉及 C1。
			if !strings.Contains(se.Error(), "C1") {
				t.Fatalf("混装说明应指出舱位 C1: %s", se.Error())
			}
			// 拒绝后整次调整不生效：G1、G2 仍在 C1，G3 仍未装载。
			g1, _ := r.Cargo("G1")
			g2, _ := r.Cargo("G2")
			g3, _ := r.Cargo("G3")
			if g1.CompartmentID != "C1" || !g1.Loaded {
				t.Fatalf("G1 不应被单独卸下: %+v", g1)
			}
			if g2.CompartmentID != "C1" || !g2.Loaded {
				t.Fatalf("G2 应留在 C1: %+v", g2)
			}
			if g3.Loaded || g3.CompartmentID != "" {
				t.Fatalf("G3 应仍未装载: %+v", g3)
			}
			// 已用 50、剩余 50。
			cpt, _ := r.Compartment("C1")
			if cpt.UsedWeight != 50 || cpt.RemainingWeight != 50 {
				t.Fatalf("拒绝后舱位重量应保持 50/50: %+v", cpt)
			}
			if len(cpt.Cargo) != 2 || cpt.Cargo[0].ID != "G1" || cpt.Cargo[1].ID != "G2" {
				t.Fatalf("C1 清单应仍是 G1、G2: %+v", cpt.Cargo)
			}
			assertReplaceProfilesUnchanged(t, r, true)
		})
	}
}
