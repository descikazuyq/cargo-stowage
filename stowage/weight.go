package stowage

import (
	"math"
	"sort"
)

// 本文件是舱位重量规则的唯一维护位置：正常重量以正整数千克计量，
// 舱位已用重量是其中全部货物重量之和，剩余重量为承重减去已用重量，
// 空舱已用为零、剩余等于承重。查询、预览、正式调整（Adjust）与资料
// 更正（AmendCargo）都通过这里的规则计算重量，保证各处对合计、
// “恰好达到承重允许、超过承重拒绝”以及 int64 溢出的处理一致。

// weightTally 是一组货物重量合计的结果。
//
// sum 为全部货物重量之和；overflow 表示该合计无法用 int64 表示。
// 溢出时不保留任何合计数值（sum 为零值），调用方不得展示回绕后的
// 数字；货物清单本身仍由调用方照常返回。
type weightTally struct {
	sum      int64
	overflow bool
}

// tallyWeights 按给定顺序合计重量并检测 int64 溢出，是全部重量合计
// 逻辑（查询快照、预览前后配载、调整前后舱位重量、最终配载校验）共用的
// 唯一入口。合计超过 int64 最大值时 overflow 为 true，且不会产生回绕值。
func tallyWeights(weights []int64) weightTally {
	var sum int64
	for _, w := range weights {
		if w > math.MaxInt64-sum {
			return weightTally{overflow: true}
		}
		sum += w
	}
	return weightTally{sum: sum}
}

// tallyCargoWeights 合计一组货物的重量。为保证结果确定，合计按货物编号
// 字典序进行；溢出与否与遍历顺序无关，排序只让累加路径在各调用间一致。
func tallyCargoWeights(set map[string]*cargo) weightTally {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	weights := make([]int64, 0, len(ids))
	for _, id := range ids {
		weights = append(weights, set[id].weight)
	}
	return tallyWeights(weights)
}

// usedWeight 合计一组货物的已用重量。该结果只用于合计必然可表示的场景：
// 登记处内的实际配载、已通过承重与溢出校验的最终配载，以及调整记录的
// 前后重量。可能溢出的模拟配载必须使用 tallyCargoWeights 判断后再展示。
func usedWeight(set map[string]*cargo) int64 {
	return tallyCargoWeights(set).sum
}

// weightRejection 按舱位承重评估一组货物的重量规则，构造对应的重量拒绝
// 原因；重量合法（含恰好达到承重）时返回 nil。三种结果互斥：
//
//   - 合计超过 int64 最大值：ErrOverflow，预计已用、剩余与超出量无法用
//     int64 表示，对应数值字段保持零值，仅提供舱位编号与承重；
//   - 合计可表示但超过承重：ErrOverweight，保留真实预计总重量、负的剩余
//     重量与超出量；
//   - 合计不超过承重（恰好相等也算）：nil。
//
// 同一份配载的重量原因与混装原因由调用方另行组合，本函数只管重量。
func weightRejection(compartmentID string, maxWeight int64, set map[string]*cargo) *Rejection {
	tally := tallyCargoWeights(set)
	if tally.overflow {
		return &Rejection{
			CompartmentID: compartmentID,
			Kind:          ErrOverflow,
			MaxWeight:     maxWeight,
		}
	}
	if tally.sum > maxWeight {
		return &Rejection{
			CompartmentID:   compartmentID,
			Kind:            ErrOverweight,
			MaxWeight:       maxWeight,
			UsedWeight:      tally.sum,
			RemainingWeight: maxWeight - tally.sum,
			Overweight:      tally.sum - maxWeight,
		}
	}
	return nil
}
