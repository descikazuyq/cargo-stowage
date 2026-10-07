package stowage_test

import (
	"errors"
	"fmt"
	"math"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// 本文件为正式提交（Adjust）在“每条操作本身合法、但整批安排完成后的
// 最终配载违反承重或混装限制”时的错误读法提供可运行示例：
// Adjust 不返回成功结果，只返回一个结构化错误；先按舱位编号字典序
// 选择最靠前的违规舱位，同一舱位先报告重量问题（超重/溢出）再报告
// 混装，且与操作的排列顺序无关；混装错误指向最终舱内不允许混装且
// 编号字典序最靠前的货物（可能是原本就在舱内的货物，而非本批新加入
// 的货物）；同一批安排用 Preview 可以一次看到全部拒绝原因；拒绝后
// 查询仍是提交前的配载，不会有部分装卸已经完成。

// 主示例：两个舱位在同一批安排后都违规——C1 只构成混装冲突，C2 只
// 构成超重。正式提交只报告一个结构化错误：按舱位编号字典序选中 C1 的
// 混装冲突，被指出的是原舱内不允许混装且编号最靠前的 G2，而不是本批
// 新加入的 G1；把操作顺序倒置后再提交，选择不变。
func Example_adjustRejectionEarliestCompartment() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	// C1 承重充足（1000 千克），整批后只会有混装问题；C2 只有 50 千克。
	must(reg.RegisterCompartment("C1", 1000))
	must(reg.RegisterCompartment("C2", 50))
	// C1 的原舱货物 G2、G7 同去上海、都不允许混装（同目的地共舱合法）。
	must(reg.RegisterCargo("G2", 10, "上海", false))
	must(reg.RegisterCargo("G7", 10, "上海", false))
	// 本批新加入 C1 的 G1 去北京、允许混装；G3、G4 同目的地、都允许
	// 混装，将装入 C2，合计 60 千克超过 C2 的承重 50 千克。
	must(reg.RegisterCargo("G1", 10, "北京", true))
	must(reg.RegisterCargo("G3", 30, "X", true))
	must(reg.RegisterCargo("G4", 30, "X", true))

	// 初始配载：G2、G7 已在 C1，C1 已用 20 千克；其余货物未装载。
	_, err := reg.Adjust("init", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G7", Target: "C1"},
	})
	must(err)

	fmt.Println("== 提交前配载 ==")
	for _, id := range []string{"G1", "G2", "G3", "G4", "G7"} {
		c, _ := reg.Cargo(id)
		fmt.Printf("%s: %s\n", id, orUnloaded(c.CompartmentID))
	}
	c1, _ := reg.Compartment("C1")
	c2, _ := reg.Compartment("C2")
	fmt.Printf("C1: 已用 %d 千克、剩余 %d 千克\n", c1.UsedWeight, c1.RemainingWeight)
	fmt.Printf("C2: 已用 %d 千克、剩余 %d 千克\n", c2.UsedWeight, c2.RemainingWeight)

	// 正式提交：C2 的两条装载写在前面，C1 的混装操作写在最后。
	ops := []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G4", Target: "C2"},
		{Kind: stowage.OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	}
	res, err := reg.Adjust("bad-batch", ops)
	fmt.Println("== 正式提交 bad-batch（C2 的操作写在前面）==")
	fmt.Println("有成功结果:", res != nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("错误类别:", serr.Kind)
		fmt.Println("涉及对象:", serr.ID)
		fmt.Println("调整编号:", serr.AdjustmentID)
		fmt.Println("中文说明:", serr)
	}

	// 把 C1 的操作换到最前面再提交：报告的舱位与货物不变。
	_, err = reg.Adjust("bad-batch-2", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G3", Target: "C2"},
		{Kind: stowage.OpLoad, CargoID: "G4", Target: "C2"},
	})
	if errors.As(err, &serr) {
		fmt.Println("== 操作顺序倒置后再提交 ==")
		fmt.Println("错误类别:", serr.Kind)
		fmt.Println("涉及对象:", serr.ID)
	}

	// 同一批安排先 Preview：可以一次看到全部拒绝原因。
	pv, err := reg.Preview(ops)
	if err != nil {
		panic(err)
	}
	fmt.Println("== 同一批安排先 Preview ==")
	fmt.Println("可提交:", pv.Submittable)
	for i, rej := range pv.Rejections {
		fmt.Printf("拒绝原因 %d: %s %s\n", i+1, rej.CompartmentID, rej.Kind)
	}

	// 拒绝后查询：配载仍是提交前的样子，没有部分装卸完成。
	fmt.Println("== 拒绝后查询 ==")
	for _, id := range []string{"G1", "G2", "G3", "G4", "G7"} {
		c, _ := reg.Cargo(id)
		fmt.Printf("%s: %s\n", id, orUnloaded(c.CompartmentID))
	}
	c1, _ = reg.Compartment("C1")
	c2, _ = reg.Compartment("C2")
	fmt.Printf("C1: 已用 %d 千克、剩余 %d 千克\n", c1.UsedWeight, c1.RemainingWeight)
	fmt.Printf("C2: 已用 %d 千克、剩余 %d 千克\n", c2.UsedWeight, c2.RemainingWeight)

	// Output:
	// == 提交前配载 ==
	// G1: （未装载）
	// G2: C1
	// G3: （未装载）
	// G4: （未装载）
	// G7: C1
	// C1: 已用 20 千克、剩余 980 千克
	// C2: 已用 0 千克、剩余 50 千克
	// == 正式提交 bad-batch（C2 的操作写在前面）==
	// 有成功结果: false
	// 错误类别: 混装冲突
	// 涉及对象: G2
	// 调整编号: bad-batch
	// 中文说明: 舱位 C1 存在不同目的地货物，但货物 G2 不允许混装
	// == 操作顺序倒置后再提交 ==
	// 错误类别: 混装冲突
	// 涉及对象: G2
	// == 同一批安排先 Preview ==
	// 可提交: false
	// 拒绝原因 1: C1 混装冲突
	// 拒绝原因 2: C2 超重
	// == 拒绝后查询 ==
	// G1: （未装载）
	// G2: C1
	// G3: （未装载）
	// G4: （未装载）
	// G7: C1
	// C1: 已用 20 千克、剩余 980 千克
	// C2: 已用 0 千克、剩余 50 千克
}

