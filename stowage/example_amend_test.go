package stowage_test

import (
	"errors"
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

// newAmendRegistry 新建登记处并完成初始配载：舱位 C1 承重 100 千克，装入
// 两件目的地不同、都允许混装的货物（G1 30 千克去上海、G2 20 千克去北京），
// 已用 50 千克。每个示例都从这里独立开始，不依赖其他示例留下的状态。
func newAmendRegistry() *stowage.Registry {
	reg := stowage.NewRegistry()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(reg.RegisterCompartment("C1", 100))
	must(reg.RegisterCargo("G1", 30, "上海", true))
	must(reg.RegisterCargo("G2", 20, "北京", true))
	_, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
		{Kind: stowage.OpLoad, CargoID: "G2", Target: "C1"},
	})
	must(err)
	return reg
}

// 更正已装载货物 G2 的三项资料：重量 20 -> 40 千克、目的地 北京 -> 上海
// （与同舱 G1 相同）、同时取消混装许可。三项作为一次整体替换生效，货物
// 编号与所属舱位不变；目的地一致后同舱共载不再要求混装许可。
func Example_amendLoadedCargo() {
	reg := newAmendRegistry()

	show := func(label string) {
		g2, _ := reg.Cargo("G2")
		c1, _ := reg.Compartment("C1")
		fmt.Printf("%s: G2 重量 %d 千克、目的地 %s、允许混装 %t、所在舱位 %s\n",
			label, g2.Weight, g2.Destination, g2.AllowMixed, g2.CompartmentID)
		fmt.Printf("%s: 舱位 C1 已用 %d 千克、剩余 %d 千克\n",
			label, c1.UsedWeight, c1.RemainingWeight)
	}
	show("更正前")

	// 三项资料一次整体替换：重量、目的地、混装许可。
	err := reg.AmendCargo("G2", 40, "上海", false)
	fmt.Println("更正返回错误:", err)

	show("更正后")

	// Output:
	// 更正前: G2 重量 20 千克、目的地 北京、允许混装 true、所在舱位 C1
	// 更正前: 舱位 C1 已用 50 千克、剩余 50 千克
	// 更正返回错误: <nil>
	// 更正后: G2 重量 40 千克、目的地 上海、允许混装 false、所在舱位 C1
	// 更正后: 舱位 C1 已用 70 千克、剩余 30 千克
}

// 更正后的新重量使当前舱位超重时被拒绝：G1 改到 90 千克后舱位预计合计
// 110 千克，超过承重 100 千克。失败后旧资料、装载位置与舱位重量全部保留。
func Example_amendOverweightRejected() {
	reg := newAmendRegistry()

	err := reg.AmendCargo("G1", 90, "上海", true)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("拒绝原因:", serr.Kind)
		fmt.Println("涉及编号:", serr.ID)
		fmt.Println("说明:", serr)
	}

	// 失败后查询：三项旧资料、装载位置与舱位重量全部保留。
	g1, _ := reg.Cargo("G1")
	c1, _ := reg.Compartment("C1")
	fmt.Printf("G1 仍是 %d 千克、目的地 %s、允许混装 %t、所在舱位 %s\n",
		g1.Weight, g1.Destination, g1.AllowMixed, g1.CompartmentID)
	fmt.Printf("舱位 C1 已用 %d 千克、剩余 %d 千克\n", c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 拒绝原因: 超重
	// 涉及编号: C1
	// 说明: 舱位 C1 总重量 110 千克超过最大承重 100 千克
	// G1 仍是 30 千克、目的地 上海、允许混装 true、所在舱位 C1
	// 舱位 C1 已用 50 千克、剩余 50 千克
}

// 目的地仍不同（上海/北京）时取消 G1 的混装许可被拒绝：不同目的地共舱
// 要求所有货物都允许混装。失败后旧资料、装载位置与舱位重量全部保留。
func Example_amendMixedConflictRejected() {
	reg := newAmendRegistry()

	err := reg.AmendCargo("G1", 30, "上海", false)
	var serr *stowage.Error
	if errors.As(err, &serr) {
		fmt.Println("拒绝原因:", serr.Kind)
		fmt.Println("涉及编号:", serr.ID)
		fmt.Println("说明:", serr)
	}

	// 失败后查询：三项旧资料、装载位置与舱位重量全部保留。
	g1, _ := reg.Cargo("G1")
	c1, _ := reg.Compartment("C1")
	fmt.Printf("G1 仍是 %d 千克、目的地 %s、允许混装 %t、所在舱位 %s\n",
		g1.Weight, g1.Destination, g1.AllowMixed, g1.CompartmentID)
	fmt.Printf("舱位 C1 已用 %d 千克、剩余 %d 千克\n", c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 拒绝原因: 混装冲突
	// 涉及编号: G1
	// 说明: 舱位 C1 存在不同目的地货物，但货物 G1 不允许混装
	// G1 仍是 30 千克、目的地 上海、允许混装 true、所在舱位 C1
	// 舱位 C1 已用 50 千克、剩余 50 千克
}

// 更正后舱位承重恰好用满是允许的，不算超重：G2 改到 70 千克后舱位合计
// 恰好 100 千克，等于承重，更正成功。
func Example_amendExactCapacity() {
	reg := newAmendRegistry()

	err := reg.AmendCargo("G2", 70, "北京", true)
	fmt.Println("更正返回错误:", err)
	c1, _ := reg.Compartment("C1")
	fmt.Printf("舱位 C1 已用 %d 千克、剩余 %d 千克\n", c1.UsedWeight, c1.RemainingWeight)

	// Output:
	// 更正返回错误: <nil>
	// 舱位 C1 已用 100 千克、剩余 0 千克
}
