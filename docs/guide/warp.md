---
title: Warp 变形
description: 编译智能对象的仿射、透视、单片与均匀网格 Warp，执行正反坐标映射与图像投影，并编辑已声明的预设控制参数。
---
# Warp 变形

智能对象的放置信息（`Placed`）描述了来源内容如何被变换到画布上。`psdwarp` 把这些元数据编译成可执行的几何，支持正向 / 逆向坐标映射和图像投影。**几何精度和像素精度分别报告**。

## 支持范围

| 类型 | 入口 | 状态 |
| --- | --- | --- |
| 仿射与透视 | `psdwarp.Compile` / `psdwarp.Project` | 支持 |
| 单片 Warp | `psdwarp.Compile` / `CompileMathematical` | 支持，数学投影需显式选择 |
| 均匀网格 | `psdwarp.CompileUniformGrid` / `ProjectUniformGrid`、来源重绘与 `Editor.SetSmartObjectWarp` | 受限 10×10 / 13×13，含正反映射、ROI、编辑保存；来源投影逐次显式 `ApproximateCustomWarp`，仍限仿射放置与已认证均匀切片 |
| 预设几何 | `psdwarp.CompilePresetGeometry`、`psdkit.CompilePresetWarpGeometry`、来源重绘 | 已声明横向 Arc Lower 的独立几何；整图、区域、分块及单实例来源重绘逐次显式选择数学采样 |
| 预设参数编辑 | `Editor.SetSmartObjectWarpPreset` | 已有横向 Arc Lower 的 bend 参数，逐次显式近似与外链映射，更新实例像素并保存重开续编 |
| 任意 quilt / 其他预设 | — | 未支持，返回 `unsupported` |
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

这个四角入口限 RGB8 父文档、受支持的栅格来源及没有活动 Warp 的实例。它更新放置变换、图层边界和实例栅格，保留蒙版、效果、不透明度及未知字节；不编辑 Warp 控制点或预设参数。已有 4×4 自定义控制点使用下节的新入口。退化、无效或不支持的投影原子拒绝。

`SetSmartObjectTransformWithOptions(ctx, id, corners, resolver, SmartTransformOptions)` 接受明确的外链解析器和采样策略。嵌入来源或本编辑器已有更新的来源不需要 resolver；未更新的外链须显式解析。解析器在锁外执行，期间编辑器发生变化则返回 `ErrStaleLinkedEdit`。

`SmartTransformOptions.AssumeUntaggedSRGB` 明确解释无配置来源；`ApproximatePlacedProjection` 允许透明边界的数学采样，默认关闭，不代表 Photoshop 像素等价。浏览器通过 `editor.SetSmartObjectTransformWithOptions` 调用，参数为 `[id, corners, options]`，resolver 由请求的 `links` 资产映射注入。

## 编辑 4×4 自定义 Warp 控制点

`Editor.SetSmartObjectWarp(ctx, id, points, SmartWarpEditOptions)` 编辑**已有 4×4 custom 单片**的 16 个控制点，并从当前来源重建这个实例的未烘焙像素。点按原有行优先顺序排列，坐标沿用来源像素域；不会改成父文档四角坐标，也不更改 Warp.Bounds、Trnf 或 nonAffineTransform。

投影沿用既有控制点包围域归一化规则，整体平移控制点的来源域不等于移动父文档中的实例；画布放置仍由四角决定。

```go
metadata, err := source.ReadLayerMetadata(ctx, layerIndex)
if err != nil {
	return err
}
// points 是从初始来源元数据读取并复制的现有16点；连续编辑时保留上一次目标值。
points := append([]psdmodel.Point(nil), metadata.Layer.Placed.Warp.MeshPoints...)
points[5].X += 0.25
if err := editor.SetSmartObjectWarp(ctx, id, points, psdkit.SmartWarpEditOptions{}); err != nil {
	return err
}
return editor.SavePSD(ctx, "edited-warp.psd")
```

首次可从 `source.ReadLayerMetadata` 获取原始控制点；来源文档是不可变的，不会反映编辑器尚未保存的修改。重开已保存文件后可从新文档读取更新后的控制点并继续编辑。仅更改原 mesh 数值的 float64 字节，描述符顺序、未知放置字段、尾部及未编辑实例保持；未知 Warp 语义仍拒绝，不会删除字段来取得支持。

严格的已认证仿射单片默认可编辑。需要数学 projective Warp 时，必须在**每次编辑**显式传入 `SmartWarpEditOptions{Render: &psdkit.SceneOptions{ApproximateCustomWarp: true}}`。外链也通过本次 `Render` 的 `ExternalResolver`／`ExternalRasterResolver` 显式映射；来源字体、滤镜与其他近似分别选择，不从前次调用继承。缺来源、折叠、自相交、退化、非有限坐标或预算不足都原子拒绝，解析回调在锁外执行，过期编辑返回 `ErrStaleLinkedEdit`。

