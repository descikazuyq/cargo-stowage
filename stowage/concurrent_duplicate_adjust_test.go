package stowage

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// 本文件为正式提交（Adjust）的幂等调整补充并发回归保障：同一个登记处
// 尚未成功使用过某调整编号时，两份使用相同调整编号、相同安排的提交同时
// 到达，两次调用都必须成功，而货物实际只装载一次——
//   - 后完成的那份不得因为货物已被先完成的那份装入，就把重复提交当成
//     “再次装载”而返回状态不符 ErrStateMismatch，也不得额外扣减剩余
//     重量或新增舱位清单记录；
//   - 两份返回都描述同一次首次装载：每件货物从未装载变为目标舱位，舱位
//     重量从 0 变为 100 千克，调整编号相同；变化记录沿用按编号排列的
//     现有规则，不能有一份写成“100 -> 100”，也不能漏掉其中一件货物；
//   - 无论哪份调用先完成，另一份都拿到完整的首次结果，最终查询与实际
//     生效的装载一致。
//
// 相同安排仍按现有内容比较规则识别：另一份提交只交换两条装载操作的
// 顺序，或只给调整、货物及目标舱位编号加上首尾空白，并发下仍得到上述
// 结果。返回记录也保持现有独立快照约定：改写一份结果里的货物去向或舱位
// 重量，另一份已返回的记录与登记处查询都保留原值；随后再以原编号、原
// 内容提交，取回的仍是未被改写的首次记录，当前配载保持满载。
//
// 固定场景：C1 为承重 100 千克的空舱位；G1 重 30 千克、G2 重 70 千克，
// 目的地相同且都不允许混装。同一份调整把两件货物装入 C1，合计恰好
// 100 千克——达到承重仍是合法装载。
//
// 顺序重复提交的既有保障见 stowage_test.go 中的 TestAdjustmentIDIdempotent
// 等用例；不同调整编号并发争用同一件货物时恰好一份生效、另一份状态不符
// 的保障见 concurrent_adjust_test.go。二者与本文件关注的“同一次调整尚在
// 提交时又收到原内容”互不替代。

// duplicateBatchID 是并发两份提交共同使用的调整编号；每轮都从全新登记处
// 开始，该编号在开跑前必定未被成功使用过。
const duplicateBatchID = "load-g1-g2"

// duplicateSubmission 描述参与一轮并发重复提交的一份调用。
type duplicateSubmission struct {
	label string // 调用名称（"A"/"B"），用于辨认哪份先完成
	aid   string // 提交使用的调整编号（可能带首尾空白）
	ops   []Op   // 提交的安排（编号可能带首尾空白、顺序可能相反）
}

// duplicateOutcome 记录一份并发提交的返回。
type duplicateOutcome struct {
	who string
	res *AdjustmentResult
	err error
}

// newConcurrentDuplicateRegistry 构造固定的初始配载：一个承重 100 千克的
// 空舱位 C1，与两件同目的地（东港）、均不允许混装的未装载货物
// G1(30 千克)、G2(70 千克)。
func newConcurrentDuplicateRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, "C1", 100)
	mustRegisterCargo(t, r, "G1", 30, "东港", false)
	mustRegisterCargo(t, r, "G2", 70, "东港", false)
	return r
}

// wantDuplicateFirstLoadResult 是两份提交都应返回的同一次首次装载记录：
// 两件货物各从未装载变为 C1（按货物编号字典序排列），C1 重量从 0 变为
// 100 千克（不是 100 -> 100），编号为去空白后的 duplicateBatchID。
func wantDuplicateFirstLoadResult() *AdjustmentResult {
	return &AdjustmentResult{
		ID: duplicateBatchID,
		CargoChanges: []CargoChange{
			{CargoID: "G1", From: "", To: "C1"},
			{CargoID: "G2", From: "", To: "C1"},
		},
		CompartmentChanges: []CompartmentChange{
			{CompartmentID: "C1", WeightBefore: 0, WeightAfter: 100},
		},
	}
}

