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
| 编辑器视图 | `editor.SmartObjects`，包含当前 `FileType`、嵌入状态和共享实例 |
| 当前来源原字节 | `editor.ReadSmartObjectContents`，读取原始嵌入内容、编辑覆盖层或显式解析的外链 |
| 可编辑来源文档 | `editor.OpenSmartObjectSource`，限 PSD/PSB；PNG/JPEG/TIFF 使用原字节读取接口 |

## 替换内容

```go
replacement, err := os.ReadFile("new-content.png")
if err != nil {
	return err
}
if err := editor.ReplaceContents(ctx, layerID, replacement); err != nil {
	return err // 未支持的算法、Warp 或来源组合会被拒绝
}
```

- `ReplaceContents` 支持 RGB8 PSD/PSB、PNG、JPEG 和受支持的扁平 RGB8 TIFF 来源，并更新所有共享该来源的现存实例。传入的编码字节原样保存，不会把 PNG/JPEG/TIFF 自动转成 PSD；来源记录的 `FileType` 同步更新。
- 原始文档与调用方输入保持不可变。新来源、实例像素和尺寸元数据一起提交；任意共享实例不支持时整次操作失败。
- 实例带有受支持的智能滤镜时，从新来源按各自当前变换、滤镜栈和原滤镜蒙版重算一次。尚未保存的滤镜参数保持，不重复滤镜，也不把父图层蒙版、效果、Fill 或透明度烘焙进缓存。
- 原型画布尺寸变化时保持目标四角不动；Photoshop 默认的“替换内容”则保留像素比例与中心点，两者行为不同。
- `ReplaceSmartObjectContents` 是 `ReplaceContents` 的显式别名，两者都会替换共享来源；`ReplaceSmartObjectContentsWithOptions` 对应带选项的入口。

保存父文档时，PSD/PSB writer 同时写出来源记录、更新后的实例栅格和合成预览。重新打开后，可以用 `SmartObjects` 检查来源类型，用 `ReadSmartObjectContents` 取回编码字节；PSD/PSB 来源仍可通过 `OpenSmartObjectSource` 继续编辑。

扁平 TIFF 来源支持 classic、条带式 RGB8 / RGBA8，保留大小端、ICC 及原始编码字节；压缩包括未压缩、PackBits、LZW（标签 5）和 Deflate（标签 8 / 32946），LZW / Deflate 均支持 Predictor 2 水平差分。瓦片式、平面分离、高位深或其他未支持的 TIFF 组织仍拒绝，不回退到私有预览缓存。

