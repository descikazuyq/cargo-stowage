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
