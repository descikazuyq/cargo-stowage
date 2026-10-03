package stowage

// OpKind 表示调整操作的种类。
type OpKind int

const (
	// OpLoad 装载：将未装载的货物装入目标舱位。
	OpLoad OpKind = iota
	// OpUnload 卸下：将已装载的货物从当前舱位移除，货物记录保留。
	OpUnload
	// OpMove 移动：将已装载的货物直接改放到目标舱位。
	OpMove
)

// String 返回操作种类名称。
func (k OpKind) String() string {
	switch k {
	case OpLoad:
		return "装载"
	case OpUnload:
		return "卸下"
	case OpMove:
		return "移动"
	default:
		return "未知"
	}
}

// Op 是一次调整中的单个操作。
type Op struct {
	Kind    OpKind
	CargoID string
	Target  string // 装载/移动时的目标舱位；卸下时忽略
}

// CargoChange 记录一件货物调整前后的所属舱位。
type CargoChange struct {
	CargoID string
	From    string // 空字符串表示未装载
	To      string // 空字符串表示未装载
}

// CompartmentChange 记录一个舱位调整前后的总重量。
type CompartmentChange struct {
	CompartmentID string
	WeightBefore  int64
	WeightAfter   int64
}

// AdjustmentResult 是一次成功调整的结果快照。
type AdjustmentResult struct {
	ID                 string
	CargoChanges       []CargoChange
	CompartmentChanges []CompartmentChange
}

// CargoView 是货物信息的独立快照。
type CargoView struct {
	ID            string
	Weight        int64
	Destination   string
	AllowMixed    bool
	Loaded        bool
	CompartmentID string // 空字符串表示未装载
}

// CompartmentView 是舱位信息的独立快照。
type CompartmentView struct {
	ID              string
	MaxWeight       int64
	UsedWeight      int64
	RemainingWeight int64
	Cargo           []CargoView // 按编号字典序排列
}

// MixedDestination 描述预计配载中一个舱位内某个目的地对应的全部货物。
type MixedDestination struct {
	Destination string
	CargoIDs    []string // 按编号字典序排列
}

// Rejection 描述预计配载中单个舱位的一条拒绝原因。
//
// Kind 为 ErrOverweight 时，UsedWeight、RemainingWeight、Overweight 与
// MaxWeight 均有效；Kind 为 ErrOverflow（重量溢出）时，预计已用重量、
// 剩余重量与超出量无法用 int64 表示，对应数值字段保持零值，仅提供
// MaxWeight、货物清单与混装原因；Kind 为 ErrMixedLoading 时，
// Destinations 给出各目的地对应的货物，OffendingCargo 列出其中全部
// 不允许混装的货物编号。
type Rejection struct {
	CompartmentID   string
	Kind            ErrorKind
	MaxWeight       int64
	UsedWeight      int64              // 超重时为预计总重量；溢出或混装时为零值
	RemainingWeight int64              // 超重时为预计剩余重量（负值）；其余为零值
	Overweight      int64              // 超重时为超出承重的千克数；其余为零值
	Destinations    []MixedDestination // 混装冲突时各目的地及其货物，按目的地字典序排列
	OffendingCargo  []string           // 混装冲突时全部不允许混装的货物编号，字典序排列
}

// CompartmentPreview 是受影响舱位调整前后的完整配载快照。
// Before 反映调整前配载，清单中的货物保持原所属舱位；After 反映预计
// 配载，清单中的货物一律显示为已装载且属于该舱位，编号、重量、目的地
// 与混装许可仍取登记资料。预计超重时 RemainingWeight 为负值；预计重量
// 溢出 int64 时 After 不提供已用与剩余重量数值（保持零值），货物清单
// 照常返回。
type CompartmentPreview struct {
	ID     string
	Before CompartmentView
	After  CompartmentView
}

// PreviewResult 是一次预览的完整结果，本身是与登记处状态无关的独立快照。
type PreviewResult struct {
	// Submittable 表示按当前登记处状态，这组操作能否作为一次调整提交。
	Submittable bool
	// CargoChanges 列出清单涉及货物的原舱位与预计舱位，按货物编号字典序排列；
	// 未装载用空编号表示。
	CargoChanges []CargoChange
	// Compartments 列出全部受影响舱位调整前后的完整货物清单、已用重量与
	// 剩余重量，按舱位编号字典序排列。即使交换后总重量不变也会列出。
	Compartments []CompartmentPreview
	// Rejections 列出预计配载的全部拒绝原因，按舱位编号排列，同一舱位
	// 先重量（超重/溢出）后混装。为空表示 Submittable 为 true。
	Rejections []Rejection
}
