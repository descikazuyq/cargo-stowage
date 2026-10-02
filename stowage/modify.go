package stowage

import "strings"

// ModifyCargo 修改一件已登记货物的重量、目的地与是否允许混装。
//
// 编号去掉首尾空白后识别并区分大小写；重量必须为正整数千克，目的地去掉
// 首尾空白后不得为空。三项资料作为一个整体一次替换：任一不合法，整次
// 修改不生效，货物的旧资料、所属舱位重量与其他货物均保持原样，不会只
// 保存其中合法的部分。提交与现有资料相同的内容也视为成功，不新增货物。
//
// 未装载货物只需满足资料本身的要求，修改后仍为未装载，编号保持原样。
// 已装载货物按三项新资料共同生效后的最终配载判断，不会因修改某个字段
// 的中间状态不合法而拒绝最终合法的资料：所属舱位的已用重量扣除旧重量、
// 再计入新重量，剩余重量随之变化；恰好达到承重允许保存，超过承重或
// 合计无法用 int64 表示则拒绝。混装规则与调整一致——同舱目的地相同
// 时仍允许不混装的货物共舱，出现不同目的地时所有货物都必须允许混装。
// 重量与混装同时不合法时先报告重量原因；溢出时不提供回绕后的总重量。
//
// 修改不占用调整编号，也不改写已成功调整保存的结果：之后以原编号和
// 原内容重复提交调整，仍返回首次成功时的变化与重量，不重新执行。
// 修改前已经返回的查询或预览快照继续保留原内容，调用方修改这些返回
// 值不影响更正后的登记资料。
func (r *Registry) ModifyCargo(id string, weight int64, destination string, allowMixed bool) error {
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
	c, ok := r.cargos[cid]
	if !ok {
		return fail(ErrNotFound, cid, "货物 %s 不存在", cid)
	}

	// 未装载货物只须满足资料本身的要求，不涉及配载。
	if c.compartmentID == "" {
		c.weight = weight
		c.destination = dest
		c.allowMixed = allowMixed
		return nil
	}

	// 已装载货物：在模拟配载上按三项新资料共同生效后的最终状态判断，
	// 不会因修改某个字段的中间状态不合法而拒绝最终合法的资料。
	comp := r.compartments[c.compartmentID]
	simSet := make(map[string]*cargo, len(comp.cargo))
	for memberID, member := range comp.cargo {
		if memberID == c.id {
			modified := *c
			modified.weight = weight
			modified.destination = dest
			modified.allowMixed = allowMixed
			simSet[memberID] = &modified
		} else {
			simSet[memberID] = member
		}
	}
	final := map[string]map[string]*cargo{comp.id: simSet}

	if rej := r.firstRejection(final); rej != nil {
		switch rej.Kind {
		case ErrOverflow:
			return fail(ErrOverflow, rej.CompartmentID, "舱位 %s 重量合计超过 int64 可表示范围", rej.CompartmentID)
		case ErrOverweight:
			return fail(ErrOverweight, rej.CompartmentID,
				"舱位 %s 总重量 %d 千克超过最大承重 %d 千克",
				rej.CompartmentID, rej.UsedWeight, rej.MaxWeight)
		default:
			return fail(ErrMixedLoading, rej.firstOffender(),
				"舱位 %s 存在不同目的地货物，但货物 %s 不允许混装",
				rej.CompartmentID, rej.firstOffender())
		}
	}

	// 全部合法，正式生效：货物编号与所属舱位保持原样。
	c.weight = weight
	c.destination = dest
	c.allowMixed = allowMixed
	return nil
}
