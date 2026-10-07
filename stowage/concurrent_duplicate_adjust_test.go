package stowage

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// 本文件为正式提交配载调整补充并发重复提交的回归保障：同一个登记处同时
// 收到两次使用相同调整编号、相同安排的提交时——
//   - 两次调用都应成功，实际装载只发生一次：不能因为另一份调用已经装入
//     货物，就把重复提交当成再次装载而返回状态不符；
//   - 两个调用返回的变化记录都描述同一次首次装载：每件货物从未装载变为
//     目标舱位，舱位重量从 0 变为 100 千克，调整编号相同，记录沿用现有
//     按编号排列的规则（不漏货物，也不出现从 100 到 100 的记录）；
//   - 无论哪次调用先完成，另一份都拿到完整的首次结果，最终查询与这次
//     实际生效的装载一致；
//   - 相同安排按现有内容比较规则识别：只交换两条装载操作的顺序，或给
//     调整、货物及目标舱位编号加上首尾空白，仍视为同一次调整；
//   - 返回记录保持独立快照约定：调用方改写其中一份结果，不影响另一份
//     已返回的记录与登记处查询；随后以原编号、原内容再提交，取得的仍是
//     未被改写的首次记录。
//
// 使用条件：一个承重 100 千克的空舱位 C1，两件尚未装载的货物 G1(30)、
// G2(70)，目的地相同且都不允许混装；同一份调整把两件货物装入 C1，恰好
// 达到承重，仍是合法装载。调整编号此前未成功使用。

// dupAdjID 是并发重复提交共用的调整编号（去空白后的值）。
const dupAdjID = "adj-dup-load"

// dupOpsVariant 描述同一份安排的一种写法：内容相同，仅操作顺序或编号
// 首尾空白不同，按现有内容比较规则应识别为同一次调整。
type dupOpsVariant struct {
	name  string
	adjID string // 提交时使用的调整编号（可能带首尾空白）
	ops   []Op
}

// dupOpsVariants 给出同一份“把 G1、G2 装入 C1”安排的三种写法。
func dupOpsVariants() []dupOpsVariant {
	return []dupOpsVariant{
		{
			name:  "原始写法",
			adjID: dupAdjID,
			ops: []Op{
				{Kind: OpLoad, CargoID: "G1", Target: "C1"},
				{Kind: OpLoad, CargoID: "G2", Target: "C1"},
			},
		},
		{
			name:  "交换两条装载操作的顺序",
			adjID: dupAdjID,
			ops: []Op{
				{Kind: OpLoad, CargoID: "G2", Target: "C1"},
				{Kind: OpLoad, CargoID: "G1", Target: "C1"},
			},
		},
		{
			name:  "编号加首尾空白",
			adjID: "  " + dupAdjID + "  ",
			ops: []Op{
				{Kind: OpLoad, CargoID: " G1 ", Target: " C1 "},
				{Kind: OpLoad, CargoID: "  G2", Target: "C1  "},
			},
		},
	}
}

// newDuplicateLoadRegistry 登记一个承重 100 千克的空舱位 C1 与两件同
// 目的地、均不允许混装的未装载货物 G1(30)、G2(70)。
func newDuplicateLoadRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCargo(t, r, "G1", 30, "东港", false)
	mustRegisterCargo(t, r, "G2", 70, "东港", false)
	return r
}

// wantDupResult 是首次装载应产生的完整变化记录：两件货物都从未装载变为
// C1（按货物编号排列），舱位重量从 0 变为 100 千克。
func wantDupResult() *AdjustmentResult {
	return &AdjustmentResult{
		ID: dupAdjID,
		CargoChanges: []CargoChange{
			{CargoID: "G1", From: "", To: "C1"},
			{CargoID: "G2", From: "", To: "C1"},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 0, WeightAfter: 100},
		},
	}
}

// dupOutcome 记录一次并发重复提交的结果。
type dupOutcome struct {
	variant dupOpsVariant
	res     *AdjustmentResult
	err     error
}

// assertDupFirstLoadResult 核对一次返回的结果完整描述了首次装载。
func assertDupFirstLoadResult(t *testing.T, o dupOutcome) {
	t.Helper()
	if o.err != nil {
		t.Fatalf("写法「%s」的重复提交应成功，实际报错: %v", o.variant.name, o.err)
	}
	if o.res == nil {
		t.Fatalf("写法「%s」的重复提交应返回首次结果", o.variant.name)
	}
	if !reflect.DeepEqual(o.res, wantDupResult()) {
		t.Fatalf("写法「%s」应返回完整的首次装载记录 %+v，实际 %+v",
			o.variant.name, wantDupResult(), o.res)
	}
}

// assertDupFinalStowage 核对实际装载只发生一次：C1 清单各包含 G1、G2
// 一次，已用重量 100 千克、剩余 0（恰好达到承重仍是合法装载，重复调用
// 不得额外扣减或新增清单记录）；两件货物查询都显示已装载并属于 C1。
func assertDupFinalStowage(t *testing.T, r *Registry) {
	t.Helper()
	view, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询舱位 C1 失败: %v", err)
	}
	if got := cargoIDs(*view); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("C1 清单应各包含 G1、G2 一次，实际 %v", got)
	}
	if view.MaxWeight != 100 || view.UsedWeight != 100 || view.RemainingWeight != 0 {
		t.Fatalf("C1 应为已用 100/剩余 0 的满载状态，实际 %+v", view)
	}
	for _, id := range []string{"G1", "G2"} {
		c, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !c.Loaded || c.CompartmentID != "C1" {
			t.Fatalf("货物 %s 应已装载于 C1，实际 %+v", id, c)
		}
	}
}