// assertDuplicateFirstLoadResult 核对一份返回就是完整的首次装载记录：
// 编号一致、两件货物的变化都在且按编号排列、舱位重量 0 -> 100。
func assertDuplicateFirstLoadResult(t *testing.T, res *AdjustmentResult, who string) {
	t.Helper()
	if res == nil {
		t.Fatalf("%s 应成功并返回首次装载的变化记录，实际无结果", who)
	}
	want := wantDuplicateFirstLoadResult()
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("%s 返回的变化记录应为首次装载 %+v，实际 %+v", who, want, res)
	}
}

// assertDuplicateFinalStowage 核对实际配载只发生一次装载：C1 恰好满载
// （已用 100、剩余 0），清单中 G1、G2 各出现一次且按编号排列；两件货物
// 查询都显示已装载并属于 C1，重量、目的地与混装许可保持登记资料。
func assertDuplicateFinalStowage(t *testing.T, r *Registry) {
	t.Helper()
	c1, err := r.Compartment("C1")
	if err != nil {
		t.Fatalf("查询舱位 C1 失败: %v", err)
	}
	if c1.MaxWeight != 100 || c1.UsedWeight != 100 || c1.RemainingWeight != 0 {
		t.Fatalf("两件货物合计恰好达到承重，C1 应已用 100/剩余 0，实际 %+v", c1)
	}
	if got := cargoIDs(*c1); !reflect.DeepEqual(got, []string{"G1", "G2"}) {
		t.Fatalf("C1 清单应各包含 G1、G2 一次且按编号排列，实际 %v", got)
	}

	// 清单快照中的每件货物都显示已装载、属于 C1，登记资料保持原样。
	wantWeight := map[string]int64{"G1": 30, "G2": 70}
	if len(c1.Cargo) != 2 {
		t.Fatalf("C1 不应有额外或缺失的清单记录，实际 %d 条: %+v", len(c1.Cargo), c1.Cargo)
	}
	for _, listed := range c1.Cargo {
		w, ok := wantWeight[listed.ID]
		if !ok {
			t.Fatalf("C1 清单出现不属于本次调整的货物: %+v", listed)
		}
		if !listed.Loaded || listed.CompartmentID != "C1" ||
			listed.Weight != w || listed.Destination != "东港" || listed.AllowMixed {
			t.Fatalf("清单中的货物 %s 资料异常: %+v（应已在 C1、重 %d 千克、东港、不允许混装）",
				listed.ID, listed, w)
		}
	}

	// 货物查询与舱位清单一致，登记资料同样不变。
	for id, w := range wantWeight {
		c, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !c.Loaded || c.CompartmentID != "C1" {
			t.Fatalf("货物 %s 应显示已装载并属于 C1，实际 %+v", id, c)
		}
		if c.Weight != w || c.Destination != "东港" || c.AllowMixed {
			t.Fatalf("货物 %s 的重量、目的地或混装许可被改动: %+v（应保持 %d 千克、东港、不允许混装）",
				id, c, w)
		}
	}
}

// assertDuplicateSnapshotsIndependent 核对两份返回各自为独立快照：改写
// mutated 这份里的货物去向与舱位重量后，other 这份已返回的记录与登记处
// 查询都保留原值；再以 original 的原编号、原内容提交，取回未被改写的
// 首次记录，当前配载仍为满载。
func assertDuplicateSnapshotsIndependent(
	t *testing.T,
	r *Registry,
	mutated, other *AdjustmentResult,
	original duplicateSubmission,
) {
	t.Helper()
	// 调用方改写其中一份结果：把 G1 的去向改写、把舱位重量改成 100 -> 999。
	mutated.CargoChanges[0].From = "C9"
	mutated.CargoChanges[0].To = "CX"
	mutated.CompartmentChanges[0].WeightBefore = 100
	mutated.CompartmentChanges[0].WeightAfter = 999

	// 另一份已经返回的记录不受影响，仍是首次装载的原值。
	assertDuplicateFirstLoadResult(t, other, "未被改写的另一份返回记录")
	// 登记处查询同样保留原配载。
	assertDuplicateFinalStowage(t, r)

	// 随后再以原编号、原内容提交：取得的仍是未被改写的首次记录。
	again, err := r.Adjust(original.aid, original.ops)
	if err != nil {
		t.Fatalf("改写调用方持有的结果后，原编号原内容提交仍应成功: %v", err)
	}
	assertDuplicateFirstLoadResult(t, again, "原编号原内容再次提交")
	// 当前配载保持满载，没有因任何一次重复调用多装或多扣。
	assertDuplicateFinalStowage(t, r)
}

