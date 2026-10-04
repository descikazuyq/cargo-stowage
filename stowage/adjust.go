package stowage

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Adjust 提交一次调整，可同时包含多件货物的装载、卸下和移动。
//
// 调整编号遵循编号规则（去掉首尾空白后非空）。成功后以相同编号和
// 相同内容重复提交，返回首次成功的结果而不再改变配载，即使之后另有
// 调整也一样；同一编号用于不同内容被拒绝。失败的调整不占用编号，
// 可以修正后重试。卸下操作忽略目标舱位，其目标值不影响卸下结果，
// 也不参与内容判断。内容比较按编号去空白后的原始字节进行：登记处能
// 区分的编号（包括含无效 UTF-8 字节的编号）在内容判断中同样区分。
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

	// 按最终配载校验承重与混装限制，返回第一个问题即拒绝整次调整。
	if rej := r.firstRejection(final); rej != nil {
		switch rej.Kind {
		case ErrOverflow:
			return nil, f(ErrOverflow, rej.CompartmentID, "舱位 %s 重量合计超过 int64 可表示范围", rej.CompartmentID)
		case ErrOverweight:
			return nil, f(ErrOverweight, rej.CompartmentID,
				"舱位 %s 总重量 %d 千克超过最大承重 %d 千克",
				rej.CompartmentID, rej.UsedWeight, r.compartments[rej.CompartmentID].maxWeight)
		default:
			return nil, f(ErrMixedLoading, rej.firstOffender(),
				"舱位 %s 存在不同目的地货物，但货物 %s 不允许混装",
				rej.CompartmentID, rej.firstOffender())
		}
	}

	// 全部合法，正式生效。先计算受影响舱位调整前后的重量。
	cargoChanges := make([]CargoChange, 0, len(vops))
	compChanges := make([]CompartmentChange, 0, len(affected))
	for id := range affected {
		compChanges = append(compChanges, CompartmentChange{
			CompartmentID: id,
			WeightBefore:  sumSet(r.compartments[id].cargo),
			WeightAfter:   sumSet(final[id]),
		})
	}
	sort.Slice(compChanges, func(i, j int) bool {
		return compChanges[i].CompartmentID < compChanges[j].CompartmentID
	})

	for _, v := range vops {
		c := v.cargo
		from := c.compartmentID
		var to string
		switch v.op.Kind {
		case OpLoad:
			r.compartments[v.target.id].cargo[c.id] = c
			c.compartmentID = v.target.id
			to = v.target.id
		case OpUnload:
			delete(r.compartments[from].cargo, c.id)
			c.compartmentID = ""
		case OpMove:
			delete(r.compartments[from].cargo, c.id)
			r.compartments[v.target.id].cargo[c.id] = c
			c.compartmentID = v.target.id
			to = v.target.id
		}
		cargoChanges = append(cargoChanges, CargoChange{CargoID: c.id, From: from, To: to})
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

// validOp 是一条通过逐条校验的操作，附带其涉及的内部记录。
type validOp struct {
	op     Op
	cargo  *cargo
	target *compartment // 装载/移动的目标舱位；卸下时为 nil
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
			vops = append(vops, validOp{op, c, t})
			affected[t.id] = true
		case OpUnload:
			if c.compartmentID == "" {
				return nil, nil, nil, failf(ErrStateMismatch, cid, "货物 %s 未装载，不能卸下", cid)
			}
			vops = append(vops, validOp{op, c, nil})
			affected[c.compartmentID] = true
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
			vops = append(vops, validOp{op, c, t})
			affected[c.compartmentID] = true
			affected[t.id] = true
		default:
			return nil, nil, nil, failf(ErrInvalidOp, cid, "货物 %s 的操作种类 %d 无法识别", cid, int(op.Kind))
		}
	}

	// 在模拟配载上应用全部操作，判断以完成后的最终状态为准。
	final = make(map[string]map[string]*cargo, len(r.compartments))
	for id, comp := range r.compartments {
		set := make(map[string]*cargo, len(comp.cargo))
		for cid, c := range comp.cargo {
			set[cid] = c
		}
		final[id] = set
	}
	for _, v := range vops {
		switch v.op.Kind {
		case OpLoad:
			final[v.target.id][v.cargo.id] = v.cargo
		case OpUnload:
			delete(final[v.cargo.compartmentID], v.cargo.id)
		case OpMove:
			delete(final[v.cargo.compartmentID], v.cargo.id)
			final[v.target.id][v.cargo.id] = v.cargo
		}
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

		// 重量原因：溢出优先，溢出时不提供任何重量数值。
		var sum int64
		overflow := false
		for _, c := range set {
			if c.weight > math.MaxInt64-sum {
				overflow = true
				break
			}
			sum += c.weight
		}
		var rej *Rejection
		if overflow {
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
// 即视为相同内容，排列顺序不影响判断。卸下操作忽略目标舱位，其目标值
// 不参与内容判断。编号按去掉首尾空白后的原始字节精确比较：任何字节
// 差异都视为不同内容，包含无效 UTF-8 字节的编号与包含 Unicode 替代
// 字符 U+FFFD 的编号也不相同。
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
