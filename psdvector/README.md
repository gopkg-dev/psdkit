# psdvector

`psdvector` 是只依赖 Go 标准库的路径记录解码与填充蒙版栅格器。
`DecodeMask` 读取 PSD `vmsk`/`vsms` 的版本 3 payload；`DecodePath` 读取保存路径、工作路径的无头 26 字节记录。
原始 header、每条 record、padding、source span、坐标整数、控制点及组件 metadata 均保留。
源输入 bytes 不会被修改或借用。

`EncodeMask` / `EncodePath` 从 typed 数据写出独立 payload，原 header/record/span 作为源编码依据。
不修改数据的 roundtrip 保持未知记录及 padding 字节一致；修改 flags、控制点、开闭、组件操作、
路径与 knot 顺序，或增加/删除路径时，重写对应字段与 16 位 knot 数量。
已有 `RecordIndex` 用于关联源记录，新建项可设为 -1。
既有点可修改像素 X/Y 或原始 RawX/RawY；同时修改两种表示且不一致会报错。
新点使用像素坐标，编码时按文档宽高归一为 signed 8.24；超过表示范围明确拒绝。
非零 winding 可用于栅格化，但目前没有对应的 PSD path fill-rule wire 定义，编码时明确拒绝。

编辑器提供 `psdedit.Editor.SetVectorMask(ctx, layerID, mask)`，用于替换已有且唯一的 `vmsk`/`vsms`。
输入的文档尺寸必须匹配源文件；编码和 typed 数据由编辑器独立持有，原有用户蒙版、density 与 feather 保留。
替换的预算包含旧、新两份数据共同驻留的峰值，取消或超限不会留下部分编辑。
`verification/vector-edit-independent.json` 记录九个真实 Photoshop 保存、重开与重新合成样本：
整数路径、空蒙版、用户蒙版叠加和纯色形状编辑，结构一致且逐像素误差为 0。
这些整数边界样本的通过不能代替下述曲线及分数边缘的抗锯齿验收。

`Rasterize(ctx, mask, bounds, Options{})` 返回独立 `image.Alpha`，`bounds` 使用绝对文档像素坐标。
`RasterizeNRGBA` 返回 RGB 为 coverage、Alpha 为 255 的灰度图。
轮廓使用 cubic Bezier，开放轮廓在填充时闭合；此包不绘制描边。
支持 even-odd/nonzero 填充及 combine、exclude、subtract、intersect 和延续组件。
未知 selector、未识别的组件 metadata 和未知 flags 保留可读；激活后的栅格化明确报告不支持。

默认采用 8×8 中心采样，可选择 4×4；cubic 按像素 tolerance 自适应展平。
`AreaCoverage` 沿 X 精确积分填充区间，再沿 Y 使用固定中点采样；`AreaSamples` 可选 4、8、16、64，
默认为 64。采样使用绝对文档坐标，整图与同位置 ROI 具有相同相位。
`CoverageLevels16` 在全部轮廓和布尔操作组合完成后，对原始覆盖率一次量化为 0、17、…、255：
等级为 `min(15, floor(coverage * 16))`。此选项不先把覆盖率四舍五入到 8 位，也不重复叠加半透明轮廓。
`NoAntialias` 使用像素中心的单次成员判断，仅输出 0 或 255，与面积采样及 16 级量化互斥。
这些覆盖率选项与字体 hinting 分别实现；16 级 Alpha 与原生输出具有相同等级数，不能单独证明像素一致。
内存预算包括输出、展平线段、交点、组件及扫描线；CPU 工作量、分段数量和曲线深度分别有界。
输出坐标限制在 ±2^40 以内，保证转换到浮点数后仍能区分亚像素。

本机 Photoshop 26.1 的真实 PSD/PNG 已验证原始字段、8.24 数值、布尔操作、invert/disabled flags，
并确认普通 raster alpha 未烘焙 vector coverage、纯色填充层的缓存 alpha 已烘焙 coverage。
原始 metadata 一致不能代替像素一致。
当前中心采样尚未匹配 Photoshop 的特殊抗锯齿：严格逐像素 Alpha 误差 2 的验收中，
分数矩形、cubic、自交与斜线仍有失败记录；`verification/vector-independent.json` 保留实际误差。
因此当前栅格输出应视为有界的几何近似，不能声明与 Photoshop 的所有边缘逐像素一致。

格式依据：[Adobe PSD/PSB 规范](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/)。
记录组件语义使用 [psd-tools 官方字段文档](https://psd-tools.readthedocs.io/en/latest/_modules/psd_tools/psd/vector.html)
交叉核验；实现代码独立编写，生产包不依赖 Python、Photoshop 或其他图形库。
