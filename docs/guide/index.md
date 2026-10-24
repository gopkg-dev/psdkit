---
title: 概览
description: psdkit 是什么、适合做什么、如何安装，以及一个最小可运行示例。
---
# psdkit 概览

psdkit 是纯 Go 的 Photoshop PSD/PSB 工具库：按需读取图层与通道、受控编辑并保存为可继续编辑的文档，再把支持范围内的图层树渲染为 PNG/JPEG。核心库只依赖 Go 标准库，同一份实现可以编译为 WebAssembly，在浏览器 Worker 中本机处理文件。

<ul class="card-grid">
  <li><a href="quickstart.html"><strong>快速开始</strong><span>新建、打开、编辑、保存、导出，五分钟跑通。</span></a></li>
  <li><a href="../status.html"><strong>能力矩阵</strong><span>读取、编辑、保存预览、实时重绘、近似策略分别报告。</span></a></li>
  <li><a href="../browser.html"><strong>浏览器 SDK</strong><span>Go Wasm + Web Worker，文件不离开本机。</span></a></li>
  <li><a href="../demo/"><strong>Playground</strong><span>导入自己的 PSD，直接调用真实 Go 入口。</span></a></li>
</ul>

## 适合的场景

- **模板与批量出图**：打开设计师交付的 PSD，替换智能对象内容或图片层像素，批量渲染 PNG/JPEG。
- **文档检查与索引**：不解码像素即可列出图层树、资源、通道区间和 Descriptor，适合上传校验与资产管理。
- **受控编辑后回存**：改名、显隐、不透明度、像素替换、增删图层后保存为 PSD/PSB，未修改的数据尽量保持原字节。
- **浏览器内处理**：在 Web Worker 中运行同一份 Go 实现，用户文件只在本机内存中处理。

> [!IMPORTANT]
> psdkit 不是 Photoshop 的完整替代品。读取、保存、显示文件内保存的预览和实时重绘是不同的能力；超出支持范围时库返回 `unsupported` 等结构化错误，而不是伪造画面。完整边界见[能力矩阵](../status.md)。

## 安装

需要 **Go 1.27.3+**。安装模块：

```sh
go get github.com/gopkg-dev/psdkit@v0.3.0
```

本指南对应 `v0.3.0`，变更见[发布说明](../releases/v0.3.0.md)，支持范围以[该 Tag 的能力矩阵](https://github.com/gopkg-dev/psdkit/blob/v0.3.0/docs/status.md)为准。`v0.2.0` 的[历史交付核验](../releases/verification-2026-10-24.md)不包含后续版本新增能力。

本地开发时，可以用 module `replace` 指向本地仓库：

```sh
go mod edit -replace github.com/gopkg-dev/psdkit=/path/to/psdkit
```

## 最小示例

新建一个 640×480 的透明 RGB8 文档并保存为 PSD：

```go
package main

import (
	"context"
	"log"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	ctx := context.Background()
	editor, err := psdkit.CreateDocument(ctx, 640, 480)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = editor.Close() }()
	if err := editor.SavePSD(ctx, "example.psd"); err != nil {
		log.Fatal(err)
	}
}
```

仓库自带的示例无需外部素材即可运行：

```sh
go run ./examples/create -out /tmp/example.psd
go run ./examples/index -psd /tmp/example.psd
go run ./examples/edit -psd /tmp/example.psd -out /tmp/edited.psd
```

## 核心概念

| 概念 | 说明 |
| --- | --- |
| `SourceDocument` | 原始文档索引。保留资源、未知块和通道区间，像素按需解码，不会预先分配整图。 |
| `Editor` | 受控编辑层。源文档保持不可变，修改以覆盖层记录，保存时写出新文档。 |
| `LayerID` | 编辑器内稳定的图层 ID，从 `Layers` / `Tree` 读取，不等于图层在文件中的序号。 |
| 保存预览 | PSD 内保存的合成图或图层像素，可能是旧缓存，读取快、覆盖面广。 |
| 实时重绘 | Go 根据图层树重新计算画面，只在支持范围内输出。 |
| 显式近似 | 少数高级效果提供 `Approximate*` 选项，默认关闭，需要调用方明确开启。 |

## 下一步

- 跟随[快速开始](quickstart.md)完成一次完整的读写流程。
- 在[能力矩阵](../status.md)确认你的文档用到的特性是否在支持范围内。
- 打开 [Playground](../demo/) 导入自己的 PSD，查看图层树与两种预览来源。
