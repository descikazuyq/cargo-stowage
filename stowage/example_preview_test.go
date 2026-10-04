package stowage_test

import (
	"errors"
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// newSwapRegistry 新建登记处并完成初始配载：两个承重均为 30 千克的舱位，
// 各装入一件 30 千克、目的地相同且不允许混装的货物。每个示例都从这里
// 独立开始，不依赖其他示例留下的状态。
func newSwapRegistry() *stowage.Registry {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 30))
	must(reg.RegisterCompartment("C2", 30))
	must(reg.RegisterCargo("G1", 30, "上海", false))
	must(reg.RegisterCargo("G2", 30, "上海", false))
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C2"},
	})
	must(err)
	return reg
}

// cargoIDs 提取舱位快照中的货物编号清单。
func cargoIDs(v stowage.CompartmentView) []string {
	ids := make([]string, 0, len(v.Cargo))
	for _, c := range v.Cargo {
		ids = append(ids, c.ID)
	}
	return ids
}

// 预览同一批安排中的两件货物互换：两个舱位各自满载，但限制按整批安排
// 完成后的最终配载判断，交换后每个舱位仍恰好在承重内，因此可以提交。
func Example_previewSwap() {
	reg := newSwapRegistry()

	res, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpMove, CargoID: "G1", Target: "C2"},
		{Kind: stowage.OpMove, CargoID: "G2", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("可提交:", res.Submittable)
	for _, ch := range res.CargoChanges {
		fmt.Printf("货物 %s: %s -> %s\n", ch.CargoID, ch.From, ch.To)
	}
	// 即使交换后总重量不变，两个舱位仍会列出前后的完整货物清单。
	for _, cp := range res.Compartments {
		fmt.Printf("舱位 %s 前: %v（%d/%d 千克）\n",
			cp.ID, cargoIDs(cp.Before), cp.Before.UsedWeight, cp.Before.MaxWeight)
		fmt.Printf("舱位 %s 后: %v（%d/%d 千克）\n",
			cp.ID, cargoIDs(cp.After), cp.After.UsedWeight, cp.After.MaxWeight)
	}
	// 预计清单中的货物位置按交换后的所属舱位解释。
	fmt.Printf("预计清单中 %s 属于舱位 %s\n",
		res.Compartments[0].After.Cargo[0].ID,
		res.Compartments[0].After.Cargo[0].CompartmentID)

	// 预览是只读的：查询当前货物仍看到交换前的位置。
	g1, _ := reg.Cargo("G1")
	fmt.Println("预览后查询 G1 所在舱位:", g1.CompartmentID)

	// Output:
	// 可提交: true
	// 货物 G1: C1 -> C2
	// 货物 G2: C2 -> C1
	// 舱位 C1 前: [G1]（30/30 千克）
	// 舱位 C1 后: [G2]（30/30 千克）
	// 舱位 C2 前: [G2]（30/30 千克）
	// 舱位 C2 后: [G1]（30/30 千克）
	// 预计清单中 G2 属于舱位 C1
	// 预览后查询 G1 所在舱位: C1
}

// 预览一组会被拒绝的安排：向已满载的 C1 装入第三件货物（10 千克、目的地
// 不同、允许混装）。每条操作本身合法，预览照常返回预计清单，但
// Submittable 为 false，且同时列出超重与混装冲突两条拒绝原因。
func Example_previewRejected() {
	reg := newSwapRegistry()
	if err := reg.RegisterCargo("G3", 10, "北京", true); err != nil {
		panic(err)
	}

	res, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G3", Target: "C1"},
	})
	// 操作本身合法，预览调用不失败；不可提交体现在结果里。
	fmt.Println("预览调用出错:", err != nil)
	fmt.Println("可提交:", res.Submittable)
	for _, cp := range res.Compartments {
		fmt.Printf("舱位 %s 后: %v（%d/%d 千克）\n",
			cp.ID, cargoIDs(cp.After), cp.After.UsedWeight, cp.After.MaxWeight)
	}
	for _, rej := range res.Rejections {
		switch rej.Kind {
		case stowage.ErrOverweight:
			fmt.Printf("舱位 %s 超重 %d 千克（%d/%d）\n",
				rej.CompartmentID, rej.Overweight, rej.UsedWeight, rej.MaxWeight)
		case stowage.ErrMixedLoading:
			for _, d := range rej.Destinations {
				fmt.Printf("舱位 %s 目的地 %s: %v\n", rej.CompartmentID, d.Destination, d.CargoIDs)
			}
			fmt.Printf("不允许混装的货物: %v\n", rej.OffendingCargo)
		}
	}

	// 被拒绝的预览同样不改变配载。
	c1, _ := reg.Compartment("C1")
	fmt.Println("预览后 C1 实际货物:", cargoIDs(*c1))

	// Output:
	// 预览调用出错: false
	// 可提交: false
	// 舱位 C1 后: [G1 G3]（40/30 千克）
	// 舱位 C1 超重 10 千克（40/30）
	// 舱位 C1 目的地 上海: [G1]
	// 舱位 C1 目的地 北京: [G3]
	// 不允许混装的货物: [G1]
	// 预览后 C1 实际货物: [G1]
}

// 操作本身不合法（货物编号不存在）时，预览返回结构化错误且没有局部
// 预览，与“操作合法但最终配载不可提交”的拒绝结果不同。
func Example_previewInvalidOp() {
	reg := newSwapRegistry()

	res, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G9", Target: "C1"},
	})
	fmt.Println("有预览结果:", res != nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("错误原因:", serr.Kind)
		fmt.Println("涉及编号:", serr.ID)
	}

	// Output:
	// 有预览结果: false
	// 错误原因: 对象不存在
	// 涉及编号: G9
}

// 预览通过后用同一批操作正式提交：只有 Adjust 成功才应用安排，此后的
// 查询结果与之前的预计配载对应。
func Example_adjustAfterPreview() {
	reg := newSwapRegistry()

	ops := []stowage.Op{
		{Kind: stowage.OpMove, CargoID: "G1", Target: "C2"},
		{Kind: stowage.OpMove, CargoID: "G2", Target: "C1"},
	}
	res, err := reg.Preview(ops)
	if err != nil {
		panic(err)
	}
	fmt.Println("预览可提交:", res.Submittable)

	// 预览本身不改变配载，也不占用调整编号；正式提交才应用安排。
	adj, err := reg.Adjust("swap-1", ops)
	if err != nil {
		panic(err)
	}
	fmt.Println("已提交调整:", adj.ID)
	g1, _ := reg.Cargo("G1")
	g2, _ := reg.Cargo("G2")
	fmt.Println("提交后 G1 所在舱位:", g1.CompartmentID)
	fmt.Println("提交后 G2 所在舱位:", g2.CompartmentID)

	// Output:
	// 预览可提交: true
	// 已提交调整: swap-1
	// 提交后 G1 所在舱位: C2
	// 提交后 G2 所在舱位: C1
}
