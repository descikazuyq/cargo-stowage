package stowage

import (
	"sort"
	"strconv"
	"strings"
)

// Adjust 提交一次调整，可同时包含多件货物的装载、卸下和移动。
//
// 调整编号遵循编号规则（去掉首尾空白后非空）。成功后以相同编号和
// 相同内容重复提交，返回首次成功的结果而不再改变配载，即使之后另有
// 调整也一样；同一编号用于不同内容被拒绝。失败的调整不占用编号，
// 可以修正后重试。内容比较按编号去空白后的原始字节进行：登记处能
// 区分的编号（包括含无效 UTF-8 字节的编号）在内容判断中同样区分。
// 卸下操作忽略目标值，其目标不参与内容比较；装载与移动的目标舱位
// 参与比较。
//
// 一次调整中的所有操作作为一个整体生效：任一操作不合法，整次调整
// 都不生效，货物归属与各舱位重量保持原样。合法性按全部操作完成后的
// 最终配载判断：舱位总重量不得超过承重；同一目的地的货物可以共舱，
// 不同目的地共舱时所有货物都必须允许混装。重量合计超过 int64 可表示
// 范围也会被拒绝。
func (r *Registry) Adjust(adjustmentID string, ops []Op) (*AdjustmentResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	aid := strings.TrimSpace(adjustmentID)
	if aid == "" {
		return nil, fail(ErrInvalidID, "", "调整编号为空")
	}

	// f 构造带调整编号的错误。
	f := func(kind ErrorKind, id string, format string, args ...any) *Error {
		e := fail(kind, id, format, args...)
		e.AdjustmentID = aid
		return e
	}

	// 幂等：编号已成功使用过。
	if saved, ok := r.adjustments[aid]; ok {
		if r.contents[aid] == canonicalKey(ops) {
			return cloneResult(saved), nil
		}
		return nil, f(ErrAdjustmentIDConflict, aid, "调整编号 %s 已用于不同内容", aid)
	}

	if len(ops) == 0 {
		return nil, f(ErrEmptyAdjustment, aid, "调整 %s 不包含任何操作", aid)
	}

	vops, final, affected, err := r.prepareOps(ops, f)
	if err != nil {
		return nil, err
	}

	// 按最终配载校验承重与混装限制，返回第一个问题即拒绝整次调整；
	// 拒绝原因到结构化错误的转换与 AmendCargo 共用。
	if rej := r.firstRejection(final); rej != nil {
		return nil, rejectionError(rej, f)
	}

	// 全部合法，正式生效。先计算受影响舱位调整前后的重量；前后配载都
	// 经过重量校验（原配载由历次成功操作累积而成，最终配载刚通过检查），
	// 合计必然可以用 int64 表示。
	cargoChanges := make([]CargoChange, 0, len(vops))
	compChanges := make([]CompartmentChange, 0, len(affected))
	for id := range affected {
		before, _ := weightTotal(r.compartments[id].cargo)
		after, _ := weightTotal(final[id])
		compChanges = append(compChanges, CompartmentChange{
			CompartmentID: id,
			WeightBefore:  before,
			WeightAfter:   after,
		})
	}
	sort.Slice(compChanges, func(i, j int) bool {
		return compChanges[i].CompartmentID < compChanges[j].CompartmentID
	})

	for _, v := range vops {
		c := v.cargo
		// 正式生效的落点与预演模拟共用 relocate，去向沿用逐条校验时由
		// routeFor 确定的同一份 from/to，因此生效结果与预览逐件对应。
		relocate(c, v.rt, r.cargoSet(v.rt.from), r.cargoSet(v.rt.to))
		c.compartmentID = v.rt.to
		cargoChanges = append(cargoChanges, CargoChange{CargoID: c.id, From: v.rt.from, To: v.rt.to})
	}
	sort.Slice(cargoChanges, func(i, j int) bool {
		return cargoChanges[i].CargoID < cargoChanges[j].CargoID
	})

	result := &AdjustmentResult{
		ID:                 aid,
		CargoChanges:       cargoChanges,
		CompartmentChanges: compChanges,
	}
	r.adjustments[aid] = result
	r.contents[aid] = canonicalKey(ops)
	return cloneResult(result), nil
}

// validOp 是一条通过逐条校验的操作，附带其涉及的货物与由 routeFor
// 确定的去向。落点与变化记录只需货物和去向，原始操作与目标舱位在逐条
// 校验完成后不再需要。
type validOp struct {
	cargo *cargo
	rt    cargoRoute // 由 routeFor 确定的原舱位与最终舱位
}