// 同一舱位同时超重又有混装冲突时，正式提交先报告重量问题：错误类别
// 是超重，涉及对象是舱位 C1（不是任何一件货物），中文说明给出预计
// 总重量（100 千克）与最大承重（50 千克）。同一批安排的 Preview 能
// 看到这个舱位其实有两条拒绝原因——正式提交的单条错误不表示整批
// 只有一个问题。
func Example_adjustRejectionWeightBeforeMixed() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 50))
	// G1 去上海、不允许混装；G2 去北京、允许混装。两件合计 100 千克。
	must(reg.RegisterCargo("G1", 60, "上海", false))
	must(reg.RegisterCargo("G2", 40, "北京", true))

	ops := []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	}
	res, err := reg.Adjust("wm-1", ops)
	fmt.Println("有成功结果:", res != nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("错误类别:", serr.Kind)
		fmt.Println("涉及对象:", serr.ID)
		fmt.Println("调整编号:", serr.AdjustmentID)
		fmt.Println("中文说明:", serr)
	}

	// Preview 保留同一舱位的全部原因：先超重，后混装。
	pv, err := reg.Preview(ops)
	if err != nil {
		panic(err)
	}
	fmt.Print("同一批 Preview 的全部原因:")
	for _, rej := range pv.Rejections {
		fmt.Printf(" %s/%s", rej.CompartmentID, rej.Kind)
	}
	fmt.Println()

	// 拒绝后两件货物都仍未装载，C1 仍是空舱。
	for _, id := range []string{"G1", "G2"} {
		c, _ := reg.Cargo(id)
		fmt.Printf("拒绝后 %s: %s\n", id, orUnloaded(c.CompartmentID))
	}
	c1, _ := reg.Compartment("C1")
	fmt.Printf("C1: 已用 %d 千克、剩余 %d 千克\n", c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 有成功结果: false
	// 错误类别: 超重
	// 涉及对象: C1
	// 调整编号: wm-1
	// 中文说明: 舱位 C1 总重量 100 千克超过最大承重 50 千克
	// 同一批 Preview 的全部原因: C1/超重 C1/混装冲突
	// 拒绝后 G1: （未装载）
	// 拒绝后 G2: （未装载）
	// C1: 已用 0 千克、剩余 50 千克
}

// 两件货物重量都达到 int64 上限、合计无法用 int64 表示，而该舱同时
// 还构成混装冲突时，重量溢出仍先于同舱混装被报告：错误类别是重量
// 溢出、涉及对象是舱位 C1，中文说明只指出合计超过 int64 可表示范围，
// 不引用那个无法表示的预计合计数值。
func Example_adjustRejectionOverflowBeforeMixed() {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", math.MaxInt64))
	// G1 去上海、不允许混装，已在舱内；G2 去北京、允许混装，尚未装载。
	must(reg.RegisterCargo("G1", math.MaxInt64, "上海", false))
	must(reg.RegisterCargo("G2", math.MaxInt64, "北京", true))
	_, err := reg.Adjust("init", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
	})
	must(err)

	res, err := reg.Adjust("ov-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
	})
	fmt.Println("有成功结果:", res != nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("错误类别:", serr.Kind)
		fmt.Println("涉及对象:", serr.ID)
		fmt.Println("调整编号:", serr.AdjustmentID)
		fmt.Println("中文说明:", serr)
	}

	// 拒绝后 G2 仍未装载，C1 仍只有原舱的 G1。
	g2, _ := reg.Cargo("G2")
	c1, _ := reg.Compartment("C1")
	fmt.Printf("拒绝后 G2: %s\n", orUnloaded(g2.CompartmentID))
	fmt.Printf("拒绝后 C1 货物: %v\n", cargoIDs(*c1))

	// Output:
	// 有成功结果: false
	// 错误类别: 重量溢出
	// 涉及对象: C1
	// 调整编号: ov-1
	// 中文说明: 舱位 C1 重量合计超过 int64 可表示范围
	// 拒绝后 G2: （未装载）
	// 拒绝后 C1 货物: [G1]
}
