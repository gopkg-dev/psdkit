# psdcolor

`Convert` 将 `psdfile.CompositePixels` 的原始通道转为拥有独立内存、直通 Alpha 的 sRGB `image.NRGBA`。
它只生成显示图像，不会修改原始 1/8/16/32 位样本、ICC、调色板或 NaN 位模式。

```go
display, err := psdcolor.Convert(ctx, pixels, psdcolor.Options{
    ICCProfile: icc,
    Alpha:      psdcolor.AlphaWhiteMatte,
})
```

`TransformProfile(ctx, rgba, sourceICC, destinationICC, options...)` 将直通 Alpha 的 RGB8 图片
从明确的源配置转换到明确的目的 RGB 配置。它支持 Relative Colorimetric、无黑点补偿的
RGB matrix/TRC：输入曲线、D50 PCS 矩阵及目的逆曲线之间保持 float64，只在最终 RGB8 输出时
截断 gamut 并舍入。Alpha、坐标矩形与输入内存保持原样，返回独立紧凑像素。
目的采样曲线必须严格递增，参数曲线不能含平台、裁剪区间或下降的分支边界。
LUT、奇异矩阵及不可逆目的曲线明确返回 `ErrUnsupported`。空配置默认返回错误，
仅 `ProfileTransformOptions{AssumeSRGB: true}` 显式为缺失的任一配置提供标准 sRGB。
`ProfileTransformWorkingBytes` 使用相同的无像素分配检查，返回新输出及两份解析工作数据的
保守峰值，供递归调用方在现存图片/配置的费用之外预留；成功预估不代表所有 ICC 曲线可用。
默认独立转换费用上限为 256 MiB，每个配置上限 4 MiB。
Photoshop 26.1 的 Adobe RGB ↔ sRGB 两个受控转换，原图层 RGB8 所有通道及 Alpha 均逐值完全一致；
证据见 `verification/p6-profile-transform-independent.json`。

整数 RGB/Gray 无 ICC 时按编码后的 sRGB 显示，16 位以 65535 缩放。32 位 RGB/Gray 默认表示线性光，
经过 sRGB 显示传递函数；来自其他编码的数据应显式使用 `FloatEncodedRGB`。
ICC 矩阵仍作用于 32 位线性源的原色，已经线性的样本不会再次应用输入 TRC。

ICC 支持 v2/v4 RGB XYZ 矩阵、`curv` 采样/幂次曲线、`para` 类型 0–4，以及 Gray TRC 的 XYZ/Lab PCS。
RGB/Gray/CMYK 的前向 LUT 支持 `mft1` Lab、`mft2` XYZ/旧版 Lab 和 v4 `mAB`。
一维表线性插值，三维 CLUT 使用四面体插值，四维先在第一维线性插值，再计算三维四面体。
`IntentRelativeColorimetric` 默认选择 `A2B1`，感知和饱和度选择 `A2B0`/`A2B2`；缺少对应表时采用 `A2B0`。
转换使用 D50 PCS 到 D65 的 Bradford 适配和目标 gamut 截断；不执行黑点补偿。
RGB 矩阵列已经位于 PCS，不能重复应用 `chad`。Gray TRC 也使用 PCS 白点，不能把 v2 的媒体 `wtpt`
当作 PCS 白点。绝对色度、device-link、`D2B` 多过程元素、未定义的 `mft1` XYZ 或不匹配的配置
返回 `ErrUnsupported`，不会忽略已提供的 ICC。32 位线性源配合输入 LUT 需要明确使用 `FloatEncodedRGB`，
因为 LUT 不能可靠拆成独立输入 TRC 与线性转换。

Bitmap 的 0 位表示白、1 位表示黑。Indexed 使用完整 768 字节的 R/G/B 分段调色板，
透明索引由 `TransparencyIndex` 明确指定。CMYK 支持原始 8/16 位反色通道，缺 ICC 时默认拒绝转换；
`AllowUnmanagedCMYK` 才允许 `(1-C)*(1-K)` 近似显示，这不是受色彩管理的 CMYK。
Lab 使用 D50：8 位 a/b 中性值 128；Photoshop 16 位 a/b 中性 word 32768、每单位 256 个 word。

额外通道默认不当作透明度。`AlphaStraight` 适用于原始图层通道；`AlphaWhiteMatte` 适用于白底预乘的保存合成图。
`AlphaChannel=0` 选择颜色分量之后的第一通道。图层计数的负号与原生全局 `Mtrn`/`Mt16`/`Mt32`
声明应由持有文档元数据的调用方解释。蒙版和其他额外通道不应靠通道数量猜成 Alpha。

移除 8 位白底预乘会放大已经发生的源舍入，无法恢复保存前的原始颜色。
Lab8 的低 Alpha 饱和颜色尤其明显；使用原始图层通道或 16 位样本可以保留更多颜色信息。
原生校验中 Lab8 原图层直通 Alpha 与 Photoshop 的可见 RGB 误差不超过 1，
同文件保存的 8 位合成图因白底舍入出现最大 9 的预乘 RGB 差异，这属于原始预览的数据损失。

NaN/Infinity 默认返回 `ErrNonFinite`；`NonFiniteZero` 或 `NonFiniteClamp` 仅替换显示值。
有限 HDR 颜色超出 `[0,1]` 默认返回 `ErrOutOfRange`；`ToneMapClamp` 必须显式选择，
它仅截断显示范围，不是曝光或高光压缩。Alpha 始终要求 `[0,1]`，不会随颜色 HDR 截断。
源浮点位模式始终保持原样。

默认预算为 6400 万像素、256 MiB 输出、4 MiB 配置及一维曲线/CLUT 存储；负预算无效。
形状、stride 与数据长度必须一致，循环与 ICC 曲线解析支持 Context 取消。
生产包只使用 Go 标准库。Photoshop/Pillow/LittleCMS 仅用于可选的外部 QA。

数值依据：[ICC.1:2022 Annex F / parametric curves](https://www.color.org/specification/ICC.1-2022-05.pdf)、
[ICC sRGB registry](https://registry.color.org/rgb-registry/srgb)、
[Adobe PSD/PSB specification](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/)。