LZW 按 [TIFF 6.0 第 13–14 节](https://www.itu.int/itudoc/itu-t/com16/tiff-fx/docs/tiff6.pdf) 使用 MSB-first、9–12 位字典和规定的提前位宽切换；每条带单独清表并验证 EOI。字典工作区受预算限制，截断、越界引用、多余输出与尾随字节均拒绝。已用独立 LibTIFF 编码样本验证，并覆盖替换、外链转嵌入、保留外链更新及 PSD/PSB 保存重开后的实时重绘。

## 修改单个实例的变换

`SetSmartObjectTransform` 修改单个实例的目标四角，重绘该实例栅格并支持 PSD/PSB 保存重开，其他共享实例和来源原字节不变。带选项的 `SetSmartObjectTransformWithOptions` 支持显式外链解析及投影策略。当前要求 RGB8 父文档、受支持来源且没有活动 Warp，详见[变换编辑](warp.md#编辑单个智能对象的变换)。

## 显式渲染策略

`ReplaceContentsWithOptions`、`ReplaceSmartObjectContentsWithOptions`、`EmbedLinkedContentsWithOptions` 和 `UpdateLinkedContentsWithOptions` 接受同一种 `SmartContentsOptions`。其中 `ApproximateCustomWarp` 选择经几何证明的数学 Warp 模型，`AssumeUntaggedSRGB` 为无配置来源提供明确的 sRGB 色彩解释；`SmartFilters` 为来源替换后的各实例滤镜重算提供算法与预算选项。

默认入口和零值选项会自动执行已有的严格滤镜子集，包括恒等空间滤镜和纯 Master Lightness；不会自动开启近似。非零高斯等需每次显式选择，重开后继续替换也一样：

```go
err := editor.ReplaceContentsWithOptions(ctx, layerID, replacement, psdkit.SmartContentsOptions{
	SmartFilters: &psdkit.SmartFilterOptions{ApproximateGaussian: true},
})
if err != nil {
	return err
}
return editor.Save(ctx, "replaced.psd", psdkit.SaveOptions{})
```

来源编码字节、当前参数和新缓存在所有共享实例成功后一起提交；某个实例的算法、投影、相对蒙版坐标、预算或取消验证失败，整次操作保持原状。外链解析仍在编辑器锁外执行，调用时复制的滤镜策略不会被解析回调改变。未支持的 Warp、滤镜算法、源格式或色彩组合返回 `unsupported`；示例成功不代表任意 Photoshop 智能对象都能替换。

重新打开后可检查保存预览、继续替换来源或修改滤镜。独立实时重绘仍须通过 `SceneOptions.SmartFilters` 显式传入算法策略，详见[智能滤镜](filters.md)；来源替换入口不会替其他调用保存近似许可。

这些编辑入口当前要求 RGB8 父文档，PSD/PSB 替换来源也须为 RGB8。PNG/JPEG/TIFF 的编码原字节保持不变，父文档中的实例栅格使用 RGB8；提供保存合成图不会绕过编辑阶段的位深或投影限制。

## 图片转可编辑 PSD

需要把图片变成可编辑的嵌套 PSD 时，先调用 `BuildRasterContents`。直接传入 PNG/JPEG/TIFF 只替换来源，不会创建 PSD 图层。

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

| 操作 | 来源与保存行为 |
| --- | --- |
| `EmbedLinkedContents` | 将显式解析到的外链转成嵌入来源，并更新全部共享实例；保存重开后不再需要 resolver |
| `UpdateLinkedContents` | 保留外链身份和路径元数据，更新来源文件大小、可选时间戳与共享实例；已有非空缓存更新，原本为空的缓存继续为空 |
| `ReadSmartObjectContents` | 优先读取当前编辑覆盖层；未编辑的外链必须显式解析，不把旧缓存当作当前外部文件 |

```go
resolve := func(ctx context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
	return replacement, ctx.Err() // replacement 来自调用方明确选择的文件
}
if err := editor.EmbedLinkedContentsWithOptions(ctx, layerID, resolve,
	psdedit.SmartContentsOptions{AssumeUntaggedSRGB: true}); err != nil {
	return err
}
```

保留外链时使用 `LinkedContentsUpdateResolver`，返回 `LinkedContentsUpdate{Data: replacement}`；可选 `Timestamp` 只修改明确提供的时间戳。父 PSD/PSB 的保存与外部文件的写入是两次独立操作，后者由调用方负责。保存并重开后，仍需提供当前外部文件的显式映射，才能读取外链来源。

同一外链容器内的其他记录与已有嵌入来源会保留，支持在全局或同一图层内的容器中逐个转为嵌入。删除存放来源记录的图层后，保存会将其来源记录保留到全局，避免其他共享实例失去来源。

解析器在编辑器锁外执行；解析期间文档发生变化时返回 `ErrStaleLinkedEdit`，不会提交部分替换。重复或不明确的来源身份、非活动层集等不支持的结构会被拒绝。

## 实时重绘

`RenderSourcePlacedLayer` 从嵌入来源或显式解析的外链重建单个智能对象图层，结果保留放置、蒙版和效果，供后续合成；`RenderSource` 在整图重绘时递归处理受支持的 PSD/PSB/PNG/JPEG/TIFF 来源。变形部分见 [Warp 变形](warp.md)。

已有 4×4 custom 单片和 10×10／13×13 均匀网格可用 `SetSmartObjectWarp` 编辑控制点，更新该实例的未烘焙像素，保存重开后继续编辑；共享来源和其他实例保持。均匀网格每次编辑、来源替换和实时重绘均须显式开启 `ApproximateCustomWarp`，外链每次明确映射。4×4 保留已有来源尺寸缩放；10×10／13×13 仅支持同尺寸来源替换，改变尺寸原子拒绝。任意 quilt 仍未开放，详见[Warp 控制点编辑](warp.md)。

已有横向 `warpArcLower` 的 bend 可用 `SetSmartObjectWarpPreset` 编辑，整图、区域、分块访问与单实例也可从实际来源实时重绘；每次显式开启 `ApproximateCustomWarp` 并映射外链，包括 bend 为0。参数与新实例像素一起提交，普通受支持组合可直接保存 PSD/PSB，重开后从来源重绘并继续编辑。范围限已认证的仿射、零透视、4×2 预设几何；来源替换仍原子拒绝，其他预设和旋转方向未开放。详见[预设参数编辑](warp.md#编辑-warp-预设控制参数)与[整图／区域来源重绘](warp.md#预设的整图与区域来源重绘)。

RGB8 智能对象的当前实时来源现可与纯阴影、Normal 阴影＋颜色叠加组合，通过 `SetEffectsWithOptions` 编辑并逐次显式开启 `ApproximateDropShadow`。来源替换后使用当前字节与变换，各共享实例保留独立效果；父层像素蒙版、Fill／Opacity 不烘焙进投影缓存。活动矢量蒙版及高级效果组合仍拒绝。

`EffectsEditOptions.Render` 只提供本次效果编辑的来源策略；外链映射、来源滤镜／Warp／文字近似与字体均独立选择，父阴影近似不会自动传给递归来源。自动 `Composite` 和保存重新严格计算当前嵌入来源；外链须先显式转嵌入，或保存时提供准确 `CompositeImage`／`Composite`，需要额外来源近似或字体时同样如此。保存重开后继续编辑仍须再次明确提供策略。完整边界见[智能对象当前来源与效果](effects.md#智能对象当前来源与效果)。

`RenderArtboard` / `RenderArtboards` 也支持画板内智能对象的独立实时来源，保留该画板背景和裁剪。通过 `ArtboardRenderOptions.Render` 提供外链解析器、来源预算及显式近似策略；不会读取其他未选择画板的来源或用实例旧像素替代。保存重开后仍须显式映射外链，完整调用见[画板重绘](rendering.md#独立画板中的智能对象)。

外链 PSD/PSB 通过 `SceneOptions.ExternalResolver` 返回文档；外链 PNG/JPEG/扁平 TIFF 可通过新增的 `ExternalRasterResolver` 直接返回编码字节，无需先转换成 PSD。`TileOptions` 提供同名字段，区域、分块、嵌套来源和单个实例重绘使用相同策略。

```go
pixels, err := psdkit.RenderSource(ctx, source, psdkit.SceneOptions{
	ExternalRasterResolver: func(ctx context.Context, record psdkit.LinkedFileRecord) ([]byte, error) {
		data, ok := selectedContents[record.ID] // 由调用方明确选择的来源映射
		if !ok {
			return nil, fmt.Errorf("未选择此外链来源")
		}
		return data, ctx.Err()
	},
})
```

回调返回的字节在本次渲染结束前须保持不变；输出像素独立持有。来源编码须匹配记录中的 PNGf/JPEG/TIFF 类型，并通过源文件大小、像素、解码和工作内存限制。ICC 转换沿用现有显式策略；JPEG 不承诺与 Photoshop 私有解码器逐像素一致。

单次整图、单区域或单实例重绘中，同一父文档内共享实例复用已解析来源；不同父文档的同名 ID 分别解析。`VisitSourceTiles` 按每个 tile 解析，以保留有界流式输出；所有 tile 使用本次调用明确提供的映射，不跨调用继承。提供栅格 resolver 后，其错误不会回退到旧缓存或文档 resolver；未提供它时，仍兼容原有 `ExternalResolver` 返回文档的方式。

`UpdateLinkedContents` 编辑、保存并重开后，可继续提供当前外部字节实时重绘，再修改内容、实例四角或不透明度并保存。外链路径与来源身份继续保留；父文档内旧缓存不充当当前外部文件。

```sh
go run ./examples/replace-contents -help
go run ./examples/placed-layer -help
```
