---
title: 智能滤镜
description: 解析智能滤镜栈与缓存，在支持范围内修改参数并自动更新像素、保存重开；近似模型须显式选择。
---
# 智能滤镜

智能滤镜附着在智能对象上，文件同时保存了有序参数和滤镜缓存。psdkit 能完整解析参数，读取缓存区域，并为一部分滤镜提供**声明模型**的实时计算——这些模型由调用方显式选择，不是 Photoshop 的私有算法。

## 读取

| 入口 | 说明 |
| --- | --- |
| `psdkit.DecodeSmartFilters` | 解析智能对象上的有序滤镜栈 |
| `source.FilterCaches` / `ReadFilterCacheView` | 滤镜缓存索引 |
| `source.DecodeFilterCacheRegion` | 显式读取缓存的某个区域 |

## 编辑

`Editor.SetSmartFilterParameters(ctx, id, stack, options)` 修改现有滤镜的已知参数。省略 `Pixels` 时，库从当前智能对象来源重新投影、执行新滤镜栈，再一起提交参数和图层像素。支持范围内可直接保存 PSD/PSB、重新打开并继续编辑，无需手动提供准确像素或额外合成图。

```go
// stack 来自该层已有的滤镜栈，修改其已知参数后自动更新像素。
err := editor.SetSmartFilterParameters(ctx, id, stack, psdkit.SmartFilterEditOptions{})
if err != nil {
	return err
}
return editor.Save(ctx, "edited.psd", psdkit.SaveOptions{})
```

空选项只执行下表中的默认子集。非零高斯模糊等近似算法，必须通过 `Render.SmartFilters` 显式选择；重新打开后再次编辑时仍需传入相应选项：

```go
err := editor.SetSmartFilterParameters(ctx, id, stack, psdkit.SmartFilterEditOptions{
	Render: &psdkit.SceneOptions{
		SmartFilters: &psdkit.SmartFilterOptions{ApproximateGaussian: true},
	},
})
```

`Render` 也可携带外链解析器、来源色彩与受支持的投影策略。自动编辑限 RGB8 父文档、已有的现代栅格智能对象及可实时重绘的来源和几何组合；禁用整个栈或所有有效滤镜会恢复当前来源投影。原有滤镜蒙版仍生效，图层不透明度、像素／矢量蒙版和效果在后续合成中应用。只更新所选实例，不更换共享来源或其他实例，也不会在旧滤镜缓存上重复执行新滤镜。

来源解析、参数资格、预算或重绘失败时，本次参数和像素均不提交。解析回调期间若发生其他编辑，返回 `ErrStaleLinkedEdit`，保留已经完成的其他编辑。自动路径不支持输出色彩空间转换和缓存来源回退；相对蒙版因新像素边界发生位移等不支持的组合返回 `unsupported`。

已有准确像素时仍可传入 `SmartFilterEditOptions{Pixels: accuratePixels}`，与 `Render` 二选一。像素矩形位于父文档坐标，内容是投影和滤镜之后、图层不透明度／蒙版／效果之前的 straight-alpha RGB8 图像；库复制并持有像素。

| 滤镜 | 可编辑的已有参数 |
| --- | --- |
| 高斯模糊 | 半径 |
| 表面模糊 | 半径、整数阈值 |
| 智能锐化 | 半径、数量、降噪；保留已有 blur / legacy 模式 |
| 亮度 / 对比度 | 亮度、对比度和已有 legacy 字段 |
| 色相 / 饱和度 | 主范围与已有色彩范围的已知参数；保留着色模式及范围数量、顺序 |

还可修改整个栈和每个滤镜的启用状态、单滤镜不透明度。滤镜族、数量、顺序、混合模式、智能对象变换、共享来源与未知元数据保持。重复、缺失或类型不明确的待编辑字段会原子拒绝。

`source.EncodeSmartFilterParameters` 仅返回更新后的 SoLd / SoLE 参数块，不更新图层栅格或完整合成图。编辑器的自动路径复用已有算法，不扩大滤镜、来源与合成组合的支持范围。若已通过显式 `Pixels` 更新图层，但文档中其他组合无法自动合成，保存仍须提供准确的 `Composite` 或 `CompositeImage`。

## 替换来源时同步重算

`ReplaceContents` 以及外链转嵌入、保留外链更新，会从新来源重新计算全部共享实例的当前滤镜，而不是只替换滤镜之前的像素。各实例保留自己的变换、滤镜参数和原滤镜蒙版；当前尚未保存的参数也不会回退。父图层蒙版、效果和透明度保持独立，任一实例失败时整次来源替换不提交。

带选项的入口通过 `SmartContentsOptions.SmartFilters` 直接选择策略，例如 `SmartFilters: &psdkit.SmartFilterOptions{ApproximateGaussian: true}`。这里不使用参数编辑入口的 `Render.SmartFilters` 嵌套。nil 和空选项仅允许严格子集；近似须每次显式开启，重开后也不会自动继承授权。可直接自动保存 PSD/PSB、读取保存预览，再继续替换来源或编辑滤镜，完整用法见[智能对象与外链](smart-objects.md#显式渲染策略)。

## 实时计算

```go
filtered, err := psdkit.ApplySmartFilters(ctx, pixels, pipeline, psdkit.SmartFilterOptions{
	ApproximateGaussian:    true,
	ApproximateSurfaceBlur: true,
})
```

| 滤镜 | 显式选项 |
| --- | --- |
| 高斯模糊 | 半径为 0 时恒等；非零半径需 `ApproximateGaussian` |
| 表面模糊 | 半径或阈值为 0 时恒等；其他值需 `ApproximateSurfaceBlur` |
| 智能锐化 | 已知现代 lens-blur 模式的半径或数量为 0 时恒等；其他值需 `ApproximateSmartSharpen` |
| 亮度 / 对比度 | 中性参数恒等；非中性需 `ApproximateBrightnessContrast` |
| 色相 / 饱和度 | 纯 Master Lightness 默认支持；其他非中性控制需 `ApproximateHueSaturation` |

`psdfilter.ApplyRegion` 只计算一个区域，`RequiredHalo` 给出所需的邻域扩展，`WorkingEstimate` 估算内存。整图重绘时通过 `SceneOptions.SmartFilters` 传入同一组选项。只包含上述恒等子集或纯 Master Lightness 的栈，可以传 `SmartFilters: &psdfilter.Options{}`，无需开启任何近似选项。

恒等子集仍检查参数范围、已知算法模式和 Normal 滤镜混合；不会因为半径或数量为 0 就忽略未知活跃参数、非 Normal 混合或非法字段。输出保留原始 alpha 和透明像素的隐藏 RGB，且仍受输出预算及取消控制。

> [!CAUTION]
> 独立调用 `RenderSource` 等实时入口时，`SmartFilters` 为 nil 仍拒绝活动智能滤镜；自动参数编辑和来源替换按空 `SmartFilters` 选择严格默认子集。这些入口都要求超出默认子集的算法显式开启对应近似选项。完整智能滤镜栈尚未完成。

默认上限：两千万像素、256 MiB 工作内存、4096 个滤镜，可通过 `MaxPixels`、`MaxWorkingBytes`、`MaxFilters` 与 `MaxSampleOperations` 调整。
