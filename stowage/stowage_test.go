package stowage

import (
	"errors"
	"math"
	"testing"
)

func newTestSystem(t *testing.T) *System {
	t.Helper()
	return New()
}

func regHold(t *testing.T, s *System, id string, cap int64) {
	t.Helper()
	if err := s.RegisterHold(id, cap); err != nil {
		t.Fatalf("register hold %q: %v", id, err)
	}
}

func regCargo(t *testing.T, s *System, id string, w int64, dest string, mix bool) {
	t.Helper()
	if err := s.RegisterCargo(id, w, dest, mix); err != nil {
		t.Fatalf("register cargo %q: %v", id, err)
	}
}

func TestRegistrationValidation(t *testing.T) {
	s := newTestSystem(t)

	cases := []struct {
		name string
		fn   func() error
	}{
		{"blank hold id", func() error { return s.RegisterHold("   ", 10) }},
		{"zero capacity", func() error { return s.RegisterHold("H1", 0) }},
		{"negative capacity", func() error { return s.RegisterHold("H1", -5) }},
		{"blank cargo id", func() error { return s.RegisterCargo("  ", 1, "X", true) }},
		{"zero weight", func() error { return s.RegisterCargo("C1", 0, "X", true) }},
		{"negative weight", func() error { return s.RegisterCargo("C1", -1, "X", true) }},
		{"blank destination", func() error { return s.RegisterCargo("C1", 1, "  ", true) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatal("want error, got nil")
			}
			var re *RegistrationError
			if !errors.As(err, &re) || !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("want RegistrationError wrapping ErrInvalidArgument, got %T %v", err, err)
			}
		})
	}
}

func TestDuplicateRegistrationKeepsOriginal(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, " H1 ", 100)
	if err := s.RegisterHold("H1", 200); !errors.Is(err, ErrHoldExists) {
		t.Fatalf("want ErrHoldExists, got %v", err)
	}
	h, err := s.GetHold("H1")
	if err != nil {
		t.Fatal(err)
	}
	if h.Capacity != 100 {
		t.Fatalf("original hold altered: capacity=%d", h.Capacity)
	}

	regCargo(t, s, "C1", 10, "D1", false)
	if err := s.RegisterCargo("C1", 20, "D2", true); !errors.Is(err, ErrCargoExists) {
		t.Fatalf("want ErrCargoExists, got %v", err)
	}
	c, err := s.GetCargo("C1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Weight != 10 || c.Destination != "D1" || c.Mixable {
		t.Fatalf("original cargo altered: %+v", c)
	}
}

func TestIDsTrimmedCaseSensitive(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, " H1 ", 100)
	regCargo(t, s, "\tC1\n", 10, " D1 ", true)
	if _, err := s.GetHold("H1"); err != nil {
		t.Fatal("trimmed hold id should resolve")
	}
	c, err := s.GetCargo("C1")
	if err != nil {
		t.Fatal("trimmed cargo id should resolve")
	}
	if c.Destination != "D1" {
		t.Fatalf("destination not trimmed: %q", c.Destination)
	}
	if _, err := s.GetCargo("c1"); err == nil {
		t.Fatal("ids must be case sensitive")
	}
}

func mustAdjust(t *testing.T, s *System, id string, ops ...Operation) *AdjustmentResult {
	t.Helper()
	res, err := s.Adjust(id, ops)
	if err != nil {
		t.Fatalf("adjust %q: %v", id, err)
	}
	return res
}

func adjErr(t *testing.T, err error, reason string) *AdjustmentError {
	t.Helper()
	var ae *AdjustmentError
	if !errors.As(err, &ae) {
		t.Fatalf("want AdjustmentError, got %T %v", err, nil)
	}
	if ae.Reason != reason {
		t.Fatalf("want reason %q, got %q (%v)", reason, ae.Reason, err)
	}
	return ae
}

