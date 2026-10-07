package stowage_test

import (
	"errors"
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// 本文件为正式提交（Adjust）因最终配载违反承重或混装限制而被拒绝的情形
// 提供可运行示例：一批安排中每条操作本身都合法，但全部完成后的最终配载
// 违规时，Adjust 只返回一个结构化错误而不返回成功结果——先按舱位编号
// 字典序选最靠前的违规舱位，同一舱位重量（超重/溢出）先于混装；混装错误
// 指出的可能是不允许混装的原舱货物而非本批新加入的货物；同一批安排用
// Preview 可以列全所有拒绝原因；被拒绝后查询仍得到提交前的配载。

// newRejectionRegistry 新建登记处并完成初始配载：舱位 C1 承重 100 千克，
// 已装 G1（60 千克、目的地上海、不允许混装）；舱位 C2 承重 50 千克，已装
// G2（40 千克、目的地北京、允许混装）。G3、G4 已登记但尚未装载。
// 每个示例都从这里独立开始，不依赖其他示例留下的状态。
func newRejectionRegistry() *stowage.Registry {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 100))
	must(reg.RegisterCompartment("C2", 50))
	must(reg.RegisterCargo("G1", 60, "上海", false))
	must(reg.RegisterCargo("G2", 40, "北京", true))
	must(reg.RegisterCargo("G3", 30, "广州", true))
	must(reg.RegisterCargo("G4", 20, "上海", true))
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C2"},
	})
	must(err)
	return reg
}

// printRejections 打印一次预览列出的全部拒绝原因，与正式提交返回的单条
// 结构化错误对照：预览按舱位编号排列、同一舱位先重量后混装，一次列全。
func printRejections(rejs []stowage.Rejection) {
	for _, rej := range rejs {
		switch rej.Kind {
		case stowage.ErrOverweight:
			fmt.Printf("预览原因: 舱位 %s 超重 %d 千克（%d/%d）\n",
				rej.CompartmentID, rej.Overweight, rej.UsedWeight, rej.MaxWeight)
		case stowage.ErrMixedLoading:
			fmt.Printf("预览原因: 舱位 %s 混装冲突，不允许混装的货物: %v\n",
				rej.CompartmentID, rej.OffendingCargo)
		}
	}
}

