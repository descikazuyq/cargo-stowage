package stowage

import (
	"reflect"
	"strings"
	"testing"
)

// 本文件为“已装载货物只更正目的地”补充自动化回归保障。
//
// 更正是沿用 AmendCargo 的完整资料替换：即使只想改目的地，也要把重量与
// 混装许可按原值一并提交。混装限制必须按同舱全部货物生效后的最终配载
// 判断，被更正货物自己允许混装并不能绕过同舱其他货物的限制；拒绝说明里
// 的编号必须指向真正不允许混装的那件（同舱有多件时取编号字典序最靠前
// 的），而不是固定填本次被更正的货物编号。
//
// 初始配载（newAmendDestinationRegistry）：舱位 C1 承重 200 千克；三件
// 货物同去“上海”，其中 G2 允许混装，G1、G3 不允许混装：
//
//	G1  10 千克  上海  不允许混装
//	G2  20 千克  上海  允许混装      （只更正它的目的地）
//	G3  30 千克  上海  不允许混装
//
// 三件经一次调整装入 C1，已用 60 千克、剩余 140 千克。全部同目的地时
// 共舱本就不要求混装许可，所以初始配载合法。

// amendDestProfiles 是三件货物的登记原值，只更正目的地时重量与混装许可
// 必须按这些值原样提交并保持不变。
var amendDestProfiles = map[string]struct {
	weight int64
	dest   string
	mixed  bool
	comp   string
}{
	"G1": {10, "上海", false, "C1"},
	"G2": {20, "上海", true, "C1"},
	"G3": {30, "上海", false, "C1"},
}

// newAmendDestinationRegistry 建立只更正目的地场景的初始配载，三件货物
// 已装入 C1，合计 60 千克、剩余 140 千克。
func newAmendDestinationRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 200)
	mustRegisterCargo(t, r, "G1", 10, "上海", false)
	mustRegisterCargo(t, r, "G2", 20, "上海", true)
	mustRegisterCargo(t, r, "G3", 30, "上海", false)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})
	return r
}

// amendDestLoadOrders 返回把三件货物装入 C1 的若干不同操作次序，用于
// 保护装载次序不影响更正结论。
func amendDestLoadOrders() [][]Op {
	return [][]Op{
		{
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
			{Kind: OpLoad, CargoID: "G2", Target: "C1"},
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
		},
		{
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			{Kind: OpLoad, CargoID: "G2", Target: "C1"},
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		},
		{
			{Kind: OpLoad, CargoID: "G2", Target: "C1"},
			{Kind: OpLoad, CargoID: "G3", Target: "C1"},
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		},
	}
}

// amendDestLoadCargoIDs 取出装载操作中的货物编号次序，仅用于失败信息。
func amendDestLoadCargoIDs(ops []Op) []string {
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.CargoID)
	}
	return ids
}

// amendDestCargoByID 从舱位清单快照中按编号找到货物；找不到时让用例失败。
func amendDestCargoByID(t *testing.T, cpt CompartmentView, id string) CargoView {
	t.Helper()
	for _, c := range cpt.Cargo {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("舱位 %s 清单中缺少货物 %s：%+v", cpt.ID, id, cpt.Cargo)
	return CargoView{}
}

// assertAmendDestStowage 通过货物查询与舱位清单两个公开入口核对同一配载：
// C1 完整列出三件货物（按编号字典序 G1、G2、G3），每件的重量、目的地与
// 混装许可与 profiles 一致且显示已装载于 C1，货物查询的归属与舱位清单
// 相互吻合，已用与剩余重量等于期望值。两个入口对同一件货物给出的资料
// 必须一致，不能一个显示旧资料、一个写入新目的地。
func assertAmendDestStowage(t *testing.T, r *Registry, profiles map[string]struct {
	weight int64
	dest   string
	mixed  bool
	comp   string
}, used, remaining int64) {
	t.Helper()
	cpt, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询舱位 C1 失败: %v", err)
	}
	if cpt.MaxWeight != 200 || cpt.UsedWeight != used || cpt.RemainingWeight != remaining {
		t.Fatalf("舱位 C1 重量错误: got 承重=%d 已用=%d 剩余=%d, want 已用=%d 剩余=%d",
			cpt.MaxWeight, cpt.UsedWeight, cpt.RemainingWeight, used, remaining)
	}
	if got := cargoIDs(*cpt); !reflect.DeepEqual(got, []string{"G1", "G2", "G3"}) {
		t.Fatalf("舱位清单应完整列出原有三件货物（字典序）: got=%v", got)
	}
	for _, id := range []string{"G1", "G2", "G3"} {
		want := profiles[id]

		// 舱位清单中的资料与位置。
		inList := amendDestCargoByID(t, *cpt, id)
		if !inList.Loaded || inList.CompartmentID != "C1" {
			t.Fatalf("清单中的 %s 应显示已装载于 C1: %+v", id, inList)
		}
		if inList.Weight != want.weight || inList.Destination != want.dest ||
			inList.AllowMixed != want.mixed {
			t.Fatalf("舱位清单中 %s 资料错误: got 重量=%d 目的地=%q 混装=%v, want 重量=%d 目的地=%q 混装=%v",
				id, inList.Weight, inList.Destination, inList.AllowMixed,
				want.weight, want.dest, want.mixed)
		}

		// 货物查询入口的资料与位置，必须与舱位清单一致。
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !cv.Loaded || cv.CompartmentID != want.comp {
			t.Fatalf("货物 %s 应已装载于 %s: %+v", id, want.comp, cv)
		}
		if cv.Weight != want.weight || cv.Destination != want.dest ||
			cv.AllowMixed != want.mixed {
			t.Fatalf("货物查询 %s 资料错误: got 重量=%d 目的地=%q 混装=%v, want 重量=%d 目的地=%q 混装=%v",
				id, cv.Weight, cv.Destination, cv.AllowMixed,
				want.weight, want.dest, want.mixed)
		}
		if cv.Weight != inList.Weight || cv.Destination != inList.Destination ||
			cv.AllowMixed != inList.AllowMixed || cv.CompartmentID != inList.CompartmentID {
			t.Fatalf("货物查询与舱位清单对 %s 的记载不一致: 查询=%+v 清单=%+v", id, cv, inList)
		}
	}
}

