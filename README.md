# 本地舱单与配载

这是一个在本机运行的本地舱单与配载。当前基线只提供可编译、可测试的起点。

## 使用

```bash
go test ./...
```

测试通过表示基线包可以加载。后续能力在这个模块上继续增加。

## 提交前核对配载安排

`stowage` 包提供 `Preview` 方法：在正式提交（`Adjust`）之前，用同一批
操作清单预演一次配载，得到预计结果与全部拒绝原因。预览是只读的——
无论结果能否提交，货物归属、舱位占用与已成功调整记录都不会改变，
也不占用任何调整编号；只有 `Adjust` 成功才真正应用安排。

### 主示例：两个满载舱位互换货物

下面是一个完整、自包含的示例：从新建登记处开始，登记两个承重均为
30 千克的舱位，各装入一件 30 千克、目的地相同且不允许混装的货物，
然后预览同一批安排中两件货物的互换。

```go
package main

import (
	"fmt"

	"github.com/descikazuyq/cargo-stowage/stowage"
)

func main() {
	reg := stowage.NewRegistry()

	// 登记两个舱位，承重均为 30 千克。
	if err := reg.RegisterCompartment("A", 30); err != nil {
		panic(err)
	}
	if err := reg.RegisterCompartment("B", 30); err != nil {
		panic(err)
	}

	// 登记两件货物：各 30 千克，目的地相同，均不允许混装。
	// 登记后货物处于未装载状态。
	if err := reg.RegisterCargo("C1", 30, "上海", false); err != nil {
		panic(err)
	}
	if err := reg.RegisterCargo("C2", 30, "上海", false); err != nil {
		panic(err)
	}

	// 初始装载：C1 装入 A，C2 装入 B。这是一次正式提交，
	// 提交成功后两个舱位各自满载（已用 30 千克，剩余 0 千克）。
	if _, err := reg.Adjust("load-1", []stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "C1", Target: "A"},
		{Kind: stowage.OpLoad, CargoID: "C2", Target: "B"},
	}); err != nil {
		panic(err)
	}

	// 预览互换：C1 移到 B，C2 移到 A，两条移动属于同一批安排。
	swap := []stowage.Op{
		{Kind: stowage.OpMove, CargoID: "C1", Target: "B"},
		{Kind: stowage.OpMove, CargoID: "C2", Target: "A"},
	}
	p, err := reg.Preview(swap)
	if err != nil {
		panic(err) // 本例不会发生：两条操作本身都合法
	}

	fmt.Println("可提交:", p.Submittable) // 可提交: true
	for _, c := range p.CargoChanges {
		fmt.Printf("货物 %s: %s -> %s\n", c.CargoID, c.From, c.To)
	}
	for _, cp := range p.Compartments {
		fmt.Printf("舱位 %s 前: 已用 %d 剩余 %d 货物 %v\n",
			cp.ID, cp.Before.UsedWeight, cp.Before.RemainingWeight, cargoIDs(cp.Before.Cargo))
		fmt.Printf("舱位 %s 后: 已用 %d 剩余 %d 货物 %v\n",
			cp.ID, cp.After.UsedWeight, cp.After.RemainingWeight, cargoIDs(cp.After.Cargo))
	}

	// 预览不改变任何状态：此时查询 C1，仍在舱位 A。
	view, _ := reg.Cargo("C1")
	fmt.Println("预览后 C1 实际所在舱位:", view.CompartmentID) // A
}

func cargoIDs(vs []stowage.CargoView) []string {
	ids := make([]string, 0, len(vs))
	for _, v := range vs {
		ids = append(ids, v.ID)
	}
	return ids
}
```

对应的输出：

```text
可提交: true
货物 C1: A -> B
货物 C2: B -> A
舱位 A 前: 已用 30 剩余 0 货物 [C1]
舱位 A 后: 已用 30 剩余 0 货物 [C2]
舱位 B 前: 已用 30 剩余 0 货物 [C2]
舱位 B 后: 已用 30 剩余 0 货物 [C1]
预览后 C1 实际所在舱位: A
```

读懂这份结果：

- `Submittable` 为 `true` 且 `Rejections` 为空，表示按当前状态这批
  安排可以作为一次调整提交。
- `CargoChanges` 按货物编号字典序列出每件涉及货物的原舱位（`From`）
  与预计舱位（`To`）：C1 从 A 到 B，C2 从 B 到 A。
- `Compartments` 列出全部受影响舱位调整前后的完整货物清单。交换中
  两个舱位的总重量都没有变化（都是 30 千克），但 A、B 两个舱位都会
  列出，因为它们的货物构成都变了。
- `After`（预计清单）中货物的位置按交换完成后的所属舱位解释：清单里
  的每件货物一律显示为已装载且属于该舱位（例如 A 舱预计清单中的 C2
  显示 `CompartmentID` 为 A）。这只是预计视图——预览之后用 `Cargo`
  查询当前货物，看到的仍是交换前的位置（C1 仍在 A）。

