---
title: 能力矩阵与路线图
description: psdkit 各能力在读取、编辑保存、保存预览、实时重绘和显式近似五个维度上的完成状态，以及明确限制与后续规划。
updated: 2026-10-23
latest_update: "活动矢量蒙版与阴影已接通：普通 RGB8 Normal 栅格支持无羽化矢量／双蒙版上的纯阴影及阴影＋颜色叠加，保留两类蒙版各自的塑形与隐藏效果阶段。已验证编辑、PSD/PSB 保存、重开和继续编辑，以及区域外和画布外的阴影来源；编辑、重绘与保存仍须显式开启 ApproximateDropShadow。矢量参与时的羽化、智能对象与高级组合继续按范围拒绝。"
wide: true
# ───────────────────────────────────────────────────────────────
# 本文件是能力状态与规划的唯一来源。首页与能力矩阵页面都从下方数据渲染。
# 状态：done 已完成 · partial 部分支持 · none 未支持 · na 不涉及
# 近似列：opt 提供显式近似选项（默认关闭）· no 不做近似，超出范围直接拒绝 · na 不涉及
# “已完成”只适用于所述范围，不代表任意 Photoshop 文档兼容。
# ───────────────────────────────────────────────────────────────
# 仅登记已有编辑→保存→重开→继续编辑验证的近期工作流；按唯一 id 计数，不表示全部功能完成率。
verified_workflows:
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
    edit: [done, "混合、剪贴、Blend If 与高级混合；像素蒙版新增/删除/属性与通道编辑；矢量蒙版 Add / Set / Remove 有限生命周期；PSD/PSB 保存重开"]
    preview: [done, "包含在文件保存的合成图中"]
    live: [done, "RGB8 的 26 种确定性混合、剪贴、蒙版及受限 Blend If/Knockout；支持禁用效果及单子层非 Normal 穿透组的禁用蒙版组合"]
    approx: [opt, "ApproximateClippedGroupBackdrop 剪贴组背景近似"]
    gaps: "歧义双蒙版拒绝；无采样默认覆盖蒙版可编辑属性但不能直接添加矢量蒙版；高位深/高级组合保存需准确合成图"
  - area: 智能对象与外链
    task_group: smart-objects
    guide: guide/smart-objects.md
    read: [done, "linked-file / placed 记录、FileType 与共享实例；来源原字节；PSD/PSB/PNG/JPEG 及扁平 RGB8 TIFF，含 LZW/Deflate 与 Predictor 2"]
    edit: [done, "RGB8 PSD/PSB、PNG/JPEG 与扁平 TIFF 的 ReplaceContents、共享来源替换、外链转嵌入与保留外链更新；原字节保留、PSD/PSB 保存重开"]
    preview: [done, "父图层保存的栅格"]
    live: [partial, "嵌入来源递归重绘；ExternalRasterResolver 接入外链 PNG/JPEG/扁平 TIFF，支持全图、区域和单实例，受资格限制"]
    approx: [opt, "ApproximatePlacedProjection、ApproximatePlacedColorOverlay"]
    gaps: "编辑限 RGB8 父文档及受支持的来源、实例投影；任意嵌套格式、实时滤镜与全部 Warp / 混合语义仍不支持；保留外链保存后仍需显式解析来源，外部文件由调用方写入"
  - area: Warp 与变换
    task_group: warp
    guide: guide/warp.md
    read: [done, "placed 变换、单片与网格 Warp 元数据"]
    edit: [partial, "SetSmartObjectTransform / WithOptions 修改无活动 Warp 的单实例四角，保留共享来源及其他实例；PSD/PSB 保存重开"]
    preview: [done, "父图层保存的栅格"]
    live: [partial, "仿射、透视、单片和受限 10×10 / 13×13 均匀网格，含正反映射"]
    approx: [opt, "ApproximateCustomWarp 数学投影"]
    gaps: "Warp 网格/预设参数编辑、任意预设 / quilt 整图与统一投影精度；部分组合继续拒绝"
  - area: 文字与字体
    task_group: text
    guide: guide/text.md
    read: [done, "TySh / EngineData 解析、文档级文字数据与关联"]
    edit: [partial, "SetTextContent 支持单样式水平点文字的 Unicode 内容、emoji、多行与空文本，须提供准确像素；SetNativeTextState"]
    preview: [done, "文字层保存的像素"]
    live: [partial, "显式字体；有限 OpenType / CFF / CFF2 排版、塑形、Hinting 与可变字体"]
    approx: [opt, "ApproximateText / ApproximateOpenTypeText"]
    gaps: "完整排版、TrueType 字节码、Photoshop 私有 Sharp 覆盖率"
  - area: 图层效果
    task_group: effects
    guide: guide/effects.md
    read: [done, "效果描述符解析与预算估算"]
    edit: [partial, "SetEffects / WithOptions；活动无羽化矢量／双蒙版颜色叠加及显式近似阴影组合，支持编辑、自动保存、重开与继续编辑"]
    preview: [done, "包含在保存合成图中，可能是旧缓存"]
    live: [partial, "颜色叠加、受限阴影、发光与描边；显式近似支持活动无羽化矢量／双蒙版的纯阴影及阴影＋颜色叠加，保留独立蒙版阶段与区域外来源"]
    approx: [opt, "ApproximateDropShadow 投影近似"]
    gaps: "全部效果、轮廓、活动 Noise/Jitter；矢量／双蒙版羽化与阴影、智能对象、组效果及高级混合集合"
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
    edit: [partial, "SetSmartFilterParameters 编辑五类已有滤镜的已知控制、启用和不透明度；支持范围内自动更新像素、保存重开及继续编辑；保留栈结构与未知字段"]
    preview: [done, "显式读取滤镜缓存区域"]
    live: [partial, "参数编辑复用当前来源自动重绘；高斯/表面模糊/智能锐化恒等子集、中性亮度对比度、纯 Master Lightness 默认支持；其余声明模型需显式近似，支持区域计算"]
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
    live: [partial, "顶层 RGB8 画板重绘与独立输出；支持画板自身明确禁用的蒙版和效果集合，保持原矩形裁剪"]
    approx: ["no", "不支持的画板组合直接拒绝"]
    gaps: "画板自身几何、增删和嵌套编辑；活动蒙版、独立画板的实时智能对象与高位深完整语义"
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
| 发布 | 尚未连接 GitHub 远端，未完成首次发布与实际 Pages 部署 |

