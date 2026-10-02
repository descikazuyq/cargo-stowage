package stowage

import (
	"sort"
	"strings"
	"sync"
)

// compartment 是舱位的内部记录。
type compartment struct {
	id        string
	maxWeight int64
	cargo     map[string]*cargo
}

// cargo 是货物的内部记录。
type cargo struct {
	id            string
	weight        int64
	destination   string
	allowMixed    bool
	compartmentID string // 空字符串表示未装载
}

// Registry 是舱单与配载的本地登记处。
//
// Registry 的所有方法都可以安全地并发调用。
type Registry struct {
	mu           sync.Mutex
	compartments map[string]*compartment
	cargos       map[string]*cargo
	adjustments  map[string]*AdjustmentResult // 已成功调整的编号 -> 保存的结果快照
	contents     map[string]string            // 已成功调整的编号 -> 规范化内容键
}

// NewRegistry 创建一个空的登记处。
func NewRegistry() *Registry {
	return &Registry{
		compartments: make(map[string]*compartment),
		cargos:       make(map[string]*cargo),
		adjustments:  make(map[string]*AdjustmentResult),
		contents:     make(map[string]string),
	}
}

// RegisterCompartment 登记一个舱位。
// 编号去掉首尾空白后不得为空，maxWeight 必须为正整数（千克）。
// 编号按去空白后的值识别并区分大小写；重复登记保留原记录。
func (r *Registry) RegisterCompartment(id string, maxWeight int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cid := strings.TrimSpace(id)
	if cid == "" {
		return fail(ErrInvalidID, "", "舱位编号为空")
	}
	if maxWeight <= 0 {
		return fail(ErrInvalidWeight, cid, "舱位 %s 最大承重必须为正整数千克，当前为 %d", cid, maxWeight)
	}
	if _, exists := r.compartments[cid]; exists {
		return fail(ErrAlreadyExists, cid, "舱位 %s 已存在", cid)
	}
	r.compartments[cid] = &compartment{
		id:        cid,
		maxWeight: maxWeight,
		cargo:     make(map[string]*cargo),
	}
	return nil
}

// RegisterCargo 登记一件货物。
// 编号与目的地去掉首尾空白后不得为空，weight 必须为正整数（千克）。
// 货物登记后处于未装载状态。
// 编号按去空白后的值识别并区分大小写；重复登记保留原记录。
func (r *Registry) RegisterCargo(id string, weight int64, destination string, allowMixed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cid := strings.TrimSpace(id)
	if cid == "" {
		return fail(ErrInvalidID, "", "货物编号为空")
	}
	if weight <= 0 {
		return fail(ErrInvalidWeight, cid, "货物 %s 重量必须为正整数千克，当前为 %d", cid, weight)
	}
	dest := strings.TrimSpace(destination)
	if dest == "" {
		return fail(ErrInvalidDestination, cid, "货物 %s 目的地为空", cid)
	}
	if _, exists := r.cargos[cid]; exists {
		return fail(ErrAlreadyExists, cid, "货物 %s 已存在", cid)
	}
	r.cargos[cid] = &cargo{
		id:          cid,
		weight:      weight,
		destination: dest,
		allowMixed:  allowMixed,
	}
	return nil
}

// Compartment 查询舱位的承重、已用重量、剩余重量与货物清单。
// 编号去掉首尾空白后识别；不存在时返回错误。
func (r *Registry) Compartment(id string) (*CompartmentView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cid := strings.TrimSpace(id)
	if cid == "" {
		return nil, fail(ErrInvalidID, "", "舱位编号为空")
	}
	comp, ok := r.compartments[cid]
	if !ok {
		return nil, fail(ErrNotFound, cid, "舱位 %s 不存在", cid)
	}
	return compartmentView(comp), nil
}

// Cargo 查询货物的当前舱位或未装载状态。
// 编号去掉首尾空白后识别；不存在时返回错误。
func (r *Registry) Cargo(id string) (*CargoView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cid := strings.TrimSpace(id)
	if cid == "" {
		return nil, fail(ErrInvalidID, "", "货物编号为空")
	}
	c, ok := r.cargos[cid]
	if !ok {
		return nil, fail(ErrNotFound, cid, "货物 %s 不存在", cid)
	}
	return cargoView(c), nil
}

// cargoView 构造货物快照（调用方持锁）。
func cargoView(c *cargo) *CargoView {
	return &CargoView{
		ID:            c.id,
		Weight:        c.weight,
		Destination:   c.destination,
		AllowMixed:    c.allowMixed,
		Loaded:        c.compartmentID != "",
		CompartmentID: c.compartmentID,
	}
}

// compartmentView 构造舱位快照（调用方持锁）。
func compartmentView(comp *compartment) *CompartmentView {
	used := sumSet(comp.cargo)
	ids := make([]string, 0, len(comp.cargo))
	for cid := range comp.cargo {
		ids = append(ids, cid)
	}
	sort.Strings(ids)
	views := make([]CargoView, 0, len(ids))
	for _, cid := range ids {
		views = append(views, *cargoView(comp.cargo[cid]))
	}
	return &CompartmentView{
		ID:              comp.id,
		MaxWeight:       comp.maxWeight,
		UsedWeight:      used,
		RemainingWeight: comp.maxWeight - used,
		Cargo:           views,
	}
}

// sumSet 合计一组货物的重量（调用方持锁）。
func sumSet(set map[string]*cargo) int64 {
	var sum int64
	for _, c := range set {
		sum += c.weight
	}
	return sum
}

// cloneResult 复制一份调整结果，避免调用方修改影响已保存的快照。
func cloneResult(r *AdjustmentResult) *AdjustmentResult {
	return &AdjustmentResult{
		ID:                 r.ID,
		CargoChanges:       append([]CargoChange(nil), r.CargoChanges...),
		CompartmentChanges: append([]CompartmentChange(nil), r.CompartmentChanges...),
	}
}