**为什么两个满载舱位仍能交换？** 如果逐条判断，C1 移入已满载的 B
会先造成暂时超重。但承重与混装限制针对的是整批安排全部完成后的最终
配载：两条移动都应用之后，每个舱位仍只有一件 30 千克货物，同目的地
也不构成混装，因此整批合法。预览与正式提交都按这一口径判断。

### 会被拒绝的安排：超重与混装冲突同时列出

沿用上面同一组初始配载（A、B 各装一件 30 千克货物），登记第三件货物
——10 千克、目的地不同、允许混装——并预览把它装入舱位 A：

```go
	if err := reg.RegisterCargo("C3", 10, "广州", true); err != nil {
		panic(err)
	}

	p2, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpLoad, CargoID: "C3", Target: "A"},
	})
	if err != nil {
		panic(err) // 不会发生：这条操作本身合法
	}

	fmt.Println("可提交:", p2.Submittable) // 可提交: false
	for _, r := range p2.Rejections {
		fmt.Printf("舱位 %s 拒绝原因: %s\n", r.CompartmentID, r.Kind)
		switch r.Kind {
		case stowage.ErrOverweight:
			fmt.Printf("  预计总重 %d 千克，承重 %d 千克，超出 %d 千克\n",
				r.UsedWeight, r.MaxWeight, r.Overweight)
		case stowage.ErrMixedLoading:
			for _, d := range r.Destinations {
				fmt.Printf("  目的地 %s: 货物 %v\n", d.Destination, d.CargoIDs)
			}
			fmt.Println("  不允许混装的货物:", r.OffendingCargo)
		}
	}
```

对应的输出：

```text
可提交: false
舱位 A 拒绝原因: 超重
  预计总重 40 千克，承重 30 千克，超出 10 千克
舱位 A 拒绝原因: 混装冲突
  目的地 上海: 货物 [C1]
  目的地 广州: 货物 [C3]
  不允许混装的货物: [C1]
```

读懂这份结果：

- 这条装载操作本身合法（货物存在、未装载、目标舱位存在），所以预览
  正常返回，`err` 为 `nil`，并且 `CargoChanges` 与 `Compartments`
  照常给出预计清单（A 舱预计货物为 `[C1 C3]`，预计已用 40 千克、
  剩余 -10 千克）。**不可提交不等于预览调用失败**：`Submittable`
  为 `false` 表示预计配载违反限制，这批安排不能照此提交。
- `Rejections` 一次列全全部拒绝原因，按舱位编号排列、同一舱位先
  重量后混装。这里只有舱位 A 受影响（`Compartments` 也只列 A），
  它同时有两条原因：
  - `ErrOverweight`：`Overweight` 为 10，即超出承重 10 千克；
    `UsedWeight`/`RemainingWeight` 给出预计总重 40 与剩余 -10。
  - `ErrMixedLoading`：`Destinations` 列出冲突的各目的地及其货物
    （上海的 C1、广州的 C3），`OffendingCargo` 列出其中不允许混装
    的货物——这里是 C1。注意 C3 自身允许混装，冲突来自 C1。

### 操作本身不合法：结构化错误，没有局部预览

如果清单中某条操作本身不合法——例如引用了不存在的货物编号——预览
不会返回部分结果，而是按输入顺序返回第一个非法操作的结构化错误：

```go
	p3, err := reg.Preview([]stowage.Op{
		{Kind: stowage.OpMove, CargoID: "C9", Target: "A"}, // C9 不存在
	})
	// p3 为 nil；err 是 *stowage.Error：
	//   Kind == stowage.ErrNotFound，ID == "C9"
	if se, ok := err.(*stowage.Error); ok {
		fmt.Println(se.Kind, se.ID) // 对象不存在 C9
	}
	_ = p3
```

这与上一节的拒绝结果是两类不同情况，不要混淆：

- **操作不合法**（货物或舱位不存在、货物状态不符、同一调整中重复
  操作、移动到原舱位等）：`Preview` 返回非 `nil` 的 `error`，结果
  为 `nil`，没有任何局部预览。逐条合法性规则与正式提交完全一致。
- **操作合法但最终配载违反限制**：`Preview` 返回 `nil` 错误和完整
  预览，`Submittable` 为 `false`，`Rejections` 列出全部拒绝原因。

### 预览与正式提交的关系

- 预览无论成功（`Submittable` 为 `true`）还是被拒绝，都不改变货物
  归属和舱位占用，也不占用调整编号；预览之后不需要任何“撤销”。
- 只有 `Adjust` 成功才真正应用安排。例如上面的交换：

```go
	if _, err := reg.Adjust("swap-1", swap); err != nil {
		panic(err)
	}
	view, _ := reg.Cargo("C1")
	fmt.Println("提交后 C1 所在舱位:", view.CompartmentID) // B
```

- 未发生其他更改时，合法交换提交成功后的查询结果与之前的预计配载
  一一对应（C1 在 B、C2 在 A，两舱各 30 千克）。
- 但正式提交始终以提交时刻的状态重新判断：如果预览之后配载或货物
  资料已有变化（另有调整生效、货物资料被更正等），旧的预览结果不
  能作为必然成功的保证，应以 `Adjust` 的返回为准。
