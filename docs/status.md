---
title: 能力矩阵与路线图
description: psdkit 各能力在读取、编辑保存、保存预览、实时重绘和显式近似五个维度上的完成状态，以及明确限制与后续规划。
updated: 2026-10-24
latest_update: "Warp 预设整图与区域来源重绘已接通：已有横向 Arc Lower、4×2、仿射、零透视且无折叠的几何，可从实际来源输出整图、ROI、Tile、分块访问和单实例。逐次显式开启 ApproximateCustomWarp，零 bend 也一样；外链逐次映射。已验证编辑、PSD/PSB 四压缩保存、重开重绘和续编，以及 JSON、真实 Wasm与失败边界。任务进度59／141（42%），Warp6／9（67%）；来源替换和其他预设继续拒绝。"
wide: true
# ───────────────────────────────────────────────────────────────
# 本文件是能力状态与规划的唯一来源。首页与能力矩阵页面都从下方数据渲染。
# 状态：done 已完成 · partial 部分支持 · none 未支持 · na 不涉及
# 近似列：opt 提供显式近似选项（默认关闭）· no 不做近似，超出范围直接拒绝 · na 不涉及
# “已完成”只适用于所述范围，不代表任意 Photoshop 文档兼容。
# ───────────────────────────────────────────────────────────────
# 仅登记已有编辑→保存→重开→继续编辑验证的近期工作流；按唯一 id 计数，不表示全部功能完成率。
verified_workflows:
  - id: warp-preset-controls-editing
    area: Warp 与变换
    title: 已声明 Warp 预设参数编辑与来源重绘
    scope: 已有横向 Arc Lower、4×2、仿射及零透视；参数编辑和整图／区域／分块／单实例来源重绘逐次显式近似与外链映射，保留原生结构与共享来源，PSD/PSB 四压缩重开重绘续编；来源替换仍拒绝。
    guide: guide/warp.md
  - id: warp-grid-editing
    area: Warp 与变换
    title: 10×10／13×13 均匀 Warp 网格编辑保存
    scope: 已认证均匀切片、仿射放置，100／169点；近似与外链逐次显式，PSD/PSB 四压缩重开续编；同尺寸来源替换保留当前网格，变尺寸及不规则网格拒绝。
    guide: guide/warp.md
  - id: warp-single-patch-editing
    area: Warp 与变换
    title: 4×4 单片自定义 Warp 控制点编辑保存
    scope: 现有明确的单一现代 SoLd／SoLE、RGB8、16点；严格／显式数学投影、外链显式映射，PSD/PSB 四压缩重开续编，当前网格在来源替换与尺寸恢复中保留。
    guide: guide/warp.md
  - id: effects-placed-shadow-overlay
    area: 图层效果
    title: 智能对象重绘结果＋阴影与颜色叠加
    scope: 当前 RGB8 实时来源，Normal 阴影＋叠加逐次显式近似；严格嵌入／替换来源 PSD/PSB 四压缩重开续编，外链须显式映射并先嵌入或提供准确合成图。
    guide: guide/effects.md
  - id: effects-feather-shadow
    area: 图层效果
    title: 已校准矢量／双蒙版羽化＋阴影
    scope: 普通 RGB8 Normal 栅格、单整数矩形、矢量羽化 0／0.5／1.5／3；像素羽化沿用已有模型，纯阴影及阴影＋叠加逐次显式近似，PSD/PSB 四压缩重开续编。
    guide: guide/effects.md
  - id: text-edit-auto-pixels
    area: 文字与字体
    title: 支持范围内文字编辑后自动更新像素
    scope: RGB8、明确 72ppi、单样式水平点文字及已有算法范围；字体与近似显式提供，PSD/PSB 四压缩保存重开续编，失败原子拒绝。
    guide: guide/text.md
  - id: artboard-live-placed-output
    area: 画板
    title: 独立画板中的实时智能对象来源
    scope: 顶层 RGB8 画板、嵌入与显式外链来源；各画板独立背景与裁剪、PSD/PSB 四压缩保存重开续编，近似逐次显式开启。
    guide: guide/rendering.md
  - id: replace-source-active-filters
    area: 智能对象与外链
    title: 替换来源时同步重算活动智能滤镜
    scope: RGB8、受支持来源与投影；各共享实例保留当前滤镜和原滤镜蒙版，PSD/PSB 四压缩保存重开续编；近似逐次显式开启。
    guide: guide/smart-objects.md
  - id: default-coverage-add-vector
    area: 混合、剪贴、蒙版与 Blend If
    title: 默认覆盖像素蒙版上新增矢量蒙版
    scope: 保留无采样默认覆盖及属性；PSD/PSB 四压缩保存重开后可继续编辑默认值和矢量几何，不新增蒙版通道。
    guide: guide/editing.md
  - id: vector-feather-color-overlay
    area: 图层效果
    title: 已校准矩形矢量羽化＋纯颜色叠加
    scope: 普通 RGB8 Normal 栅格、单整数矩形、0.5／1.5／3 像素羽化、无活动像素蒙版；无需新增近似。
    guide: guide/effects.md
  - id: vector-shadow-overlay
    area: 图层效果
    title: 活动矢量／双蒙版＋阴影与颜色叠加
    scope: 普通 Normal 栅格，活动蒙版无羽化；阴影近似须显式开启。
    guide: guide/effects.md
  - id: vector-shadow
    area: 图层效果
    title: 活动矢量／双蒙版＋纯阴影
    scope: 不含颜色叠加；普通 Normal 栅格、无羽化，须显式开启阴影近似。
    guide: guide/effects.md
  - id: pixel-shadow-overlay
    area: 图层效果
    title: 活动像素蒙版＋阴影与颜色叠加
    scope: 不含活动矢量蒙版；普通 Normal 栅格，须显式开启阴影近似。
    guide: guide/effects.md
  - id: vector-color-overlay
    area: 图层效果
    title: 活动矢量／双蒙版＋纯颜色叠加
    scope: 普通 Normal 栅格、无羽化，保留蒙版密度、反相与已有路径。
    guide: guide/effects.md
  - id: smart-filter-auto-pixels
    area: 智能滤镜
    title: 修改智能滤镜参数后自动更新像素
    scope: RGB8 及已有算法范围，无需手动提供 Pixels；近似须显式开启。
    guide: guide/filters.md
  - id: artboard-auto-save
    area: 画板
    title: 画板子层编辑后自动合成与保存
    scope: 现有顶层 RGB8 画板，无需额外 CompositeImage；保留背景与裁剪。
    guide: guide/editing.md
