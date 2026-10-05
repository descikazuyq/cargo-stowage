package stowage

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件为已装载货物的重量更正补充并发回归保障：同一舱位内两件货物
// 同时提交增重更正时，登记处允许并发调用 AmendCargo，每次更正整体替换
// 一件货物的资料，两次独立更正共同占用舱位承重时，返回的成功/拒绝结果
// 必须与最终配载一致——
//   - 任一更正单独作用于初始配载都合法，但两次都生效会超重时，恰好一次
//     成功、另一次按超重返回 ErrOverweight；允许任意一件先成功，不要求
//     固定成功者，也不能把两次独立请求当成一次整体失败的批量操作；
//   - 失败错误是不携带调整编号的结构化错误：涉及编号为舱位 C1，中文说明
//     指出预计总重量 120 千克与最大承重 100 千克；
//   - 最终配载与结果一致：成功者为新重量 60 千克、失败者仍为 30 千克，
//     C1 已用 90 千克、剩余 10 千克；失败请求不能覆盖成功者的结果，也
//     不能留下自己的新重量；
//   - 两件货物都继续显示已装载于 C1，目的地与混装许可不变，货物单独查询
//     与舱位货物清单逐一一致，清单仍完整包含两件货物且各出现一次。
//
// 同时覆盖承重恰好够用的对应情况：两件货物同时改为 50 千克，两次都必须
// 成功，最终已用 100 千克、剩余 0 千克，不能因为同时更正就拒绝其中一次。

const (
	amendContestCompartment = "C1"
	amendContestCapacity    = int64(100)
	amendContestInitWeight  = int64(30)
	amendContestDest        = "上海"
)

// amendRequest 是一份并发中的更正：把指定货物的重量整体替换为 newWeight，
// 目的地与混装许可保持初始值不变。
type amendRequest struct {
	cargoID   string
	newWeight int64
}

// amendOutcome 记录一份更正的返回结果。
type amendOutcome struct {
	req amendRequest
	err error
}

// newConcurrentAmendRegistry 建立题目中的初始配载：C1 承重 100 千克，
// G1、G2 各 30 千克、目的地均为上海、都允许混装，两件都已装入 C1，
// 即已用 60 千克、剩余 40 千克。
func newConcurrentAmendRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	mustRegisterCompartment(t, r, amendContestCompartment, amendContestCapacity)
	mustRegisterCargo(t, r, "G1", amendContestInitWeight, amendContestDest, true)
	mustRegisterCargo(t, r, "G2", amendContestInitWeight, amendContestDest, true)
	if _, err := r.Adjust("initial-load", []Op{
		{Kind: OpLoad, CargoID: "G1", Target: amendContestCompartment},
		{Kind: OpLoad, CargoID: "G2", Target: amendContestCompartment},
	}); err != nil {
		t.Fatalf("建立初始配载失败: %v", err)
	}
	return r
}

// amendSameProfile 按“只改重量、目的地与混装许可保持原值”提交一次更正。
func amendSameProfile(r *Registry, req amendRequest) error {
	return r.AmendCargo(req.cargoID, req.newWeight, amendContestDest, true)
}

// runConcurrentAmends 在同一个登记处上让两份更正在同一道起跑线后并发
// 提交。delayG1/delayG2 > 0 时对应请求在开闸后先等待该时长，用于在多轮
// 中覆盖两种生效先后；两者均为 0 时是纯粹的同时竞争。返回两份结果。
func runConcurrentAmends(t *testing.T, r *Registry, req1, req2 amendRequest,
	delayG1, delayG2 time.Duration) (amendOutcome, amendOutcome) {
	t.Helper()

	start := make(chan struct{})
	outcomes := make(chan amendOutcome, 2)
	var wg sync.WaitGroup
	submit := func(req amendRequest, delay time.Duration) {
		defer wg.Done()
		<-start
		if delay > 0 {
			time.Sleep(delay)
		}
		outcomes <- amendOutcome{req: req, err: amendSameProfile(r, req)}
	}
	wg.Add(2)
	go submit(req1, delayG1)
	go submit(req2, delayG2)
	close(start)
	wg.Wait()
	close(outcomes)

	var got []amendOutcome
	for o := range outcomes {
		got = append(got, o)
	}
	if len(got) != 2 {
		t.Fatalf("应收到两份更正结果，实际 %d 份", len(got))
	}
	return got[0], got[1]
}

