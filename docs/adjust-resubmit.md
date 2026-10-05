# 正式提交的结果与重复提交

本文说明如何用 `stowage` 包的 `Adjust` 正式提交一批配载安排：读懂
返回的调整结果、分清结果快照与当前配载，以及用同一调整编号再次
提交时的各种情况——按原内容重复提交、换成不同内容、第一次就失败
后的重试。文中示例均为完整可运行的 Go 代码，与
`stowage/example_adjust_test.go` 中的示例一一对应，可用
`go test ./...` 直接验证输出。

本文只涉及正式提交的结果与重复提交的关系；承重、混装、整批生效等
配载规则与[提交前核对配载安排](preview-check.md)所述一致，不再
重复。

## 调整结果与查询各回答什么问题

先明确两个读数各自回答什么，后面的示例都建立在这个区分上：

- `Adjust(adjustmentID, ops)` 成功时返回的 `AdjustmentResult` 是
  **这次调整首次成功那一刻**的变化记录：`CargoChanges` 按货物编号
  字典序列出每件涉及货物调整前后的所属舱位（空字符串表示未装载），
  `CompartmentChanges` 按舱位编号字典序列出每个受影响舱位调整前后
  的总重量。
- `Cargo`、`Compartment` 等查询返回的是**调用那一刻**的当前配载。

两者只在"首次成功后没有其他改动"时才一致。之后另有调整生效时，
旧的结果快照不会跟着变——它记录的是那次调整带来的变化，不是现状。

## 主示例：按原编号、原内容再次提交

场景从新建登记处开始，不依赖任何已有状态：登记承重 100 千克的舱位
C1 和一件 30 千克的货物 G1（目的地上海、允许混装）。用编号
`load-1` 的调整把 G1 装入 C1，再用另一个编号 `unload-1` 的调整把
它卸下：

```go
reg := stowage.NewRegistry()
reg.RegisterCompartment("C1", 100)
reg.RegisterCargo("G1", 30, "上海", true)

loadOps := []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
}
first, err := reg.Adjust("load-1", loadOps)
// err == nil：G1 装入 C1，first 记录首次成功的变化。

_, err = reg.Adjust("unload-1", []stowage.Op{
    {Kind: stowage.OpUnload, CargoID: "G1"},
})
// err == nil：G1 已卸下，C1 恢复为空。
```

此时按**第一次的编号和原内容**再次提交装载（对应
`Example_adjustResubmitReturnsSavedResult`）：

```go
again, err := reg.Adjust("load-1", loadOps)
```

```
首次提交 load-1: 货物 G1 未装载 -> C1
首次提交 load-1: 舱位 C1 0 -> 30 千克
再次提交返回错误: <nil>
再次提交 load-1: 货物 G1 未装载 -> C1
再次提交 load-1: 舱位 C1 0 -> 30 千克
再次查询: G1 未装载，C1 已用 0 千克、剩余 100 千克
```

读法：

- **再次提交成功，并返回首次保存的结果。** 编号 `load-1` 已成功
  使用过，且这次提交的内容与首次相同，于是 `Adjust` 直接返回首次
  成功时保存的结果快照：货物 G1 从未装载变为 C1、舱位 C1 从 0 变为
  30 千克。
- **但这次提交不会重新装货。** 返回保存的结果后登记处不再执行任何
  操作：重新查询，G1 仍是未装载，C1 已用 0 千克、剩余 100 千克——
  保持 `unload-1` 卸下后的状态。
- **不要把旧结果当成刚刚发生的配载变化。** `again` 里的"G1 未装载
  -> C1"是 `load-1` 首次成功那一刻的记录，不是这次调用造成的变化；
  要知道现在的配载，必须重新查询。

这一行为让重试安全：不确定上一次提交是否成功时（例如调用方在拿到
结果前中断），按原编号、原内容再提交一次即可——成功了返回的就是
首次的结果，不会重复执行。

## 再次提交时，什么算"原内容"

`Adjust` 按以下规则判断两次提交的内容是否相同：

- 逐条比较**操作种类、货物编号**，以及**装载和移动的目标舱位**，
  三者都相同才算同一内容。
- **操作的排列顺序不影响判断**：同一批操作换个顺序提交，仍视为
  同一内容。