matrix:
  - area: PSD/PSB 格式与原始块
    task_group: format
    guide: guide/reading.md
    read: [done, "原始区间、图像资源、层集、通道、typed Descriptor；未知块原样保留"]
    edit: [done, "PSD/PSB writer、修改跟踪、未改块保留、临时文件原子替换"]
    preview: [done, "DecodeDisplayComposite / DecodeDisplayRegion 读取文件内保存的合成图"]
    live: [na, "格式层本身不涉及重绘"]
    approx: [na, ""]
    gaps: "格式沿用 Header；更换扩展名不会转换 PSD/PSB"
  - area: 通道与位深
    task_group: depth
    guide: guide/color.md
    read: [done, "1/8/16/32 位通道与区域按需解码，保留原位深与浮点位型"]
    edit: [done, "ReplaceChannel 按原位深替换现有通道"]
    preview: [done, "保存合成图的源精度显示转换"]
    live: [partial, "RGB8 核心合成完整；16/32 位有限合成、区域与分块；RGB16 Normal/Difference 整数路径支持已知禁用像素蒙版"]
    approx: ["no", "不静默降位或量化；HDR 裁剪需显式选择"]
    gaps: "高位深全混合、高级图层与大型任意文档"
  - area: 图层树与基础属性
    task_group: layers
    guide: guide/editing.md
    read: [done, "稳定 LayerID、名称、可见性、Opacity/Fill、Bounds、层级"]
    edit: [done, "改名、显隐、Opacity/Fill、普通像素替换、添加图片层与组、移动、删除"]
    preview: [done, "DecodeLayer 读取各层保存像素"]
    live: [done, "支持范围内的 RGB8 图层树、组与 Opacity/Fill 合成"]
    approx: ["no", "不支持的图层直接拒绝"]
    gaps: "编辑高级图层后的自动预览受合成能力限制"
  - area: 混合、剪贴、蒙版与 Blend If
    task_group: blending
    guide: guide/editing.md
    read: [done, "混合模式、剪贴、像素 / 矢量蒙版、Knockout、通道限制与 Blend If 记录"]
    edit: [done, "混合、剪贴、Blend If 与高级混合；像素／矢量蒙版生命周期，含无采样默认覆盖上新增矢量蒙版；PSD/PSB 保存重开继续编辑"]
    preview: [done, "包含在文件保存的合成图中"]
    live: [done, "RGB8 的 26 种确定性混合、剪贴、蒙版及受限 Blend If/Knockout；默认覆盖与矢量蒙版全图／区域一致；支持禁用效果及受限穿透组的禁用蒙版组合"]
    approx: [opt, "ApproximateClippedGroupBackdrop 剪贴组背景近似"]
    gaps: "歧义双蒙版及无真实用户蒙版的生成覆盖载体不作为默认覆盖用户蒙版编辑；高位深／高级组合保存仍需准确合成图"
  - area: 智能对象与外链
    task_group: smart-objects
    guide: guide/smart-objects.md
    read: [done, "linked-file / placed 记录、FileType 与共享实例；来源原字节；PSD/PSB/PNG/JPEG 及扁平 RGB8 TIFF，含 LZW/Deflate 与 Predictor 2"]
    edit: [done, "RGB8 来源替换、外链转嵌入与保留外链更新；各共享实例同步重算当前滤镜，原字节和参数保留；PSD/PSB 保存重开续编"]
    preview: [done, "父图层保存的栅格"]
    live: [partial, "来源替换按各实例当前投影和滤镜重算一次；嵌入与显式外链来源支持全图、区域和单实例重绘，受算法与组合资格限制"]
    approx: [opt, "ApproximatePlacedProjection、ApproximatePlacedColorOverlay；替换滤镜策略由 SmartContentsOptions.SmartFilters 显式选择"]
    gaps: "编辑限 RGB8 父文档及受支持来源、投影和滤镜算法；任意嵌套格式、完整滤镜栈及全部 Warp／混合语义仍不支持；外链重开仍需显式解析，外部文件由调用方写入"
  - area: Warp 与变换
    task_group: warp
    guide: guide/warp.md
    read: [done, "placed 变换、单片与网格 Warp 元数据"]
    edit: [partial, "四角、已有 4×4／10×10／13×13 控制点及横向 Arc Lower bend 编辑并更新实例像素；SetSmartObjectWarpPreset 保留原生预设结构，预设／网格近似逐次显式，PSD/PSB 保存重开续编"]
    preview: [done, "父图层保存的栅格"]
    live: [partial, "仿射、透视与单片；10×10／13×13 均匀网格与已认证横向 Arc Lower 预设，逐次显式数学采样支持整图／区域／分块和单实例来源重绘，保留源域与采样相位，不用旧实例缓存"]
    approx: [opt, "ApproximateCustomWarp 数学投影"]
    gaps: "均匀网格来源尺寸迁移、透视网格；预设来源替换及其他预设几何／参数；任意／非均匀 quilt及统一原生像素精度；重复现代／legacy 镜像编辑及其余组合继续拒绝"
  - area: 文字与字体
    task_group: text
    guide: guide/text.md
    read: [done, "TySh / EngineData 解析、文档级文字数据与关联"]
    edit: [partial, "SetTextContent 在已有算法范围内通过 Render 与显式字体自动更新像素，近似逐次开启；准确 Pixels 仍支持单样式 Unicode、emoji、多行与空文本；SetNativeTextState"]
    preview: [done, "文字层保存的像素"]
    live: [partial, "显式字体；有限 OpenType / CFF / CFF2 排版、塑形、Hinting 与可变字体；RGB8、明确 72ppi 单样式水平点文字编辑后可自动更新源像素"]
    approx: [opt, "ApproximateText / ApproximateOpenTypeText"]
    gaps: "自动编辑限已有文字算法；多样式／现代 Txt2、段落及竖排／Warp 内容编辑、完整排版、TrueType 字节码和 Photoshop 私有 Sharp 覆盖率"
  - area: 图层效果
    task_group: effects
    guide: guide/effects.md
    read: [done, "效果描述符解析与预算估算"]
    edit: [partial, "SetEffects / WithOptions；矢量／双蒙版叠加、矩形羽化阴影；当前智能对象来源的纯阴影及 Normal 阴影＋叠加，参数与源像素原子更新；严格嵌入来源自动保存重开续编"]
    preview: [done, "包含在保存合成图中，可能是旧缓存"]
    live: [partial, "颜色叠加、受限阴影、发光与描边；矩形双蒙版羽化阴影、当前智能对象来源阴影＋叠加；保留各自蒙版阶段、画布外来源和实例独立效果，不使用旧来源栅格"]
    approx: [opt, "ApproximateDropShadow 投影近似"]
    gaps: "全部效果、轮廓、活动 Noise/Jitter；未校准矢量羽化、纯叠加双蒙版羽化；智能对象活动矢量蒙版、纯叠加单独编辑、外链／额外来源策略自动保存、组效果及高级混合集合"
  - area: 调整图层
    task_group: adjustments
    guide: guide/effects.md
    read: [done, "调整参数解析与编译"]
    edit: [partial, "SetAdjustment；Color Balance 与旧版 RGB Brightness/Contrast 支持已知参数保存，非中性时须提供准确合成图"]
    preview: [done, "包含在保存合成图中"]
    live: [partial, "Invert、Threshold、Posterize、Levels、Curves（含非单调 LUT）、Exposure、Channel Mixer、Hue/Saturation 纯 Master Lightness；PhotoFilter 仅密度 0，Color Balance / 旧版亮度对比度仅中性"]
    approx: [opt, "ApproximateHueSaturation 用于 Hue、Saturation、局部和 Colorize 控制；纯 Master Lightness 默认支持"]
    gaps: "非零密度 PhotoFilter、非中性 Color Balance / 亮度对比度实时重绘；非单调控制点曲线插值"
  - area: 智能滤镜
    task_group: filters
    guide: guide/filters.md
    read: [done, "有序滤镜参数与滤镜缓存索引"]
    edit: [partial, "SetSmartFilterParameters 编辑五类已有滤镜；来源替换同步重算各共享实例当前滤镜；自动更新像素、保存重开续编，保留栈结构与未知字段"]
    preview: [done, "显式读取滤镜缓存区域"]
    live: [partial, "参数编辑与来源替换自动重绘；高斯/表面模糊/智能锐化恒等子集、中性亮度对比度、纯 Master Lightness 默认支持；其余声明模型需显式近似，支持区域计算"]
    approx: [opt, "ApproximateGaussian、ApproximateSurfaceBlur、ApproximateSmartSharpen 等，经 SmartFilters 显式选择"]
    gaps: "完整智能滤镜栈；自动编辑限 RGB8 父文档、受支持的来源和投影；不支持的算法或组合需显式准确像素，近似不会自动开启"
  - area: 矢量与填充
    task_group: vectors
    guide: guide/effects.md
    read: [done, "typed 路径、矢量蒙版、纯色 / 渐变 / 图案参数"]
    edit: [partial, "矢量蒙版 Add / Set / Remove；纯色/渐变/本地图案参数；已知图案参数可改回实时范围，WithOptions 可凭准确像素保存更多缩放/角度"]
    preview: [done, "保存的填充与形状栅格"]
    live: [partial, "纯色、Classic 渐变及恒定分量；单像素图案支持有限缩放/旋转/相位，其他图案限 50/100/200% 与整圈角度；有限矢量与羽化"]
    approx: [opt, "ApproximateVectorStroke 内描边近似"]
    gaps: "非单像素图案任意缩放/角度的实时采样、全部矢量组合、描边、羽化与采样精度；未支持实时的参数保存须准确合成图"
  - area: ICC 与色彩管理
    task_group: color
    guide: guide/color.md
    read: [done, "读取文档内原始 ICC"]
    edit: [na, ""]
    preview: [done, "保存合成图按 ICC 显示转换"]
    live: [partial, "RGB matrix/TRC、部分 LUT 与 CMYK 转换"]
    approx: [opt, "AllowUnmanagedCMYK 无管理 CMYK 近似回退；忽略配置文件需显式选择"]
    gaps: "完整 LUT / CMYK 与高位深组合"
  - area: 画板
    task_group: artboards
    guide: guide/rendering.md
    read: [done, "画板记录与解析"]
    edit: [partial, "现有顶层 RGB8 画板内图层编辑、跨画板移动和根级重排；自动保存 PSD/PSB，无需 CompositeImage"]
    preview: [done, "支持范围内编辑后自动重建并保存完整画布预览；保留画板背景与裁剪"]
    live: [partial, "顶层 RGB8 画板独立输出支持嵌入与显式外链智能对象实时来源；保持各自背景、矩形裁剪及已禁用自身状态，隐藏或未选择画板不解析来源"]
    approx: [opt, "画板背景／裁剪保持原规则；子来源投影、Warp、滤镜等近似通过 Render 显式选择，Compose 控制当前画板合成"]
    gaps: "画板自身几何、增删和嵌套编辑；活动自身蒙版／效果、高位深及其余来源／合成组合；不支持缓存回退或输出配置转换"
