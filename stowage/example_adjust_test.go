package stowage_test

import (
	"errors"
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// 本文件为正式提交（Adjust）的结果读法与重复提交提供可运行示例：
// 同一调整编号首次成功后，以原内容再次提交会原样返回首次保存的变化
// 记录，却不会再次装卸；什么算“原内容”（编号去空白且区分大小写、
// 操作顺序无关、卸下目标不参与比较）；同一编号换成不同内容会得到
// “调整编号冲突”的结构化错误；第一次就失败的调整不占用编号，修正
// 后可沿用同一编号。

// newAdjustExampleRegistry 新建空登记处并登记：舱位 C1 承重 100 千克，
// 货物 G1 重 30 千克、目的地上海。每个示例都从这里独立开始，不依赖
// 其他示例留下的状态。
func newAdjustExampleRegistry() *stowage.Registry {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 100))
	must(reg.RegisterCargo("G1", 30, "上海", false))
	return reg
}

// orUnloaded 把空舱位编号显示为“（未装载）”。
func orUnloaded(id string) string {
	if id == "" {
		return "（未装载）"
	}
	return id
}

// printResult 打印一次成功调整保存的变化记录：每件货物的原舱位与新舱位，
// 以及每个受影响舱位调整前后的总重量。
func printResult(res *stowage.AdjustmentResult) {
	fmt.Println("调整编号:", res.ID)
	for _, ch := range res.CargoChanges {
		fmt.Printf("货物 %s: %s -> %s\n",
			ch.CargoID, orUnloaded(ch.From), orUnloaded(ch.To))
	}
	for _, cp := range res.CompartmentChanges {
		fmt.Printf("舱位 %s 重量: %d -> %d 千克\n",
			cp.CompartmentID, cp.WeightBefore, cp.WeightAfter)
	}
}

// printCurrent 按当前登记处状态查询 G1 与 C1：货物现在的位置，以及舱位
// 现在的已用重量与剩余重量。它反映的是查询当刻的配载，不是任何历史调整。
func printCurrent(reg *stowage.Registry) {
	g1, err := reg.Cargo("G1")
	if err != nil {
		panic(err)
	}
	fmt.Printf("当前 G1: %s\n", orUnloaded(g1.CompartmentID))
	c1, err := reg.Compartment("C1")
	if err != nil {
		panic(err)
	}
	fmt.Printf("当前 C1: 已用 %d 千克、剩余 %d 千克\n",
		c1.UsedWeight, c1.RemainingWeight)
}

