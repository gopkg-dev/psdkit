---
title: 效果、调整与填充
description: 图层效果、调整图层、矢量蒙版与纯色 / 渐变 / 图案填充的解析、编辑与实时重绘范围。
---
# 效果、调整与填充

这一组图层在文件中以参数形式保存，psdkit 可以完整解析它们的参数，并在声明范围内实时重绘。范围之外的组合返回 `unsupported`；需要近似时由调用方显式开启。

## 图层效果

| 能力 | 入口 | 范围 |
| --- | --- | --- |
| 解析 | `source.DecodeEffects` | 全部效果描述符 |
| 编辑 | `Editor.SetEffects` / `SetEffectsWithOptions` | 受支持的效果参数 |
| 光栅化 | `psdeffects.Rasterize`、`EstimateWorking`、`Validate` | 颜色叠加、受限阴影与发光 |
| 近似 | `ApproximateDropShadow` | 显式开启的投影近似 |

尚未完成：全部效果、轮廓（Contour）、Noise 与复杂组合。

## 调整图层

```go
adjusted, err := psdadjust.Apply(ctx, pixels, adjustment)
if err != nil {
	return err
}
```

| 能力 | 入口 |
| --- | --- |
| 解析 / 编辑 | `source.DecodeAdjustment`、`Editor.SetAdjustment` |
| 执行 | `psdadjust.Apply`、`psdadjust.Compile` |
| 支持类型 | Invert、Threshold、Posterize、Levels、Curves、Exposure、受控 Channel Mixer |

- **PhotoFilter**：支持参数读写，密度为 0 时恒等渲染；非零密度的实时路径仍拒绝。
- **Color Balance**：非中性曲线未完成。
- **Hue/Saturation**：近似实现需要 `ApproximateHueSaturation` 显式开启。

## 矢量与填充

| 能力 | 入口 |
| --- | --- |
| 矢量路径 | `source.DecodeVectorMask` / `DecodePath`，`psdvector.Rasterize`，`EncodeVectorMask` |
| 纯色 | `Editor.SetSolidFill` |
| 渐变 | `source.DecodeGradientFill`、`psdgradient.Render`（Classic 渐变）、`Editor.SetGradientFill` |
| 图案 | `source.DecodePatternFill` / `DecodePattern`、`psdpattern.Render`、`Editor.SetPatternFill` |
| 矢量蒙版编辑 | `Editor.SetVectorMask` |

`psdvector` 保留 typed 路径、未知记录和受控编码，提供有限光栅化；描边近似通过 `ApproximateVectorStroke` 显式开启。尚未完成：全部组合、描边、羽化与采样精度。

> [!TIP]
> 在 [Playground](../demo/) 的“图层效果与调整”和“矢量与填充”分组里，可以对导入的 PSD 直接调用这些解析入口，查看真实返回的参数结构。
