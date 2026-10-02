package stowage

import (
	"math"
	"sort"
)

// Preview 预演一批装载、卸下、移动操作，返回这批安排按当前配载完成后的
// 预计结果。
//
// 预览是只读的：无论结果能否提交，货物位置、舱位占用与已成功调整记录
// 都不会改变，也不占用任何调整编号。清单沿用与 Adjust 相同的 Op 表示
// 与编号规则，逐条合法性（货物与舱位是否存在、货物状态、重复操作、
// 移动到原舱位等）与正式提交完全一致：某条操作不合法时，按输入顺序
// 返回第一个非法操作的现有错误，不返回局部预览。
//
// 每条操作本身合法时始终返回完整预览：预计配载违反承重或混装限制时
// Submittable 为 false，并在 Rejections 中一次列全全部受影响舱位的
// 拒绝原因；判断以整批操作完成后的最终配载为准，不会因中间步骤暂时
// 超重而拒绝最终合法的交换。
//
// 预览反映调用时刻登记处的一致快照（登记、配载与已成功调整记录取自
// 同一时刻，不会混入并发调整前后的状态）。返回内容是独立快照，调用
// 方修改不影响登记处或之后的预览；状态未变时连续预览同一清单结果
// 一致。正式提交仍以提交时状态重新判断，不能凭旧预览直接放行。
func (r *Registry) Preview(ops []Op) (*PreviewResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(ops) == 0 {
		return nil, fail(ErrEmptyAdjustment, "", "预览清单不包含任何操作")
	}

	// 与正式提交共用同一套逐条校验与最终配载模拟；预览不带调整编号。
	vops, final, affected, err := r.prepareOps(ops, fail)
	if err != nil {
		return nil, err
	}

	// 涉及货物的原舱位与预计舱位，按货物编号字典序排列。
	cargoChanges := make([]CargoChange, 0, len(vops))
	for _, v := range vops {
		c := v.cargo
		change := CargoChange{CargoID: c.id, From: c.compartmentID}
		switch v.op.Kind {
		case OpLoad, OpMove:
			change.To = v.target.id
		case OpUnload:
			change.To = ""
		}
		cargoChanges = append(cargoChanges, change)
	}
	sort.Slice(cargoChanges, func(i, j int) bool {
		return cargoChanges[i].CargoID < cargoChanges[j].CargoID
	})

	// 受影响舱位调整前后的完整配载，按舱位编号字典序排列。交换中总重量
	// 不变的两个舱位同样列出。
	compIDs := make([]string, 0, len(affected))
	for id := range affected {
		compIDs = append(compIDs, id)
	}
	sort.Strings(compIDs)
	compartments := make([]CompartmentPreview, 0, len(compIDs))
	for _, id := range compIDs {
		compartments = append(compartments, CompartmentPreview{
			ID:     id,
			Before: *compartmentView(r.compartments[id]),
			After:  compartmentAfterView(r.compartments[id], final[id]),
		})
	}

	// 预计配载的全部拒绝原因，按舱位编号排列、同舱先重量后混装。
	rejs := r.evaluateFinal(final)
	sort.SliceStable(rejs, func(i, j int) bool {
		if rejs[i].CompartmentID != rejs[j].CompartmentID {
			return rejs[i].CompartmentID < rejs[j].CompartmentID
		}
		return rejKindOrder(rejs[i].Kind) < rejKindOrder(rejs[j].Kind)
	})
	rejections := make([]Rejection, 0, len(rejs))
	for _, rej := range rejs {
		rejections = append(rejections, *cloneRejection(rej))
	}

	return &PreviewResult{
		Submittable:  len(rejections) == 0,
		CargoChanges: cargoChanges,
		Compartments: compartments,
		Rejections:   rejections,
	}, nil
}

// compartmentAfterView 构造舱位在模拟配载下的完整快照。
// 重量合计溢出 int64 时，已用重量与剩余重量不提供数值（保持零值），
// 货物清单照常返回。调用方持锁。
func compartmentAfterView(comp *compartment, set map[string]*cargo) CompartmentView {
	ids := make([]string, 0, len(set))
	for cid := range set {
		ids = append(ids, cid)
	}
	sort.Strings(ids)
	cargoViews := make([]CargoView, 0, len(ids))
	var used int64
	overflow := false
	for _, cid := range ids {
		c := set[cid]
		cargoViews = append(cargoViews, *cargoView(c))
		if !overflow {
			if c.weight > math.MaxInt64-used {
				overflow = true
			} else {
				used += c.weight
			}
		}
	}
	view := CompartmentView{
		ID:        comp.id,
		MaxWeight: comp.maxWeight,
		Cargo:     cargoViews,
	}
	if !overflow {
		view.UsedWeight = used
		view.RemainingWeight = comp.maxWeight - used
	}
	return view
}

// cloneRejection 复制拒绝原因中的切片，保证调用方修改不影响其他结果。
func cloneRejection(x *Rejection) *Rejection {
	cp := &Rejection{
		CompartmentID:   x.CompartmentID,
		Kind:            x.Kind,
		MaxWeight:       x.MaxWeight,
		UsedWeight:      x.UsedWeight,
		RemainingWeight: x.RemainingWeight,
		Overweight:      x.Overweight,
	}
	if x.Destinations != nil {
		cp.Destinations = make([]MixedDestination, len(x.Destinations))
		for i, d := range x.Destinations {
			cp.Destinations[i] = MixedDestination{
				Destination: d.Destination,
				CargoIDs:    append([]string(nil), d.CargoIDs...),
			}
		}
	}
	if x.OffendingCargo != nil {
		cp.OffendingCargo = append([]string(nil), x.OffendingCargo...)
	}
	return cp
}
