package stowage_test

import (
	"errors"
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// 本文件为 AmendCargo（更正货物资料）补充完整可运行的使用示例：
// 主示例从新建登记处开始，登记舱位与货物并完成初始装载，随后对一件已
// 装载货物同时更正重量、目的地、混装许可三项资料，再查询货物与其所属
// 舱位；另外两个示例分别展示新重量使当前舱位超重、以及不同目的地仍共舱
// 时取消混装许可这两种被拒绝的情况，并在失败后查询确认原配载完整保留；
// 最后一个示例说明更正后新查询与新预览使用新资料，而先前返回的快照仍
// 保留原内容。

// newAmendRegistry 新建登记处并完成初始配载：
// C1 承重 50 千克；G1（20 千克，北京，允许混装）与 G2（10 千克，上海，
// 允许混装）经一次调整装入 C1。两件货物目的地不同，但全员允许混装，因此
// 共舱合法，舱位已用 30/50 千克。每个示例都从这里独立开始，不依赖其他
// 示例留下的状态。
func newAmendRegistry() *stowage.Registry {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 50))
	must(reg.RegisterCargo("G1", 20, "北京", true))
	must(reg.RegisterCargo("G2", 10, "上海", true))
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
	})
	must(err)
	return reg
}

// mixedCN 打印是否允许混装的中文状态。
func mixedCN(b bool) string {
	if b {
		return "允许混装"
	}
	return "不允许混装"
}

// printCargo 查询并打印货物的三项登记资料与装载位置。
func printCargo(reg *stowage.Registry, id string) {
	c, err := reg.Cargo(id)
	if err != nil {
		panic(err)
	}
	fmt.Printf("货物 %s：%d 千克，去 %s，%s，", c.ID, c.Weight, c.Destination, mixedCN(c.AllowMixed))
	if c.Loaded {
		fmt.Printf("在舱位 %s\n", c.CompartmentID)
	} else {
		fmt.Println("未装载")
	}
}

// printCompartment 查询并打印舱位的货物清单、已用重量与剩余重量。
func printCompartment(reg *stowage.Registry, id string) {
	cpt, err := reg.Compartment(id)
	if err != nil {
		panic(err)
	}
	fmt.Printf("舱位 %s：%v，已用 %d 千克，剩余 %d 千克（承重 %d）\n",
		cpt.ID, cargoIDs(*cpt), cpt.UsedWeight, cpt.RemainingWeight, cpt.MaxWeight)
}

