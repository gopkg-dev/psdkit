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

> [!WARNING]
> 受限的均匀网格具备正反映射、ROI 和预算控制，但不代表任意 quilt、预设或全部组合都能通过实时资格。`ApproximateCustomWarp` 等显式近似不会提升为 Photoshop 像素兼容。

## 在整图重绘中使用

`RenderSource` 与 `RenderSourcePlacedLayer` 会在资格检查通过时自动应用支持的变换；自定义 Warp 的数学近似需要通过 `SceneOptions{ApproximateCustomWarp: true}` 显式开启。

浏览器 Playground 的“智能对象与 Warp”分组提供 `warp.*` 入口，可以导入自己的图片，对文档中的智能对象执行投影。

```sh
go run ./examples/placed-layer -help
```