limits:
  - 不承诺任意 PSD 与 Photoshop 逐像素一致；浏览器与原生 Go 一致不等于与 Photoshop 一致。
  - 保存预览可能是旧缓存或占位图，不能当作实时重绘结果。
  - 蒙版编辑后的自动保存预览限于支持范围内的 RGB8；16/32 位及部分 Knockout / 高级组合须提供准确的显式合成图，否则返回 unsupported。
  - 不读取系统字体，不自动替换缺失字体；字体需显式提供。
  - 外链只通过调用方显式映射解析，不读取 PSD 中的系统路径或任意 URL。
  - 近似策略默认关闭，只在调用方显式开启时生效，不会为取得画面静默启用。
  - 操控变形、任意 Warp 预设与未开放的几何 / 效果组合明确返回 unsupported。
---
# 能力矩阵与路线图

psdkit 把**读取**、**可编辑保存**、**文件内保存的预览**、**实时重绘**和**显式近似**分开报告。同一图层可能可以完整读取并原样保存，却不能实时重绘。“已完成”只适用于所述范围，不代表任意 Photoshop 文档兼容。

状态整理日期：{{ page.updated }}。浏览器 Playground 的“能力目录”以运行时 `system.capabilities` 为准，列出全部可调用的 Go 入口。

{{ page.latest_update }}

