---
title: 能力矩阵与路线图
description: psdkit 各能力在读取、编辑保存、保存预览、实时重绘和显式近似五个维度上的完成状态，以及明确限制与后续规划。
updated: 2026-10-22
wide: true
# ───────────────────────────────────────────────────────────────
# 本文件是能力状态与规划的唯一来源。首页与能力矩阵页面都从下方数据渲染。
# 状态：done 已完成 · partial 部分支持 · none 未支持 · na 不涉及
# 近似列：opt 提供显式近似选项（默认关闭）· no 不做近似，超出范围直接拒绝 · na 不涉及
# “已完成”只适用于所述范围，不代表任意 Photoshop 文档兼容。
# ───────────────────────────────────────────────────────────────
matrix:
  - area: PSD/PSB 格式与原始块
    guide: guide/reading.md
    read: [done, "原始区间、图像资源、层集、通道、typed Descriptor；未知块原样保留"]
    edit: [done, "PSD/PSB writer、修改跟踪、未改块保留、临时文件原子替换"]
    preview: [done, "DecodeDisplayComposite / DecodeDisplayRegion 读取文件内保存的合成图"]
    live: [na, "格式层本身不涉及重绘"]
    approx: [na, ""]
    gaps: "格式沿用 Header；更换扩展名不会转换 PSD/PSB"
  - area: 通道与位深
    guide: guide/color.md
    read: [done, "1/8/16/32 位通道与区域按需解码，保留原位深与浮点位型"]
    edit: [done, "ReplaceChannel 按原位深替换现有通道"]
    preview: [done, "保存合成图的源精度显示转换"]
    live: [partial, "RGB8 核心合成完整；16/32 位为有限合成、区域与分块"]
    approx: ["no", "不静默降位或量化；HDR 裁剪需显式选择"]
    gaps: "高位深全混合、高级图层与大型任意文档"
  - area: 图层树与基础属性
    guide: guide/editing.md
    read: [done, "稳定 LayerID、名称、可见性、Opacity/Fill、Bounds、层级"]
    edit: [done, "改名、显隐、Opacity/Fill、普通像素替换、添加图片层与组、移动、删除"]
    preview: [done, "DecodeLayer 读取各层保存像素"]
    live: [done, "支持范围内的 RGB8 图层树、组与 Opacity/Fill 合成"]
    approx: ["no", "不支持的图层直接拒绝"]
    gaps: "编辑高级图层后的自动预览受合成能力限制"
  - area: 混合、剪贴、蒙版与 Blend If
    guide: guide/rendering.md
    read: [done, "混合模式、剪贴、像素蒙版、Knockout、通道限制、Blend If 记录"]
    edit: [partial, "原样保留；矢量蒙版可用 SetVectorMask 编辑，混合参数无修改接口"]
    preview: [done, "包含在文件保存的合成图中"]
    live: [done, "26 种确定性混合、剪贴、像素蒙版、部分 Knockout 与 Blend If，需通过资格检查"]
    approx: [opt, "ApproximateClippedGroupBackdrop 剪贴组背景近似"]
    gaps: "部分 Knockout 组合；不在支持范围的组合返回 unsupported"
  - area: 智能对象与外链
    guide: guide/smart-objects.md
    read: [done, "linked-file / placed 记录，嵌入 PSD/PSB/TIFF/PNG/JPEG 来源"]
    edit: [partial, "ReplaceContents、共享来源替换、外链转嵌入、保留外链更新"]
    preview: [done, "父图层保存的栅格"]
    live: [partial, "嵌入来源递归重绘与 RenderSourcePlacedLayer，受资格限制"]
    approx: [opt, "ApproximatePlacedProjection、ApproximatePlacedColorOverlay"]
    gaps: "任意嵌套格式与全部混合语义"
  - area: Warp 与变换
    guide: guide/warp.md
    read: [done, "placed 变换、单片与网格 Warp 元数据"]
    edit: [none, "不提供 Warp 参数编辑；替换内容时保留原变换"]
    preview: [done, "父图层保存的栅格"]
    live: [partial, "仿射、透视、单片和受限 10×10 / 13×13 均匀网格，含正反映射"]
    approx: [opt, "ApproximateCustomWarp 数学投影"]
    gaps: "任意预设 / quilt 与统一投影精度；部分组合继续拒绝"
  - area: 文字与字体
    guide: guide/text.md
    read: [done, "TySh / EngineData 解析、文档级文字数据与关联"]
    edit: [partial, "SetTextContent 仅限单样式拉丁点文字且须提供准确像素；SetNativeTextState"]
    preview: [done, "文字层保存的像素"]
    live: [partial, "显式字体；有限 OpenType / CFF / CFF2 排版、塑形、Hinting 与可变字体"]
    approx: [opt, "ApproximateText / ApproximateOpenTypeText"]
    gaps: "完整排版、TrueType 字节码、Photoshop 私有 Sharp 覆盖率"
  - area: 图层效果
    guide: guide/effects.md
    read: [done, "效果描述符解析与预算估算"]
    edit: [partial, "SetEffects / SetEffectsWithOptions"]
    preview: [done, "包含在保存合成图中，可能是旧缓存"]
    live: [partial, "颜色叠加、受限阴影与发光"]
    approx: [opt, "ApproximateDropShadow 投影近似"]
    gaps: "全部效果、轮廓、Noise 与复杂组合"
  - area: 调整图层
    guide: guide/effects.md
    read: [done, "调整参数解析与编译"]
    edit: [partial, "SetAdjustment"]
    preview: [done, "包含在保存合成图中"]
    live: [partial, "Invert、Threshold、Posterize、Levels、Curves、Exposure、Channel Mixer；PhotoFilter 仅密度 0"]
    approx: [opt, "ApproximateHueSaturation 色相 / 饱和度近似"]
    gaps: "非零密度 PhotoFilter、非中性 Color Balance"
  - area: 智能滤镜
    guide: guide/filters.md
    read: [done, "有序滤镜参数与滤镜缓存索引"]
    edit: [partial, "SetSmartFilterParameters"]
    preview: [done, "显式读取滤镜缓存区域"]
    live: [partial, "高斯模糊、表面模糊、智能锐化、亮度 / 对比度、色相 / 饱和度的声明模型，支持区域计算"]
    approx: [opt, "ApproximateGaussian、ApproximateSurfaceBlur、ApproximateSmartSharpen 等，经 SmartFilters 显式选择"]
    gaps: "完整智能滤镜栈"
  - area: 矢量与填充
    guide: guide/effects.md
    read: [done, "typed 路径、矢量蒙版、纯色 / 渐变 / 图案参数"]
    edit: [partial, "SetVectorMask、SetSolidFill、SetGradientFill、SetPatternFill"]
    preview: [done, "保存的填充与形状栅格"]
    live: [partial, "纯色、Classic 渐变、本地图案与有限矢量光栅化"]
    approx: [opt, "ApproximateVectorStroke 内描边近似"]
    gaps: "全部组合、描边、羽化与采样精度"
  - area: ICC 与色彩管理
    guide: guide/color.md
    read: [done, "读取文档内原始 ICC"]
    edit: [na, ""]
    preview: [done, "保存合成图按 ICC 显示转换"]
    live: [partial, "RGB matrix/TRC、部分 LUT 与 CMYK 转换"]
    approx: [opt, "AllowUnmanagedCMYK 无管理 CMYK 近似回退；忽略配置文件需显式选择"]
    gaps: "完整 LUT / CMYK 与高位深组合"
  - area: 画板
    guide: guide/rendering.md
    read: [done, "画板记录与解析"]
    edit: [na, ""]
    preview: [done, "文档保存的合成图"]
    live: [partial, "受支持的顶层 RGB8 画板重绘与独立输出"]
    approx: ["no", "不支持的画板组合直接拒绝"]
    gaps: "嵌套、蒙版、实时智能对象与高位深完整语义"
