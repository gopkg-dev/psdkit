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
| 光栅化 | `psdeffects.Rasterize`、`EstimateWorking`、`Validate` | 颜色叠加、受限阴影、发光与描边 |
| 近似 | `ApproximateDropShadow` | 显式开启的投影近似 |

尚未完成：全部效果、轮廓（Contour）、活动 Noise/Jitter 与复杂组合。

已禁用的像素蒙版和矢量蒙版不参与覆盖计算，因此不再阻断上述受支持效果的实时重绘。可用 `SetPixelMask` / `SetVectorMask` 禁用蒙版，再自动保存和重开；蒙版参数与采样仍保留。重新启用后，活动蒙版与效果的组合继续按原有资格检查，超出范围返回 `unsupported`。显式投影和智能对象颜色叠加路径也接受禁用矢量蒙版，仍需开启各自近似选项。

普通 RGB8 Normal 栅格层的活动蒙版新增以下组合，均可编辑已有效果、自动保存 PSD/PSB、重新打开并继续编辑：

| 组合 | 调用与边界 |
| --- | --- |
| 颜色叠加＋活动矢量蒙版 | `SetEffects`；矢量蒙版不羽化，支持其已有密度、反相和受支持路径 |
| 颜色叠加＋活动像素／矢量双蒙版 | `SetEffects`；两个蒙版均不羽化，覆盖率相乘；原有蒙版参数、样本和路径保留 |
| 颜色叠加＋投影＋活动像素蒙版 | `SetEffectsWithOptions`，显式开启 `ApproximateDropShadow`；支持多个已有颜色叠加与投影，投影限 Normal / Multiply，颜色叠加使用已支持的混合模式 |
| 投影（可含颜色叠加）＋活动矢量／双蒙版 | `SetEffectsWithOptions`，显式开启 `ApproximateDropShadow`；普通 Normal 栅格，两个活动蒙版均不羽化，保留密度、反相、链接状态和已有路径 |

纯颜色叠加没有外部效果区域，蒙版“隐藏效果”控制在上述范围内得到相同的覆盖结果。阴影组合则分别遵循像素蒙版的 `lmgm` 和矢量蒙版的 `vmgm`：先塑形时影响图层和投影来源，隐藏效果时作用于最终图层与效果结果。双蒙版可以分别选择阶段：塑形覆盖率相乘后统一量化一次，最终遮罩覆盖率相乘后做一次预乘透明度插值，避免重复应用蒙版。保留 Fill、Opacity、Blend Interior、Layer Conceals 及效果顺序。

`lmgm`／`vmgm` 的显式值优先；缺省时按各蒙版的原始链接状态，未链接的蒙版作用于最终结果。矢量蒙版对应 `NotLinked`。这一缺省规则来自 [Adobe PSD 格式规范的全局蒙版定义](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/)，阴影采样仍是显式近似模型，不承诺 Photoshop 逐像素一致。区域重绘包含输出区域外、负坐标及画布外的必要矢量塑形覆盖，不会用文件内旧阴影缓存补画。

```go
// effects 为该图层已有的效果集合，仅修改允许编辑的参数。
err := editor.SetEffectsWithOptions(ctx, id, effects, psdkit.EffectsEditOptions{
	ApproximateDropShadow: true,
})
if err != nil {
	return err
}
return editor.Save(ctx, "effects.psd", psdkit.SaveOptions{ApproximateDropShadow: true})
```

阴影近似在编辑、`Composite` 和保存时分别显式选择，重开后继续编辑也需再次选择。不开启时沿用原来的严格效果范围。矢量蒙版阴影及新增混合效果编辑限普通 Normal 栅格来源，不支持智能对象、组效果、剪贴、Blend If、Knockout 或通道限制；矢量蒙版参与阴影时，活动像素蒙版和矢量蒙版均须无羽化。来源、参数、预算或取消验证失败时不会提交部分效果更新。

已知效果的不透明度为 0 时，可保留合法的 Noise/Jitter（0–100%），无需执行噪点采样。可用 `SetEffects` 将已有噪点效果的不透明度改为 0，随后实时重绘、自动保存和重开；恢复非零不透明度仍拒绝。未知字段、重复或错误单位的 Opacity、非法参数及其他效果限制不会因不透明度为 0 而放行。

当效果集合的主开关或文档效果可见性明确关闭，且集合级语义无歧义时，原本受支持的 pass-through 组 Blend If 可与其共存，覆盖组自身和子图层上的关闭效果。保存保留效果块，重新启用后仍检查活动组合；仅关闭个别效果或将其不透明度设为 0 不会扩展这项组能力。

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
- **Color Balance / 旧版 RGB BrightnessContrast**：支持已知参数编辑和保存；非中性值的实时重绘仍拒绝，保存时须提供准确的 `Composite` 或 `CompositeImage`。中性参数支持恒等渲染。
- **Curves**：256 项查找表支持任意映射，包括非单调值；非单调控制点曲线的插值仍不支持。
- **Hue/Saturation**：RGB8 的纯 Master Lightness 默认支持，范围为 -100 至 100 的整数；要求 `Colorize=false`、Master Hue/Saturation 为 0、六个局部调整值均为 0。`SetAdjustment` 编辑后可直接实时重绘、自动保存和重新打开，不需要提供替代合成图。Hue、Saturation、局部调整和 Colorize 仍须通过 `ApproximateHueSaturation` 显式开启近似。