本矩阵对应 `v0.3.0` 的声明范围，变更见[本版说明](releases/v0.3.0.md)。安装固定版本时，应以[该 Tag 的矩阵](https://github.com/gopkg-dev/psdkit/blob/v0.3.0/docs/status.md)为准。[2026-10-24 历史交付核验](releases/verification-2026-10-24.md)固定记录 `v0.2.0`，不覆盖后续新增的羽化阴影、智能对象效果和 Warp 编辑等能力。

{% include task-plan-summary.html heading_level=2 %}

{% include verified-workflows.html items=page.verified_workflows heading_id="verified-workflows" heading_level=2 %}

33／17 等概览数字按大类的各个维度统计。一个大类新增了已验收子项后，只要仍有明确缺口，就会继续显示“部分支持”；这些数字不是功能总数或完成百分比。[任务面板](tasks.html)按明确子任务显示完成百分比，下方矩阵继续说明实际支持边界。

## 能力矩阵

{% include capability-matrix.html rows=page.matrix workflows=page.verified_workflows %}

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
| 发布 | v0.3.0 通过 Tag 工作流检查与分发；[本版说明](releases/v0.3.0.md)与[历史交付记录](releases/verification-2026-10-24.md)分别对应各自版本 |

## 路线图

完整任务拆分、优先级、依赖与验收条件统一维护在[任务面板](tasks.html)，后续以子任务验收记录更新进度。

已有 Warp 预设几何的整图与区域来源重绘已验收：已认证的横向 Arc Lower 接入 `RenderSource`、`RenderSourceTile`、`VisitSourceTiles` 与 `RenderSourcePlacedLayer`，共用仿射、零透视、4×2 和无折叠几何及显式数学采样。每次都须开启 `ApproximateCustomWarp`，包括 bend 为0，外链也须本次明确映射；保存的实例栅格和合成图不充当实时来源。

独立原生控制记录的数学采样参考、正负原点、整图／区域／分块／单实例相位、滤镜 opacity window、原始尺寸／边界／变换 schema 唯一性及空区域无来源读取均通过。PSD/PSB 四压缩编辑、保存、重开重绘与继续编辑，以及 JSON、真实 Wasm、预算、取消和相关竞态验证通过。分块遍历按每个 tile 解析来源，以保持有界流式输出，不承诺整个遍历只解析一次。来源替换、其他预设及 Photoshop 原生像素精度仍未开放。进度为 **59／141（42%）**，Warp 分组 **6／9（67%）**；下一项为删除智能滤镜实例，大类状态仍为部分支持。

Warp 预设控制参数编辑保存已验收：`SetSmartObjectWarpPreset` 修改已有横向 Arc Lower 的原生 `doub` bend，范围限4×2、仿射、零透视、完整且无重复的尺寸／边界字段、有限且绝对值小于100并通过无折叠认证的参数。仅改写原 bend 的8字节，保留预设类型、旋转、阶数、放置、未知放置字段、共享来源及其他实例；当前来源生成的未烘焙像素和参数一起提交。

每次编辑均显式开启 `ApproximateCustomWarp`，包括 bend 为0；外链每次明确映射。独立原生控制几何与数学采样参考、父蒙版／Fill／Opacity 不烘焙、PSD/PSB 四压缩两轮磁盘编辑保存重开续编、JSON、真实 Wasm及相关竞态均通过；非法 schema、其他预设、折叠、预算／取消和过期回调原子拒绝。普通受支持组合可直接保存当前生成像素，下一次参数编辑或来源重绘仍需明确策略；来源替换继续拒绝，活动效果保留准确合成图边界。本项不宣称原生像素等价，预设通用来源重绘在上方另项验收。

10×10／13×13 均匀 Warp 网格编辑保存已验收：`SetSmartObjectWarp` 接受既有100／169个控制点，只修改坐标数值，保留三次阶数、网格类型、切片位置与0／±0.6 guard；通过共用认证投影入口接入整图、区域、分块、单实例、滤镜 opacity window 和编辑候选来源。网格投影逐次显式选择 `ApproximateCustomWarp`，仍严格限制仿射放置、均匀切片和全局无折叠。

验证覆盖独立源坐标颜色参考、父蒙版／Fill／Opacity 不烘焙、共享来源与实例独立、偏移源域／guard、同尺寸来源替换、PSD/PSB 四压缩两轮保存重开续编、来源重绘／ROI，以及 JSON、真实 Wasm 和相关竞态。网格改变来源尺寸、透视、非均匀切片、非法 schema、预算／取消和过期回调原子拒绝；保存已有生成像素不等于默认开启下一次来源重绘。均匀网格子项独立验收，编辑保存及实时重绘大类仍为部分支持。

4×4 单片自定义 Warp 控制点编辑保存已验收：`SetSmartObjectWarp` 修改已有单片的16点，保持来源坐标域、放置四角和描述符布局，从当前来源重建该实例未烘焙像素；数学 projective 投影和外链解析每次显式提供。只更新明确的 mesh 数值字节，共享来源、其他实例及未知放置字段保持；来源替换与尺寸缩放／恢复后仍保留当前控制点。

验证覆盖独立解析曲线、原生固定像素与反向采样参考、非零坐标域、连续编辑、PSD/PSB 四压缩两轮磁盘保存重开续编、公开 JSON／真实 Wasm、外链事务与策略快照，以及非法几何／预算／取消原子性。普通受支持组合直接保存当前生成的实例像素，来源实时重绘与继续编辑不继承近似。单片子项与均匀网格、已声明预设参数编辑及来源重绘分别验收；任意 quilt、其他预设与其余组合继续保留缺口。

智能对象重绘结果＋阴影与颜色叠加已验收：从当前 SoLd／SoLE、共享来源字节、变换及严格支持的当前滤镜重建未着色像素，父层效果、像素蒙版、Fill 和 Opacity 后续各应用一次。纯阴影及 Normal 阴影＋颜色叠加逐次显式选择 `ApproximateDropShadow`；`EffectsEditOptions.Render` 仅提供本次编辑的来源策略，父阴影不自动开启递归来源近似。

验证包括独立混合参考、旧缓存禁读、画布外／ROI、共享来源与实例效果独立、来源替换、PSD/PSB 四压缩两轮磁盘保存重开续编、原始通道和来源字节保留、保存预览逐像素核对、回调重入／过期拒绝与策略快照，以及原生 JSON、真实 Wasm 和相关竞态。自动保存限定严格可重建的当前嵌入来源；外链每次映射，须先转嵌入或提供准确合成图，需要额外来源近似或字体时同样如此。图层效果分组已验收 **7／21（33%）**；完整类别继续标为部分支持。

已校准矢量／双蒙版羽化＋阴影已验收：普通 RGB8 Normal 栅格、单整数矩形、矢量羽化 0.5／1.5／3，活动像素蒙版沿用已有羽化模型。纯阴影和阴影＋颜色叠加均在编辑、重绘和保存时逐次显式选择 `ApproximateDropShadow`；未羽化的既有路径范围保持，新增羽化路径不放开任意几何或半径。

独立羽化积分与标量阴影参考验证各自塑形／最终隐藏、默认链接与显式阶段、密度反相、Fill／Opacity 和效果顺序；区域、画布外来源、共享几何和半径、预算边界、取消及未知标记均有回归。PSD/PSB 四压缩完成两轮真实磁盘编辑保存重开，原始像素、蒙版采样及未知块保留，保存预览与当前合成一致；原生 JSON、真实 Wasm 和相关竞态检查通过。阴影仍是显式近似，图层效果大类继续标为部分支持。

远端 CI／在线发布交付已验收：核对远端 `main`、annotated Tag、Release、三类 Actions 运行和实际站点，均对应 `dd482b7`。公共代理与校验和服务安装的 `v0.2.0` 在独立项目和全新缓存中通过 PSD/PSB 保存重开、继续编辑及像素验证；线上 Playground 完成下载、重新导入和续编后再次下载。工程分组已验收 **3／6（50%）**。该任务不增加图像算法或改变矩阵大类状态。

文字编辑后自动更新像素已验收：`TextEditOptions.Render` 使用调用方显式提供的字体，并且只运行明确选择的 `ApproximateText` 或 `ApproximateOpenTypeText`。先更新当前 TySh／EngineData，再按原画布、DPI 和变换生成完整画布的源像素；透明度、蒙版与效果保留到后续合成，不读取旧文字栅格。准确 `Pixels` 路径继续保留，自动路径没有扩大既有字体与排版算法的范围。

验证覆盖两条文字策略、PSD/PSB 四压缩、两轮磁盘保存重开续编、保存预览、空文清除后继续编辑、显式多行排版、当前元数据与未知字段保留、字体缺失或释放、选项快照、字体回调重入、过期提交拒绝、预算／读取失败／取消和关闭原子性，以及原生 JSON 与真实 Wasm。文字分组已验收 **3／10（30%）**；完整文字类别仍如实标为部分支持。

独立画板中的实时智能对象来源已验收：`RenderArtboard` / `RenderArtboards` 默认重建严格支持范围内的嵌入来源，`ArtboardRenderOptions.Render` 接入外链解析器及显式投影、Warp、滤镜等策略。每次请求都须明确映射外链；共享来源在同次输出内复用，实例自身的变换、蒙版及效果保持独立。只选择一个画板时，不解析其他画板的来源；隐藏来源不触发解析。

验证覆盖各画板背景和裁剪隔离、已保存坐标与零起点输出、过时实例像素和预览不参与结果、来源失败不返回部分图像、嵌套与工作预算、取消和关闭、PSD/PSB 四压缩两轮编辑保存重开，以及原生和 Wasm JSON 调用链。画板分组已验收 **3／9（33%）**。画板近似列现列出可显式选择的既有子来源策略，未新增或默认开启近似算法；背景和裁剪规则保持。

替换智能对象来源时同步重算活动智能滤镜已验收：每个共享实例从新来源出发，使用当前 SoLd/SoLE 中的变换、滤镜参数和各自原滤镜蒙版计算一次，不在旧缓存上重复套用滤镜。嵌入替换、外链转嵌入和保留外链更新使用相同事务路径；父图层蒙版、效果、Fill 和透明度仍在后续合成中应用。

默认只运行已有严格子集，其他算法必须逐次通过 `SmartContentsOptions.SmartFilters` 显式选择近似，外链回调也不能改变调用时捕获的策略。验证覆盖共享实例独立参数、当前参数保留、尺寸变化、原编码字节及未知字段、滤镜蒙版、预算和取消原子性、PSD/PSB 四压缩保存重开续编，以及原生和 Wasm JSON 调用链。智能对象分组已验收 **4／7（57%）** 项。

默认覆盖像素蒙版上新增矢量蒙版已验收：普通、未标记为生成覆盖的用户蒙版可保留无采样状态，与新增矢量几何同时参与覆盖。不新增 `-2`／`-3` 通道，不把默认色烘焙成采样；保存重开后修改 `DefaultColor` 仍作用于整个无采样覆盖场。生成覆盖标记和真实用户蒙版归属保持分开，未知或歧义布局继续拒绝。

验证覆盖默认值、反相、密度、羽化和禁用属性、空及画布外边界、矢量增改删与再次新增、原元数据尾部保留、预算／取消／读取失败原子性、PSD/PSB 四种压缩保存与重开后续编；源文档完整解码、整图／区域重绘及原生、Wasm JSON 调用链保持一致。蒙版混合分组已验收 **7／13** 项。

已校准矩形矢量羽化与纯颜色叠加已验收：普通 RGB8 Normal 栅格、单整数矩形、0.5／1.5／3 像素羽化、无活动像素蒙版，直接使用 `SetEffects`，不需要额外合成图或新近似选项。禁用像素蒙版的采样与元数据仍保留，重新启用后恢复组合资格检查。

本项复用已有原生羽化 Alpha 采样点，并以独立卷积和预乘混合参考验证叠加组合；不宣称新增完整 Photoshop 组合图的逐像素认证。验证覆盖区域外羽化来源、密度与反相、部分透明度和效果顺序、预算与取消、PSD/PSB 四压缩两轮编辑保存重开，以及原生与 Wasm 的 JSON 调用链。其余组合按当前矩阵继续报告边界。

活动矢量蒙版与阴影组合已补齐：普通 Normal 栅格上的纯阴影或阴影＋颜色叠加，支持无羽化矢量蒙版和像素／矢量双蒙版。`lmgm`、`vmgm` 分别决定两类蒙版参与塑形还是遮住最终效果；缺省按原链接状态，显式值优先。塑形覆盖缓存包含阴影实际来源，整图与区域重绘保持一致。

验收覆盖默认拒绝与显式近似、两个蒙版阶段的交叉组合、部分透明度、密度／反相、多效果顺序、负坐标和画布外来源、当前蒙版生命周期，以及 PSD/PSB 四种压缩下的编辑、自动保存、重开与继续编辑。真实 Wasm 已完成两轮编辑、下载和重新导入，并核对下载文件的像素；浏览器 JSON 桥接同样通过。没有增加新的效果族或默认近似。

已补齐两项常用效果组合：无羽化活动矢量蒙版（含像素／矢量双蒙版）上的纯颜色叠加，以及活动像素蒙版上的颜色叠加＋投影。后者复用显式阴影近似模型，保留蒙版作用阶段、部分透明、Fill／Opacity、内部混合及多效果顺序；没有扩大 Photoshop 私有算法的精度承诺。

两项分别验证 PSD/PSB 四种压缩下的编辑、自动保存、重开和继续编辑，并覆盖当前蒙版新增／修改／删除、未知块保留、预算和取消失败的原子性；图层效果的其余组合继续按边界报告为部分支持。

智能滤镜自动更新像素已经接通：从当前智能对象来源和当前变换重绘一次，保留原始滤镜蒙版，成功后一起提交参数与像素。整个栈或全部有效滤镜关闭时恢复来源投影；共享实例、来源字节和未知字段保持。失败不会留下部分参数或像素更新。

验收覆盖严格默认子集、五类显式近似、外链解析、滤镜蒙版、PSD/PSB 四种通道压缩、磁盘保存、重开继续编辑及浏览器 JSON 调用桥接。实际 Wasm Playground 已完成编辑、下载、重新导入及继续编辑后再次下载，并独立核对两份文件的像素和参数。未提供对应近似策略时仍拒绝超出默认范围的参数。此项补齐编辑工作流，未增加滤镜算法；智能滤镜整体仍为部分支持。

现有顶层 RGB8 画板内的图层编辑也已支持自动合成与保存，无需额外提供 `CompositeImage`；画板自身几何、增删和嵌套编辑仍未开放。

接下来按风险和实现复杂度推进，每项都先验证编辑、保存重开及失败原子性：

1. **删除智能滤镜实例**：先补齐首项、末项、唯一项和全部删除，重建索引并从当前来源更新像素，验证保存重开和继续编辑。
2. **剩余效果与填充组合**：逐项验证组效果、其他效果族、普通图案缩放与旋转，建立独立参考。
3. **复杂几何、文字、高位深与 ICC**：分开推进其他 Warp 预设／quilt、多样式排版、16/32 位合成及色彩组合。
4. **工程与发布**：维护性能、预算及平台一致性；后续新版本在审核提交、CI、Pages 与可公开示例后发布。

尚未完成：任意输入的全部 Photoshop 功能与逐像素兼容；独立素材的公开分发审核；移动端与无障碍的完整人工验收。旧版长文档及原生验收记录保留在外部 `psdkit-verification`，不作为在线文档依赖。
