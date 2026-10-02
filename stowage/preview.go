package stowage

import (
	"math"
	"sort"
)

// CompartmentPreview 是一个受影响舱位调整前后的配载快照。
type CompartmentPreview struct {
	CompartmentID    string
	CargoBefore      []CargoView // 调整前完整货物清单，按编号字典序排列
	CargoAfter       []CargoView // 调整后完整货物清单，按编号字典序排列
	UsedWeightBefore int64       // 调整前已用重量
	UsedWeightAfter  int64       // 调整后预计已用重量；Overflow 为 true 时不提供
	RemainingBefore  int64       // 调整前剩余重量
	RemainingAfter   int64       // 调整后预计剩余重量；Overflow 为 true 时不提供
	Overflow         bool        // 预计重量合计超过 int64 可表示范围
}

// DestinationCargo 描述同一目的地对应的货物。
type DestinationCargo struct {
	Destination string
	CargoIDs    []string // 按编号字典序排列
}

// PreviewRejection 是预览发现的一条不可提交原因。
type PreviewRejection struct {
	Kind          ErrorKind // ErrOverweight、ErrOverflow 或 ErrMixedLoading
	CompartmentID string

	// 超重原因（Kind == ErrOverweight）：
	MaxWeight   int64 // 舱位最大承重
	TotalWeight int64 // 预计总重量
	OverWeight  int64 // 超出承重的千克数

	// 混装原因（Kind == ErrMixedLoading）：
	Destinations []DestinationCargo // 各目的地对应的货物，按目的地编号字典序排列
	UnmixedCargo []string           // 全部不允许混装的货物编号，按字典序排列
}

// PreviewResult 是配载预览的结果。
//
// 预览不改变任何登记记录：无论结果如何，货物位置、舱位占用和已成功调整
// 记录都保持不变。返回的所有切片均为独立快照，调用方修改不影响登记处或
// 之后的预览。
type PreviewResult struct {
	Submittable         bool                 // 整批操作是否可提交
	CargoChanges        []CargoChange        // 涉及货物的原舱位与预计舱位，按编号字典序排列；未装载用空编号表示
	CompartmentPreviews []CompartmentPreview // 所有受影响舱位调整前后的配载，按编号字典序排列
	Rejections          []PreviewRejection   // 所有拒绝原因，按舱位编号、同舱先重量后混装排列
}

