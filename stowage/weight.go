package stowage

import "math"

// weightTotal 合计一组货物的重量，是同一份配载（实际或预计）唯一的
// 重量合计入口：查询快照、预览快照、配载校验与调整前后重量记录都经由
// 这里计算，保证重量规则只维护一份。调用方持锁。
//
// 全部货物重量都是正整数千克，合计可以用 int64 表示时返回该合计且
// ok 为 true；合计超过 int64 可表示范围时 ok 为 false，total 保持
// 零值——与预览快照和拒绝原因中“溢出时重量数值保持零值”的约定一致，
// 调用方不得在此情况下使用 total。
func weightTotal(set map[string]*cargo) (total int64, ok bool) {
	var sum int64
	for _, c := range set {
		if c.weight > math.MaxInt64-sum {
			return 0, false
		}
		sum += c.weight
	}
	return sum, true
}