// ---------- 只改目的地：被更正货物允许混装也不能绕过同舱其他货物 ----------

// 同舱原本同去上海：G2 允许混装，G1、G3 不允许混装。只把 G2（按原值
// 提交重量 20、混装许可 true）改去另一目的地北京后，同舱出现不同目的地，
// 而 G1、G3 都不允许混装，必须返回结构化混装冲突 ErrMixedLoading。
//
// 错误涉及编号必须是真正不允许混装的同舱货物中字典序最靠前的 G1，而不是
// 固定填入被更正的 G2；中文说明同时点出所属舱位 C1 与货物 G1。
func TestAmendLoadedDestinationMixedConflictPointsAtRealOffender(t *testing.T) {
	r := newAmendDestinationRegistry(t)

	err := r.AmendCargo("G2", 20, "北京", true)
	se := requireError(t, err, ErrMixedLoading)
	if se.ID != "G1" {
		t.Fatalf("混装冲突应指出字典序最靠前的不允许混装货物 G1，实际 ID=%q", se.ID)
	}
	msg := se.Error()
	if !strings.Contains(msg, "C1") || !strings.Contains(msg, "G1") {
		t.Fatalf("混装说明应同时指出所属舱位 C1 与货物 G1: %s", msg)
	}
	if strings.Contains(msg, "G2") {
		t.Fatalf("被更正货物 G2 允许混装，说明不应把它当作肇事货物: %s", msg)
	}
	// 更正不携带调整编号。
	if se.AdjustmentID != "" {
		t.Fatalf("更正错误不应携带调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
}

// 多件不允许混装货物时，涉及编号始终取其中字典序最靠前的一件；并且无论
// 改变登记次序、装载次序还是被更正的允许混装货物是哪一件，同一配载下的
// 更正结论（ErrMixedLoading）与涉及编号都不变。每种情形里只有被更正的
// 那件允许混装，另外两件都不允许混装，因此肇事货物是另外两件中的较小者。
func TestAmendLoadedDestinationConflictOffenderStableAcrossOrderings(t *testing.T) {
	// 每种情形：哪件允许混装的货物被改去北京，以及期望指出的肇事货物编号
	// （另外两件不允许混装货物中字典序最靠前的一件）。
	cases := []struct {
		name      string
		amended   string // 被更正（也是唯一允许混装）的货物
		wantFirst string
	}{
		{name: "改G2肇事G1", amended: "G2", wantFirst: "G1"}, // G1、G3 不允许，较小为 G1
		{name: "改G1肇事G2", amended: "G1", wantFirst: "G2"}, // G2、G3 不允许，较小为 G2
		{name: "改G3肇事G1", amended: "G3", wantFirst: "G1"}, // G1、G2 不允许，较小为 G1
	}
	// 货物登记的先后次序。
	registerOrders := [][]string{
		{"G1", "G2", "G3"},
		{"G3", "G1", "G2"},
		{"G2", "G3", "G1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, regOrder := range registerOrders {
				for _, loadOps := range amendDestLoadOrders() {
					r := NewRegistry()
					mustRegisterCompartment(t, r, "C1", 200)
					for _, id := range regOrder {
						p := amendDestProfiles[id]
						// 只有被更正的那件允许混装，其余两件都不允许。
						mustRegisterCargo(t, r, id, p.weight, p.dest, id == tc.amended)
					}
					mustAdjust(t, r, "init", loadOps)

					w := amendDestProfiles[tc.amended].weight
					// 重量与混装许可按原值提交（被更正货物原本允许混装）。
					se := requireError(t, r.AmendCargo(tc.amended, w, "北京", true), ErrMixedLoading)
					if se.ID != tc.wantFirst {
						t.Fatalf("登记次序=%v 装载次序=%v：应指出肇事货物 %s，实际 ID=%q",
							regOrder, amendDestLoadCargoIDs(loadOps), tc.wantFirst, se.ID)
					}
					if !strings.Contains(se.Error(), "C1") ||
						!strings.Contains(se.Error(), tc.wantFirst) {
						t.Fatalf("说明应指出舱位 C1 与肇事货物 %s: %s",
							tc.wantFirst, se.Error())
					}
				}
			}
		})
	}
}