// assertAmendFinalStowage 按期望重量核对并发更正后的最终状态：
// 每件货物的重量、目的地、混装许可与装载位置，舱位已用/剩余重量，以及
// 货物单独查询与舱位货物清单之间的一致性（两件货物各出现一次）。
func assertAmendFinalStowage(t *testing.T, r *Registry, wantWeight map[string]int64,
	wantUsed, wantRemaining int64) {
	t.Helper()

	comp, err := r.Compartment(amendContestCompartment)
	if err != nil {
		t.Fatalf("查询舱位 %s 失败: %v", amendContestCompartment, err)
	}
	if comp.MaxWeight != amendContestCapacity ||
		comp.UsedWeight != wantUsed || comp.RemainingWeight != wantRemaining {
		t.Fatalf("舱位重量应为承重 %d、已用 %d、剩余 %d，实际 %+v",
			amendContestCapacity, wantUsed, wantRemaining, comp)
	}
	if ids := cargoIDs(*comp); !reflect.DeepEqual(ids, []string{"G1", "G2"}) {
		t.Fatalf("舱位清单应完整包含 G1、G2 且各出现一次，实际 %v", ids)
	}
	listByID := make(map[string]CargoView, len(comp.Cargo))
	for _, c := range comp.Cargo {
		listByID[c.ID] = c
	}

	ids := []string{"G1", "G2"}
	for _, id := range ids {
		cv, err := r.Cargo(id)
		if err != nil {
			t.Fatalf("查询货物 %s 失败: %v", id, err)
		}
		if !cv.Loaded || cv.CompartmentID != amendContestCompartment {
			t.Fatalf("货物 %s 应继续显示已装载于 %s，实际 %+v", id, amendContestCompartment, cv)
		}
		if cv.Destination != amendContestDest || !cv.AllowMixed {
			t.Fatalf("货物 %s 的目的地与混装许可应保持原值（%s、允许混装），实际 %+v",
				id, amendContestDest, cv)
		}
		if cv.Weight != wantWeight[id] {
			t.Fatalf("货物 %s 重量应为 %d 千克，实际 %d 千克（%+v）",
				id, wantWeight[id], cv.Weight, cv)
		}
		listed, ok := listByID[id]
		if !ok {
			t.Fatalf("舱位清单缺少货物 %s: %+v", id, comp.Cargo)
		}
		if listed != *cv {
			t.Fatalf("货物 %s 的单独查询与舱位清单资料不一致: 查询 %+v，清单 %+v",
				id, cv, listed)
		}
	}
}

// assertAmendOverweightError 核对被拒绝的更正：ErrOverweight 结构化错误，
// 涉及编号为 C1，不携带调整编号，中文说明指出预计总重量 120 千克与最大
// 承重 100 千克。
func assertAmendOverweightError(t *testing.T, loser amendOutcome) {
	t.Helper()
	var se *Error
	if !errors.As(loser.err, &se) {
		t.Fatalf("货物 %s 的更正应返回 *Error，实际为 %T: %v",
			loser.req.cargoID, loser.err, loser.err)
	}
	if se.Kind != ErrOverweight {
		t.Fatalf("货物 %s 应因超重被拒绝，实际为 %s（%v）",
			loser.req.cargoID, se.Kind, loser.err)
	}
	if se.ID != amendContestCompartment {
		t.Fatalf("超重错误应指向装不下的舱位 %s，实际 ID=%q",
			amendContestCompartment, se.ID)
	}
	if se.AdjustmentID != "" {
		t.Fatalf("更正错误不应携带调整编号，实际 AdjustmentID=%q", se.AdjustmentID)
	}
	msg := se.Error()
	for _, want := range []string{amendContestCompartment, "120", "100"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("超重说明应包含 %q（指出舱位、预计总重量 120 千克与最大承重 100 千克）: %s",
				want, msg)
		}
	}
}