// 主示例：第一次调整 load-g1 把 G1 装入 C1；另一个调整 unload-g1 把它
// 卸下。随后按第一次的编号 load-g1 和原内容（装载 G1 到 C1）再次提交：
// 返回的仍是第一次成功时保存的变化（未装载 -> C1、0 -> 30 千克），
// 但这次提交不会重新装货——重新查询，G1 仍未装载，C1 已用 0 千克。
func Example_adjustResubmitSameContent() {
	reg := newAdjustExampleRegistry()

	// 第一次调整：装载。
	loadOps := []stowage.Op{{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"}}
	first, err := reg.Adjust("load-g1", loadOps)
	if err != nil {
		panic(err)
	}
	fmt.Println("== 第一次提交 load-g1 的结果 ==")
	printResult(first)

	// 另一次调整（另一个编号）：卸下。
	if _, err := reg.Adjust("unload-g1", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G1"},
	}); err != nil {
		panic(err)
	}
	fmt.Println("== 卸下后的查询 ==")
	printCurrent(reg)

	// 按第一次的编号和原内容再次提交装载。
	again, err := reg.Adjust("load-g1", loadOps)
	if err != nil {
		panic(err)
	}
	fmt.Println("== 再次提交 load-g1 返回的结果 ==")
	printResult(again)

	// 返回的是首次保存的历史记录，不是刚刚发生的变化：当前配载没有被改动。
	fmt.Println("== 再次提交后的查询 ==")
	printCurrent(reg)

	// Output:
	// == 第一次提交 load-g1 的结果 ==
	// 调整编号: load-g1
	// 货物 G1: （未装载） -> C1
	// 舱位 C1 重量: 0 -> 30 千克
	// == 卸下后的查询 ==
	// 当前 G1: （未装载）
	// 当前 C1: 已用 0 千克、剩余 100 千克
	// == 再次提交 load-g1 返回的结果 ==
	// 调整编号: load-g1
	// 货物 G1: （未装载） -> C1
	// 舱位 C1 重量: 0 -> 30 千克
	// == 再次提交后的查询 ==
	// 当前 G1: （未装载）
	// 当前 C1: 已用 0 千克、剩余 100 千克
}

// 什么算“原内容”：调整编号去掉首尾空白后比较；同一批操作只看集合、
// 排列顺序不影响判断；卸下操作填写的目标值不参与比较（执行卸下时本来
// 就忽略它）。下面的再次提交都被识别为原调整，返回同一份首次结果，
// 且都不再次改变配载。
func Example_adjustResubmitEquivalentContent() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 100))
	must(reg.RegisterCargo("G1", 30, "上海", false))
	must(reg.RegisterCargo("G2", 20, "上海", false))

	// 首次成功：一批两条装载，编号带首尾空白提交，保存为 batch-1。
	first, err := reg.Adjust("  batch-1  ", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}

	// 另一次调整把两件都卸下，使当前配载与首次成功时不同。
	if _, err := reg.Adjust("unload-all", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G1"},
		{Kind: stowage.OpUnload, CargoID: "G2"},
	}); err != nil {
		panic(err)
	}

	// 再次提交一：编号重排空白、操作顺序倒置，仍识别为 batch-1。
	reversed, err := reg.Adjust("\tbatch-1\t", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}

	// 再次提交二：卸下操作即使填了目标（哪怕是不存在的舱位）也不参与
	// 比较，unload-all 同样只回放首次结果。
	unloadAgain, err := reg.Adjust("unload-all", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G2", Target: "CX-随便填"},
		{Kind: stowage.OpUnload, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("首次结果：")
	printResult(first)
	fmt.Println("逆序再次提交结果：")
	printResult(reversed)
	fmt.Println("卸下带目标再次提交，编号:", unloadAgain.ID)
	for _, ch := range unloadAgain.CargoChanges {
		fmt.Printf("货物 %s: %s -> %s\n",
			ch.CargoID, orUnloaded(ch.From), orUnloaded(ch.To))
	}
	// 两次再次提交都没有重新装卸：两件货物仍未装载，C1 已用 0 千克。
	for _, id := range []string{"G1", "G2"} {
		c, _ := reg.Cargo(id)
		fmt.Printf("当前 %s: %s\n", id, orUnloaded(c.CompartmentID))
	}
	c1, _ := reg.Compartment("C1")
	fmt.Printf("当前 C1: 已用 %d 千克、剩余 %d 千克\n",
		c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 首次结果：
	// 调整编号: batch-1
	// 货物 G1: （未装载） -> C1
	// 货物 G2: （未装载） -> C1
	// 舱位 C1 重量: 0 -> 50 千克
	// 逆序再次提交结果：
	// 调整编号: batch-1
	// 货物 G1: （未装载） -> C1
	// 货物 G2: （未装载） -> C1
	// 舱位 C1 重量: 0 -> 50 千克
	// 卸下带目标再次提交，编号: unload-all
	// 货物 G1: C1 -> （未装载）
	// 货物 G2: C1 -> （未装载）
	// 当前 G1: （未装载）
	// 当前 G2: （未装载）
	// 当前 C1: 已用 0 千克、剩余 100 千克
}

// 调整编号去掉首尾空白后逐字符比较，并且区分大小写：load-g1 与
// Load-G1 是两个互不相干的编号。在货物已被卸下后用大小写不同的编号
// 提交，登记处把它当作一次全新调整——它会真的再装一次货；想取回首次
// 结果，只能逐字符使用原编号。
func Example_adjustResubmitCaseSensitiveID() {
	reg := newAdjustExampleRegistry()

	// load-g1 首次成功：G1 装入 C1，随后另一次调整卸下。
	if _, err := reg.Adjust("load-g1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	}); err != nil {
		panic(err)
	}
	if _, err := reg.Adjust("unload-g1", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G1"},
	}); err != nil {
		panic(err)
	}

	// 大小写不同的 Load-G1 是全新编号，按当前状态执行，真正装货。
	fresh, err := reg.Adjust("Load-G1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("新编号调整：")
	printResult(fresh)
	printCurrent(reg)

	// 而原编号 load-g1 此时再次提交仍只回放首次结果，不会二次装载。
	again, err := reg.Adjust("load-g1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("原编号回放编号:", again.ID)
	printCurrent(reg)

	// Output:
	// 新编号调整：
	// 调整编号: Load-G1
	// 货物 G1: （未装载） -> C1
	// 舱位 C1 重量: 0 -> 30 千克
	// 当前 G1: C1
	// 当前 C1: 已用 30 千克、剩余 70 千克
	// 原编号回放编号: load-g1
	// 当前 G1: C1
	// 当前 C1: 已用 30 千克、剩余 70 千克
}

// 同一编号换成不同内容会被拒绝：错误 Kind 为 ErrAdjustmentIDConflict，
// AdjustmentID 指出冲突的调整编号，Error() 给出可直接阅读的中文说明；
// 当前配载保持不变。另一批安排应当另用一个新编号。
func Example_adjustIDConflict() {
	reg := newAdjustExampleRegistry()

	// 编号 load-g1 已成功用于“装载 G1 到 C1”。
	if _, err := reg.Adjust("load-g1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	}); err != nil {
		panic(err)
	}

	// 同一编号提交不同内容（这里改成卸下）：冲突，不返回任何结果。
	res, err := reg.Adjust("load-g1", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G1"},
	})
	fmt.Println("冲突时有结果返回:", res != nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("拒绝原因:", serr.Kind)
		fmt.Println("调整编号:", serr.AdjustmentID)
		fmt.Println("说明:", serr)
	}

	// G1 仍在 C1，舱位仍是 30/100 千克——冲突提交没有生效。
	printCurrent(reg)

	// 另一批安排另用编号即可正常提交。
	if _, err := reg.Adjust("unload-g1-v2", []stowage.Op{
		{Kind: stowage.OpUnload, CargoID: "G1"},
	}); err != nil {
		panic(err)
	}
	fmt.Println("新编号提交后：")
	printCurrent(reg)

	// Output:
	// 冲突时有结果返回: false
	// 拒绝原因: 调整编号冲突
	// 调整编号: load-g1
	// 说明: 调整编号 load-g1 已用于不同内容
	// 当前 G1: C1
	// 当前 C1: 已用 30 千克、剩余 70 千克
	// 新编号提交后：
	// 当前 G1: （未装载）
	// 当前 C1: 已用 0 千克、剩余 100 千克
}

// 第一次就失败的调整不占用编号：修正安排后沿用同一编号可以成功。
// 这与“已成功调整的重复提交”是两种不同情况——前者编号还没被保存，
// 修正后提交是一次全新生效；后者编号已绑定首次结果，再次提交只会取回
// 那份结果而不会重新执行。
func Example_adjustFailureDoesNotConsumeID() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	// C1 承重只有 20 千克，另有装得下的 C2（100 千克）；G1 重 30 千克。
	must(reg.RegisterCompartment("C1", 20))
	must(reg.RegisterCompartment("C2", 100))
	must(reg.RegisterCargo("G1", 30, "上海", false))

	// 第一次提交 load-g1：装进 C1，30 > 20，超重失败。
	_, err := reg.Adjust("load-g1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("第一次原因:", serr.Kind)
		fmt.Println("调整编号:", serr.AdjustmentID)
	}
	g1, _ := reg.Cargo("G1")
	c1, _ := reg.Compartment("C1")
	fmt.Printf("失败后 G1: %s；C1 已用 %d 千克\n",
		orUnloaded(g1.CompartmentID), c1.UsedWeight)

	// 失败不占用编号：目标改成装得下的 C2，沿用同一编号提交即成功。
	res, err := reg.Adjust("load-g1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C2"},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("修正后结果：")
	printResult(res)
	g1, _ = reg.Cargo("G1")
	fmt.Println("修正后 G1 所在舱位:", g1.CompartmentID)

	// Output:
	// 第一次原因: 超重
	// 调整编号: load-g1
	// 失败后 G1: （未装载）；C1 已用 0 千克
	// 修正后结果：
	// 调整编号: load-g1
	// 货物 G1: （未装载） -> C2
	// 舱位 C2 重量: 0 -> 30 千克
	// 修正后 G1 所在舱位: C2
}
