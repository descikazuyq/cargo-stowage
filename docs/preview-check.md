# 提交前核对配载安排

本文说明如何用 `stowage` 包在正式提交调整之前核对一批配载安排：用
`Preview` 预览、读懂预览结果、区分"不可提交"与"操作不合法"，并弄清
预览与正式提交（`Adjust`）的关系。文中示例均为完整可运行的 Go 代码，
与 `stowage/example_preview_test.go` 中的示例一一对应，可用
`go test ./...` 直接验证输出。

## 预览与正式提交的关系

先明确两者的分工，后面的示例都建立在这个前提上：

- `Preview(ops)` 是**只读**的。无论结果是可以提交还是被拒绝，货物
  归属、舱位占用与已成功调整记录都不会改变，也不占用任何调整编号。
- 只有 `Adjust(adjustmentID, ops)` **成功返回**才真正应用安排。
- 预览通过不等于提交必然成功：正式提交按**提交时**的登记处状态重新
  判断。若预览之后配载或货物资料已有其他更改（例如另有调整生效或
  货物资料被更正），旧的预览结果不能作为必然成功的保证；未发生其他
  更改时，合法安排提交后的查询结果与之前的预计配载一一对应。

## 主示例：预览两件满载货物的互换

场景从新建登记处开始，不依赖任何已有状态：登记两个承重均为 30 千克
的舱位 C1、C2，再登记两件各 30 千克、目的地相同（上海）且不允许混装
的货物 G1、G2，并通过一次正式调整把 G1 装入 C1、G2 装入 C2，使两个
舱位各自满载：

```go
reg := stowage.NewRegistry()
reg.RegisterCompartment("C1", 30)
reg.RegisterCompartment("C2", 30)
reg.RegisterCargo("G1", 30, "上海", false)
reg.RegisterCargo("G2", 30, "上海", false)
_, err := reg.Adjust("load-1", []stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G1", Target: "C1"},
    {Kind: stowage.OpLoad, CargoID: "G2", Target: "C2"},
})
// err == nil：初始装载完成，C1、C2 各装 30/30 千克。
```

现在预览同一批安排中的两件货物互换（G1 移到 C2、G2 移到 C1）：

```go
res, err := reg.Preview([]stowage.Op{
    {Kind: stowage.OpMove, CargoID: "G1", Target: "C2"},
    {Kind: stowage.OpMove, CargoID: "G2", Target: "C1"},
})
```

**为什么两个满载舱位仍能交换？** 承重与混装限制针对的是**整批安排
完成后的最终配载**，而不是每条操作的中间状态。单独看"G1 移入 C2"
会让 C2 暂时达到 60 千克，但同一批安排里 G2 同时移出，最终每个舱位
仍恰好 30 千克，因此整批合法。结果如下（对应
`Example_previewSwap`）：

```
可提交: true
货物 G1: C1 -> C2
货物 G2: C2 -> C1
舱位 C1 前: [G1]（30/30 千克）
舱位 C1 后: [G2]（30/30 千克）
舱位 C2 前: [G2]（30/30 千克）
舱位 C2 后: [G1]（30/30 千克）
预计清单中 G2 属于舱位 C1
预览后查询 G1 所在舱位: C1
```

读法：

- `Submittable` 为 `true`：这组操作按当前状态可以作为一次调整提交。
- `CargoChanges` 给出每件涉及货物的**原舱位**（`From`）与**预计
  舱位**（`To`），按货物编号字典序排列；空字符串表示未装载。
- `Compartments` 列出全部受影响舱位**前后**的完整货物清单、已用
  重量与剩余重量，按舱位编号字典序排列。交换前后两个舱位的总重量
  都没有变化（都是 30 千克），但两个舱位仍会列出，便于核对清单
  本身的变化。
- `After`（预计清单）中货物的位置按**交换后的所属舱位**解释：C1
  的预计清单里 G2 显示为已装载且属于 C1。这只是预览快照的展示方式
  ——预览是只读的，用 `Cargo("G1")` 查询当前货物，看到的仍是交换
  前的位置 C1。

## 被拒绝的预览：每条操作合法，但整体不可提交

在同一组初始配载上（C1、C2 仍各装一件 30 千克货物），再登记第三件
货物 G3：10 千克、目的地北京（与 G1 不同）、允许混装，然后预览把它
装入已满载的 C1：

