// Package stowage 提供本地舱单与配载能力：登记舱位和货物，执行装载、
// 卸下、移动调整，并查询配载情况。
//
// 承重与重量均为正整数千克（int64）。编号与目的地在去掉首尾空白后不得
// 为空；编号按去空白后的值识别且区分大小写。
package stowage

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Location 表示一件货物当前所处的位置。
type Location struct {
	// HoldID 为所在舱位编号；货物未装载时为空字符串。
	HoldID string
	// Loaded 报告货物是否已装载。
	Loaded bool
}

// Unloaded 是未装载位置的规范表示。
func Unloaded() Location { return Location{} }

// CargoInfo 是货物信息的独立快照。
type CargoInfo struct {
	ID          string
	Weight      int64
	Destination string
	// Mixable 表示该货物是否允许与不同目的地的货物共舱。
	Mixable  bool
	Location Location
}

// CargoRef 是清单中的一条货物引用（编号与重量），按编号字典序排列。
type CargoRef struct {
	ID     string
	Weight int64
}

// HoldView 是单个舱位的配载视图，为独立快照。
type HoldView struct {
	ID       string
	Capacity int64
	Used     int64
	Cargo    []CargoRef
}

// Remaining 返回舱位剩余承重。
func (v HoldView) Remaining() int64 { return v.Capacity - v.Used }

// CargoMove 描述一件货物在一次调整中位置的变化，两者均为规范位置
// （未装载时 Location.Loaded 为 false）。
type CargoMove struct {
	CargoID string
	From    Location
	To      Location
}

// HoldWeight 描述一个受影响舱位调整前后的总重量。
type HoldWeight struct {
	HoldID string
	Before int64
	After  int64
}

// AdjustmentResult 是一次成功调整的结果快照。
type AdjustmentResult struct {
	// AdjustmentID 为用户提交调整时使用的编号。
	AdjustmentID string
	// Moves 列出每件涉及货物的原位置与新位置，按货物编号字典序排列。
	Moves []CargoMove
	// HoldWeights 列出受影响舱位调整前后的重量，按舱位编号字典序排列。
	HoldWeights []HoldWeight
}

// Operation 表示单条装卸调整操作。
type Operation struct {
	// Kind 为操作种类：Load（装载）、Unload（卸下）或 Move（移动）。
	Kind OpKind
	// CargoID 为货物编号，首尾空白会被去除。
	CargoID string
	// TargetHoldID 为目标舱位编号；仅 Load 与 Move 使用，首尾空白会被去除。
	TargetHoldID string
}

// OpKind 表示调整操作的种类。
type OpKind int

const (
	// Load 将未装载货物装入目标舱位。
	Load OpKind = iota + 1
	// Unload 将已装载货物卸下，使其回到未装载状态。
	Unload
	// Move 将已装载货物移动到另一个舱位。
	Move
)

// String 返回操作种类的文字描述。
func (k OpKind) String() string {
	switch k {
	case Load:
		return "load"
	case Unload:
		return "unload"
	case Move:
		return "move"
	default:
		return fmt.Sprintf("invalid(%d)", int(k))
	}
}

var (
	// ErrHoldExists 在重复登记舱位时返回（包装在 *RegistrationError 中）。
	ErrHoldExists = errors.New("hold already exists")
	// ErrCargoExists 在重复登记货物时返回（包装在 *RegistrationError 中）。
	ErrCargoExists = errors.New("cargo already exists")
	// ErrNotFound 表示查询或调整引用了不存在的舱位或货物。
	ErrNotFound = errors.New("not found")
	// ErrInvalidArgument 表示参数不合法（空白编号、非正重量等）。
	ErrInvalidArgument = errors.New("invalid argument")
)

// RegistrationError 说明一次登记失败的对象与原因。重复登记时系统保留原
// 记录不变，Err 为 ErrHoldExists 或 ErrCargoExists。
type RegistrationError struct {
	Kind string // "hold" 或 "cargo"
	ID   string // 规范化后的编号
	Err  error
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("stowage: %s registration %q: %s", e.Kind, e.ID, e.Err)
}