// runDuplicateRound 在全新登记处上让两份提交在同一道起跑线后并发执行，
// 返回先完成那份的标签。delayA/delayB > 0 时对应提交开闸后先等待，用于
// 在多轮中覆盖两种完成先后；均为 0 时是纯粹的同时提交。
//
// 每轮都核对：两次调用都成功且返回同一次首次装载记录；实际装载只发生
// 一次；两份记录互为独立快照，改写一份不影响另一份、登记处与后续回放。
func runDuplicateRound(t *testing.T, a, b duplicateSubmission, delayA, delayB time.Duration) string {
	t.Helper()
	r := newConcurrentDuplicateRegistry(t)

	start := make(chan struct{})
	outcomes := make(chan duplicateOutcome, 2)
	var wg sync.WaitGroup
	submit := func(s duplicateSubmission, delay time.Duration) {
		defer wg.Done()
		<-start
		if delay > 0 {
			time.Sleep(delay)
		}
		res, err := r.Adjust(s.aid, s.ops)
		outcomes <- duplicateOutcome{who: s.label, res: res, err: err}
	}
	wg.Add(2)
	go submit(a, delayA)
	go submit(b, delayB)
	close(start)

	first := <-outcomes
	second := <-outcomes
	wg.Wait()

	for _, o := range []duplicateOutcome{first, second} {
		if o.err != nil {
			t.Fatalf("两份提交内容相同，%s 不应失败（不得把并发重复提交当成再次装载）: %v",
				o.who, o.err)
		}
		assertDuplicateFirstLoadResult(t, o.res, "提交 "+o.who)
	}
	assertDuplicateFinalStowage(t, r)

	// 固定改写 A 这份结果：多轮中 A 既有先完成也有后完成，两种位置下
	// “另一份已返回记录”不受改写影响都会被覆盖到。
	resA := first.res
	resB := second.res
	if first.who == "B" {
		resA, resB = second.res, first.res
	}
	assertDuplicateSnapshotsIndependent(t, r, resA, resB, a)

	return first.who
}