```go
reg.RegisterCargo("G3", 10, "北京", true)
res, err := reg.Preview([]stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G3", Target: "C1"},
})
```

这条操作本身完全合法（货物、舱位都存在，G3 尚未装载），所以**预览
调用不会失败**：`err == nil`，预计清单照常返回。不可提交体现在结果
里（对应 `Example_previewRejected`）：

```
预览调用出错: false
可提交: false
舱位 C1 后: [G1 G3]（40/30 千克）
舱位 C1 超重 10 千克（40/30）
舱位 C1 目的地 上海: [G1]
舱位 C1 目的地 北京: [G3]
不允许混装的货物: [G1]
预览后 C1 实际货物: [G1]
```

读法：

- `Submittable` 为 `false`，`Rejections` 一次列全全部受影响舱位的
  所有拒绝原因（按舱位编号排列，同一舱位先重量后混装）。这里 C1
  同时有两条：
  - **超重**：`Kind == ErrOverweight`，`CompartmentID` 指出受影响
    舱位是 C1，`UsedWeight`/`MaxWeight`/`Overweight` 给出
    40/30 千克、超出 10 千克（`RemainingWeight` 为 -10）。
  - **混装冲突**：`Kind == ErrMixedLoading`，`Destinations` 列出
    舱内各目的地及其货物（上海：G1，北京：G3），`OffendingCargo`
    列出其中全部**不允许混装**的货物——这里是 G1。注意被拒绝的
    不一定是新装入的货物：G3 允许混装，冲突来自不允许混装的 G1。
- 被拒绝的预览同样不改变任何状态：查询 C1 的实际货物仍只有 G1。

**不要把"不可提交"理解成预览调用本身失败。** 每条操作合法时预览
始终返回完整结果，能否提交看 `Submittable` 与 `Rejections`；只有
操作本身不合法时 `Preview` 才返回错误（见下节）。

## 操作不合法：结构化错误，没有局部预览

用不存在的货物编号预览（对应 `Example_previewInvalidOp`）：

```go
res, err := reg.Preview([]stowage.Op{
    {Kind: stowage.OpLoad, CargoID: "G9", Target: "C1"},
})
```

```
有预览结果: false
错误原因: 对象不存在
涉及编号: G9
```

`Preview` 与 `Adjust` 共用同一套逐条校验（货物与舱位是否存在、货物
状态、同一批中重复操作、移动到原舱位等）。某条操作不合法时，按输入
顺序返回第一个非法操作的结构化错误 `*stowage.Error`——`Kind` 给出
原因（这里是 `ErrNotFound`），`ID` 给出涉及的编号——并且**不返回
任何局部预览**（`res == nil`）。这与上节的拒绝结果是两类情况：

| 情况 | 返回值 | 含义 |
| --- | --- | --- |
| 每条操作合法，最终配载违反限制 | `err == nil`，`Submittable == false`，`Rejections` 列全原因 | 安排整体不可提交，可据原因修改后再次预览 |
| 某条操作本身不合法 | `err != nil`（`*stowage.Error`），无预览结果 | 清单写错了，先修正操作本身 |

## 预览通过后正式提交

预览通过后用**同一批操作**提交（对应 `Example_adjustAfterPreview`）：

```go
res, _ := reg.Preview(ops)   // res.Submittable == true，配载未变
adj, err := reg.Adjust("swap-1", ops)
```

```
预览可提交: true
已提交调整: swap-1
提交后 G1 所在舱位: C2
提交后 G2 所在舱位: C1
```

只有 `Adjust` 成功返回，安排才真正生效；此后的查询结果与之前预览的
预计配载对应。再次提醒：如果预览之后、提交之前登记处状态有变（另有
调整生效、货物资料被 `AmendCargo` 更正等），`Adjust` 仍按提交时的
状态重新判断，可能成功也可能被拒绝，旧的预览结果不构成保证；拿不准
时在提交前重新预览一次即可。

货物资料本身的更正方法（三项资料整体替换、超重与混装两种拒绝、失败后
原配载完整保留）见[更正已登记货物的资料](amend-cargo.md)。更正成功
之后新发起的查询与预览都按新资料计算，而更正之前已经返回的查询快照与
预览快照不会被追溯改写，仍保留更正前的内容。