func (e *RegistrationError) Unwrap() error { return e.Err }

// ReferenceError 描述调整中一条引用了不存在对象的操作。
type ReferenceError struct {
	Kind      string // "hold" 或 "cargo"
	ID        string
	Operation OpKind
}

func (e *ReferenceError) Error() string {
	return fmt.Sprintf("stowage: %s %q does not exist (operation %s)", e.Kind, e.ID, e.Operation)
}

func (e *ReferenceError) Is(target error) bool { return target == ErrNotFound }

// AdjustmentError 说明一次调整被拒绝的具体原因。涉及的货物或舱位编号会
// 在 ID 中给出（终态校验类错误可能涉及多个编号）。
type AdjustmentError struct {
	// Reason 为机器可读的原因代码：
	// empty、duplicate_operation、invalid_operation、not_loaded、
	// same_hold、over_capacity、mixed_destination、id_conflict、overflow。
	Reason string
	// Kind 为相关对象种类："adjustment"、"cargo"、"hold" 或 "operations"。
	Kind string
	// ID 为相关对象编号；多个对象时按字典序排列并以逗号连接。
	ID string
	// Err 为底层错误，可能为 ErrInvalidArgument 或 ErrNotFound。
	Err error
}

func (e *AdjustmentError) Error() string {
	switch e.Reason {
	case "empty":
		return "stowage: adjustment contains no operations"
	case "duplicate_operation":
		return fmt.Sprintf("stowage: cargo %q appears more than once in adjustment", e.ID)
	case "invalid_operation":
		return fmt.Sprintf("stowage: operation %s on cargo %q is invalid", e.ID, e.Kind)
	case "not_loaded":
		return fmt.Sprintf("stowage: cargo %q is not loaded but operation requires it to be", e.ID)
	case "same_hold":
		return fmt.Sprintf("stowage: cargo %q is already in hold %q", e.ID, e.Kind)
	case "over_capacity":
		return fmt.Sprintf("stowage: hold %q would exceed its capacity", e.ID)
	case "mixed_destination":
		return fmt.Sprintf("stowage: mixed-destination conflict in hold %q", e.ID)
	case "id_conflict":
		return fmt.Sprintf("stowage: adjustment %q was already submitted with different content", e.ID)
	case "overflow":
		return fmt.Sprintf("stowage: weight computation overflows int64 in hold %q", e.ID)
	default:
		return fmt.Sprintf("stowage: adjustment rejected: %s (%s %q)", e.Reason, e.Kind, e.ID)
	}
}

func (e *AdjustmentError) Unwrap() error { return e.Err }

type holdRec struct {
	id       string
	capacity int64
	used     int64
	cargo    map[string]int64 // 货物编号 -> 重量
}

type cargoRec struct {
	id          string
	weight      int64
	destination string
	mixable     bool
	hold        string // 空串表示未装载
}

// System 是本地舱单与配载系统。零值不可用，请使用 New 创建。
// 单个 System 的导出方法可被多个 goroutine 同时调用。
type System struct {
	mu       sync.Mutex
	holds    map[string]*holdRec
	cargos   map[string]*cargoRec
	adjusted map[string]AdjustmentResult
}

// New 创建空的配载系统。
func New() *System {
	return &System{
		holds:    make(map[string]*holdRec),
		cargos:   make(map[string]*cargoRec),
		adjusted: make(map[string]AdjustmentResult),
	}
}

func normalizeID(s string) (string, bool) {
	id := strings.TrimSpace(s)
	return id, id != ""
}

