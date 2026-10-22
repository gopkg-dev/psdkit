# 高精度普通合成

`psdprecision.Render` 接收只读 `Scene`，直接输出独立的 `FloatImage.Pix`，每像素四个 float64
表示直通 RGBA。颜色可小于 0 或大于 1，Alpha 始终位于 `[0,1]`。
合成过程不经过 NRGBA8，也不统一截断 HDR、转换 gamma 或应用 ICC。

```go
result, err := psdprecision.Render(ctx, scene, psdprecision.Options{
    MaxWorkingBytes: 64 << 20,
})
```

图层按从底到顶排列，Normal 使用 source-over；Multiply 按分量相乘，Screen 计算互补乘积。
Linear Dodge 在 32 位线性域保留颜色相加产生的 HDR，在 16 位编码域按原生规则饱和重叠混合操作。
Alpha 始终按 source-over 合并，透明背景不参与重叠颜色计算。32 位 Screen 明确返回 `ErrUnsupported`，
与 [Photoshop 的实际模式限制](https://helpx.adobe.com/photoshop/desktop/repair-retouch/adjust-light-tone/blending-mode-descriptions.html)一致。
Normal 组先在透明画布上合成，再应用一次组透明度和蒙版。
像素蒙版包含矩形外默认值、反转与密度，顺序是采样、反转、密度。
完整不带有效蒙版的 pass-through 组可直接绘制子图层；部分透明或带蒙版时，先复制背景并绘制子图层，
再按组透明度和蒙版覆盖率在原始背景与结果之间插值预乘颜色和 Alpha。
独立数学 Scene 支持这一连续浮点模型；源文档的 16 位部分透明/带蒙版穿透组暂时明确拒绝，
因为原生整数舍入尚未通过严格误差线。其他混合模式返回 `ErrUnsupported`。
普通栅格剪贴链支持 `Clipping` 和 `BlendClipped`：先合并剪贴颜色，再应用一次基底 Alpha、
透明度和蒙版；`BlendClipped=false` 则先将基底与外部背景混合，再应用剪贴图层颜色。
多剪贴、剪贴层像素蒙版、透明基底和浮点 HDR 均保留。组作为剪贴基底或剪贴图层仍需原生校准。
`BlendRanges` 顺序为灰度、红、绿、蓝，支持分裂黑/白阈值与透明背景。
高位深灰度权重为 30/59/11，分裂坡度含 15 位控制域的一个端点单位；不先量化为 8 位。
自己的 Blend If 作用于 pass-through 组仍未进入这一数值路径。
羽化、矢量蒙版、效果、调整图层和智能对象尚未进入这一数值路径。

`psdscene.RenderPrecision(ctx, source, PrecisionOptions)` 从 PSD/PSB 的活动图层集建立树，按需解码 16/32 位通道。
RGB/Gray16 在编码颜色域计算，RGB/Gray32 在线性域计算；灰度扩展为相同的三个颜色分量。
结果记录 `SourceDepth`、`Encoding` 和完整原始 ICC 副本，供后续独立显示转换使用。
缺少图层的保存预览不会冒充源图层重建；调用原始 `DecodeComposite` 或显示转换入口读取它。

源适配器在读取像素前预算全部浮点图层、蒙版、输出和同时存在的组缓冲，并用区域解码的
`MaxWorkingBytes` 限制剩余的原始通道/行工作区。组缓冲按深度复用。
完整 ZIP 工作量继续受 `Region.MaxInflatedBytes` 限制；提高解压规模不会提高总工作内存预算。
上下文取消、源关闭和 ReaderAt 错误会传播，失败不返回部分结果。

默认输入 NaN/Infinity 或混合算术溢出返回 `ErrNonFinite`；显式 `NonFiniteZero` 将独立计算样本替换为 0，
`NonFiniteClamp` 将正无穷替换为 1、负无穷与 NaN 替换为 0。有限 HDR 值始终保留，
有限越界 Alpha 或蒙版样本仍报错。所有策略都不会改变原始 PSD 浮点位模式。

原生数值验收使用 Photoshop 保存的独立合并单图层 PSD，读取其直通高位深通道；PNG 仅作视觉伴随文件。
四组 RGB16/RGB32 的半透明叠加、Normal 组透明度和组/图层像素蒙版均通过。
16 位颜色最大差异约 1.33 个保存样本单位；32 位颜色最大绝对差异约 `5.68e-8`。
细节保存在 `verification/p6-precision-independent.json`，可用 `examples/precision` 重跑。

新增原生混合矩阵中的六组已匹配：16 位 Multiply/Screen/Linear Dodge，32 位 Multiply/Linear Dodge/部分穿透组。
32 位 Linear Dodge 的控制像素真实保存了大于 1 的颜色，Go 保留此值而不截断。
16 位 Linear Dodge 的特殊 Fill 不满 100% 时尚未校准，源入口明确拒绝这项组合。
16 位三层部分穿透组曾出现最大 `2.07195` 个源样本单位差异，超过预先设定的 2 个单位；
原失败数值保留在 `p6-precision-blends-before-source-gate.json`，源入口现在返回 `ErrUnsupported`。
`p6-precision-blends-independent.json` 分别记录六组匹配与严格能力拒绝，未把这条原生比较标为通过。

剪贴与 Blend If 新增 16 组原生控制，另加四组强 destination 和多通道相交补充控制；
阈值字节、剪贴标记、clbl、混合模式均由独立解析器核对。
32 位全部十组通过，直通颜色最大绝对差异约 `1.62e-7`，Alpha 最大约 `1.18e-7`。
16 位 Normal 剪贴、多剪贴加蒙版、源灰度范围通过既定两个源样本单位阈值；其他五组的原生内部
整数舍入仍有约 2.0–4.27 个保存样本单位差异。实现保留连续浮点合成结果，未伪称其达到原生整数一致性。
完整失败和通过数值见 `verification/p6-clipping-independent.json`；原始浮点端点假设、Q15 端点检索
和首次元数据误判也保留在相邻的 `p6-clipping-*-diagnostic.json`、`p6-range-q15-diagnostic.json`。
四组补充控制完整数值见 `p6-clipping-supplement-independent.json`，其中 16 位两组仍有最多约
3.05 个保存样本单位差异；两个 `*-rgb32-independent.json` 单独记录十组经过校准的 RGB32 范围，
并明确列出排除的 16 位控制，不将它们计为通过。

`RenderRGB16` / `RenderRGB16Region` 提供普通编码 RGB16 的独立整数 Normal/Difference 路径：
活动层数量由全树 `MaxLayers` 限制，每层 Bounds 非空，允许部分画布、负原点和空隙，首层须为 Normal。
隐藏和零透明度层跳过，蒙版、组、剪贴及 Blend If 拒绝；样本须为精确 uint16/65535，Opacity 为 byte/255。
全局活动 Bounds 的边界框先填白 RGB、零 alpha，首活动层只在自己的 Bounds 内替换，后续层使用整数 source-over。
Difference 先单独舍入调整后的源色，输出为 Q15/32768，alpha 为零时保留当前 RGB。
外部 Scene 的全部活动浮点样本在 ROI 之前验证；Source 则先核完整元数据并保持 Raw/RLE 按区域读取。
调用期间 Source 的公开元数据须保持只读；私有 BBox 不代表整个 Source 记录的快照。
十一组原生代表控制的 506 个存储像素零差，未存储的 57 个画布像素不作原生证明；
边界框外 RGBA 零只是数学输出政策。`RenderRGB16Normal` 的更宽 Normal 层数和其它不透明路径继续保持。
后续五层、九层 PSB 和三十一层控制的 182 个实际保存像素逐码值一致，28 个未保存像素单独保留。
127/4096 层有独立数学验证；原生证明仍限注册控制。现有层数和存储预算不构成样本访问次数配额。

`RenderRGB16Opaque` 与 `RenderRGB16OverOpaque` 新增 Darken/Lighten，逐通道选择 Q15 最小或最大值。
原有 alpha×byte opacity 和 source-over 最近整数阶段保持；不透明 Normal 底层须覆盖目标窗口。
Source 入口仍校验原画布底层，ROI 不能隐藏其它位置的非不透明底色。
多层由既有 `MaxLayers` 约束；3/5 层素材有独立有理数和 API 验证，新的原生范围限六个固定控制。
Native request 可规范化 RAW 字，但全部 RGBA 的 Q15 值及 metadata/ICC 须保持，不能称作源 RAW byteexact。
一般透明底、蒙版、组、剪贴、Blend If 和特殊 Fill 的支持范围保持，见
外部归档中的 `verification/p6-minmax16-production-wave39-final-audit.json`。
