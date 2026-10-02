# 本地舱单与配载

在本机运行的本地舱单与配载。提供 `stowage` 包，供本机程序登记舱位和
货物、执行批量装卸调整并查询配载情况。

## 使用

```go
import "github.com/descikazuyq/cargo-stowage/stowage"

sys := stowage.New()
_ = sys.RegisterHold("H1", 1000)                 // 舱位编号、最大承重（正整数千克）
_ = sys.RegisterCargo("C1", 100, "Shanghai", true) // 编号、重量、目的地、是否允许混装

// 一次调整可同时装载、卸下、移动多件货物；按终态判断承重与混装，
// 任一操作不合法则整次不生效。
res, err := sys.Adjust("A1", []stowage.Operation{
    {Kind: stowage.Load, CargoID: "C1", TargetHoldID: "H1"},
})
// res.Moves / res.HoldWeights 为涉及货物的位置变化与舱位前后重量。

hold, _ := sys.GetHold("H1") // 承重、已用、剩余及按编号排序的货物清单
c, _ := sys.GetCargo("C1")   // 当前舱位；未装载时 c.Location.Loaded == false
```

要点：

- 编号与目的地去首尾空白后不得为空；编号按去空白后的值识别、区分大小写。
- 重复登记返回 `ErrHoldExists` / `ErrCargoExists`，原记录保留。
- 调整编号同样遵循编号规则：相同编号相同内容重复提交返回首次结果；
  相同编号不同内容返回原因代码为 `id_conflict` 的 `*AdjustmentError`；
  失败的调整不占用编号。
- 重量与承重为 `int64`，累加溢出会被拒绝。
- 所有查询与调整结果都是独立快照。

## 测试

```bash
go test ./...
```
