---
title: 智能对象与外链
description: 读取智能对象来源、替换内容、处理嵌入与外链，以及图片转可编辑 PSD。
---
# 智能对象与外链

psdkit 区分三样东西：**父图层保存的栅格**、**智能对象的真实来源**和**实时重绘结果**。替换内容时修改的是真实来源，并同步更新共享该来源的受支持实例。

## 读取来源

| 范围 | 入口 |
| --- | --- |
| 来源记录 | `source.LinkedFiles`、`ReadLinkedFileView`，linked-file / placed 记录及 typed 元数据 |
| 嵌入文档 | `OpenLinkedDocument`（PSD/PSB）、`OpenLinkedTIFFLayers`（TIFF） |
| 嵌入图片 | `DecodeLinkedRaster`、`LinkedRasterFormat` |
| 编辑器视图 | `editor.SmartObjects`、`editor.OpenSmartObjectSource` |

## 替换内容

```go
replacement, err := os.ReadFile("new-content.psd")
if err != nil {
	return err
}
if err := editor.ReplaceContents(ctx, layerID, replacement); err != nil {
	return err // 不支持的 Warp、实时滤镜等会被拒绝
}
```

- `ReplaceContents` 替换嵌入的 RGB8 PSD/PSB 来源，并更新所有共享实例。原始块保持不可变，新内容进入编辑覆盖层。
- 原型画布尺寸变化时保持目标四角不动；Photoshop 默认的“替换内容”则保留像素比例与中心点，两者行为不同。
- `ReplaceSmartObjectContents` 处理共享来源；`EmbedLinkedContents` 把外链转为嵌入；`UpdateLinkedContents` 保留外链并更新内容。

## 图片转可编辑 PSD

`ReplaceContentsWithOptions` 提供显式的 `SmartContentsOptions`。其中 `ApproximateCustomWarp`
选择经几何证明的数学 Warp 模型，`AssumeUntaggedSRGB` 为无配置的印花提供明确色彩解释。
默认 `ReplaceContents` 及零值选项保持严格策略，示例成功不代表任意 Warp 均受支持。

```go
data, err := psdkit.BuildRasterContents(ctx, pixels) // 不裁剪、不修改样本
if err != nil {
	return err
}
if err := editor.ReplaceContents(ctx, layerID, data); err != nil {
	return err
}
```

`RasterContentsWorkingBytes` 预先估算所需内存。

## 外链

外链只通过调用方提供的 resolver 解析。psdkit **不会**把 PSD 中记录的路径当作读取本机文件或下载 URL 的授权。浏览器 SDK 用已导入资产的显式映射代替系统路径，见[浏览器 SDK](../browser.md)。

## 实时重绘

`RenderSourcePlacedLayer` 从嵌入来源重建单个智能对象图层，结果保留放置、蒙版和效果，供后续合成；`RenderSource` 在整图重绘时递归处理受支持的嵌入 PSD/PSB/PNG/JPEG 来源。变形部分见 [Warp 变形](warp.md)。

PNG/JPEG/TIFF 和嵌套 PSD/PSB 的支持依具体路径决定；色彩、尺寸、位深和透明边界都会保留。

```sh
go run ./examples/replace-contents -help
go run ./examples/placed-layer -help
```