// prepareOps 逐条校验操作并在模拟配载上应用全部操作。
// 调用方必须持有 r.mu。任一操作不合法时返回该错误，最终配载与受影响
// 舱位集合只在全部操作合法时返回。受影响舱位包括各操作的原舱位与
// 目标舱位（交换中的两个舱位都会纳入）。
func (r *Registry) prepareOps(
	ops []Op,
	failf func(kind ErrorKind, id string, format string, args ...any) *Error,
) (vops []validOp, final map[string]map[string]*cargo, affected map[string]bool, err error) {
	seen := make(map[string]bool)
	vops = make([]validOp, 0, len(ops))
	affected = make(map[string]bool)

	for _, op := range ops {
		cid := strings.TrimSpace(op.CargoID)
		if cid == "" {
			return nil, nil, nil, failf(ErrInvalidID, "", "货物编号为空")
		}
		c, ok := r.cargos[cid]
		if !ok {
			return nil, nil, nil, failf(ErrNotFound, cid, "货物 %s 不存在", cid)
		}
		if seen[cid] {
			return nil, nil, nil, failf(ErrDuplicateOp, cid, "货物 %s 在同一次调整中出现多次", cid)
		}
		seen[cid] = true

		// 逐条校验，确定操作种类与目标舱位；通过后由 routeFor 统一
		// 固定去向，模拟落点、预览变化记录与正式生效共用这同一份去向。
		var target *compartment
		switch op.Kind {
		case OpLoad:
			if c.compartmentID != "" {
				return nil, nil, nil, failf(ErrStateMismatch, cid, "货物 %s 已装载于舱位 %s，不能再次装载", cid, c.compartmentID)
			}
			tid := strings.TrimSpace(op.Target)
			if tid == "" {
				return nil, nil, nil, failf(ErrInvalidID, "", "目标舱位编号为空")
			}
			t, ok := r.compartments[tid]
			if !ok {
				return nil, nil, nil, failf(ErrNotFound, tid, "舱位 %s 不存在", tid)
			}
			target = t
		case OpUnload:
			if c.compartmentID == "" {
				return nil, nil, nil, failf(ErrStateMismatch, cid, "货物 %s 未装载，不能卸下", cid)
			}
			// 卸下忽略目标值，target 保持 nil。
		case OpMove:
			if c.compartmentID == "" {
				return nil, nil, nil, failf(ErrStateMismatch, cid, "货物 %s 未装载，不能移动", cid)
			}
			tid := strings.TrimSpace(op.Target)
			if tid == "" {
				return nil, nil, nil, failf(ErrInvalidID, "", "目标舱位编号为空")
			}
			t, ok := r.compartments[tid]
			if !ok {
				return nil, nil, nil, failf(ErrNotFound, tid, "舱位 %s 不存在", tid)
			}
			if t.id == c.compartmentID {
				return nil, nil, nil, failf(ErrDuplicateOp, cid, "货物 %s 已在舱位 %s，不能移动到原舱位", cid, t.id)
			}
			target = t
		default:
			return nil, nil, nil, failf(ErrInvalidOp, cid, "货物 %s 的操作种类 %d 无法识别", cid, int(op.Kind))
		}

		rt := routeFor(op.Kind, c.compartmentID, target)
		vops = append(vops, validOp{cargo: c, rt: rt})
		touchedCompartments(affected, rt)
	}

	// 在模拟配载上应用全部操作，落点与正式生效共用 relocate，判断以
	// 完成后的最终状态为准。
	final = make(map[string]map[string]*cargo, len(r.compartments))
	for id, comp := range r.compartments {
		set := make(map[string]*cargo, len(comp.cargo))
		for cid, c := range comp.cargo {
			set[cid] = c
		}
		final[id] = set
	}
	for _, v := range vops {
		relocate(v.cargo, v.rt, final[v.rt.from], final[v.rt.to])
	}
	return vops, final, affected, nil
}

