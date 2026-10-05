package stowage_test

import (
	"errors"
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// newResubmitRegistry 新建登记处：舱位 C1 承重 100 千克，货物 G1 30 千克
// （目的地上海、允许混装），尚未装载。每个示例都从这里独立开始，不依赖
// 其他示例留下的状态。
func newResubmitRegistry() *stowage.Registry {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 100))
	must(reg.RegisterCargo("G1", 30, "上海", true))
	return reg
}

// posText 把空舱位编号显示为“未装载”。
func posText(id string) string {
	if id == "" {
		return "未装载"
	}
	return id
}

// printAdjustment 打印一次调整结果记录的货物归属变化与舱位重量变化。
func printAdjustment(label string, res *stowage.AdjustmentResult) {
	for _, ch := range res.CargoChanges {
		fmt.Printf("%s: 货物 %s %s -> %s\n", label, ch.CargoID, posText(ch.From), posText(ch.To))
	}
	for _, ch := range res.CompartmentChanges {
		fmt.Printf("%s: 舱位 %s %d -> %d 千克\n", label, ch.CompartmentID, ch.WeightBefore, ch.WeightAfter)
	}
}

// 首次用编号 load-1 把 G1 装入 C1，再用另一个编号 unload-1 卸下，然后按
// load-1 的编号和原内容再次提交：成功返回首次保存的结果（G1 从未装载变为
// C1、舱位 0 -> 30 千克），但这次提交不会重新装货，查询看到的仍是卸下后
// 的状态。
func Example_adjustResubmitReturnsSavedResult() {
	reg := newResubmitRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}

	loadOps := []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	}
	first, err := reg.Adjust("load-1", loadOps)
	must(err)
	printAdjustment("首次提交 load-1", first)

	// 另一个调整编号把货物卸下。
	_, err = reg.Adjust("unload-1", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G1"},
	})
	must(err)

	// 按第一次的编号和原内容再次提交：成功，返回首次保存的结果。
	again, err := reg.Adjust("load-1", loadOps)
	fmt.Println("再次提交返回错误:", err)
	printAdjustment("再次提交 load-1", again)

	// 但再次提交不会重新装货：查询反映调用时的状态。
	g1, _ := reg.Cargo("G1")
	c1, _ := reg.Compartment("C1")
	fmt.Printf("再次查询: G1 %s，C1 已用 %d 千克、剩余 %d 千克\n",
		posText(g1.CompartmentID), c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 首次提交 load-1: 货物 G1 未装载 -> C1
	// 首次提交 load-1: 舱位 C1 0 -> 30 千克
	// 再次提交返回错误: <nil>
	// 再次提交 load-1: 货物 G1 未装载 -> C1
	// 再次提交 load-1: 舱位 C1 0 -> 30 千克
	// 再次查询: G1 未装载，C1 已用 0 千克、剩余 100 千克
}

// 判断“同一内容”时：逐条比较操作种类、货物编号以及装载和移动的目标舱位；
// 操作排列顺序不影响判断；编号去掉首尾空白后比较并区分大小写；卸下操作
// 填写的目标不参与比较。下面再次提交的操作顺序不同、卸下多填了目标、
// 编号与目标带首尾空白，仍视为同一内容，返回首次保存的结果。
func Example_adjustResubmitEquivalentContent() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 100))
	must(reg.RegisterCompartment("C2", 100))
	must(reg.RegisterCargo("G1", 30, "上海", true))
	must(reg.RegisterCargo("G2", 20, "北京", true))
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C2"},
	})
	must(err)

	_, err = reg.Adjust("t-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpUnload, CargoID: "G2"},
	})
	must(err)

	// 顺序不同、卸下多填目标、编号与目标带首尾空白：仍是同一内容。
	again, err := reg.Adjust("  t-1  ", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G2", Target: "C9"},
		{Kind: stowage.OpLoad, CargoID: "G1", Target: " C1 "},
	})
	fmt.Println("再次提交返回错误:", err)
	fmt.Println("返回结果编号:", again.ID)
	printAdjustment("返回结果", again)

	// Output:
	// 再次提交返回错误: <nil>
	// 返回结果编号: t-1
	// 返回结果: 货物 G1 未装载 -> C1
	// 返回结果: 货物 G2 C2 -> 未装载
	// 返回结果: 舱位 C1 0 -> 30 千克
	// 返回结果: 舱位 C2 20 -> 0 千克
}

// 同一编号换成不同内容提交会被拒绝：返回调整编号冲突的结构化错误，
// 当前配载保持不变。另一批安排应另用编号。
func Example_adjustIDConflict() {
	reg := newResubmitRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C2", 100))

	_, err := reg.Adjust("plan-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	must(err)

	// 同一编号、不同内容（目标舱位不同）：被拒绝。
	_, err = reg.Adjust("plan-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C2"},
	})
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("拒绝原因:", serr.Kind)
		fmt.Println("涉及调整编号:", serr.AdjustmentID)
		fmt.Println("说明:", serr)
	}

	// 被拒绝的提交不生效：当前配载保持不变。
	g1, _ := reg.Cargo("G1")
	c1, _ := reg.Compartment("C1")
	fmt.Printf("当前配载: G1 位于 %s，C1 已用 %d 千克、剩余 %d 千克\n",
		g1.CompartmentID, c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 拒绝原因: 调整编号冲突
	// 涉及调整编号: plan-1
	// 说明: 调整编号 plan-1 已用于不同内容
	// 当前配载: G1 位于 C1，C1 已用 30 千克、剩余 70 千克
}

// 第一次就失败的调整不占用编号：修正安排后可沿用同一编号提交成功。
// 这与已成功调整的重复提交是不同情况。
func Example_adjustRetryAfterFailure() {
	reg := newResubmitRegistry()

	// 目标舱位不存在，第一次提交失败。
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C9"},
	})
	fmt.Println("首次提交:", err)

	// 修正安排后沿用同一编号提交：作为全新的调整校验并生效。
	res, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}
	printAdjustment("修正后提交 load-1", res)
	g1, _ := reg.Cargo("G1")
	fmt.Println("提交后 G1 所在舱位:", g1.CompartmentID)

	// Output:
	// 首次提交: 舱位 C9 不存在
	// 修正后提交 load-1: 货物 G1 未装载 -> C1
	// 修正后提交 load-1: 舱位 C1 0 -> 30 千克
	// 提交后 G1 所在舱位: C1
}