// Preview 预览一批装载、卸下、移动操作完成后的配载，但不改变任何登记记录。
//
// 沿用已有操作表示和编号规则，预览不需要调整编号，也不占用编号。无论结果
// 如何，货物位置、舱位占用和已成功调整记录都保持不变。
//
// 清单为空时返回已有的调整为空错误。逐条校验操作本身的合法性，任一操作
// 不合法时按输入顺序返回第一个非法操作的现有错误种类及涉及对象，不返回
// 局部预览。预览与正式提交对货物状态和重复操作的判断保持一致。
//
// 操作本身全部合法时，按整批操作完成后的最终配载判断是否可提交：舱位总
// 重量不得超过承重；不同目的地共舱时所有货物都必须允许混装。判断以整批
// 操作完成后的配载为准，不能因为某一步暂时超过承重而拒绝最终合法的交换。
//
// 预计配载违反承重或混装限制时，仍返回预计变化，并一次列全所有受影响舱位
// 的拒绝原因。超重说明舱位编号、承重、预计总重量及超出千克数；混装冲突
// 说明舱位、各目的地对应的货物及其中全部不允许混装的货物编号。只要存在
// 拒绝原因就标记为不可提交。同一舱位可同时有两种原因，按舱位编号排列，
// 同舱先重量后混装；目的地与对应货物按字典序排列。预计重量超过 int64
// 范围时用重量溢出原因替代超重原因，该舱位不提供预计已用重量、剩余重量
// 和超出量的数值，货物清单和混装原因照常返回。
//
// 预览反映同一个时刻的登记和配载，不混合并发调整前后的记录。实际状态
// 未变时，连续预览同一清单结果一致；正式提交仍按提交时状态判断，不能
// 直接使用旧预览放行。
func (r *Registry) Preview(ops []Op) (*PreviewResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(ops) == 0 {
		return nil, fail(ErrEmptyAdjustment, "", "预览不包含任何操作")
	}

	vops, affected, verr := r.validateOps(ops)
	if verr != nil {
		return nil, verr
	}

	final := r.simulate(vops)

	res := &PreviewResult{Submittable: true}

	// 涉及货物的原舱位与预计舱位。
	cargoChanges := make([]CargoChange, 0, len(vops))
	for _, v := range vops {
		c := v.cargo
		from := c.compartmentID
		var to string
		switch v.op.Kind {
		case OpLoad:
			to = v.target.id
		case OpUnload:
			to = ""
		case OpMove:
			to = v.target.id
		}
		cargoChanges = append(cargoChanges, CargoChange{CargoID: c.id, From: from, To: to})
	}
	sort.Slice(cargoChanges, func(i, j int) bool {
		return cargoChanges[i].CargoID < cargoChanges[j].CargoID
	})
	res.CargoChanges = cargoChanges

	// 受影响舱位按编号字典序排列。
	compIDs := make([]string, 0, len(affected))
	for id := range affected {
		compIDs = append(compIDs, id)
	}
	sort.Strings(compIDs)

	for _, id := range compIDs {
		before := r.compartments[id]
		afterSet := final[id]

		cp := CompartmentPreview{
			CompartmentID:    id,
			CargoBefore:      sortedCargoViews(before.cargo),
			CargoAfter:       sortedCargoViews(afterSet),
			UsedWeightBefore: sumSet(before.cargo),
		}

		// 重量合计，检测 int64 溢出。
		var sum int64
		overflow := false
		for _, c := range afterSet {
			if c.weight > math.MaxInt64-sum {
				overflow = true
				break
			}
			sum += c.weight
		}

		if overflow {
			cp.Overflow = true
			res.Rejections = append(res.Rejections, PreviewRejection{
				Kind:          ErrOverflow,
				CompartmentID: id,
			})
		} else {
			cp.UsedWeightAfter = sum
			cp.RemainingBefore = before.maxWeight - cp.UsedWeightBefore
			cp.RemainingAfter = before.maxWeight - sum
			if sum > before.maxWeight {
				res.Rejections = append(res.Rejections, PreviewRejection{
					Kind:          ErrOverweight,
					CompartmentID: id,
					MaxWeight:     before.maxWeight,
					TotalWeight:   sum,
					OverWeight:    sum - before.maxWeight,
				})
			}
		}

		// 混装冲突：按目的地分组。
		destGroups := make(map[string][]string)
		for _, c := range afterSet {
			destGroups[c.destination] = append(destGroups[c.destination], c.id)
		}
		if len(destGroups) > 1 {
			groups := make([]DestinationCargo, 0, len(destGroups))
			for dest, ids := range destGroups {
				sort.Strings(ids)
				groups = append(groups, DestinationCargo{Destination: dest, CargoIDs: ids})
			}
			sort.Slice(groups, func(i, j int) bool {
				return groups[i].Destination < groups[j].Destination
			})
			var unmixed []string
			for _, c := range afterSet {
				if !c.allowMixed {
					unmixed = append(unmixed, c.id)
				}
			}
			sort.Strings(unmixed)
			res.Rejections = append(res.Rejections, PreviewRejection{
				Kind:          ErrMixedLoading,
				CompartmentID: id,
				Destinations:  groups,
				UnmixedCargo:  unmixed,
			})
		}

		res.CompartmentPreviews = append(res.CompartmentPreviews, cp)
	}

	// 拒绝原因按舱位编号、同舱先重量后混装排列。
	sort.SliceStable(res.Rejections, func(i, j int) bool {
		if res.Rejections[i].CompartmentID != res.Rejections[j].CompartmentID {
			return res.Rejections[i].CompartmentID < res.Rejections[j].CompartmentID
		}
		return rejectionOrder(res.Rejections[i].Kind) < rejectionOrder(res.Rejections[j].Kind)
	})

	res.Submittable = len(res.Rejections) == 0
	return res, nil
}

// rejectionOrder 返回拒绝原因在同一舱位内的排列次序：重量原因在前，混装在后。
func rejectionOrder(kind ErrorKind) int {
	switch kind {
	case ErrOverweight, ErrOverflow:
		return 0
	case ErrMixedLoading:
		return 1
	default:
		return 2
	}
}

// sortedCargoViews 返回货物集合按编号字典序排列的独立快照（调用方持锁）。
func sortedCargoViews(set map[string]*cargo) []CargoView {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	views := make([]CargoView, 0, len(ids))
	for _, id := range ids {
		views = append(views, *cargoView(set[id]))
	}
	return views
}