父层蒙版、Fill、Opacity 和效果不会烘焙进新的实例像素；支持的当前来源和严格滤镜参与重建。颜色叠加保留到合成，受支持阴影须通过本次 `Render.Compose.ApproximateDropShadow` 显式确定所需来源范围，其余活动效果组合仍按范围拒绝。共享来源字节和其他实例不变；再替换来源时仍携带当前网格，并按当前来源尺寸缩放，恢复尺寸时也不会退回旧网格或坐标域。

普通受支持组合可直接保存当前已生成的实例像素，无需额外 `Pixels` 或 `CompositeImage`。这不新增或默认选择近似算法；重开后进行来源实时重绘或再次编辑仍须明确提供原策略。若自动保存中的活动效果需要重新解析外链、来源字体或额外近似，须先转为严格可重建的嵌入来源，或显式提供准确合成图；保存不会继承前一次 `Render`。

此单片路径只接受单一现代 SoLd／SoLE 的明确结构，不新增 Warp、不改点数、网格阶数或预设参数。均匀网格使用下节声明的范围；任意 quilt、非均匀切片、重复现代放置块及 legacy 镜像仍拒绝。已验证独立几何与像素参考、PSD/PSB 四种压缩两轮保存重开续编、连续编辑、来源替换、显式外链和近似门禁。

## 编辑 10×10／13×13 均匀网格

同一个 `SetSmartObjectWarp` 入口现支持**已有** 10×10／13×13 均匀 custom 网格，分别接收100／169个行优先控制点。每条坐标轴的点数必须与现有字段一致；只修改 float64 数值字节，保持 `deformNumRows`／`deformNumCols`、三次阶数、网格类型、切片位置、单位和原始 Prefix。不能用此入口增加点数、创建新网格或把不规则 quilt 改写成支持的结构。

```go
// points 从已有均匀网格读取并复制，保留原来的100或169点。
if err := editor.SetSmartObjectWarp(ctx, id, points, psdkit.SmartWarpEditOptions{
	Render: &psdkit.SceneOptions{ApproximateCustomWarp: true},
}); err != nil {
	return err
}
return editor.SavePSD(ctx, "edited-grid.psd")
```

网格来源投影是显式数学采样模型，**即使放置四角是仿射也必须每次开启 `ApproximateCustomWarp`**。该选项不放行透视网格、非均匀切片或折叠。切片内点须等分原始 `Warp.Bounds`，外边界接受原边界或成对 ±0.6 像素 guard，实际源窗口与全局控制域归一化保持；有效性继续经过整网格无折叠认证，不宣称与 Photoshop 完全逐像素一致。

网格已接入 `RenderSource`、`RenderSourcePlacedLayer`、`RenderSourceTile`／分块访问和编辑器候选来源渲染。编辑、同尺寸 `ReplaceContentsWithOptions` 和重开后的来源实时重绘分别显式选择近似；外链每次通过本次 `Render` 或场景选项的 resolver 映射，不沿用先前映射，也不回退实例旧缓存。父层蒙版、Fill／Opacity 和效果保留在后续合成，控制点与新的未烘焙像素原子提交。

普通受支持组合可直接保存本次已生成的实例像素；下一次编辑或来源实时重绘仍须再次显式选择策略。PSD/PSB 四压缩均验证了编辑、保存、重开和继续编辑；需要重新解析来源的活动效果仍遵守前述准确合成图边界。

同尺寸替换来源保留当前控制点与原始切片；**均匀网格改变来源尺寸暂不支持，原子拒绝**，不会把0.6像素 guard 随尺寸放大或丢弃当前编辑。4×4 单片已有的来源尺寸迁移保持不变。预算、取消、回调重入／过期、非法 schema、非有限点、折叠和无效网格均有失败原子性验证。

> [!WARNING]
> 受限的均匀网格具备正反映射、ROI 和预算控制，但不代表任意 quilt、其他预设或全部组合都能通过实时资格。`ApproximateCustomWarp` 等显式近似不会提升为 Photoshop 像素兼容。

## 编辑 Warp 预设控制参数

`Editor.SetSmartObjectWarpPreset(ctx, id, SmartWarpPresetParameters, SmartWarpEditOptions)` 修改**已有横向 `warpArcLower`** 的 bend 数值，并从当前嵌入来源或本次明确映射的外链重建该实例未烘焙像素。此入口保持预设类型和旋转方向，不把预设改写为 custom 网格。

```go
if err := editor.SetSmartObjectWarpPreset(ctx, id, psdkit.SmartWarpPresetParameters{
	Value: 25,
}, psdkit.SmartWarpEditOptions{
	Render: &psdkit.SceneOptions{ApproximateCustomWarp: true},
}); err != nil {
	return err
}
return editor.SavePSD(ctx, "edited-preset.psd")
```

