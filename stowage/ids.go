package stowage

import "strings"

// 编号识别规则：货物、舱位与调整编号只去掉首尾空白，其余字节原样
// 保留——区分大小写，编号内部的空白、冒号、竖线以及原始字节差异（包括
// 无效 UTF-8 字节）都参与区分，不做任何字符集层面的规整。登记处能
// 分别登记和查询的两个编号，在配载操作检查与调整内容比较中也必须是
// 两个对象；成功结果与错误中的对象编号一律使用去空白后的值。
//
// 这一规则只在本文件维护一份：登记、查询、资料更正、配载操作检查与
// 调整内容比较都经由 trimID / normalizeOp 识别编号，因此预览、正式
// 提交与按原调整编号再次提交对同一批编号的识别始终一致。

// trimID 按编号识别规则规范化单个编号：去掉首尾空白，其余字节不动。
func trimID(id string) string {
	return strings.TrimSpace(id)
}

// normalizeOp 按编号识别规则规范化一条配载操作：货物编号去掉首尾空白；
// 装载与移动的目标舱位编号同样只去首尾空白，卸下操作忽略目标值（执行
// 时同样忽略），其目标一律视为空。操作种类与编号内部字节原样保留。
//
// 逐条校验（prepareOps）与调整内容比较（canonicalKey）都以此为准，
// 同一份安排在预览、正式提交与重复提交中被识别为同一批对象。
func normalizeOp(op Op) Op {
	n := Op{Kind: op.Kind, CargoID: trimID(op.CargoID)}
	if op.Kind != OpUnload {
		n.Target = trimID(op.Target)
	}
	return n
}