func TestLoadUnloadMoveBasics(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regHold(t, s, "H2", 100)
	regCargo(t, s, "C1", 30, "D1", false)
	regCargo(t, s, "C2", 20, "D1", false)

	res := mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"})
	if len(res.Moves) != 1 {
		t.Fatalf("want 1 move, got %d", len(res.Moves))
	}
	m := res.Moves[0]
	if m.From.Loaded || !m.To.Loaded || m.To.HoldID != "H1" {
		t.Fatalf("bad move: %+v", m)
	}
	if len(res.HoldWeights) != 1 || res.HoldWeights[0].Before != 0 || res.HoldWeights[0].After != 30 {
		t.Fatalf("bad hold weights: %+v", res.HoldWeights)
	}

	h, _ := s.GetHold("H1")
	if h.Used != 30 || h.Remaining() != 70 || len(h.Cargo) != 1 || h.Cargo[0].ID != "C1" {
		t.Fatalf("bad hold view: %+v", h)
	}

	// 已装载不能再次装载。
	_, err := s.Adjust("A2", []Operation{{Kind: Load, CargoID: "C1", TargetHoldID: "H2"}})
	adjErr(t, err, "invalid_operation")

	// 卸下未装载货物应拒绝。
	_, err = s.Adjust("A3", []Operation{{Kind: Unload, CargoID: "C2"}})
	adjErr(t, err, "not_loaded")

	// 移动到原舱位应拒绝。
	_, err = s.Adjust("A4", []Operation{{Kind: Move, CargoID: "C1", TargetHoldID: "H1"}})
	adjErr(t, err, "same_hold")

	// 移动到另一舱位。
	res = mustAdjust(t, s, "A5",
		Operation{Kind: Move, CargoID: "C1", TargetHoldID: "H2"},
		Operation{Kind: Load, CargoID: "C2", TargetHoldID: "H1"},
	)
	if len(res.Moves) != 2 || res.Moves[0].CargoID != "C1" || res.Moves[1].CargoID != "C2" {
		t.Fatalf("moves not sorted: %+v", res.Moves)
	}
	if res.Moves[0].From.HoldID != "H1" || res.Moves[0].To.HoldID != "H2" {
		t.Fatalf("bad move for C1: %+v", res.Moves[0])
	}
	if len(res.HoldWeights) != 2 {
		t.Fatalf("want 2 hold weights, got %+v", res.HoldWeights)
	}
	want := map[string][2]int64{"H1": {30, 20}, "H2": {0, 30}}
	for _, hw := range res.HoldWeights {
		w := want[hw.HoldID]
		if hw.Before != w[0] || hw.After != w[1] {
			t.Fatalf("unexpected weight change: %+v", hw)
		}
	}

	// 卸下后货物记录保留、舱位收回重量。
	mustAdjust(t, s, "A6", Operation{Kind: Unload, CargoID: "C1"})
	c, _ := s.GetCargo("C1")
	if c.Location.Loaded {
		t.Fatal("C1 should be unloaded")
	}
	h2, _ := s.GetHold("H2")
	if h2.Used != 0 {
		t.Fatalf("H2 weight not recovered: %d", h2.Used)
	}
}

func TestReferencesMustExist(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regCargo(t, s, "C1", 10, "D1", false)

	_, err := s.Adjust("A1", []Operation{{Kind: Load, CargoID: "C9", TargetHoldID: "H1"}})
	var ref *ReferenceError
	if !errors.As(err, &ref) || ref.Kind != "cargo" || !errors.Is(err, ErrNotFound) {
		t.Fatalf("want cargo ReferenceError, got %v", err)
	}

	_, err = s.Adjust("A2", []Operation{{Kind: Load, CargoID: "C1", TargetHoldID: "H9"}})
	if !errors.As(err, &ref) || ref.Kind != "hold" {
		t.Fatalf("want hold ReferenceError, got %v", err)
	}
}

func TestOverCapacityCheckedOnFinalState(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regHold(t, s, "H2", 100)
	regCargo(t, s, "C1", 60, "D1", false)
	regCargo(t, s, "C2", 60, "D1", false)
	mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"})
	mustAdjust(t, s, "A2", Operation{Kind: Load, CargoID: "C2", TargetHoldID: "H2"})

	// 两个满载舱位交换 60 的货物，终态仍为 60，应成功。
	res := mustAdjust(t, s, "A3",
		Operation{Kind: Move, CargoID: "C1", TargetHoldID: "H2"},
		Operation{Kind: Move, CargoID: "C2", TargetHoldID: "H1"},
	)
	if len(res.HoldWeights) != 2 {
		t.Fatalf("want 2 affected holds, got %d", len(res.HoldWeights))
	}
	for _, hw := range res.HoldWeights {
		if hw.Before != 60 || hw.After != 60 {
			t.Fatalf("swap weights wrong: %+v", hw)
		}
	}

	// 终态超重应拒绝，且状态保持交换后的原样。
	regCargo(t, s, "C3", 50, "D1", false)
	_, err := s.Adjust("A4", []Operation{
		{Kind: Load, CargoID: "C3", TargetHoldID: "H1"},
		{Kind: Move, CargoID: "C2", TargetHoldID: "H2"}, // 60+60=120 超重
	})
	adjErr(t, err, "over_capacity")

	c3, _ := s.GetCargo("C3")
	if c3.Location.Loaded {
		t.Fatal("failed adjustment must not load C3")
	}
	c2, _ := s.GetCargo("C2")
	if c2.Location.HoldID != "H1" {
		t.Fatalf("failed adjustment must not move C2, got %q", c2.Location.HoldID)
	}
	h1, _ := s.GetHold("H1")
	if h1.Used != 60 {
		t.Fatalf("H1 weight changed after failed adjustment: %d", h1.Used)
	}
}