// 主示例：对一件已装载货物同时更正重量、目的地、混装许可三项资料。
//
// G1 原资料为 20 千克、北京、允许混装，与来自上海的 G2 靠全员允许混装
// 共舱于 C1（共 30/50 千克）。一次调用把 G1 的三项资料整体替换为 25
// 千克、上海、不允许混装：目的地改成与同舱 G2 相同后，全舱只有上海一个
// 目的地，同一目的地共舱不要求所有货物都允许混装，因此可以同时取消 G1
// 的混装许可。更正不改变货物编号与所属舱位，G1 仍在 C1。
func Example_amendCargo() {
	reg := newAmendRegistry()

	fmt.Println("== 更正前 ==")
	printCargo(reg, "G1")
	printCargo(reg, "G2")
	printCompartment(reg, "C1")

	// 一次调用整体替换三项资料，而不是分三次逐个字段修改：校验按三项
	// 共同生效后的最终配载判断，不会因“先取消混装、目的地还没改好”之类
	// 的中间状态拒绝一个最终合法的更正。
	if err := reg.AmendCargo("G1", 25, "上海", false); err != nil {
		panic(err)
	}

	fmt.Println("== 更正后 ==")
	printCargo(reg, "G1")
	printCargo(reg, "G2")
	printCompartment(reg, "C1")
	// 重量读法：舱位是“先扣除 G1 的旧重量 20、再计入新重量 25”，所以
	// 已用重量从 30 变成 25+10=35、剩余从 20 变成 15；不是在原 30 上再
	// 累计一次新重量（30+25=55）。G2 的 10 千克与目的地、混装许可都没有
	// 变化。目的地读法：G1、G2 现在同为上海，虽然 G1 不允许混装、G2 仍
	// 允许混装，共舱也完全合法——混装许可只在舱内出现不同目的地时才被
	// 要求。

	// 把 G1 改成 40 千克：10(G2)+40(G1)=50，恰好达到 C1 的承重，剩余为
	// 0。承重恰好用满是允许的，不会被当成超重。
	if err := reg.AmendCargo("G1", 40, "上海", false); err != nil {
		panic(err)
	}
	fmt.Println("== 承重恰好用满 ==")
	printCargo(reg, "G1")
	printCompartment(reg, "C1")

	// Output:
	// == 更正前 ==
	// 货物 G1：20 千克，去 北京，允许混装，在舱位 C1
	// 货物 G2：10 千克，去 上海，允许混装，在舱位 C1
	// 舱位 C1：[G1 G2]，已用 30 千克，剩余 20 千克（承重 50）
	// == 更正后 ==
	// 货物 G1：25 千克，去 上海，不允许混装，在舱位 C1
	// 货物 G2：10 千克，去 上海，允许混装，在舱位 C1
	// 舱位 C1：[G1 G2]，已用 35 千克，剩余 15 千克（承重 50）
	// == 承重恰好用满 ==
	// 货物 G1：40 千克，去 上海，不允许混装，在舱位 C1
	// 舱位 C1：[G1 G2]，已用 50 千克，剩余 0 千克（承重 50）
}

// 拒绝情况一：新重量会让货物当前所在舱位超重。
//
// G1 若从 20 千克改成 45 千克，C1 预计为 45+10=55 千克，超过承重 50。
// 目的地与混装许可保持不变，纯粹因重量被拒。超重错误的“涉及编号”是
// 所属舱位 C1，而不是货物编号——超重是舱位整体的状态。
func Example_amendRejectedOverweight() {
	reg := newAmendRegistry()

	err := reg.AmendCargo("G1", 45, "北京", true)
	fmt.Println("更正被接受:", err == nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("错误原因:", serr.Kind)
		fmt.Println("涉及编号（超重时为所属舱位）:", serr.ID)
		fmt.Println("说明:", err)
	}

	// 更正失败是一次整体失败：三项旧资料、装载位置与舱位重量全部保留。
	printCargo(reg, "G1")
	printCompartment(reg, "C1")

	// Output:
	// 更正被接受: false
	// 错误原因: 超重
	// 涉及编号（超重时为所属舱位）: C1
	// 说明: 舱位 C1 总重量 55 千克超过最大承重 50 千克
	// 货物 G1：20 千克，去 北京，允许混装，在舱位 C1
	// 舱位 C1：[G1 G2]，已用 30 千克，剩余 20 千克（承重 50）
}

// 拒绝情况二：舱内目的地仍不同，却取消货物的混装许可。
//
// G1 仍去北京、G2 仍去上海，这时把 G1 的混装许可从允许改为不允许，
// 最终配载就出现“不同目的地共舱、但 G1 不允许混装”，因此被拒。混装
// 冲突错误的“涉及编号”是那件不允许混装的货物 G1（同舱有多件时取编号
// 字典序最靠前的一件）；说明文字同时给出冲突所在的舱位 C1。
func Example_amendRejectedMixed() {
	reg := newAmendRegistry()

	err := reg.AmendCargo("G1", 20, "北京", false)
	fmt.Println("更正被接受:", err == nil)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("错误原因:", serr.Kind)
		fmt.Println("涉及编号（混装冲突时为不允许混装的货物）:", serr.ID)
		fmt.Println("说明:", err)
	}

	// 失败后逐项回查：G1 的三项旧资料与位置、G2 的资料，以及 C1 的已用
	// 与剩余重量，都与更正前完全一致。
	printCargo(reg, "G1")
	printCargo(reg, "G2")
	printCompartment(reg, "C1")

	// Output:
	// 更正被接受: false
	// 错误原因: 混装冲突
	// 涉及编号（混装冲突时为不允许混装的货物）: G1
	// 说明: 舱位 C1 存在不同目的地货物，但货物 G1 不允许混装
	// 货物 G1：20 千克，去 北京，允许混装，在舱位 C1
	// 货物 G2：10 千克，去 上海，允许混装，在舱位 C1
	// 舱位 C1：[G1 G2]，已用 30 千克，剩余 20 千克（承重 50）
}