// evaluateFinal 按最终配载评估每个舱位的承重与混装限制，一个舱位可以
// 同时得到重量原因（超重或溢出）与混装原因。结果不保证顺序。
// 调用方必须持有 r.mu。
func (r *Registry) evaluateFinal(final map[string]map[string]*cargo) []*Rejection {
	var rejs []*Rejection
	for id, set := range final {
		comp := r.compartments[id]

		// 重量原因：合计规则与快照共用同一入口；溢出优先，溢出时不提供
		// 任何重量数值。
		sum, ok := weightTotal(set)
		var rej *Rejection
		if !ok {
			rej = &Rejection{
				CompartmentID: id,
				Kind:          ErrOverflow,
				MaxWeight:     comp.maxWeight,
			}
		} else if sum > comp.maxWeight {
			rej = &Rejection{
				CompartmentID:   id,
				Kind:            ErrOverweight,
				MaxWeight:       comp.maxWeight,
				UsedWeight:      sum,
				RemainingWeight: comp.maxWeight - sum,
				Overweight:      sum - comp.maxWeight,
			}
		}

		// 混装原因：不同目的地共舱时，列出全部不允许混装的货物。
		destinations := make(map[string][]string)
		destSet := make(map[string]bool)
		var offenders []string
		for _, c := range set {
			destinations[c.destination] = append(destinations[c.destination], c.id)
			destSet[c.destination] = true
			if !c.allowMixed {
				offenders = append(offenders, c.id)
			}
		}
		if len(destSet) > 1 && len(offenders) > 0 {
			dests := make([]MixedDestination, 0, len(destinations))
			for dest, ids := range destinations {
				sorted := append([]string(nil), ids...)
				sort.Strings(sorted)
				dests = append(dests, MixedDestination{Destination: dest, CargoIDs: sorted})
			}
			sort.Slice(dests, func(i, j int) bool {
				return dests[i].Destination < dests[j].Destination
			})
			sort.Strings(offenders)
			mr := &Rejection{
				CompartmentID:  id,
				Kind:           ErrMixedLoading,
				MaxWeight:      comp.maxWeight,
				Destinations:   dests,
				OffendingCargo: offenders,
			}
			if rej != nil {
				rejs = append(rejs, rej, mr)
			} else {
				rejs = append(rejs, mr)
			}
		} else if rej != nil {
			rejs = append(rejs, rej)
		}
	}
	return rejs
}

// firstRejection 以与正式调整一致的顺序返回第一个拒绝原因；没有则 nil。
// 调用方必须持有 r.mu。
func (r *Registry) firstRejection(final map[string]map[string]*cargo) *Rejection {
	rejs := r.evaluateFinal(final)
	if len(rejs) == 0 {
		return nil
	}
	sort.Slice(rejs, func(i, j int) bool {
		if rejs[i].CompartmentID != rejs[j].CompartmentID {
			return rejs[i].CompartmentID < rejs[j].CompartmentID
		}
		return rejKindOrder(rejs[i].Kind) < rejKindOrder(rejs[j].Kind)
	})
	return rejs[0]
}

// rejKindOrder 规定同一舱位的拒绝原因次序：重量（超重/溢出）先于混装。
func rejKindOrder(k ErrorKind) int {
	switch k {
	case ErrOverflow, ErrOverweight:
		return 0
	default:
		return 1
	}
}

// firstOffender 返回混装原因中编号最小的货物，用于错误说明。
func (x *Rejection) firstOffender() string {
	if len(x.OffendingCargo) == 0 {
		return ""
	}
	return x.OffendingCargo[0]
}

// canonicalKey 生成调整内容的规范化键：操作种类、货物、目标舱位相同
// 即视为相同内容，排列顺序不影响判断。卸下操作忽略目标舱位（执行时
// 同样忽略），其目标值不影响内容判断；装载与移动的目标舱位参与比较。
// 编号按去掉首尾空白后的原始字节精确比较：任何字节差异都视为不同内容，
// 包含无效 UTF-8 字节的编号与包含 Unicode 替代字符 U+FFFD 的编号也不相同。
func canonicalKey(ops []Op) string {
	sorted := make([]Op, len(ops))
	for i, op := range ops {
		target := strings.TrimSpace(op.Target)
		if op.Kind == OpUnload {
			target = ""
		}
		sorted[i] = Op{
			Kind:    op.Kind,
			CargoID: strings.TrimSpace(op.CargoID),
			Target:  target,
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		if sorted[i].CargoID != sorted[j].CargoID {
			return sorted[i].CargoID < sorted[j].CargoID
		}
		return sorted[i].Target < sorted[j].Target
	})
	// 长度前缀编码保留原始字节，不做任何字符集层面的规整，
	// 任意字节内容都不会因分隔符或转义而碰撞。
	var b strings.Builder
	for _, op := range sorted {
		b.WriteString(strconv.Itoa(int(op.Kind)))
		b.WriteByte('|')
		writeRawField(&b, op.CargoID)
		writeRawField(&b, op.Target)
	}
	return b.String()
}

// writeRawField 以“长度:原始字节”的形式写入一个字段。
func writeRawField(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}