// TestConcurrentAmendTwoCargosGainWeightOnlyOneTakesEffect 是核心并发回归：
// G1、G2 同时从 30 千克更正为 60 千克，任一更正单独作用于初始配载都合法
// （60+30=90 ≤ 100），但两次都生效会达到 120 千克，因此多轮并发中必须
// 恰好一次成功、另一次按超重拒绝，且两种生效先后都实际出现过（不规定哪件
// 成功）；无论哪件成功，返回结果与最终配载都一致，不是整批失败。
func TestConcurrentAmendTwoCargosGainWeightOnlyOneTakesEffect(t *testing.T) {
	req1 := amendRequest{cargoID: "G1", newWeight: 60}
	req2 := amendRequest{cargoID: "G2", newWeight: 60}

	// 前置事实：两次更正各自单独提交都合法（已用 90、剩余 10），并发下
	// 的拒绝只能来自两次共同占用舱位承重，而不是资料本身错误。
	t.Run("每份更正单独提交都合法", func(t *testing.T) {
		for _, req := range []amendRequest{req1, req2} {
			r := newConcurrentAmendRegistry(t)
			if err := amendSameProfile(r, req); err != nil {
				t.Fatalf("货物 %s 单独更正为 %d 千克本应合法: %v",
					req.cargoID, req.newWeight, err)
			}
			want := map[string]int64{"G1": amendContestInitWeight, "G2": amendContestInitWeight}
			want[req.cargoID] = req.newWeight
			assertAmendFinalStowage(t, r, want, 90, 10)
		}
	})

	// 并发竞争：多轮覆盖两种先后，并穿插不偏不倚的同时竞争。
	wins := map[string]int{"G1": 0, "G2": 0}
	const rounds = 40
	for i := 0; i < rounds; i++ {
		var delayG1, delayG2 time.Duration
		switch i % 4 {
		case 0:
			delayG2 = 2 * time.Millisecond // 偏向 G1 先生效
		case 1:
			delayG1 = 2 * time.Millisecond // 偏向 G2 先生效
		case 2:
			delayG2 = 5 * time.Millisecond // 再次偏向 G1，加大先后差
		default:
			// 两份更正零等待同时提交，纯粹的锁竞争。
		}

		r := newConcurrentAmendRegistry(t)
		o1, o2 := runConcurrentAmends(t, r, req1, req2, delayG1, delayG2)

		var winner, loser *amendOutcome
		for _, o := range []amendOutcome{o1, o2} {
			o := o
			if o.err == nil {
				if winner != nil {
					t.Fatalf("两次更正都成功：G1 与 G2 都改为 60 千克，舱位将达到 120 千克超重")
				}
				winner = &o
			} else {
				if loser != nil {
					t.Fatalf("两次更正都被拒绝：%s 返回 %v，%s 返回 %v，应有恰好一次成功，"+
						"不能当成整批失败", loser.req.cargoID, loser.err, o.req.cargoID, o.err)
				}
				loser = &o
			}
		}
		if winner == nil || loser == nil {
			t.Fatalf("并发更正结束后成功/失败数量异常: winner=%v loser=%v", winner, loser)
		}
		assertAmendOverweightError(t, *loser)

		// 最终配载必须与各自结果一致：成功者 60、失败者仍为 30，已用 90、剩余 10。
		wantWeight := map[string]int64{
			winner.req.cargoID: 60,
			loser.req.cargoID:  amendContestInitWeight,
		}
		assertAmendFinalStowage(t, r, wantWeight, 90, 10)
		wins[winner.req.cargoID]++
	}

	// 极端调度下若某种先后始终未出现，补几轮强偏向竞争；仍不出现才判定
	// 失败。两件货物都应能在并发更正中先生效（不规定固定成功者）。
	forceMissing := func(missing string, missingReq, otherReq amendRequest) {
		for j := 0; j < 3 && wins[missing] == 0; j++ {
			r := newConcurrentAmendRegistry(t)
			o1, o2 := runConcurrentAmends(t, r, missingReq, otherReq, 0, 25*time.Millisecond)
			for _, o := range []amendOutcome{o1, o2} {
				if o.err == nil {
					wins[o.req.cargoID]++
				}
			}
		}
	}
	if wins["G1"] == 0 {
		forceMissing("G1", req1, req2)
	}
	if wins["G2"] == 0 {
		forceMissing("G2", req2, req1)
	}
	if wins["G1"] == 0 || wins["G2"] == 0 {
		t.Fatalf("经过 %d 轮并发更正仍只观察到一种生效先后（G1 成功 %d 次、G2 成功 %d 次），"+
			"两种先后都应被接受", rounds, wins["G1"], wins["G2"])
	}
	t.Logf("并发增重竞争 %d 轮：G1 先生效 %d 次，G2 先生效 %d 次",
		rounds, wins["G1"], wins["G2"])
}

// TestConcurrentAmendTwoCargosExactCapacityBothSucceed 覆盖承重恰好够用的
// 对应情况：G1、G2 同时从 30 千克更正为 50 千克，合计恰好 100 千克，
// 两次都必须成功，最终已用 100 千克、剩余 0 千克，不能因为发生同时更正
// 就拒绝其中一次；既有资料整体替换与失败保留原资料的行为在并发下继续适用。
func TestConcurrentAmendTwoCargosExactCapacityBothSucceed(t *testing.T) {
	req1 := amendRequest{cargoID: "G1", newWeight: 50}
	req2 := amendRequest{cargoID: "G2", newWeight: 50}

	rounds := 40
	for i := 0; i < rounds; i++ {
		var delayG1, delayG2 time.Duration
		switch i % 4 {
		case 0:
			delayG2 = 2 * time.Millisecond
		case 1:
			delayG1 = 2 * time.Millisecond
		case 2:
			delayG2 = 5 * time.Millisecond
		default:
			// 零等待同时提交。
		}

		r := newConcurrentAmendRegistry(t)
		o1, o2 := runConcurrentAmends(t, r, req1, req2, delayG1, delayG2)
		for _, o := range []amendOutcome{o1, o2} {
			if o.err != nil {
				t.Fatalf("两件货物同时更正为 50 千克合计恰好 100 千克，货物 %s 不应被拒绝: %v",
					o.req.cargoID, o.err)
			}
		}
		assertAmendFinalStowage(t, r, map[string]int64{"G1": 50, "G2": 50}, 100, 0)
	}
}