- 调整编号、货物编号、目标舱位都**去掉首尾空白后**比较，并**区分
  大小写**（`T-1` 与 `t-1` 是两个不同的调整编号）。
- **卸下操作填写的目标值不参与比较**（执行时同样忽略），填不填、
  填什么都不影响内容判断。

下面的例子中，首次提交 `t-1` 包含"G1 装入 C1、G2 卸下"两条操作；
再次提交时操作顺序颠倒、卸下多填了目标 `C9`、调整编号与目标舱位
都带首尾空白，仍被判定为同一内容（对应
`Example_adjustResubmitEquivalentContent`）：

```go
_, err = reg.Adjust("t-1", []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
    {Kind: stowage.OpUnload, CargoID: "G2"},
})

// 顺序不同、卸下多填目标、编号与目标带首尾空白：仍是同一内容。
again, err := reg.Adjust("  t-1  ", []stowage.Op{
    {Kind: stowage.OpUnload, CargoID: "G2", Target: "C9"},
    {Kind: stowage.OpLoad, CargoID: "G1", Target: " C1 "},
})
```

```
再次提交返回错误: <nil>
返回结果编号: t-1
返回结果: 货物 G1 未装载 -> C1
返回结果: 货物 G2 C2 -> 未装载
返回结果: 舱位 C1 0 -> 30 千克
返回结果: 舱位 C2 20 -> 0 千克
```

注意再次提交命中已保存结果时，`Adjust` 不再校验操作本身——上例中
卸下操作填的 `C9` 并不存在，但因为内容判定为相同，直接返回首次
保存的结果，编号也按去空白后的 `t-1` 记录。

## 同一编号换成不同内容：编号冲突

同一调整编号用于**不同内容**会被拒绝。下面的例子先用 `plan-1` 把
G1 装入 C1，再用同一编号提交"G1 装入 C2"——目标舱位不同，内容
不同（对应 `Example_adjustIDConflict`）：

```go
_, err := reg.Adjust("plan-1", []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
})
// err == nil：plan-1 已成功。

_, err = reg.Adjust("plan-1", []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C2"},
})
```

```
拒绝原因: 调整编号冲突
涉及调整编号: plan-1
说明: 调整编号 plan-1 已用于不同内容
当前配载: G1 位于 C1，C1 已用 30 千克、剩余 70 千克
```

读法：

- 返回结构化错误 `*stowage.Error`，用 `errors.As` 取出后：`Kind`
  是 `ErrAdjustmentIDConflict`，`AdjustmentID` 给出冲突的调整编号
  （这里是 `plan-1`），`Error()` 给出可直接阅读的中文说明。
- **当前配载保持不变**：被拒绝的提交不生效，货物归属与舱位重量
  维持原样，可以查询确认。
- 另一批不同的安排应**另用一个新编号**提交，不要复用已成功的
  编号。

## 第一次就失败的调整不占用编号

如果调整**第一次提交就失败**（操作不合法，或最终配载违反承重、
混装限制），这个编号不会被占用：修正安排后可以**沿用同一编号**
再次提交（对应 `Example_adjustRetryAfterFailure`）：

```go
// 目标舱位不存在，第一次提交失败。
_, err := reg.Adjust("load-1", []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C9"},
})

// 修正安排后沿用同一编号提交：作为全新的调整校验并生效。
res, err := reg.Adjust("load-1", []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
})
```

```
首次提交: 舱位 C9 不存在
修正后提交 load-1: 货物 G1 未装载 -> C1
修正后提交 load-1: 舱位 C1 0 -> 30 千克
提交后 G1 所在舱位: C1
```

这与已成功调整的重复提交是**两种不同情况**：

| 上次用该编号提交的结果 | 再次用同一编号提交 |
| --- | --- |
| 已**成功** | 内容相同：返回首次保存的结果，不再执行；内容不同：编号冲突，被拒绝 |
| 已**失败** | 编号未占用，按一次全新的调整校验并执行 |

## 与其他文档的关系

- 提交前想先核对安排能否通过、预计结果如何，用只读的 `Preview`，
  见[提交前核对配载安排](preview-check.md)。
- 已登记货物的重量、目的地、混装许可需要更正时用 `AmendCargo`，
  见[更正已登记货物的资料](amend-cargo.md)。更正不占用调整编号，
  也不改写已成功调整保存的结果。
