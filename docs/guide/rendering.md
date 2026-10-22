---
title: 合成、重绘与导出
description: 区分保存预览与实时重绘，按整图、图层组、区域、分块和画板输出 PNG/JPEG。
---
# 合成、重绘与导出

psdkit 提供三类画面来源，它们回答的是不同的问题：

| 来源 | 入口 | 适用 |
| --- | --- | --- |
| **保存预览** | `DecodeDisplayComposite` / `DecodeDisplayRegion` | 快速显示文件内保存的合成图；可能是旧缓存 |
| **RGB8 图层合成** | `Composite` / `CompositeRegion`、`Editor.Composite` | 已解码的 RGB8 图层树，或编辑后的文档 |
| **来源实时重绘** | `RenderSource` / `RenderSourceTile` / `VisitSourceTiles` | 从原始文档重建，包括嵌入的智能对象内容 |

## 支持范围

RGB8 核心合成覆盖支持范围内的组、剪贴、26 种确定性混合模式、Opacity/Fill、像素蒙版、部分 Knockout、通道限制和 Blend If。具体图层和组合仍需要通过资格检查，超出范围返回包装了 `ErrUnsupported` 的错误。

```go
pixels, err := psdkit.RenderSource(ctx, source)
if errors.Is(err, psdkit.ErrUnsupported) {
	// 回退到保存预览，并在界面上标明来源
	pixels, err = psdkit.DecodeDisplayComposite(ctx, source)
}
if err != nil {
	return err
}
```

> [!IMPORTANT]
> 回退到保存预览时，请在产品界面上标明画面来源。保存预览不反映编辑，也可能与当前图层不一致。

## 独立图层组合成

`editor.CompositeGroup(ctx, groupID)` 在透明背景上独立合成指定图层组，保留组自身的蒙版、透明度和子图层顺序，
不包含组外的祖先与兄弟图层。输出自动裁去透明边框，返回矩形仍保留文档坐标；空组返回 1×1 透明图。
调用不修改编辑或保存修订，并沿用整图合成的资格检查、预算与取消机制。

## 区域、分块与画板

| 目标 | 入口 |
| --- | --- |
| 实时区域 | `RenderSourceTile(ctx, source, bounds)` |
| 分块遍历 | `VisitSourceTiles(ctx, source, bounds, size, visit)`，逐块返回独立像素 |
| 高位深受限合成 | `RenderSourcePrecision` / `RenderSourcePrecisionTile` / `VisitSourcePrecisionTiles` |
| 单个智能对象 | `RenderSourcePlacedLayer` |
| 画板 | `RenderArtboard` / `RenderArtboards`，受支持的顶层 RGB8 画板 |
| 模板套图 | `NewRenderer` / `Inspect` / `examples/render` |

区域坐标、通道局部矩形、图层 `Bounds` 与画板裁剪可能位于不同坐标域，请按类型注释传入。

## 显式近似

少数高级特性只能近似重绘，需要调用方在 `SceneOptions` 或对应选项中明确开启：

| 选项 | 范围 |
| --- | --- |
| `ApproximateVectorStroke` | 原始矢量路径的有界实心内描边 |
| `ApproximateCustomWarp` | 自定义 Warp 的数学投影 |
| `ApproximatePlacedProjection` | 透明边框透视放置的全画布双线性采样 |
| `ApproximateText` / `ApproximateOpenTypeText` | 显式字体的拉丁文字渲染 |
| `SmartFilters` | 选择实时滤镜算法与近似策略 |

这些选项只代表各自声明的范围，**不能为了得到画面而全部打开**。近似结果不等于 Photoshop 像素兼容。

## 编码与输出

原生 Go 的 `psdcompose.Render` / `RenderRegion` 将像素合成划分为 32 行的小任务，默认最多使用 4 个 Go 处理器；`Options.Workers: 1` 可强制串行。并行任务写入独立行块，共享只读准备数据，工作内存不足时减少任务数。像素公式、分辨率和采样精度保持不变。`js/wasm` 保持单线程，goroutine 不会带来浏览器多核加速。

浏览器 SDK 仅需要 PNG 时，可以使用 `engine.call('editor.Composite', { target: editor, args: [{}], imageOutput: 'png' })`，省去重复的原始 RGBA 传输。默认仍同时返回 PNG 与 RGBA，PNG 始终采用无损编码。

```go
file, err := os.Create("out.png")
if err != nil {
	return err
}
defer func() { err = errors.Join(err, file.Close()) }()
return psdkit.Encode(file, pixels, "png") // 或 "jpeg"，质量 95
```

- JPEG 不保留透明度，会铺白；请明确业务的背景策略。
- `PrepareOutput` 负责尺寸与裁片，`ValidateOptions` 先校验参数。
- 原始通道与高位深结果不要隐式转成 RGB8。
- 渲染器使用期间，输入图像保持只读；需要修改时先复制。

```sh
go run ./examples/create -out /tmp/new.psd
go run ./examples/composite -psd /tmp/new.psd -out /tmp/composite.png
go run ./examples/scene -help
```
