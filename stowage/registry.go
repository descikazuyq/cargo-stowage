package stowage

import (
	"math"
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
	// 实际查询：清单展示货物的真实所属舱位。
	return compartmentSnapshot(comp, comp.cargo, false), nil
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

// compartmentSnapshot 按统一规则构造舱位配载快照，供舱位查询与调整预览
// 共用（调用方持锁）。set 是该舱位清单对应的货物集合：实际配载传舱位
// 当前货物，预计配载传模拟完成后的集合。
//
// projected 为 true 时表示预计配载：清单中的货物一律显示为已装载且属于
// comp（预计所属舱位），编号、重量、目的地与混装许可仍取登记资料；
// projected 为 false 时表示实际配载，每件货物显示其真实装载状态与所属
// 舱位。两类清单都包含 set 中的全部货物并按货物编号字典序排列。
//
// 重量合计溢出 int64 时（只会出现在预计配载），货物清单照常返回，已用
// 重量与剩余重量保持零值，由拒绝原因表达溢出；其余情况剩余重量为
// 最大承重减已用重量（预计超重时为负值）。
func compartmentSnapshot(comp *compartment, set map[string]*cargo, projected bool) *CompartmentView {
	ids := make([]string, 0, len(set))
	for cid := range set {
		ids = append(ids, cid)
	}
	sort.Strings(ids)

	cargoViews := make([]CargoView, 0, len(ids))
	for _, cid := range ids {
		view := *cargoView(set[cid])
		if projected {
			// cargoView 返回值副本，改写预计位置不影响登记处或同批其他快照。
			view.Loaded = true
			view.CompartmentID = comp.id
		}
		cargoViews = append(cargoViews, view)
	}

	view := &CompartmentView{
		ID:        comp.id,
		MaxWeight: comp.maxWeight,
		Cargo:     cargoViews,
	}
	if used, overflow := sumCargoWeight(set); !overflow {
		view.UsedWeight = used
		view.RemainingWeight = comp.maxWeight - used
	}
	return view
}

// sumCargoWeight 合计一组货物的重量，按集合当前内容计算（调用方持锁）。
// 合计超过 int64 可表示范围时 overflow 为 true、sum 为部分和，调用方不
// 应采用其数值；查询、预览清单与承重/溢出拒绝判断共用同一套汇总规则。
func sumCargoWeight(set map[string]*cargo) (sum int64, overflow bool) {
	for _, c := range set {
		if c.weight > math.MaxInt64-sum {
			return sum, true
		}
		sum += c.weight
	}
	return sum, false
}

// cloneResult 复制一份调整结果，避免调用方修改影响已保存的快照。
func cloneResult(r *AdjustmentResult) *AdjustmentResult {
	return &AdjustmentResult{
		ID:                 r.ID,
		CargoChanges:       append([]CargoChange(nil), r.CargoChanges...),
		CompartmentChanges: append([]CompartmentChange(nil), r.CompartmentChanges...),
	}
}