func TestMixingRules(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 1000)
	regCargo(t, s, "A", 10, "D1", true)
	regCargo(t, s, "B", 10, "D2", true)
	regCargo(t, s, "C", 10, "D3", false)

	// 同目的地可共舱（即使不允许混装）。
	regCargo(t, s, "D", 10, "D1", false)
	mustAdjust(t, s, "A1",
		Operation{Kind: Load, CargoID: "A", TargetHoldID: "H1"},
		Operation{Kind: Load, CargoID: "D", TargetHoldID: "H1"},
	)

	// 加入 D2：D 不允许混装 -> 冲突。
	_, err := s.Adjust("A2", []Operation{{Kind: Load, CargoID: "B", TargetHoldID: "H1"}})
	adjErr(t, err, "mixed_destination")

	// 卸下 D 后装入 B、C：不同目的地但全部允许混装（A、B 允许，C 不允许）-> 冲突。
	mustAdjust(t, s, "A3", Operation{Kind: Unload, CargoID: "D"})
	_, err = s.Adjust("A4", []Operation{
		{Kind: Load, CargoID: "B", TargetHoldID: "H1"},
		{Kind: Load, CargoID: "C", TargetHoldID: "H1"},
	})
	adjErr(t, err, "mixed_destination")

	// A 与 B 均允许混装，不同目的地可共舱。
	mustAdjust(t, s, "A5", Operation{Kind: Load, CargoID: "B", TargetHoldID: "H1"})
}

func TestDuplicateCargoInOneAdjustment(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regHold(t, s, "H2", 100)
	regCargo(t, s, "C1", 10, "D1", false)
	_, err := s.Adjust("A1", []Operation{
		{Kind: Load, CargoID: "C1", TargetHoldID: "H1"},
		{Kind: Move, CargoID: " C1 ", TargetHoldID: "H2"},
	})
	adjErr(t, err, "duplicate_operation")
}

func TestEmptyAdjustmentRejected(t *testing.T) {
	s := newTestSystem(t)
	_, err := s.Adjust("A1", nil)
	adjErr(t, err, "empty")
	_, err = s.Adjust("A1", []Operation{})
	adjErr(t, err, "empty")
	if _, err := s.Adjust("  ", nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("blank adjustment id: %v", err)
	}
}

func TestIdempotentSubmission(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regHold(t, s, "H2", 100)
	regCargo(t, s, "C1", 10, "D1", false)

	first := mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"})

	// 相同编号相同内容、顺序无关 —— 此时 C1 已在 H1，直接装载本会非法，
	// 但幂等提交应返回首次结果。
	again, err := s.Adjust(" A1 ", []Operation{{Kind: Load, CargoID: "C1", TargetHoldID: " H1 "}})
	if err != nil {
		t.Fatalf("idempotent resubmit: %v", err)
	}
	if len(again.Moves) != len(first.Moves) || again.Moves[0].To.HoldID != "H1" {
		t.Fatalf("idempotent result differs: %+v vs %+v", again, first)
	}

	// 之后另有调整，再用原编号原内容提交，仍返回首次结果且不改变配载。
	mustAdjust(t, s, "A2", Operation{Kind: Move, CargoID: "C1", TargetHoldID: "H2"})
	third, err := s.Adjust("A1", []Operation{{Kind: Load, CargoID: "C1", TargetHoldID: "H1"}})
	if err != nil {
		t.Fatalf("idempotent resubmit after later adjustment: %v", err)
	}
	if third.Moves[0].To.HoldID != "H1" {
		t.Fatalf("must return first result (H1), got %+v", third.Moves[0])
	}
	c, _ := s.GetCargo("C1")
	if c.Location.HoldID != "H2" {
		t.Fatalf("stowage must not change: C1 in %q", c.Location.HoldID)
	}

	// 同一编号用于不同内容应拒绝。
	_, err = s.Adjust("A1", []Operation{{Kind: Unload, CargoID: "C1"}})
	adjErr(t, err, "id_conflict")
}

