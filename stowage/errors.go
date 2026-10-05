package stowage

import "fmt"

// ErrorKind 描述一次配载操作失败的具体原因。
type ErrorKind int

const (
	// ErrOK 表示没有错误（内部使用）。
	ErrOK ErrorKind = iota
	// ErrInvalidID 编号去掉首尾空白后为空。
	ErrInvalidID
	// ErrInvalidWeight 重量或最大承重不是正整数。
	ErrInvalidWeight
	// ErrInvalidDestination 目的地去掉首尾空白后为空。
	ErrInvalidDestination
	// ErrInvalidOp 操作种类无法识别。
	ErrInvalidOp
	// ErrAlreadyExists 登记对象已存在。
	ErrAlreadyExists
	// ErrNotFound 涉及的货物或舱位不存在。
	ErrNotFound
	// ErrStateMismatch 货物状态与操作不符（未装载或已装载）。
	ErrStateMismatch
	// ErrDuplicateOp 同一调整中货物重复出现，或移动到原舱位。
	ErrDuplicateOp
	// ErrEmptyAdjustment 调整不包含任何操作。
	ErrEmptyAdjustment
	// ErrOverweight 舱位总重量超过最大承重。
	ErrOverweight
	// ErrMixedLoading 不同目的地货物混装，但有货物不允许混装。
	ErrMixedLoading
	// ErrOverflow 重量合计超过 int64 可表示范围。
	ErrOverflow
	// ErrAdjustmentIDConflict 调整编号已用于不同内容。
	ErrAdjustmentIDConflict
)

// String 返回错误原因的中文说明。
func (k ErrorKind) String() string {
	switch k {
	case ErrInvalidID:
		return "编号为空"
	case ErrInvalidWeight:
		return "重量或承重非正整数"
	case ErrInvalidDestination:
		return "目的地为空"
	case ErrInvalidOp:
		return "操作种类无效"
	case ErrAlreadyExists:
		return "对象已存在"
	case ErrNotFound:
		return "对象不存在"
	case ErrStateMismatch:
		return "状态不符"
	case ErrDuplicateOp:
		return "重复操作"
	case ErrEmptyAdjustment:
		return "调整为空"
	case ErrOverweight:
		return "超重"
	case ErrMixedLoading:
		return "混装冲突"
	case ErrOverflow:
		return "重量溢出"
	case ErrAdjustmentIDConflict:
		return "调整编号冲突"
	default:
		return "未知错误"
	}
}

// Error 是配载操作的结构化失败结果，指出涉及的对象与具体原因。
type Error struct {
	Kind         ErrorKind
	ID           string // 涉及的货物或舱位编号，可能为空
	AdjustmentID string // 涉及的调整编号，可能为空
	detail       string
}

func (e *Error) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return e.Kind.String()
}

// fail 构造一个带说明的结构化错误。
func fail(kind ErrorKind, id string, format string, args ...any) *Error {
	return &Error{Kind: kind, ID: id, detail: fmt.Sprintf(format, args...)}
}

// toError 把最终配载评估出的一条拒绝原因转换成结构化错误，是正式配载
// 调整（Adjust）与已装载货物更正（AmendCargo）共用的唯一转换入口：
// 重量原因（超重/溢出）指向舱位，混装冲突指向编号字典序最靠前的不允许
// 混装货物，错误类别、涉及对象与中文说明在两种操作中保持一致。
// 更正不携带调整编号；调整需要附带编号时，由调用方在返回的错误上
// 设置 AdjustmentID。
func (x *Rejection) toError() *Error {
	switch x.Kind {
	case ErrOverflow:
		return fail(ErrOverflow, x.CompartmentID,
			"舱位 %s 重量合计超过 int64 可表示范围", x.CompartmentID)
	case ErrOverweight:
		return fail(ErrOverweight, x.CompartmentID,
			"舱位 %s 总重量 %d 千克超过最大承重 %d 千克",
			x.CompartmentID, x.UsedWeight, x.MaxWeight)
	default:
		offender := x.firstOffender()
		return fail(ErrMixedLoading, offender,
			"舱位 %s 存在不同目的地货物，但货物 %s 不允许混装",
			x.CompartmentID, offender)
	}
}