// 被拒绝后重新查询：被更正货物仍保留原目的地、原重量、原混装许可并在原舱位；
// 同舱其他货物的资料与位置也都不变。舱位清单继续完整列出三件原有货物，
// 已用 60、剩余 140 保持更正前的值，不能出现货物查询显示旧资料、舱位清单
// 却已写入新目的地的分裂。
func TestAmendLoadedDestinationRejectedLeavesStowageIntact(t *testing.T) {
	r := newAmendDestinationRegistry(t)
	beforeComp := snapshotCompartments(r, "C1")
	beforeCargo := snapshotCargo(r, "G1", "G2", "G3")

	requireError(t, r.AmendCargo("G2", 20, "北京", true), ErrMixedLoading)

	// 用提交前快照整体核对：舱位清单与货物查询都不应有任何变化。
	assertCompartmentsUnchanged(t, r, beforeComp)
	assertCargoUnchanged(t, r, beforeCargo)
	// 再分别核对两个入口对每件货物的资料严格一致，杜绝新旧资料分裂。
	assertAmendDestStowage(t, r, amendDestProfiles, 60, 140)

	// 明确点出最容易出错的分裂：G2 在货物查询与舱位清单里都必须仍是上海。
	g2, _ := r.Cargo("G2")
	if g2.Destination != "上海" || g2.Weight != 20 || !g2.AllowMixed ||
		g2.CompartmentID != "C1" || !g2.Loaded {
		t.Fatalf("被更正的 G2 应保留原重量 20、目的地 上海、允许混装 true 并在 C1: %+v", g2)
	}
	cpt, _ := r.Compartment("C1")
	g2InList := amendDestCargoByID(t, *cpt, "G2")
	if g2InList.Destination != "上海" {
		t.Fatalf("舱位清单不得写入被拒的新目的地，G2 应仍为上海: %+v", g2InList)
	}
}

// ---------- 同一规则允许的更正：全员允许混装 ----------

// 同舱全部货物都允许混装时，把其中一件改去另一目的地应成功：货物保持已
// 装载于原舱、重量占用不变（只改目的地，重量按原值提交），新的货物查询
// 与舱位清单都显示新目的地，其他货物资料与位置不变。
func TestAmendLoadedDestinationSucceedsWhenAllAllowMixed(t *testing.T) {
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 200)
	mustRegisterCargo(t, r, "G1", 10, "上海", true)
	mustRegisterCargo(t, r, "G2", 20, "上海", true)
	mustRegisterCargo(t, r, "G3", 30, "上海", true)
	mustAdjust(t, r, "init", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: OpLoad, CargoID: "G3", Target: "C1"},
	})

	if err := r.AmendCargo("G2", 20, "北京", true); err != nil {
		t.Fatalf("全员允许混装时把 G2 改去另一目的地应成功: %v", err)
	}

	want := map[string]struct {
		weight int64
		dest   string
		mixed  bool
		comp   string
	}{
		"G1": {10, "上海", true, "C1"},
		"G2": {20, "北京", true, "C1"},
		"G3": {30, "上海", true, "C1"},
	}
	assertAmendDestStowage(t, r, want, 60, 140)

	// 被更正货物明确核对：仍是原重量、仍允许混装、仍在 C1，只有目的地变化。
	g2, _ := r.Cargo("G2")
	if g2.Weight != 20 || g2.Destination != "北京" || !g2.AllowMixed ||
		!g2.Loaded || g2.CompartmentID != "C1" {
		t.Fatalf("G2 应只改目的地为北京，重量 20、允许混装、留在 C1: %+v", g2)
	}
}

// 提交的目的地只是原值增加首尾空白时，去空白后仍与同舱货物一致：即使同舱
// 其他货物不允许混装，也应成功（去空白后同舱仍同目的地，不构成混装冲突）。
// 保存的目的地沿用现有去空白规则，即不带空白的原值，重量与混装许可按原值
// 提交、保持不变，货物仍在原舱、重量占用不变。
func TestAmendLoadedDestinationOnlyWhitespaceSucceedsDespiteOthersNotMixed(t *testing.T) {
	r := newAmendDestinationRegistry(t)

	if err := r.AmendCargo("G2", 20, "  上海  ", true); err != nil {
		t.Fatalf("目的地仅增加首尾空白、去空白后与同舱一致时应成功: %v", err)
	}

	// 目的地按去空白规则保存为“上海”，与 G1、G3 一致，故共舱依旧合法。
	assertAmendDestStowage(t, r, amendDestProfiles, 60, 140)

	g2, _ := r.Cargo("G2")
	if g2.Destination != "上海" {
		t.Fatalf("保存的目的地应按现有去空白规则去掉首尾空白，实际 %q", g2.Destination)
	}
}
