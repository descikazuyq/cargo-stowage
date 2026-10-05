package stowage

// 本文件集中维护装载、卸下、移动三种操作的货物去向规则，使预演安排
// （Preview）、正式生效（Adjust）与货物变化记录三处共用同一份去向判断，
// 避免某一处修改后其他结果不同步。
//
// 去向只有三条：装载从未装载到目标舱位；卸下从原舱位回到未装载；
// 移动从原舱位到目标舱位。未装载一律以空舱位编号表示。

// cargoRoute 描述一件货物在一次调整中的去向：from 为原舱位、to 为
// 最终舱位，空字符串表示未装载。
type cargoRoute struct {
	from string
	to   string
}

// routeFor 是三种操作唯一的去向规则。调用方必须已完成逐条校验：
// 装载与移动的 target 非 nil，货物当前状态与操作种类相符；卸下忽略
// 目标值，target 为 nil 且不参与去向。
//
// 预演的货物变化记录、模拟配载落点与正式生效都由这里得到 from/to，
// 因此同一批安排在预览与正式提交中逐件货物的原舱位、最终舱位一致。
func routeFor(kind OpKind, current string, target *compartment) cargoRoute {
	switch kind {
	case OpLoad:
		return cargoRoute{from: "", to: target.id}
	case OpUnload:
		return cargoRoute{from: current, to: ""}
	case OpMove:
		return cargoRoute{from: current, to: target.id}
	}
	// 不可达：prepareOps 已拒绝无法识别的操作种类，routeFor 只会收到
	// 装载、卸下、移动三种之一。
	return cargoRoute{from: current, to: current}
}

// relocate 按去向把一件货物从源舱位集合移到目标舱位集合，是模拟配载与
// 真实配载唯一的落点逻辑：from 非空时从源集合删除，to 非空时加入目标
// 集合。from/to 为空字符串表示该侧不涉及任何舱位（未装载），相应集合
// 传 nil 即为无操作。调用方持锁。
func relocate(c *cargo, rt cargoRoute, fromSet, toSet map[string]*cargo) {
	if rt.from != "" {
		delete(fromSet, c.id)
	}
	if rt.to != "" {
		toSet[c.id] = c
	}
}

// touchedCompartments 按去向登记受影响舱位：装载只影响目标舱位，卸下
// 只影响原舱位，移动同时影响原舱位与目标舱位（交换中的两个舱位都会
// 纳入）。空舱位编号（未装载）不登记。
func touchedCompartments(affected map[string]bool, rt cargoRoute) {
	if rt.from != "" {
		affected[rt.from] = true
	}
	if rt.to != "" {
		affected[rt.to] = true
	}
}

// cargoSet 取登记处中某舱位当前的货物集合；空编号（未装载）返回 nil。
// 调用方持锁。
func (r *Registry) cargoSet(compartmentID string) map[string]*cargo {
	if compartmentID == "" {
		return nil
	}
	return r.compartments[compartmentID].cargo
}
