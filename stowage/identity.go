package stowage

import (
	"sort"
	"strconv"
	"strings"
)

// 本文件集中维护配载操作的“编号识别规则”，使三处入口共用同一份规整
// 结果，避免同一规则在不同地方各自维护后漂移：
//   - 操作逐条检查（prepareOps，Preview 预演与 Adjust 正式提交共用）；
//   - 已成功调整按原编号、原内容再次提交时的内容比较（canonicalKey）；
//   - 货物、舱位、调整编号在登记与查询处的识别（normalizeID）。
//
// 识别规则只有一条：编号只去掉首尾空白，仍区分大小写；编号内部的空白、
// 冒号、竖线以及原始字节差异都不做任何规整或合并。因此登记处能分别
// 登记、查询的两个编号，在操作检查与内容比较中也必定是两个对象，
// 包括含不同无效 UTF-8 字节的编号。

// normalizeID 是全登记处唯一的编号规整：只去掉首尾空白，其余字符
// （含内部空白、冒号、竖线、无效 UTF-8 字节）逐字节保留，并区分
// 大小写。货物编号、舱位编号与调整编号都经此识别；成功结果和错误中
// 携带的对象编号也都是这里返回的去首尾空白后的值。
func normalizeID(id string) string {
	return strings.TrimSpace(id)
}

// normOp 是一条操作按编号识别规则规整后的形态：货物编号与目标编号只
// 去掉首尾空白，操作种类与排列顺序不变。是否检查目标、目标是否参与
// 内容比较由操作种类决定（见 resolveTarget 与 contentTarget）；规整
// 阶段不丢弃卸下操作填写的目标，只是后续规则一律不读它。
type normOp struct {
	kind    OpKind
	cargoID string
	target  string
}

// normalizeOps 对一批操作统一规整编号，保持输入顺序。逐条检查与调整
// 内容比较都从这里取编号，保证给同一件货物、同一个目标舱位的编号加上
// 首尾空白，在预览、正式提交与再次提交中都不会变成另一个对象。
func normalizeOps(ops []Op) []normOp {
	out := make([]normOp, len(ops))
	for i, op := range ops {
		out[i] = normOp{
			kind:    op.Kind,
			cargoID: normalizeID(op.CargoID),
			target:  normalizeID(op.Target),
		}
	}
	return out
}

// contentTarget 返回一条操作参与内容比较的目标舱位：装载与移动使用
// 规整后的目标舱位，目标变化属于不同内容；卸下操作在执行时忽略目标，
// 其目标（空值、已登记或不存在的舱位）一律视为空编号、不参与比较。
func contentTarget(op normOp) string {
	if op.kind == OpUnload {
		return ""
	}
	return op.target
}

// canonicalKey 生成调整内容的规范化键：操作种类、货物、目标舱位相同
// 即视为相同内容，排列顺序不影响判断。入参必须是 normalizeOps 的规整
// 结果，比较只按去首尾空白后的原始字节精确进行：任何字节差异都视为
// 不同内容，包含无效 UTF-8 字节的编号与包含 Unicode 替代字符 U+FFFD
// 的编号也不相同。卸下操作的目标由 contentTarget 统一忽略。
func canonicalKey(nops []normOp) string {
	sorted := append([]normOp(nil), nops...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].kind != sorted[j].kind {
			return sorted[i].kind < sorted[j].kind
		}
		if sorted[i].cargoID != sorted[j].cargoID {
			return sorted[i].cargoID < sorted[j].cargoID
		}
		return contentTarget(sorted[i]) < contentTarget(sorted[j])
	})
	// 长度前缀编码保留原始字节，不做任何字符集层面的规整，
	// 任意字节内容都不会因分隔符或转义而碰撞。
	var b strings.Builder
	for _, op := range sorted {
		b.WriteString(strconv.Itoa(int(op.kind)))
		b.WriteByte('|')
		writeRawField(&b, op.cargoID)
		writeRawField(&b, contentTarget(op))
	}
	return b.String()
}

// writeRawField 以“长度:原始字节”的形式写入一个字段。
func writeRawField(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}
