# psdeffects

纯 Go 的现代图层效果参数与独立 coverage primitive，生产包只依赖标准库。
`psdfile.Document.DecodeEffects(ctx, block)` 读取 `lfx2`/`lmfx` 的 object version 0、descriptor version 16。
整个 descriptor 的有序 entries、重复 keys、unknown raw spans、IEEE unit bits 和内部 padding spans 均保留。
返回数据独立于来源生命周期；原始文件和输入 alpha 均不修改。

支持 RGB ColorOverlay（`SoFi`/`solidFill`），以及基本 DropShadow、OuterGlow 与纯色 Stroke 的参数：颜色、混合模式、透明度、size、spread、
distance、angle/globalLight、线性 contour、stroke 位置，以及原始 metadata。
全局光照读取资源 1037，缺省使用格式规范中的 30 度；资源 1042 控制文档效果可见性。
非线性 contour、noise/jitter、非默认 glow range/technique、gradient/pattern、overprint、
未知效果家族及未知启用语义，读取后保留，激活渲染时明确拒绝。

`Rasterize(ctx, sourceAlpha, set, Options{})` 返回 owned `Background`/`Interior`/`Foreground` coverage planes 和扩展 bounds。
coverage 不预乘 effect opacity、RGB 或 blend mode；源 alpha 应代表应用 Fill opacity 之前的形状。
DropShadow/OuterGlow 位于背景，ColorOverlay 位于内层，Stroke 位于前景；最终 body/effect 合成由调用方负责。
Stroke 在 body 上需按未乘 effect opacity 的 coverage 挖空，再绘制 stroke。
`LayerConceals` 为 true 的阴影按源形状 alpha 挖空，即使 Fill 为 0 仍保留外部阴影。

默认工作内存 256 MiB、sample work 1 亿次、128 个启用效果；预算包含各输出及临时缓冲。
取消、超限或不支持的语义不会返回部分结果。
描边使用像素中心距离的一像素线性边缘；full-spread glow 按默认 50% range 归一该边缘。
非零 blur 暂使用分离高斯近似，不能声明所有 Photoshop 效果逐像素一致。

`verification/effects-independent.json` 保留六个真实 Photoshop 26.1 样本的严格逐像素结果：
硬投影与 Fill0 投影误差 0，outside stroke 误差 0，full-spread glow 与 glow/insideStroke/Fill50 组合误差 1。
非零 blur/globalLight 投影仍最大误差 15，整组验收保持 failed，阈值 2 未放宽。
这些样本不证明不同尺寸、分数边界、复杂半透明形状或所有描边位置均与 Photoshop 一致。

`psdcompose` 的默认接入保持严格边界：普通 Normal raster、无蒙版/剪贴/自定义 Blend If，
投影、发光和描边的效果混合模式为 Normal；投影为零 blur 和整数位移，发光为满 spread/默认 range 与 2 像素 radius，
描边为 inside/outside 的 2 像素 width。发光及描边要求源 alpha 为 0/255。
柔化投影、center/fractional 描边、fractional 位移和未校准组合返回 `ErrUnsupported`，
不会默认调用近似 primitive。
参数编辑的 size 3 样本分别出现 Stroke 最大误差 15、Glow 最大误差 18，
因此其它整数 size 也不默认支持；`verification/effects-edit-size3-independent.json` 保留失败历史证据。
`EstimateWorking` 供合成器提前保留所有 coverage、shape alpha 和最大 scratch 峰值。

ColorOverlay 已独立接入 `psdcompose` 的 26 种 RGB 确定性混合模式，包括特殊 Fill 内核、RGB 分量混合、
Subtract/Divide。支持源 alpha 的全范围，以及图层 opacity、Fill 和透明/不透明背景的组合。
缺省 `infx=false` 时先把 Fill 绘到原背景，在该颜色/alpha 上应用 ColorOverlay，再按原形状 alpha 和
图层 opacity 插值；`infx=true` 时先在不透明源颜色上套内层样式，随后统一应用 body Fill 和原形状。
因此样式不会把部分源 alpha 重复乘两次，且 `infx=true` 的 Fill0 会同时抑制内层颜色叠加。
多 ColorOverlay 实例保留原 descriptor 次序，第一项在最上方；`Interior` coverage 按实际从下到上的
绘制顺序返回。受控编辑保持实例数和原语义，允许修改各项颜色、opacity、mode，并同步 legacy
`sofi` 中代表第一项的两组 RGB、opacity、blend；仅修改其它项时 legacy 原字节不变。
硬投影、inside 2-pixel 描边与 ColorOverlay 的组合及 Fill0 已通过原生对照，仍继承这些效果原有的几何边界。
蒙版、剪贴、自定义 Blend If、非 Normal 图层模式及其余高级家族交互仍需额外原生验证。

`verification/color-overlay-evidence.json` 汇总 74 条真实 Photoshop 对照：67 条实时源图层渲染、
5 条保存后原生打开/编辑/强制刷新、2 条 `infx=true` 分组控制；alpha 最大误差 1、premul RGB 最大误差 2。
`color-overlay-accepted-native-manifest.json` 可直接用于重新跑 67 条 source render。
最初 `subtract`/`divide` action string ID 被 Photoshop 静默退回 Normal 的四份控制保留为无效控制，
改用实际 `blendSubtraction`/`blendDivide` ID 的四份新文件才计入对应模式覆盖；失败历史没有删除。
该汇总绑定效果相关源码文件，整个模块的不可变 snapshot/checkpoint 是单独的验证门。

格式依据：[Adobe PSD/PSB 格式规范](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/)。
效果 UI 参数依据：[Adobe 图层效果说明](https://helpx.adobe.com/photoshop/using/layer-effects-styles.html)。
descriptor 字段以 [psd-tools 官方 API](https://psd-tools.readthedocs.io/en/latest/reference/psd_tools.api.effects.html)
和真实样本交叉核查；实现代码独立编写。
