---
title: PSD/PSB 与按需读取
description: 打开 PSD/PSB 原始文档、读取头信息与图层结构，按通道、区域和原位深解码像素。
---
# PSD/PSB 与按需读取

`SourceDocument` 是原始文档索引：打开时只解析结构，保留资源、未知块、重复字段、签名、padding、层集和通道区间，**不主动解码像素**。需要像素时再按图层、通道或区域显式解码，并保持原位深。

## 打开文档

```go
source, err := psdkit.OpenDocument("input.psb")
if err != nil {
	return err
}
defer func() { _ = source.Close() }()

h := source.Header
fmt.Println(h.Width, h.Height, h.Depth, h.ColorMode, h.Version) // Version 1 = PSD，2 = PSB
```

放在返回 `error` 的函数中，导入 `fmt` 和 `github.com/gopkg-dev/psdkit`。

| 入口 | 所有权与用途 |
| --- | --- |
| `OpenDocument(path)` | 拥有打开的文件，用完 `Close` |
| `ParseDocument(data)` | 借用字节切片，使用期间不得修改 |
| `ReadDocument(readerAt, size)` | 借用 `io.ReaderAt`，不会关闭调用方资源 |
| `LoadOptions{Limits: …}` | 三个入口都接受，控制索引与解码预算 |

## 读取结构与元数据

| 方法 | 说明 |
| --- | --- |
| `LayerMetadata` / `ReadLayerMetadata` | 单个图层的语义元数据（名称、混合、蒙版、placed、文字等） |
| `Artboards`、`LinkedFiles`、`Patterns`、`FilterCaches` | 画板、智能对象来源、图案、滤镜缓存记录 |
| `DecodeDescriptor` / `EncodeDescriptor` | 有序 typed Action Descriptor，保留重复键和原始区间 |
| `ReadSpan` / `CopySpan` / `CopySpanBuffer` | 有界复制任意原始区间 |
| `IndexMetadataBytes` | 当前索引占用的元数据内存 |

公开的源元数据保持只读；需要修改时使用 [`Editor`](editing.md)。

## 按需解码像素

```go
plane, err := source.DecodeChannel(ctx, 0, 0) // 图层 0 的第 0 个通道
if err != nil {
	return err
}
fmt.Println(plane.Width, plane.Height, len(plane.Data))
```

| 方法 | 结果 |
| --- | --- |
| `DecodeChannel` / `DecodeChannelRegion` | 单个图层通道，可只解码一个矩形区域 |
| `DecodeLayer` | 一个图层的全部通道（原位深） |
| `DecodeComposite` / `DecodeCompositeChannelRegion` | 文件内保存的合成通道 |
| `Decode` | RGB8 兼容快照，用于 `Composite` 等 RGB8 入口 |

读取前先检查图层和通道索引是否在范围内。`psdfile.NewRegionCache` 提供有界 LRU 区域缓存，适合反复读取同一区域。

## 原位深样本格式

`Plane.Data` 是独立的样本缓冲：

- **1 位**：按行 MSB-first 打包；
- **8 位**：每样本一个字节；
- **16 / 32 位**：大端字节序。32 位浮点保留 NaN、负零等位型。

这些入口**不会**自动做色彩转换、量化或去白 matte。显示转换见 [ICC 与位深](color.md)。

> [!WARNING]
> 文件内保存的合成图可能是 Photoshop 留下的旧缓存，甚至是占位图，不能当作实时重绘结果。需要反映图层当前状态时使用[实时重绘](rendering.md)。

## 生命周期

- 关闭源文档会等待在途操作完成；已经返回的独立像素仍可继续使用。
- `ParseDocument` / `ReadDocument` 借用的数据在源文档关闭前不得修改。
- 预算通过 `LoadOptions.Limits`、`psdkit.DefaultSourceLimits()` 和各操作的选项控制，详见[预算、取消与错误](limits.md)。
