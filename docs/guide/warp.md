---
title: Warp 变形
description: 编译智能对象的仿射、透视、单片与均匀网格 Warp，执行正反坐标映射与图像投影。
---
# Warp 变形

智能对象的放置信息（`Placed`）描述了来源内容如何被变换到画布上。`psdwarp` 把这些元数据编译成可执行的几何，支持正向 / 逆向坐标映射和图像投影。**几何精度和像素精度分别报告**。

## 支持范围

| 类型 | 入口 | 状态 |
| --- | --- | --- |
| 仿射与透视 | `psdwarp.Compile` / `psdwarp.Project` | 支持 |
| 单片 Warp | `psdwarp.Compile` / `CompileMathematical` | 支持，数学投影需显式选择 |
| 均匀网格 | `psdwarp.CompileUniformGrid` / `ProjectUniformGrid`、`psdkit.CompileUniformGridWarpGeometry` | 受限 10×10 / 13×13，含正反映射、ROI 与预算 |
| 预设几何 | `psdwarp.CompilePresetGeometry`、`psdkit.CompilePresetWarpGeometry` | 显式几何与预览，不接入自动整图 |
| 任意 quilt / 预设整图 | — | 未支持，返回 `unsupported` |
| 操控变形 | — | 未支持 |

## 编译与投影

```go
view, err := source.ReadLayerMetadata(ctx, layerIndex)
if err != nil {
	return err
}
patch, err := psdwarp.Compile(ctx, view.Layer.Placed)
if err != nil {
	return err // 非受支持的 Warp 会在这里被拒绝
}
warped, origin, err := patch.Project(ctx, contentPixels, canvasBounds)
if err != nil {
	return err
}
```

`Compile` 返回的 `Patch` 与 `CompileUniformGrid` 返回的 `UniformGrid` 都提供 `Forward`、`Inverse`、`Bounds` 和 `Project`，`WorkingBytes` 用于预算估算。

## 编辑单个智能对象的变换

`Editor.SetSmartObjectTransform(ctx, id, corners)` 修改单个栅格智能对象的目标四角，并重新生成该实例的保存像素。`corners` 是父文档坐标下的左上、右上、右下、左下四点；共享来源和其他实例保持不变。

```go
corners := [4]psdmodel.Point{
	{X: 20, Y: 20}, {X: 220, Y: 20},
	{X: 220, Y: 120}, {X: 20, Y: 120},
}
if err := editor.SetSmartObjectTransform(ctx, id, corners); err != nil {
	return err
}
```

当前限 RGB8 父文档、受支持的栅格来源及没有活动 Warp 的实例。它更新放置变换、图层边界和实例栅格，保留蒙版、效果、不透明度及未知字节；不编辑 Warp 网格或预设参数。退化、无效或不支持的投影原子拒绝。

`SetSmartObjectTransformWithOptions(ctx, id, corners, resolver, SmartTransformOptions)` 接受明确的外链解析器和采样策略。嵌入来源或本编辑器已有更新的来源不需要 resolver；未更新的外链须显式解析。解析器在锁外执行，期间编辑器发生变化则返回 `ErrStaleLinkedEdit`。

`SmartTransformOptions.AssumeUntaggedSRGB` 明确解释无配置来源；`ApproximatePlacedProjection` 允许透明边界的数学采样，默认关闭，不代表 Photoshop 像素等价。浏览器通过 `editor.SetSmartObjectTransformWithOptions` 调用，参数为 `[id, corners, options]`，resolver 由请求的 `links` 资产映射注入。

> [!WARNING]
> 受限的均匀网格具备正反映射、ROI 和预算控制，但不代表任意 quilt、预设或全部组合都能通过实时资格。`ApproximateCustomWarp` 等显式近似不会提升为 Photoshop 像素兼容。

## 在整图重绘中使用

`RenderSource` 与 `RenderSourcePlacedLayer` 会在资格检查通过时自动应用支持的变换；自定义 Warp 的数学近似需要通过 `SceneOptions{ApproximateCustomWarp: true}` 显式开启。

浏览器 Playground 的“智能对象与 Warp”分组提供 `warp.*` 入口，可以导入自己的图片，对文档中的智能对象执行投影。

```sh
go run ./examples/placed-layer -help
```
