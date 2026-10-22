---
title: ICC 与位深
description: 读取原位深样本、按 ICC 做显示转换、受限的 16/32 位合成，以及所有需要显式选择的色彩策略。
---
# ICC 与位深

psdkit 的原则是**不悄悄改变样本**：读取保持原位深与浮点位型，不自动做色彩转换、量化或去白 matte。显示转换、降位和 HDR 处理都由调用方显式选择。

## 位深

| 位深 | 读取 | 合成 |
| --- | --- | --- |
| 1 位 | 按行 MSB-first 打包 | 通过 RGB8 快照 |
| 8 位 | 字节样本 | RGB8 核心合成（完整支持范围） |
| 16 位 | 大端字节 | `RenderSourcePrecision` 有限合成、区域与分块 |
| 32 位浮点 | 大端字节，保留 NaN / 负零 | 同上，非有限值策略显式选择 |

```go
float, err := psdkit.RenderSourcePrecision(ctx, source)
if err != nil {
	return err // 高位深的高级图层与全混合尚未完成
}
// float.Pix 为直通 RGBA float64 样本，SourceDepth / ICCProfile 记录来源
fmt.Println(float.Width, float.Height, float.SourceDepth)
```

`RenderSourcePrecision` 只接受 16/32 位 RGB 或灰度文档，8 位文档返回 `unsupported`，请使用 RGB8 入口。`RenderSourcePrecisionTile` 和 `VisitSourcePrecisionTiles` 按区域 / 分块计算；`PrecisionOptions` 默认 256 MiB 工作内存、两千万画布像素、一万个图层和 64 层嵌套。浏览器 SDK 把大浮点数组作为小端 `.f64le` 二进制输出，不自动量化 HDR。

## ICC 与显示转换

| 入口 | 说明 |
| --- | --- |
| `ReadSourceRGBProfile` | 读取文档内原始 ICC |
| `DecodeDisplayComposite` + `DisplayOptions` | 保存合成图按源配置文件显示转换 |
| `ConvertSamples` | 把源精度样本转为显示用 NRGBA |
| `TransformProfile` / `ProfileTransformWorkingBytes` | RGB8 图像在两个 ICC 之间转换及预算 |

支持 RGB matrix/TRC 配置文件与部分 LUT / CMYK。渲染意图默认为相对比色，绝对比色未实现；提供了却不支持的配置文件**不会被静默忽略**，而是报错。

## 需要显式选择的策略

| 策略 | 选项 |
| --- | --- |
| 忽略源配置文件 | `DisplayOptions.IgnoreSourceProfile`，或传入空的非 nil `Color.ICCProfile` |
| 无管理 CMYK 近似回退 | `ColorOptions.AllowUnmanagedCMYK`（仅在未提供 ICC 时生效） |
| 非有限值与色调映射 | `ColorOptions.NonFinite`、`ColorOptions.ToneMap` |
| 忽略透明度 | `DisplayOptions.IgnoreTransparency` |

```sh
go run ./examples/color -psd /tmp/new.psd -out /tmp/display.png
go run ./examples/precision -help
```

> [!NOTE]
> 色彩显示转换成功，不代表高位深的高级图层可以实时合成。完整边界见[能力矩阵](../status.md)。
