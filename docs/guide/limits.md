---
title: 预算、取消与错误
description: 用预算和 Context 安全处理不可信文件，并用 errors.Is / errors.AsType 识别结构化错误。
---
# 预算、取消与错误

psdkit 面向可能处理用户上传文件的服务：每个入口都有独立的加载、解码、工作和输出预算，所有耗时操作接受 `context.Context`，错误保留可识别的类型和结构化位置。

## 资源预算

读取预算通过 `LoadOptions.Limits` 设置，零值选择默认值，负值无效：

| 字段 | 默认 | 含义 |
| --- | --- | --- |
| `MaxFileBytes` | 500 MiB | 文件大小 |
| `MaxMetadataBytes` | 32 MiB | 索引保留的元数据 |
| `MaxReadBytes` | 8 MiB | 单次原始读取 |
| `MaxBlocks` / `MaxLayers` | 100,000 / 10,000 | 块数与图层数 |
| `MaxDecodePixels` | 20,000,000 | 单次解码像素 |
| `MaxDecodeBytes` | 256 MiB | 单次解码缓冲 |
| `MaxDescriptorDepth` / `MaxDescriptorItems` | 32 / 100,000 | Descriptor 嵌套与条目 |

```go
limits := psdkit.DefaultSourceLimits()
limits.MaxFileBytes = 64 << 20
source, err := psdkit.OpenDocument(path, psdkit.LoadOptions{Limits: limits})
```

合成、滤镜、效果、色彩、Warp 等操作各自有 `MaxWorkingBytes`、`MaxPixels`、`MaxOutputBytes` 等选项。区域读取并不意味着 ZIP 压缩数据可以跳过完整校验。

> [!NOTE]
> 预算约束的是单次操作的分配，不是整个进程的 RSS 上限。大文件服务仍应配合进程级限制与并发控制。

## 取消

所有耗时入口都检查 Context。设置超时或在请求结束时取消，即可中止解码、合成和编码：

```go
ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
defer cancel()
pixels, err := psdkit.RenderSource(ctx, source)
if errors.Is(err, context.DeadlineExceeded) {
	// 超时：返回 503 或降级到保存预览
}
```

关闭源文档会等待在途操作结束；已经返回的独立像素仍然有效。

## 错误处理

使用 `errors.Is` 判断类别，而不是匹配错误文本：

| 错误 | 含义 |
| --- | --- |
| `psdkit.ErrMalformed` | 文件无效或被截断 |
| `psdkit.ErrLimit`、`ErrCompositeLimit`、`ErrColorLimit` | 超出预算 |
| `psdkit.ErrUnsupported`、`ErrUnsupportedDecode`、`ErrCompositeUnsupported`、`ErrUnsupportedEdit`、`ErrColorUnsupported` | 超出支持范围 |
| `psdkit.ErrInvalidEdit` | 编辑参数无效 |
| `psdkit.ErrClosed` | 源文档已关闭 |
| `psdfont.ErrMissingFont` / `ErrMissingGlyph` | 缺字体或字形 |

读取错误还携带结构化位置：

```go
if readErr, ok := errors.AsType[*psdkit.ReadError](err); ok {
	log.Printf("feature=%s path=%s offset=%d", readErr.Feature, readErr.Path, readErr.Offset)
}
```

## 浏览器中的错误码

Worker SDK 把同样的错误映射为 `code`，并保留 `message`、`path`、`feature`、`offset` 与 `audit`：

| code | 场景 |
| --- | --- |
| `unsupported` | 超出支持范围，不返回替代图像 |
| `resource_limit` | 超出工作区预算 |
| `missing_font` / `missing_glyph` | 需要导入字体 |
| `invalid_request` | 参数、句柄或资产无效 |
| `canceled` | 超过 Go deadline 或被取消 |
| `worker_timeout` | SDK 看门狗超时，工作区已释放 |

失败的调用会清理本次新增的句柄与资产，但不声称回滚已经提交的编辑。详见[浏览器 SDK](../browser.md)。