// assertDupRegistrationsUnchanged 确认两件货物的登记资料（重量、目的地、
// 混装许可）在并发重复提交后保持登记时的原样。
func assertDupRegistrationsUnchanged(t *testing.T, r *Registry) {
	t.Helper()
	for _, id := range []string{"G1", "G2"} {
		c, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		wantWeight := map[string]int64{"G1": 30, "G2": 70}[id]
		if c.Weight != wantWeight || c.Destination != "东港" || c.AllowMixed {
			t.Fatalf("货物 %s 的登记资料被改动: %+v（应保持 %d 千克、东港、不允许混装）",
				id, c, wantWeight)
		}
	}
}

// runDupRound 在同一个登记处上让同一编号、同一安排的两份写法在同一道
// 起跑线后并发提交。delayA/delayB > 0 时对应写法在开闸后先等待该时长，
// 用于覆盖两种完成先后；两者均为 0 时是纯粹的同时提交。返回两份结果与
// 本轮使用的登记处（供调用方继续核对快照独立性）。
func runDupRound(t *testing.T, variantA, variantB dupOpsVariant, delayA, delayB time.Duration) (dupOutcome, dupOutcome, *Registry) {
	t.Helper()
	r := newDuplicateLoadRegistry(t)

	start := make(chan struct{})
	outcomes := make(chan dupOutcome, 2)
	var wg sync.WaitGroup
	submit := func(v dupOpsVariant, delay time.Duration) {
		defer wg.Done()
		<-start
		if delay > 0 {
			time.Sleep(delay)
		}
		res, err := r.Adjust(v.adjID, v.ops)
		outcomes <- dupOutcome{variant: v, res: res, err: err}
	}
	wg.Add(2)
	go submit(variantA, delayA)
	go submit(variantB, delayB)
	close(start)
	wg.Wait()
	close(outcomes)

	var got []dupOutcome
	for o := range outcomes {
		got = append(got, o)
	}
	if len(got) != 2 {
		t.Fatalf("并发重复提交应有两份结果，实际 %d 份", len(got))
	}

	// 两次调用都应成功，且都拿到完整的首次装载记录——无论哪次先完成。
	for _, o := range got {
		assertDupFirstLoadResult(t, o)
	}
	// 实际装载只发生一次，最终查询与这次生效的装载一致。
	assertDupFinalStowage(t, r)
	assertDupRegistrationsUnchanged(t, r)
	return got[0], got[1], r
}

// TestConcurrentDuplicateAdjustSameContentCommitsOnce 是核心回归：同一
// 编号、同一安排的两次提交同时到达，多轮覆盖两种完成先后与纯粹的锁竞争，
// 两次调用都必须成功并拿到同一份首次装载记录，实际装载只发生一次。
func TestConcurrentDuplicateAdjustSameContentCommitsOnce(t *testing.T) {
	variants := dupOpsVariants()

	// 两两组合（含同种写法）：两份提交都视为同一安排的重复。
	pairs := []struct {
		name string
		a, b dupOpsVariant
	}{
		{"原始写法对原始写法", variants[0], variants[0]},
		{"原始写法对交换顺序", variants[0], variants[1]},
		{"原始写法对首尾空白", variants[0], variants[2]},
		{"交换顺序对首尾空白", variants[1], variants[2]},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			const rounds = 20
			for i := 0; i < rounds; i++ {
				var delayA, delayB time.Duration
				switch i % 3 {
				case 0:
					delayB = 2 * time.Millisecond // 偏向写法 A 先完成
				case 1:
					delayA = 2 * time.Millisecond // 偏向写法 B 先完成
				default:
					// 两份提交零等待同时开抢，纯粹的锁竞争。
				}
				runDupRound(t, p.a, p.b, delayA, delayB)
			}
		})
	}
}

// TestConcurrentDuplicateAdjustResultsAreIndependentSnapshots 核对返回记录
// 的独立快照约定：并发重复提交后，调用方改写其中一份结果里的货物去向与
// 舱位重量，另一份已返回的记录与登记处查询都保留原值；随后再以原编号、
// 原内容提交，取得的仍是未被改写的首次记录，当前配载保持满载。
func TestConcurrentDuplicateAdjustResultsAreIndependentSnapshots(t *testing.T) {
	variants := dupOpsVariants()
	first, second, r := runDupRound(t, variants[0], variants[1], 0, 0)

	// 改写第一份返回的记录：货物去向与舱位重量。
	first.res.CargoChanges[0].To = "C9"
	first.res.CompartmentChanges[0].WeightBefore = 100
	first.res.CompartmentChanges[0].WeightAfter = 0

	// 另一份已返回的记录保留原值。
	if !reflect.DeepEqual(second.res, wantDupResult()) {
		t.Fatalf("改写一份返回记录不应影响另一份已返回的记录: %+v", second.res)
	}

	// 登记处查询保留原值：配载保持满载，两件货物仍在 C1。
	assertDupFinalStowage(t, r)

	// 随后再以原编号、原内容提交，取得的仍是未被改写的首次记录，
	// 当前配载保持满载。
	res3, err := r.Adjust(dupAdjID, variants[0].ops)
	if err != nil {
		t.Fatalf("改写返回记录后以原编号、原内容再提交应成功: %v", err)
	}
	if !reflect.DeepEqual(res3, wantDupResult()) {
		t.Fatalf("再提交应返回未被改写的首次记录 %+v，实际 %+v", wantDupResult(), res3)
	}
	assertDupFinalStowage(t, r)
	assertDupRegistrationsUnchanged(t, r)
}
