---
title: 智能滤镜
description: 解析智能滤镜栈与缓存，编辑参数，并在显式选择的模型下实时或按区域计算。
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

`Editor.SetSmartFilterParameters` 修改受支持滤镜的参数，`source.EncodeSmartFilterParameters` 只编码参数而不修改文档。

## 实时计算

```go
filtered, err := psdkit.ApplySmartFilters(ctx, pixels, pipeline, psdkit.SmartFilterOptions{
	ApproximateGaussian:    true,
	ApproximateSurfaceBlur: true,
})
```

| 滤镜 | 显式选项 |
| --- | --- |
| 高斯模糊 | `ApproximateGaussian` |
| 表面模糊 | `ApproximateSurfaceBlur` |
| 智能锐化 | `ApproximateSmartSharpen` |
| 亮度 / 对比度 | `ApproximateBrightnessContrast` |
| 色相 / 饱和度 | `ApproximateHueSaturation` |

`psdfilter.ApplyRegion` 只计算一个区域，`RequiredHalo` 给出所需的邻域扩展，`WorkingEstimate` 估算内存。整图重绘时通过 `SceneOptions.SmartFilters` 传入同一组选项。

> [!CAUTION]
> 未开启对应选项时，含这些滤镜的图层不会被实时重绘，而是返回 `unsupported`。完整智能滤镜栈尚未完成。

默认上限：两千万像素、256 MiB 工作内存、4096 个滤镜，可通过 `MaxPixels`、`MaxWorkingBytes`、`MaxFilters` 与 `MaxSampleOperations` 调整。