// RegisterHold 登记一个舱位。capacity 必须为正整数千克。编号去空白后为
// 空、或舱位已存在时返回 *RegistrationError；已存在时原记录保留不变。
func (s *System) RegisterHold(id string, capacity int64) error {
	norm, ok := normalizeID(id)
	if !ok {
		return &RegistrationError{Kind: "hold", ID: norm, Err: fmt.Errorf("%w: hold id must not be blank", ErrInvalidArgument)}
	}
	if capacity <= 0 {
		return &RegistrationError{Kind: "hold", ID: norm, Err: fmt.Errorf("%w: capacity must be a positive integer", ErrInvalidArgument)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.holds[norm]; exists {
		return &RegistrationError{Kind: "hold", ID: norm, Err: ErrHoldExists}
	}
	s.holds[norm] = &holdRec{id: norm, capacity: capacity, cargo: make(map[string]int64)}
	return nil
}

// RegisterCargo 登记一件货物。weight 必须为正整数千克；编号与目的地去
// 空白后均不得为空。新登记货物处于未装载状态。货物已存在时返回
// *RegistrationError 且原记录保留不变。
func (s *System) RegisterCargo(id string, weight int64, destination string, mixable bool) error {
	norm, ok := normalizeID(id)
	if !ok {
		return &RegistrationError{Kind: "cargo", ID: norm, Err: fmt.Errorf("%w: cargo id must not be blank", ErrInvalidArgument)}
	}
	if weight <= 0 {
		return &RegistrationError{Kind: "cargo", ID: norm, Err: fmt.Errorf("%w: weight must be a positive integer", ErrInvalidArgument)}
	}
	dest := strings.TrimSpace(destination)
	if dest == "" {
		return &RegistrationError{Kind: "cargo", ID: norm, Err: fmt.Errorf("%w: destination must not be blank", ErrInvalidArgument)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cargos[norm]; exists {
		return &RegistrationError{Kind: "cargo", ID: norm, Err: ErrCargoExists}
	}
	s.cargos[norm] = &cargoRec{id: norm, weight: weight, destination: dest, mixable: mixable}
	return nil
}

// normOp 是规范化并初步校验后的操作。
type normOp struct {
	kind   OpKind
	cargo  string
	target string
}

// Adjust 执行一次编号为 adjustmentID 的调整，可同时包含多件货物的装载、
// 卸下与移动，每件货物至多出现一次。
//
// 承重与混装限制按全部操作完成后的终态判断；任一操作不合法则整次调整
// 不生效。以相同编号和相同内容（操作种类、货物、目标舱位相同，与排列
// 顺序无关）重复提交时，直接返回首次成功的结果且不再改变配载；同一编号
// 用于不同内容时返回 *AdjustmentError（Reason 为 id_conflict）。失败的
// 调整不占用编号。
func (s *System) Adjust(adjustmentID string, ops []Operation) (*AdjustmentResult, error) {
	adjID, ok := normalizeID(adjustmentID)
	if !ok {
		return nil, &AdjustmentError{Reason: "invalid_operation", Kind: "adjustment", ID: adjID,
			Err: fmt.Errorf("%w: adjustment id must not be blank", ErrInvalidArgument)}
	}

	normalized := make([]normOp, 0, len(ops))
	for _, op := range ops {
		n, err := normalizeOp(op)
		if err != nil {
			return nil, &AdjustmentError{Reason: "invalid_operation", Kind: "operations", ID: op.CargoID, Err: err}
		}
		normalized = append(normalized, n)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if saved, exists := s.adjusted[adjID]; exists {
		if sameContent(saved.Moves, normalized) {
			result := cloneResult(saved)
			return &result, nil
		}
		return nil, &AdjustmentError{Reason: "id_conflict", Kind: "adjustment", ID: adjID}
	}

	if len(normalized) == 0 {
		return nil, &AdjustmentError{Reason: "empty", Kind: "adjustment", ID: adjID}
	}

	// 每件货物在一次调整中至多出现一次。
	seen := make(map[string]bool, len(normalized))
	for _, op := range normalized {
		if seen[op.cargo] {
			return nil, &AdjustmentError{Reason: "duplicate_operation", Kind: "cargo", ID: op.cargo}
		}
		seen[op.cargo] = true
	}

	// 引用的货物与舱位必须存在。
	for _, op := range normalized {
		c, exists := s.cargos[op.cargo]
		if !exists {
			return nil, &ReferenceError{Kind: "cargo", ID: op.cargo, Operation: op.kind}
		}
		if op.kind == Load || op.kind == Move {
			if _, exists := s.holds[op.target]; !exists {
				return nil, &ReferenceError{Kind: "hold", ID: op.target, Operation: op.kind}
			}
		}
		switch op.kind {
		case Load:
			if c.hold != "" {
				return nil, &AdjustmentError{Reason: "invalid_operation", Kind: op.kind.String(), ID: c.id}
			}
		case Unload:
			if c.hold == "" {
				return nil, &AdjustmentError{Reason: "not_loaded", Kind: "cargo", ID: c.id}
			}
		case Move:
			if c.hold == "" {
				return nil, &AdjustmentError{Reason: "not_loaded", Kind: "cargo", ID: c.id}
			}
			if c.hold == op.target {
				return nil, &AdjustmentError{Reason: "same_hold", Kind: op.target, ID: c.id}
			}
		default:
			return nil, &AdjustmentError{Reason: "invalid_operation", Kind: "operations", ID: c.id}
		}
	}

	// 在副本上推演终态，保证失败时系统状态原样不变。
	staging, touched, err := s.buildStaging(normalized)
	if err != nil {
		return nil, err
	}

	// 校验终态：承重与混装。
	touchedIDs := make([]string, 0, len(touched))
	for id := range touched {
		touchedIDs = append(touchedIDs, id)
	}
	sort.Strings(touchedIDs)
	for _, id := range touchedIDs {
		h := staging[id]
		if err := validateHold(h, s.cargos); err != nil {
			return nil, err
		}
	}

	// 提交：把推演结果写回真实记录。
	moves := make([]CargoMove, 0, len(normalized))
	weightBefore := make(map[string]int64, len(touchedIDs))
	weightAfter := make(map[string]int64, len(touchedIDs))
	for _, id := range touchedIDs {
		weightBefore[id] = s.holds[id].used
		weightAfter[id] = staging[id].used
	}
	for _, op := range normalized {
		c := s.cargos[op.cargo]
		from := locOf(c.hold)
		switch op.kind {
		case Load:
			s.holds[op.target].cargo[c.id] = c.weight
		case Unload:
			delete(s.holds[c.hold].cargo, c.id)
		case Move:
			delete(s.holds[c.hold].cargo, c.id)
			s.holds[op.target].cargo[c.id] = c.weight
		}
		c.hold = targetHoldAfter(op)
		moves = append(moves, CargoMove{CargoID: c.id, From: from, To: locOf(c.hold)})
	}
	for _, id := range touchedIDs {
		s.holds[id].used = staging[id].used
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].CargoID < moves[j].CargoID })

	weights := make([]HoldWeight, 0, len(touchedIDs))
	for _, id := range touchedIDs {
		weights = append(weights, HoldWeight{HoldID: id, Before: weightBefore[id], After: weightAfter[id]})
	}

	result := AdjustmentResult{AdjustmentID: adjID, Moves: moves, HoldWeights: weights}
	// 保存深拷贝快照，使之后的配载变化不影响已存结果。
	s.adjusted[adjID] = cloneResult(result)
	return &result, nil
}

func normalizeOp(op Operation) (normOp, error) {
	cid, ok := normalizeID(op.CargoID)
	if !ok {
		return normOp{}, fmt.Errorf("%w: cargo id must not be blank", ErrInvalidArgument)
	}
	n := normOp{kind: op.Kind, cargo: cid}
	switch op.Kind {
	case Load, Move:
		tid, ok := normalizeID(op.TargetHoldID)
		if !ok {
			return normOp{}, fmt.Errorf("%w: target hold id must not be blank", ErrInvalidArgument)
		}
		n.target = tid
	case Unload:
		if strings.TrimSpace(op.TargetHoldID) != "" {
			return normOp{}, fmt.Errorf("%w: unload takes no target hold", ErrInvalidArgument)
		}
	default:
		return normOp{}, fmt.Errorf("%w: unknown operation kind %d", ErrInvalidArgument, int(op.Kind))
	}
	return n, nil
}

// targetHoldAfter 返回一件操作完成后货物所属的舱位；卸下时为空串。
func targetHoldAfter(op normOp) string {
	if op.kind == Unload {
		return ""
	}
	return op.target
}

// buildStaging 构造受影响舱位的推演副本并应用全部操作，同时完成带溢出
// 检查的重量计算。
func (s *System) buildStaging(ops []normOp) (map[string]*holdRec, map[string]bool, error) {
	staging := make(map[string]*holdRec)
	touched := make(map[string]bool)

	cloneHold := func(id string) *holdRec {
		if h, ok := staging[id]; ok {
			return h
		}
		src := s.holds[id]
		h := &holdRec{id: src.id, capacity: src.capacity, used: src.used, cargo: make(map[string]int64, len(src.cargo))}
		for cid, w := range src.cargo {
			h.cargo[cid] = w
		}
		staging[id] = h
		touched[id] = true
		return h
	}

	addWeight := func(h *holdRec, delta int64) error {
		sum, overflow := addInt64(h.used, delta)
		if overflow || sum < 0 {
			return &AdjustmentError{Reason: "overflow", Kind: "hold", ID: h.id}
		}
		h.used = sum
		return nil
	}

	for _, op := range ops {
		c := s.cargos[op.cargo]
		switch op.kind {
		case Load:
			h := cloneHold(op.target)
			h.cargo[c.id] = c.weight
			if err := addWeight(h, c.weight); err != nil {
				return nil, nil, err
			}
		case Unload:
			h := cloneHold(c.hold)
			delete(h.cargo, c.id)
			if err := addWeight(h, -c.weight); err != nil {
				return nil, nil, err
			}
		case Move:
			src := cloneHold(c.hold)
			delete(src.cargo, c.id)
			if err := addWeight(src, -c.weight); err != nil {
				return nil, nil, err
			}
			dst := cloneHold(op.target)
			dst.cargo[c.id] = c.weight
			if err := addWeight(dst, c.weight); err != nil {
				return nil, nil, err
			}
		}
	}
	return staging, touched, nil
}

// validateHold 校验一个推演舱位的终态重量与混装限制。
func validateHold(h *holdRec, cargos map[string]*cargoRec) error {
	// 以清单重新求和，确保 used 与货物明细一致且均为非负。
	var sum int64
	ids := make([]string, 0, len(h.cargo))
	for id := range h.cargo {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := h.cargo[id]
		if w <= 0 {
			return &AdjustmentError{Reason: "overflow", Kind: "hold", ID: h.id}
		}
		v, overflow := addInt64(sum, w)
		if overflow {
			return &AdjustmentError{Reason: "overflow", Kind: "hold", ID: h.id}
		}
		sum = v
	}
	if sum > h.capacity {
		return &AdjustmentError{Reason: "over_capacity", Kind: "hold", ID: h.id}
	}

	// 混装：同一目的地可共舱；存在不同目的地时，所有货物都必须允许混装。
	destSet := make(map[string]bool)
	allMixable := true
	for _, id := range ids {
		c := cargos[id]
		destSet[c.destination] = true
		if !c.mixable {
			allMixable = false
		}
	}
	if len(destSet) > 1 && !allMixable {
		return &AdjustmentError{Reason: "mixed_destination", Kind: "hold", ID: h.id}
	}
	return nil
}

// sameContent 判断已保存结果与新提交的操作集合是否内容相同。
func sameContent(saved []CargoMove, ops []normOp) bool {
	if len(saved) != len(ops) {
		return false
	}
	// 以货物编号为键比较操作种类与目标舱位；每件货物至多一项。
	want := make(map[string]normOp, len(ops))
	for _, op := range ops {
		want[op.cargo] = op
	}
	for _, m := range saved {
		op, ok := want[m.CargoID]
		if !ok {
			return false
		}
		var target string
		var kind OpKind
		if !m.From.Loaded && m.To.Loaded {
			kind = Load
			target = m.To.HoldID
		} else if m.From.Loaded && !m.To.Loaded {
			kind = Unload
		} else if m.From.Loaded && m.To.Loaded {
			kind = Move
			target = m.To.HoldID
		} else {
			return false // 未装载到未装载不是合法操作
		}
		if kind != op.kind || target != op.target {
			return false
		}
	}
	return true
}

func locOf(hold string) Location {
	if hold == "" {
		return Location{}
	}
	return Location{HoldID: hold, Loaded: true}
}

// addInt64 返回 a+b，并报告是否溢出 int64 范围。
func addInt64(a, b int64) (int64, bool) {
	sum := a + b
	if b > 0 && sum < a {
		return 0, true
	}
	if b < 0 && sum > a {
		return 0, true
	}
	return sum, false
}

func cloneResult(r AdjustmentResult) AdjustmentResult {
	cp := AdjustmentResult{AdjustmentID: r.AdjustmentID}
	if r.Moves != nil {
		cp.Moves = append([]CargoMove(nil), r.Moves...)
	}
	if r.HoldWeights != nil {
		cp.HoldWeights = append([]HoldWeight(nil), r.HoldWeights...)
	}
	return cp
}

// GetHold 按编号查看舱位的承重、已用、剩余重量及货物清单。货物清单按
// 编号字典序排列；返回值为独立快照。舱位不存在时返回包装 ErrNotFound 的
// *ReferenceError。
func (s *System) GetHold(id string) (*HoldView, error) {
	norm, ok := normalizeID(id)
	if !ok {
		return nil, &ReferenceError{Kind: "hold", ID: norm}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, exists := s.holds[norm]
	if !exists {
		return nil, &ReferenceError{Kind: "hold", ID: norm}
	}
	return holdViewOf(h), nil
}

// GetCargo 按编号查看货物信息与当前位置（未装载时 Location.Loaded 为
// false）。返回值为独立快照。货物不存在时返回包装 ErrNotFound 的
// *ReferenceError。
func (s *System) GetCargo(id string) (*CargoInfo, error) {
	norm, ok := normalizeID(id)
	if !ok {
		return nil, &ReferenceError{Kind: "cargo", ID: norm}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, exists := s.cargos[norm]
	if !exists {
		return nil, &ReferenceError{Kind: "cargo", ID: norm}
	}
	return &CargoInfo{
		ID:          c.id,
		Weight:      c.weight,
		Destination: c.destination,
		Mixable:     c.mixable,
		Location:    locOf(c.hold),
	}, nil
}

// Holds 返回全部舱位视图，按编号字典序排列；各视图均为独立快照。
func (s *System) Holds() []HoldView {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.holds))
	for id := range s.holds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]HoldView, 0, len(ids))
	for _, id := range ids {
		out = append(out, *holdViewOf(s.holds[id]))
	}
	return out
}

// Cargos 返回全部货物信息，按编号字典序排列；各项均为独立快照。
func (s *System) Cargos() []CargoInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.cargos))
	for id := range s.cargos {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]CargoInfo, 0, len(ids))
	for _, id := range ids {
		c := s.cargos[id]
		out = append(out, CargoInfo{
			ID:          c.id,
			Weight:      c.weight,
			Destination: c.destination,
			Mixable:     c.mixable,
			Location:    locOf(c.hold),
		})
	}
	return out
}

func holdViewOf(h *holdRec) *HoldView {
	refs := make([]CargoRef, 0, len(h.cargo))
	for id, w := range h.cargo {
		refs = append(refs, CargoRef{ID: id, Weight: w})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return &HoldView{ID: h.id, Capacity: h.capacity, Used: h.used, Cargo: refs}
}