## 路线图

完整任务拆分、优先级、依赖与验收条件统一维护在[任务面板](tasks.html)，后续以子任务验收记录更新进度。

活动矢量蒙版与阴影组合已补齐：普通 Normal 栅格上的纯阴影或阴影＋颜色叠加，支持无羽化矢量蒙版和像素／矢量双蒙版。`lmgm`、`vmgm` 分别决定两类蒙版参与塑形还是遮住最终效果；缺省按原链接状态，显式值优先。塑形覆盖缓存包含阴影实际来源，整图与区域重绘保持一致。

验收覆盖默认拒绝与显式近似、两个蒙版阶段的交叉组合、部分透明度、密度／反相、多效果顺序、负坐标和画布外来源、当前蒙版生命周期，以及 PSD/PSB 四种压缩下的编辑、自动保存、重开与继续编辑。真实 Wasm 已完成两轮编辑、下载和重新导入，并核对下载文件的像素；浏览器 JSON 桥接同样通过。没有增加新的效果族或默认近似，能力总数保持 **33 个已完成、17 个部分支持、0 个未支持、9 类显式近似**。

已补齐两项常用效果组合：无羽化活动矢量蒙版（含像素／矢量双蒙版）上的纯颜色叠加，以及活动像素蒙版上的颜色叠加＋投影。后者复用显式阴影近似模型，保留蒙版作用阶段、部分透明、Fill／Opacity、内部混合及多效果顺序；没有扩大 Photoshop 私有算法的精度承诺。

两项分别验证 PSD/PSB 四种压缩下的编辑、自动保存、重开和继续编辑，并覆盖当前蒙版新增／修改／删除、未知块保留、预算和取消失败的原子性。能力总数仍为 **33 个已完成、17 个部分支持、0 个未支持、9 类显式近似**；图层效果的其余组合继续按边界报告为部分支持。

智能滤镜自动更新像素已经接通：从当前智能对象来源和当前变换重绘一次，保留原始滤镜蒙版，成功后一起提交参数与像素。整个栈或全部有效滤镜关闭时恢复来源投影；共享实例、来源字节和未知字段保持。失败不会留下部分参数或像素更新。

验收覆盖严格默认子集、五类显式近似、外链解析、滤镜蒙版、PSD/PSB 四种通道压缩、磁盘保存、重开继续编辑及浏览器 JSON 调用桥接。实际 Wasm Playground 已完成编辑、下载、重新导入及继续编辑后再次下载，并独立核对两份文件的像素和参数。未提供对应近似策略时仍拒绝超出默认范围的参数。此项补齐编辑工作流，未增加滤镜算法，因此能力总数保持 **33 个已完成、17 个部分支持、0 个未支持、9 类显式近似**；智能滤镜整体仍为部分支持。

现有顶层 RGB8 画板内的图层编辑也已支持自动合成与保存，无需额外提供 `CompositeImage`；画板自身几何、增删和嵌套编辑仍未开放。

接下来按风险和实现复杂度推进，每项都先验证编辑、保存重开及失败原子性：

1. **剩余图层效果组合**：逐项验证羽化、智能对象与组效果混合集合，建立独立参考。
2. **普通图案缩放与旋转**：扩大非单像素图案的已验证采样范围。
3. **复杂几何、文字、高位深与 ICC**：分开推进 Warp 预设／quilt、多样式排版、16/32 位合成及色彩组合。
4. **工程与发布**：维护性能、预算及平台一致性；审核提交、远端、CI、Pages 与可公开示例后发布。

尚未完成：任意输入的全部 Photoshop 功能与逐像素兼容；独立素材的公开分发审核；移动端与无障碍的完整人工验收。旧版长文档及原生验收记录保留在外部 `psdkit-verification`，不作为在线文档依赖。