纯明度回归覆盖全部 201 个整数控制的原生灰阶结果；彩色与透明像素的独立对照存在最多 1 字节差异，复杂前置混合不承诺逐像素一致。

## 矢量与填充

| 能力 | 入口 |
| --- | --- |
| 矢量路径 | `source.DecodeVectorMask` / `DecodePath`，`psdvector.Rasterize`，`EncodeVectorMask` |
| 纯色 | `Editor.SetSolidFill` |
| 渐变 | `source.DecodeGradientFill`、`psdgradient.Render`（Classic 渐变）、`Editor.SetGradientFill` |
| 图案 | `source.DecodePatternFill` / `DecodePattern`、`psdpattern.Render`、`Editor.SetPatternFill` / `SetPatternFillWithOptions` |
| 矢量蒙版编辑 | `Editor.AddVectorMask` / `SetVectorMask` / `RemoveVectorMask`，生命周期限制见[图层编辑](editing.md) |

`psdvector` 保留 typed 路径、未知记录和受控编码，提供有限光栅化；描边近似通过 `ApproximateVectorStroke` 显式开启。尚未完成：全部组合、描边、羽化与采样精度。

普通 Normal 栅格层上的单个整数矩形矢量蒙版，支持 .5 / 1.5 / 3 像素羽化；可与已禁用的像素蒙版共存。像素蒙版重新启用后，该双蒙版羽化组合仍拒绝，已有样本与参数不会丢失。

### 恒定 Classic 渐变

当全部颜色停止点的 RGB 完全相同、全部透明度停止点的值完全相同时，Linear / Radial Classic 渐变可直接重绘为常值色场；没有透明度停止点时视为完全不透明。这一子集支持非默认 Smoothness、同值重叠停止点、端点 Midpoint，以及 1×1 或极小缩放的退化几何，保存保留原始渐变描述符。

所有字段仍须合法、有限且满足预算。Dither、未知形状或插值方式继续拒绝；不再恒定的分量仍遵守原有采样限制。支持 `SetGradientFill` 编辑、自动保存、重开与继续编辑。

只有颜色恒定、透明度仍变化时，也允许颜色组中的重复同值停止点和端点 Midpoint；透明度组仍按原有算法采样。只有透明度恒定时同理。只要仍有变化分量，就继续要求 Smoothness=4096 和有效几何；不会用常量分量绕过另一分量的采样限制。

### 图案参数与准确图层像素

`SetPatternFill` 的自动采样范围为唯一的本地 RGB8 图案、50 / 100 / 200% 缩放、整圈角度（0、±360、±720 等）和整数相位。保存保留请求的原始角度值；接近整圈但不相等的角度、90/180/270° 仍不在实时范围内。

实际本地图案为 **1×1 像素**时，周期采样恒定，`SetPatternFill` 额外支持任意有限正缩放、有限角度及有限相位（含小数相位）。结果保留唯一像素的 RGBA，包括透明像素下的隐藏 RGB；保存不会简化图案块或改写请求参数。切换到更大的图案时立即恢复上述采样限制，参数也必须能被原描述符精确表示。此扩展不放宽 `WithOptions` 提供准确像素时的原有整数相位规则。

来源的缩放、角度或小数相位暂不支持重绘时，只要参数布局明确且本地图案有效，也可直接改成上述支持范围内的目标值。例如将 75%、37°、0.5 像素相位改成 100%、0°、整数相位，即可自动重绘并保存，无需提供 `Pixels`。

`SetPatternFillWithOptions(ctx, id, &fill, options)` 可同时提供 `PatternFillEditOptions{Pixels: accuratePixels}`，从而保存其他正有限缩放和有限角度的已知参数。目标相位仍须为整数且绝对值不超过 `1<<40`，未知 PtFl 布局和不明确的本地图案引用会被拒绝。

`Pixels` 必须与当前图层 `Bounds` 完全一致，是图层不透明度、蒙版和效果之前的 straight-alpha RGB8 像素。库独立复制它，保留本地图案原字节及无关元数据；不检查画面是否符合 Photoshop 采样算法。

这个入口只扩展参数与准确缓存的编辑保存范围。未支持的缩放／角度仍无法 `Composite` 或自动生成保存预览；调用方须通过 `SaveOptions.Composite` / `CompositeImage` 提供准确的完整合成图。省略 `Pixels` 时与 `SetPatternFill` 的严格范围相同。

> [!TIP]
> 在 [Playground](../demo/) 的“图层效果与调整”和“矢量与填充”分组里，可以对导入的 PSD 直接调用这些解析入口，查看真实返回的参数结构。