limits:
  - 不承诺任意 PSD 与 Photoshop 逐像素一致；浏览器与原生 Go 一致不等于与 Photoshop 一致。
  - 保存预览可能是旧缓存或占位图，不能当作实时重绘结果。
  - 不读取系统字体，不自动替换缺失字体；字体需显式提供。
  - 外链只通过调用方显式映射解析，不读取 PSD 中的系统路径或任意 URL。
  - 近似策略默认关闭，只在调用方显式开启时生效，不会为取得画面静默启用。
  - 操控变形、任意 Warp 预设与未开放的几何 / 效果组合明确返回 unsupported。
---
# 能力矩阵与路线图

psdkit 把**读取**、**可编辑保存**、**文件内保存的预览**、**实时重绘**和**显式近似**分开报告。同一图层可能可以完整读取并原样保存，却不能实时重绘。“已完成”只适用于所述范围，不代表任意 Photoshop 文档兼容。

状态整理日期：{{ page.updated }}。浏览器 Playground 的“能力目录”以运行时 `system.capabilities` 为准，列出全部可调用的 Go 入口。

## 能力矩阵

{% include capability-matrix.html rows=page.matrix %}

> [!NOTE]
> “保存预览”读取 PSD 内已保存的像素，速度快、覆盖面广，但可能是旧缓存；“实时重绘”由 Go 根据当前图层树重新计算，只在支持范围内输出，超出范围返回 `unsupported`，不会伪造图像。

## 明确限制

{% for item in page.limits %}- {{ item }}
{% endfor %}

## 项目基础

| 范围 | 状态 |
| --- | --- |
| 模块 | `github.com/gopkg-dev/psdkit`，Go 1.27.3+，核心库只依赖标准库 |
| 浏览器 | Go Wasm、Worker SDK、本机导入、能力目录、二进制下载 |
| 工程 | golangci-lint、格式检查、跨平台测试、Pages 构建 |
| 文档与演示 | 首页、文档网站与 Playground；移动端与无障碍持续验收 |
| 发布 | 尚未连接 GitHub 远端，未完成首次发布与实际 Pages 部署 |

## 路线图

1. **首次发布**：审核提交范围，连接远端，运行 GitHub CI，启用 Pages，发布首个版本。
2. **兼容性**：按真实需求推进效果、文字、Warp、滤镜和高位深组合，每项补充独立参考测试。
3. **性能**：建立公开可重复的基准，优化区域读取、分块和工作预算；平台一致性与原生精度分开验收。
4. **文档与示例**：补充更多自有轻量示例素材，完成公开分发审核；私有样例 PSD 不公开分发。

尚未完成：任意输入的全部 Photoshop 功能与逐像素兼容；独立素材的公开分发审核；移动端与无障碍的完整人工验收。旧版长文档及原生验收记录保留在外部 `psdkit-verification`，不作为在线文档依赖。
