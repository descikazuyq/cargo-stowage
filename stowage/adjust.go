package stowage

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// Adjust 提交一次调整，可同时包含多件货物的装载、卸下和移动。
//
// 调整编号遵循编号规则（去掉首尾空白后非空）。成功后以相同编号和
// 相同内容重复提交，返回首次成功的结果而不再改变配载，即使之后另有
// 调整也一样；同一编号用于不同内容被拒绝。失败的调整不占用编号，
// 可以修正后重试。
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

	// 逐条校验操作本身的合法性。
	vops, affected, verr := r.validateOps(ops)
	if verr != nil {
		verr.AdjustmentID = aid
		return nil, verr
	}

	// 在模拟配载上应用全部操作。
	final := r.simulate(vops)

	// 按最终配载校验承重与混装限制。
	for id, set := range final {
		var sum int64
		destinations := make(map[string]bool)
		for _, c := range set {
			if c.weight > math.MaxInt64-sum {
				return nil, f(ErrOverflow, id, "舱位 %s 重量合计超过 int64 可表示范围", id)
			}
			sum += c.weight
			destinations[c.destination] = true
		}
		if sum > r.compartments[id].maxWeight {
			return nil, f(ErrOverweight, id, "舱位 %s 总重量 %d 千克超过最大承重 %d 千克", id, sum, r.compartments[id].maxWeight)
		}
		if len(destinations) > 1 {
			for _, c := range set {
				if !c.allowMixed {
					return nil, f(ErrMixedLoading, c.id, "舱位 %s 存在不同目的地货物，但货物 %s 不允许混装", id, c.id)
				}
			}
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

// canonicalKey 生成调整内容的规范化键：操作种类、货物、目标舱位相同
// 即视为相同内容，排列顺序不影响判断。
func canonicalKey(ops []Op) string {
	sorted := make([]Op, len(ops))
	for i, op := range ops {
		sorted[i] = Op{
			Kind:    op.Kind,
			CargoID: strings.TrimSpace(op.CargoID),
			Target:  strings.TrimSpace(op.Target),
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
	b, err := json.Marshal(sorted)
	if err != nil {
		// Op 仅含基本类型，不会失败。
		panic(err)
	}
	return string(b)
}

// validOp 是一条通过逐条合法性校验的操作。
type validOp struct {
	op     Op
	cargo  *cargo
	target *compartment // 装载/移动的目标舱位；卸下时为 nil
}

// validateOps 逐条校验操作本身的合法性（调用方持锁）。
//
// 返回通过校验的操作与受影响舱位集合。任一操作不合法时，按输入顺序返回
// 第一个非法操作的现有错误种类及涉及对象，不返回局部结果。
func (r *Registry) validateOps(ops []Op) ([]validOp, map[string]bool, *Error) {
	seen := make(map[string]bool)
	vops := make([]validOp, 0, len(ops))
	affected := make(map[string]bool)

	for _, op := range ops {
		cid := strings.TrimSpace(op.CargoID)
		if cid == "" {
			return nil, nil, fail(ErrInvalidID, "", "货物编号为空")
		}
		c, ok := r.cargos[cid]
		if !ok {
			return nil, nil, fail(ErrNotFound, cid, "货物 %s 不存在", cid)
		}
		if seen[cid] {
			return nil, nil, fail(ErrDuplicateOp, cid, "货物 %s 在同一次调整中出现多次", cid)
		}
		seen[cid] = true

		switch op.Kind {
		case OpLoad:
			if c.compartmentID != "" {
				return nil, nil, fail(ErrStateMismatch, cid, "货物 %s 已装载于舱位 %s，不能再次装载", cid, c.compartmentID)
			}
			tid := strings.TrimSpace(op.Target)
			if tid == "" {
				return nil, nil, fail(ErrInvalidID, "", "目标舱位编号为空")
			}
			t, ok := r.compartments[tid]
			if !ok {
				return nil, nil, fail(ErrNotFound, tid, "舱位 %s 不存在", tid)
			}
			vops = append(vops, validOp{op, c, t})
			affected[t.id] = true
		case OpUnload:
			if c.compartmentID == "" {
				return nil, nil, fail(ErrStateMismatch, cid, "货物 %s 未装载，不能卸下", cid)
			}
			vops = append(vops, validOp{op, c, nil})
			affected[c.compartmentID] = true
		case OpMove:
			if c.compartmentID == "" {
				return nil, nil, fail(ErrStateMismatch, cid, "货物 %s 未装载，不能移动", cid)
			}
			tid := strings.TrimSpace(op.Target)
			if tid == "" {
				return nil, nil, fail(ErrInvalidID, "", "目标舱位编号为空")
			}
			t, ok := r.compartments[tid]
			if !ok {
				return nil, nil, fail(ErrNotFound, tid, "舱位 %s 不存在", tid)
			}
			if t.id == c.compartmentID {
				return nil, nil, fail(ErrDuplicateOp, cid, "货物 %s 已在舱位 %s，不能移动到原舱位", cid, t.id)
			}
			vops = append(vops, validOp{op, c, t})
			affected[c.compartmentID] = true
			affected[t.id] = true
		default:
			return nil, nil, fail(ErrInvalidOp, cid, "货物 %s 的操作种类 %d 无法识别", cid, int(op.Kind))
		}
	}
	return vops, affected, nil
}

// simulate 在配载副本上应用全部操作，返回最终配载（调用方持锁）。
// 不修改任何内部记录。
func (r *Registry) simulate(vops []validOp) map[string]map[string]*cargo {
	final := make(map[string]map[string]*cargo, len(r.compartments))
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
	return final
}
