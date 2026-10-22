---
title: Go API
description: psdkit 的包结构、常用入口与 API 约定；完整签名以 pkg.go.dev 与源码注释为准。
---
# Go API

模块路径为 `github.com/gopkg-dev/psdkit`。根包 `psdkit` 汇总了常用入口与类型别名，适合大多数应用直接使用；需要更细控制时再引入对应子包。完整签名与注释见 [pkg.go.dev](https://pkg.go.dev/github.com/gopkg-dev/psdkit)（首次公开并被索引后可用），或在仓库中运行 `go doc`。

```sh
go doc github.com/gopkg-dev/psdkit
go doc github.com/gopkg-dev/psdkit.Editor
```

## 常用入口

| 任务 | 入口 |
| --- | --- |
| 打开原始文档 | `OpenDocument` / `ParseDocument` / `ReadDocument` → `*SourceDocument` |
| 编辑与保存 | `OpenEditor` / `NewEditor` / `CreateDocument` → `*Editor`；`SavePSD` / `WritePSD` |
| 保存预览 | `DecodeDisplayComposite` / `DecodeDisplayRegion` |
| 实时重绘 | `RenderSource` / `RenderSourceTile` / `VisitSourceTiles` / `RenderArtboards` |
| 高位深 | `RenderSourcePrecision` / `ConvertSamples` |
| RGB8 快照合成 | `source.Decode` + `Composite` / `CompositeRegion` |
| 智能对象 | `Editor.ReplaceContents`、`BuildRasterContents`、`RenderSourcePlacedLayer` |
| 文字与字体 | `OpenFont`、`LayoutOpenType`、`RasterizeFontRun`、`RenderTextLayer` |
| 色彩 | `ReadSourceRGBProfile`、`TransformProfile` |
| 编码 | `Encode`（PNG / JPEG）、`PrepareOutput` |
| 模板套图 | `NewRenderer`、`Inspect`、`DefaultConfig`、`ValidateConfig` |

## 包结构

| 包 | 职责 |
| --- | --- |
| `psdkit` | 统一入口与类型别名 |
| `psdfile` | 原始 PSD/PSB 索引、按需解码、Descriptor、区间读取 |
| `psdedit` | 受控编辑与 PSD/PSB writer |
| `psdmodel` / `psdparse` | 解码后的文档模型与 RGB8 兼容解析 |
| `psdcompose` | 从已解码图层树重建 RGB8 画面 |
| `psdscene` | 从原始文档有界重建 RGB8 画面，递归解析智能对象 |
| `psdrender` | 模板套图引擎与 PNG/JPEG 编码 |
| `psdprecision` | 不经 8 位量化的源精度合成 |
| `psdcolor` | 原通道样本到显示像素的色彩转换与 ICC |
| `psdfont` / `psdtext` | 字体读取、排版、光栅化；文字 EngineData |
| `psdwarp` | 仿射、透视、单片与网格 Warp |
| `psdeffects` / `psdadjust` / `psdfilter` | 图层效果、调整、智能滤镜 |
| `psdvector` / `psdgradient` / `psdpattern` | 矢量路径、渐变、图案 |
| `psdweb` | 浏览器 Worker 使用的有界内存协议 |

## API 约定

- **Context 优先**：耗时操作的第一个参数是 `context.Context`，用于取消与超时。
- **显式所有权**：`Open*` 拥有资源需要 `Close`；`Parse*` / `Read*` 借用调用方数据，使用期间不得修改。返回的像素是独立拷贝。
- **预算可配置**：各入口的 `Options` 都有 `Max*` 字段，零值选择默认值，负值无效。
- **错误可识别**：使用 `errors.Is` 判断 `ErrUnsupported`、`ErrLimit` 等类别，用 `errors.AsType[*psdkit.ReadError]` 读取结构化位置。
- **近似需显式**：所有 `Approximate*` 选项默认关闭。
- **坐标与位深**：注释会说明坐标域（文档、图层、通道局部）和样本格式；不要隐式转换为 RGB8。

> [!NOTE]
> 当前尚未发布 v1，公共 API 仍可能调整。浏览器 Playground 的能力目录由源码注释自动生成（`go run ./cmd/psdkit-web-docs`），与本页使用相同的 Go 入口。

## 浏览器

浏览器中通过 Worker SDK 调用同一组入口，参数与返回值按 Go 类型做 JSON 映射，二进制独立传输。见[浏览器 SDK](../browser.md)。
