package stowage

import "strings"

// AmendCargo 按货物编号更正一件货物的重量、目的地与混装许可。
//
// 编号与目的地沿用登记规则：去掉首尾空白后不得为空，编号区分大小写；
// weight 必须为正整数（千克）。空编号、货物不存在、非正重量或去空白后
// 目的地为空都按现有结构化错误拒绝；修改不存在的货物不会变成登记。
//
// 三项资料作为一次完整替换生效，内容与现有资料完全相同时同样成功，
// 但不新增货物、不再次占用重量。更正不占用配载调整编号，也不影响已
// 成功调整保存的结果：之后以原编号、原内容重复提交调整，仍返回首次
// 成功时的变化与重量。更正前返回的查询或预览快照继续保留原内容。
//
// 未装载货物只需资料本身合法，更正后仍为未装载。已装载货物按三项新
// 资料共同生效后的最终配载判断，不会因修改某个字段的中间状态不合法
// 而拒绝最终合法的资料：所属舱位先扣除该货物旧重量、再计入新重量，
// 恰好达到承重允许保存，超过承重或合计无法用 int64 表示则拒绝；
// 同舱目的地全部相同时允许不混装的货物共舱，出现不同目的地时所有
// 货物都必须允许混装。
//
// 更正失败时，货物的三项旧资料、所属舱位重量及其他货物全部保留。
func (r *Registry) AmendCargo(id string, weight int64, destination string, allowMixed bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cid := strings.TrimSpace(id)
	if cid == "" {
		return fail(ErrInvalidID, "", "货物编号为空")
	}
	c, ok := r.cargos[cid]
	if !ok {
		return fail(ErrNotFound, cid, "货物 %s 不存在", cid)
	}
	if weight <= 0 {
		return fail(ErrInvalidWeight, cid, "货物 %s 重量必须为正整数千克，当前为 %d", cid, weight)
	}
	dest := strings.TrimSpace(destination)
	if dest == "" {
		return fail(ErrInvalidDestination, cid, "货物 %s 目的地为空", cid)
	}

	// 已装载货物：在所属舱位的模拟配载上替换该货物，按三项资料共同
	// 生效后的最终状态统一判断，而不是逐个字段制造中间状态。
	if c.compartmentID != "" {
		comp := r.compartments[c.compartmentID]
		sim := &cargo{
			id:            c.id,
			weight:        weight,
			destination:   dest,
			allowMixed:    allowMixed,
			compartmentID: comp.id,
		}
		set := make(map[string]*cargo, len(comp.cargo))
		for otherID, other := range comp.cargo {
			if otherID == c.id {
				set[c.id] = sim
			} else {
				set[otherID] = other
			}
		}
		final := map[string]map[string]*cargo{comp.id: set}
		if rej := r.firstRejection(final); rej != nil {
			switch rej.Kind {
			case ErrOverflow:
				return fail(ErrOverflow, comp.id, "舱位 %s 重量合计超过 int64 可表示范围", comp.id)
			case ErrOverweight:
				return fail(ErrOverweight, comp.id,
					"舱位 %s 总重量 %d 千克超过最大承重 %d 千克",
					comp.id, rej.UsedWeight, comp.maxWeight)
			default:
				return fail(ErrMixedLoading, rej.firstOffender(),
					"舱位 %s 存在不同目的地货物，但货物 %s 不允许混装",
					comp.id, rej.firstOffender())
			}
		}
	}

	c.weight = weight
	c.destination = dest
	c.allowMixed = allowMixed
	return nil
}