// 两个舱位同时违规时正式提交选中哪一条：一批两条装载，G3（30 千克、
// 目的地广州、允许混装）装入 C1，G4（20 千克、目的地上海、允许混装）
// 装入 C2。每条操作本身都合法，但完成后的最终配载里 C1 只剩混装冲突
// （90/100 千克不超重，上海与广州共舱而原舱的 G1 不允许混装），C2 超重
// （40+20=60 > 50）。Adjust 只返回一个结构化错误：按舱位编号字典序选中
// 靠前的 C1，报告它的混装冲突；错误对象是不允许混装的原舱货物 G1，
// 而不是本批新加入的 G3。倒置操作顺序不改变这个选择；同一批安排用
// Preview 可以看到 C2 的超重也在其中；被拒绝后查询仍是提交前的配载。
func Example_adjustRejectedPicksFirstCompartment() {
	reg := newRejectionRegistry()

	ops := []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G3", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G4", Target: "C2"},
	}

	// 正式提交：整批被拒绝，没有成功结果，只有一个结构化错误。
	res, err := reg.Adjust("plan-1", ops)
	fmt.Println("有结果返回:", res != nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("拒绝原因:", serr.Kind)
		fmt.Println("涉及编号:", serr.ID)
		fmt.Println("调整编号:", serr.AdjustmentID)
		fmt.Println("说明:", serr)
	}

	// 同一批安排的预览列全所有拒绝原因：单条错误不代表只有一个问题。
	prev, err := reg.Preview(ops)
	if err != nil {
		panic(err)
	}
	fmt.Println("预览可提交:", prev.Submittable)
	printRejections(prev.Rejections)

	// 失败不占用编号；倒置操作顺序沿用同一编号再提交，选中的仍是
	// C1 的混装冲突，指出的仍是原舱货物 G1。
	_, err = reg.Adjust("plan-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: stowage.OpLoad, CargoID: "G3", Target: "C1"},
	})
	if errors.As(err, &serr) {
		fmt.Println("倒置顺序后拒绝原因:", serr.Kind)
		fmt.Println("倒置顺序后涉及编号:", serr.ID)
	}

	// 被拒绝后查询：货物归属与舱位占用保持提交前的样子，不存在
	// “部分装卸已完成”的中间状态。
	for _, id := range []string{"G3", "G4"} {
		c, _ := reg.Cargo(id)
		fmt.Printf("当前 %s: %s\n", id, orUnloaded(c.CompartmentID))
	}
	for _, id := range []string{"C1", "C2"} {
		cp, _ := reg.Compartment(id)
		fmt.Printf("当前 %s: %v，已用 %d 千克、剩余 %d 千克\n",
			id, cargoIDs(*cp), cp.UsedWeight, cp.RemainingWeight)
	}

	// Output:
	// 有结果返回: false
	// 拒绝原因: 混装冲突
	// 涉及编号: G1
	// 调整编号: plan-1
	// 说明: 舱位 C1 存在不同目的地货物，但货物 G1 不允许混装
	// 预览可提交: false
	// 预览原因: 舱位 C1 混装冲突，不允许混装的货物: [G1]
	// 预览原因: 舱位 C2 超重 10 千克（60/50）
	// 倒置顺序后拒绝原因: 混装冲突
	// 倒置顺序后涉及编号: G1
	// 当前 G3: （未装载）
	// 当前 G4: （未装载）
	// 当前 C1: [G1]，已用 60 千克、剩余 40 千克
	// 当前 C2: [G2]，已用 40 千克、剩余 10 千克
}

// 同一舱位同时超重与混装冲突时先报告重量问题：C1 承重 50 千克，已装
// G1（40 千克、目的地上海、不允许混装）；本批把 G2（30 千克、目的地
// 北京、允许混装）装入 C1 后，最终配载 70/50 千克超重，同时上海与北京
// 共舱而 G1 不允许混装。Adjust 返回的错误 Kind 为超重、指向舱位 C1，
// 说明里给出预计总重量与最大承重；同一批安排的预览则按先重量后混装
// 列出两条原因。
func Example_adjustRejectedWeightBeforeMixed() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 50))
	must(reg.RegisterCargo("G1", 40, "上海", false))
	must(reg.RegisterCargo("G2", 30, "北京", true))
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	must(err)

	ops := []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
	}

	// 同舱重量与混装问题并存，正式提交先报告重量问题。
	_, err = reg.Adjust("add-g2", ops)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("拒绝原因:", serr.Kind)
		fmt.Println("涉及编号:", serr.ID)
		fmt.Println("调整编号:", serr.AdjustmentID)
		fmt.Println("说明:", serr)
	}

	// 预览按先重量后混装列全同舱的两条原因。
	prev, err := reg.Preview(ops)
	if err != nil {
		panic(err)
	}
	printRejections(prev.Rejections)

	// 被拒绝后查询：G2 未装载，C1 仍是提交前的样子。
	g2, _ := reg.Cargo("G2")
	fmt.Println("当前 G2:", orUnloaded(g2.CompartmentID))
	c1, _ := reg.Compartment("C1")
	fmt.Printf("当前 C1: %v，已用 %d 千克、剩余 %d 千克\n",
		cargoIDs(*c1), c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 拒绝原因: 超重
	// 涉及编号: C1
	// 调整编号: add-g2
	// 说明: 舱位 C1 总重量 70 千克超过最大承重 50 千克
	// 预览原因: 舱位 C1 超重 20 千克（70/50）
	// 预览原因: 舱位 C1 混装冲突，不允许混装的货物: [G1]
	// 当前 G2: （未装载）
	// 当前 C1: [G1]，已用 40 千克、剩余 10 千克
}