// 更正之后的新查询与新预览都按新资料计算；但更正之前已经拿到的查询
// 快照与预览快照不会被追溯改写，仍保留更正前的内容。
func Example_amendSnapshotAndPreview() {
	reg := newAmendRegistry()
	// 再加一个承重 22 千克的 C2，用于预览“把 G1 移到 C2”。
	if err := reg.RegisterCompartment("C2", 22); err != nil {
		panic(err)
	}

	// 更正前先拿一份货物与舱位查询快照，以及一份预览快照。此时 G1 重
	// 20 千克，移入 C2 后 20 <= 22，可以提交。
	oldCargo, err := reg.Cargo("G1")
	if err != nil {
		panic(err)
	}
	oldComp, err := reg.Compartment("C1")
	if err != nil {
		panic(err)
	}
	oldPreview, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpMove, CargoID: "G1", Target: "C2"},
	})
	if err != nil {
		panic(err)
	}
	oldC2After := oldPreview.Compartments[1].After // 受影响舱位按编号排序：C1、C2
	fmt.Println("更正前旧预览可提交:", oldPreview.Submittable)
	fmt.Printf("旧预览中 G1 移到 C2 后预计已用: %d 千克\n", oldC2After.UsedWeight)

	// 更正 G1：20 千克、北京 -> 25 千克、上海、不允许混装。
	if err := reg.AmendCargo("G1", 25, "上海", false); err != nil {
		panic(err)
	}

	// 更正后重新预览同一清单：新重量 25 > C2 承重 22，新预览按新资料
	// 判断为不可提交，并给出 25/22、超重 3 千克。
	newPreview, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpMove, CargoID: "G1", Target: "C2"},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("更正后新预览可提交:", newPreview.Submittable)
	rej := newPreview.Rejections[0]
	fmt.Printf("新预览中 C2: %d/%d 千克，超重 %d 千克\n",
		rej.UsedWeight, rej.MaxWeight, rej.Overweight)

	// 先前拿到的旧快照不被追溯改写：旧预览仍按 20 千克预计，更正前的
	// 查询快照仍是北京、30 千克。
	fmt.Printf("旧预览快照未变: G1 %d 千克 去 %s\n",
		oldC2After.Cargo[0].Weight, oldC2After.Cargo[0].Destination)
	fmt.Printf("更正前查询快照未变: G1 %d 千克 去 %s，C1 已用 %d 千克\n",
		oldCargo.Weight, oldCargo.Destination, oldComp.UsedWeight)

	// 重新查询得到的才是新资料；G1 实际仍在 C1（预览始终是只读的）。
	printCargo(reg, "G1")
	printCompartment(reg, "C1")

	// Output:
	// 更正前旧预览可提交: true
	// 旧预览中 G1 移到 C2 后预计已用: 20 千克
	// 更正后新预览可提交: false
	// 新预览中 C2: 25/22 千克，超重 3 千克
	// 旧预览快照未变: G1 20 千克 去 北京
	// 更正前查询快照未变: G1 20 千克 去 北京，C1 已用 30 千克
	// 货物 G1：25 千克，去 上海，不允许混装，在舱位 C1
	// 舱位 C1：[G1 G2]，已用 35 千克，剩余 15 千克（承重 50）
}