当前范围要求 RGB8 父文档、明确的单一现代 SoLd／SoLE、横向 Arc Lower、`uOrder=4`／`vOrder=2`、仿射四角、零透视参数及像素单位来源边界。原生 `Sz` 尺寸对象须唯一且宽高各出现一次，边界的四条边也各出现一次。原描述符须有 `doub` 编码的 bend 数值，目标值有限且绝对值小于100，并通过无折叠认证；该数值边界不表示所有来源比例和负 bend 都有效。其他预设、竖向旋转、透视、quilt、自定义 envelope、缺失或重复尺寸／边界以及未知 Warp 语义仍原子拒绝。

预设像素使用显式数学采样模型，**包括 bend 为0时，每次编辑也必须开启 `ApproximateCustomWarp`**。外链每次通过 `Render.ExternalResolver`／`ExternalRasterResolver` 明确映射；近似和映射不从前次调用继承。独立 Arc Lower 控制几何不等于 Photoshop 原生逐像素采样；需要单独进行显式投影时，可在 `CompilePresetGeometry` 的结果上调用 `ProjectMathematicalWithOptions`。

成功时只修改已有 bend 数值字节，保留预设类型、旋转方向、阶数、来源边界、放置四角、未知放置描述符字段及来源原字节；其他共享实例不变。来源滤镜沿用已有严格子集或本次显式选择的近似，父蒙版、Fill／Opacity 和效果保留到后续合成。普通受支持组合可直接保存新生成的实例像素，无需额外 `Pixels` 或 `CompositeImage`，保存也无需再次开启 Warp 近似；PSD/PSB 重开后可再次显式选择策略并继续编辑。活动效果的准确合成图边界与前述控制点编辑一致。

当前预设的来源替换仍原子拒绝，不会丢失新 bend 或退回原参数。参数编辑与来源实时重绘均限上述已认证几何，不代表任意预设或全部组合已经支持。

## 预设的整图与区域来源重绘

已有横向 Arc Lower 现已接入 `RenderSource`、`RenderSourcePlacedLayer`、`RenderSourceTile` 和 `VisitSourceTiles`。整图、绝对坐标区域、分块访问与单实例输出使用同一几何及采样相位，从当前嵌入来源或本次明确映射的外链生成像素；不会用旧实例栅格或保存合成图补画。

```go
pixels, err := psdkit.RenderSource(ctx, source, psdkit.SceneOptions{
	ApproximateCustomWarp: true,
})
if err != nil {
	return err
}
region, err := psdkit.RenderSourceTile(ctx, source, image.Rect(20, 20, 120, 80), psdkit.TileOptions{
	ApproximateCustomWarp: true,
})
```

`SceneOptions` 与 `TileOptions` 的 `ExternalResolver`／`ExternalRasterResolver` 分别解析外链 PSD/PSB 或栅格来源，每次调用都须重新明确提供。分块遍历按每个 tile 解析来源，映射取自本次遍历选项，不跨 tile 保留完整来源缓存。**包括 bend 为0，来源实时重绘也始终显式开启 `ApproximateCustomWarp`**；编辑参数时的 `Render` 不会成为重开后或下一次渲染的默认策略。来源字体、滤镜及效果的额外近似仍分别选择。

来源重绘限横向 `warpArcLower`、4×2、仿射、零透视、有限且绝对值小于100并通过无折叠认证的几何。原描述符须明确且无重复地提供尺寸双轴、边界四边、变换与 Warp 字段；缺失或重复不能通过最后一个值覆盖来取得支持。其他预设、竖向旋转、透视预设、quilt、缺少策略或外链映射仍返回错误，不回退旧像素。父蒙版、Fill／Opacity 和效果保留在后续合成，原生预设结构与共享来源不变；预算、取消和来源解析错误保持原有失败边界。

参数编辑、PSD/PSB 保存、重开后的来源重绘及再次编辑共用同一数学模型；普通受支持组合可直接保存已生成实例像素，仍不需要额外 `Pixels` 或 `CompositeImage`。这一范围不开放预设来源替换，也不声明 Photoshop 原生逐像素一致。

## 在整图重绘中使用

`RenderSource` 与 `RenderSourcePlacedLayer` 会在资格检查通过时自动应用支持的变换；自定义 Warp 与上述横向 Arc Lower 预设的数学近似通过 `SceneOptions{ApproximateCustomWarp: true}` 逐次显式开启。

浏览器 Playground 的“智能对象与 Warp”分组提供 `warp.*` 入口，可以导入自己的图片，对文档中的智能对象执行投影。

```sh
go run ./examples/placed-layer -help
```