// TestConcurrentAdjustSameIDSameContentLoadsOnce 是核心并发回归：编号
// 此前未成功使用时，两份相同编号、相同安排的提交同时到达，多轮中两份
// 调用都必须成功且货物只装载一次；两种完成先后都实际出现过（不规定哪份
// 先完成），但无论谁先谁后，两份返回、最终查询与快照独立性都满足要求。
func TestConcurrentAdjustSameIDSameContentLoadsOnce(t *testing.T) {
	// 标准安排：G1、G2 依次装入 C1。
	standardOps := func() []Op {
		return []Op{
			{Kind: OpLoad, CargoID: "G1", Target: "C1"},
			{Kind: OpLoad, CargoID: "G2", Target: "C1"},
		}
	}
	cases := []struct {
		name string
		newA func() duplicateSubmission
		newB func() duplicateSubmission
	}{
		{
			name: "两份编号与安排逐字相同",
			newA: func() duplicateSubmission {
				return duplicateSubmission{label: "A", aid: duplicateBatchID, ops: standardOps()}
			},
			newB: func() duplicateSubmission {
				return duplicateSubmission{label: "B", aid: duplicateBatchID, ops: standardOps()}
			},
		},
		{
			name: "另一份只交换两条装载操作的顺序",
			newA: func() duplicateSubmission {
				return duplicateSubmission{label: "A", aid: duplicateBatchID, ops: standardOps()}
			},
			newB: func() duplicateSubmission {
				return duplicateSubmission{
					label: "B",
					aid:   duplicateBatchID,
					ops: []Op{
						{Kind: OpLoad, CargoID: "G2", Target: "C1"},
						{Kind: OpLoad, CargoID: "G1", Target: "C1"},
					},
				}
			},
		},
		{
			name: "另一份给调整货物及舱位编号加首尾空白",
			newA: func() duplicateSubmission {
				return duplicateSubmission{
					label: "A",
					aid:   "  " + duplicateBatchID + "  ",
					ops: []Op{
						{Kind: OpLoad, CargoID: " G1 ", Target: " C1 "},
						{Kind: OpLoad, CargoID: "\tG2\t", Target: "\tC1\t"},
					},
				}
			},
			newB: func() duplicateSubmission {
				return duplicateSubmission{label: "B", aid: duplicateBatchID, ops: standardOps()}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 前置事实：每份提交单独执行都合法，且两件合计恰好 100 千克
			// 属于合法装载；并发下若出现拒绝只能来自重复识别错误，而不是
			// 承重、混装或编号等资料问题。
			t.Run("每份提交单独执行都合法且恰好满载", func(t *testing.T) {
				a, b := tc.newA(), tc.newB()
				rA := newConcurrentDuplicateRegistry(t)
				resA, err := rA.Adjust(a.aid, a.ops)
				if err != nil {
					t.Fatalf("提交 A 单独执行本应合法: %v", err)
				}
				assertDuplicateFirstLoadResult(t, resA, "提交 A")
				assertDuplicateFinalStowage(t, rA)

				rB := newConcurrentDuplicateRegistry(t)
				resB, err := rB.Adjust(b.aid, b.ops)
				if err != nil {
					t.Fatalf("提交 B 单独执行本应合法: %v", err)
				}
				assertDuplicateFirstLoadResult(t, resB, "提交 B")
				assertDuplicateFinalStowage(t, rB)
			})

			// 并发提交：多轮覆盖两种完成先后，并穿插零等待的同时竞争。
			firsts := map[string]int{"A": 0, "B": 0}
			const rounds = 40
			for i := 0; i < rounds; i++ {
				var delayA, delayB time.Duration
				switch i % 4 {
				case 0:
					delayB = 2 * time.Millisecond // 偏向 A 先完成
				case 1:
					delayA = 2 * time.Millisecond // 偏向 B 先完成
				case 2:
					delayB = 5 * time.Millisecond // 再次偏向 A，加大先后差
				default:
					// 两份零等待同时开跑，纯粹的锁竞争。
				}
				a, b := tc.newA(), tc.newB()
				firsts[runDuplicateRound(t, a, b, delayA, delayB)]++
			}

			// 极端调度下若某种先后始终未出现，补几轮强偏向竞争；仍不出现
			// 才判定失败。两种完成先后都必须被实际观察到（不规定哪份先
			// 完成，但任一份先完成时另一份都应拿到完整首次结果）。
			if firsts["A"] == 0 {
				for j := 0; j < 3 && firsts["A"] == 0; j++ {
					a, b := tc.newA(), tc.newB()
					firsts[runDuplicateRound(t, a, b, 0, 25*time.Millisecond)]++
				}
			}
			if firsts["B"] == 0 {
				for j := 0; j < 3 && firsts["B"] == 0; j++ {
					a, b := tc.newA(), tc.newB()
					firsts[runDuplicateRound(t, a, b, 25*time.Millisecond, 0)]++
				}
			}
			if firsts["A"] == 0 || firsts["B"] == 0 {
				t.Fatalf("经过 %d 轮并发提交仍只观察到一种完成先后（A 先完成 %d 次、B 先完成 %d 次），"+
					"两种先后都应被接受", rounds, firsts["A"], firsts["B"])
			}
			t.Logf("并发重复提交 %d 轮：A 先完成 %d 次，B 先完成 %d 次",
				rounds, firsts["A"], firsts["B"])
		})
	}
}