func TestFailedAdjustmentDoesNotConsumeID(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 10)
	regCargo(t, s, "C1", 50, "D1", false)

	_, err := s.Adjust("A1", []Operation{{Kind: Load, CargoID: "C1", TargetHoldID: "H1"}})
	adjErr(t, err, "over_capacity")

	// 修正后用同一编号重试应成功。
	regHold(t, s, "H2", 100)
	mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H2"})
}

func TestSnapshotsAreIndependent(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regCargo(t, s, "C1", 10, "D1", false)
	res := mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"})

	// 调用方篡改返回值。
	res.Moves[0].To.HoldID = "HACK"
	res.HoldWeights[0].After = 999

	h, _ := s.GetHold("H1")
	h.Cargo[0].ID = "HACK"
	h.Used = 42
	h2, _ := s.GetHold("H1")
	if h2.Used != 10 || h2.Cargo[0].ID != "C1" {
		t.Fatalf("hold snapshot mutation leaked: %+v", h2)
	}

	c, _ := s.GetCargo("C1")
	c.Location.HoldID = "HACK"
	c2, _ := s.GetCargo("C1")
	if c2.Location.HoldID != "H1" {
		t.Fatal("cargo snapshot mutation leaked")
	}

	// 已保存的调整结果不受后续配载变化影响。
	mustAdjust(t, s, "A2", Operation{Kind: Unload, CargoID: "C1"})
	if res.Moves[0].To.HoldID != "HACK" { // 我们之前改过的副本
		t.Fatal("saved result slice was replaced")
	}
	saved, err := s.Adjust("A1", []Operation{{Kind: Load, CargoID: "C1", TargetHoldID: "H1"}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Moves[0].To.HoldID != "H1" || saved.HoldWeights[0].After != 10 {
		t.Fatalf("stored result leaked mutation: %+v", saved)
	}
}

func TestListingsSorted(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H2", 100)
	regHold(t, s, "H1", 100)
	regHold(t, s, "h1", 100)
	hs := s.Holds()
	if len(hs) != 3 || hs[0].ID != "H1" || hs[1].ID != "H2" || hs[2].ID != "h1" {
		t.Fatalf("holds not lexicographically sorted: %+v", hs)
	}

	regCargo(t, s, "C3", 1, "D", true)
	regCargo(t, s, "C1", 1, "D", true)
	regCargo(t, s, "C2", 1, "D", true)
	mustAdjust(t, s, "A1",
		Operation{Kind: Load, CargoID: "C3", TargetHoldID: "H1"},
		Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"},
		Operation{Kind: Load, CargoID: "C2", TargetHoldID: "H1"},
	)
	h, _ := s.GetHold("H1")
	for i, want := range []string{"C1", "C2", "C3"} {
		if h.Cargo[i].ID != want {
			t.Fatalf("cargo list not sorted: %+v", h.Cargo)
		}
	}
}

func TestOverflowRejected(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", math.MaxInt64)
	regCargo(t, s, "C1", math.MaxInt64, "D1", true)
	regCargo(t, s, "C2", 1, "D1", true)
	mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"})

	// MaxInt64 + 1 溢出：必须拒绝，不能出现负数或绕过承重。
	_, err := s.Adjust("A2", []Operation{{Kind: Load, CargoID: "C2", TargetHoldID: "H1"}})
	if err == nil {
		t.Fatal("overflow load must be rejected")
	}
	// 该场景同时构成算术溢出与超重，任一拒绝原因均可，但状态必须不变。
	var ae *AdjustmentError
	if !errors.As(err, &ae) || (ae.Reason != "overflow" && ae.Reason != "over_capacity") {
		t.Fatalf("want overflow/over_capacity, got %v", err)
	}
	h, _ := s.GetHold("H1")
	if h.Used != math.MaxInt64 || h.Remaining() != 0 {
		t.Fatalf("state changed after overflow: used=%d", h.Used)
	}
}

func TestUnloadedShownExplicitly(t *testing.T) {
	s := newTestSystem(t)
	regHold(t, s, "H1", 100)
	regCargo(t, s, "C1", 10, "D1", false)
	mustAdjust(t, s, "A1", Operation{Kind: Load, CargoID: "C1", TargetHoldID: "H1"})
	res2 := mustAdjust(t, s, "A2", Operation{Kind: Unload, CargoID: "C1"})
	m := res2.Moves[0]
	if !m.From.Loaded || m.From.HoldID != "H1" || m.To.Loaded {
		t.Fatalf("unload move must show from H1 to unloaded: %+v", m)
	}
}
