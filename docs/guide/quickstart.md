---
title: 快速开始
description: 用一个完整的 Go 程序走通新建、打开、读取图层、编辑、保存和导出图片。
---
# 快速开始

本页用一个完整程序走通 psdkit 的常用流程：**新建 → 打开 → 读取图层 → 编辑 → 保存 → 导出图片**。每段代码都来自页末的完整示例，可以直接运行。

> [!TIP]
> 只想先看效果？打开 [Playground](../demo/)：示例 PSD 由 Go 生成，在浏览器 Worker 中用同一份 Go 实现打开、编辑和重绘。

## 1. 准备项目

```sh
mkdir psdkit-quickstart && cd psdkit-quickstart
go mod init example.com/quickstart
go get github.com/gopkg-dev/psdkit@v0.3.0
```

本指南对应 `v0.3.0`，支持范围见[该 Tag 的能力矩阵](https://github.com/gopkg-dev/psdkit/blob/v0.3.0/docs/status.md)。`v0.2.0` 的[历史安装与保存重开记录](../releases/verification-2026-10-24.md)保留原版本的核验结果。本地开发时，可用 `go mod edit -replace github.com/gopkg-dev/psdkit=/path/to/psdkit` 指向本地仓库；这与公共版本安装验证分开。

## 2. 新建文档并添加图层

`CreateDocument` 创建透明 RGB8 文档，自带一个透明底层（ID 为 1）。`AddRaster` 在指定父级与位置插入一张 RGB8 图片图层，父级 `0` 表示画布根部，位置按自下而上计数。

```go
editor, err := psdkit.CreateDocument(ctx, 640, 480)
if err != nil {
	return err
}
defer func() { err = errors.Join(err, editor.Close()) }()

card := image.NewNRGBA(image.Rect(0, 0, 320, 200))
for y := range 200 {
	for x := range 320 {
		card.SetNRGBA(x, y, color.NRGBA{R: 34, G: 228, B: 255, A: 255})
	}
}
if _, err := editor.AddRaster(0, 1, "卡片", card, image.Pt(160, 140)); err != nil {
	return err
}
return editor.SavePSD(ctx, path)
```

需要 PSB 时传入 `psdkit.CreateOptions{Version: 2}`。

## 3. 打开文档并读取图层

`OpenEditor` 打开文件并建立编辑器；`Layers` 返回扁平列表，`Tree` 返回带 `Children` 的树。两者都是独立拷贝，同级图层自下而上排列。

```go
editor, err := psdkit.OpenEditor(input)
if err != nil {
	return err
}
defer func() { err = errors.Join(err, editor.Close()) }()

layers, err := editor.Layers()
if err != nil {
	return err
}
for _, layer := range layers {
	fmt.Printf("#%d %s visible=%t opacity=%d bounds=%v\n",
		layer.ID, layer.Name, layer.Visible, layer.Opacity, layer.Bounds)
}
```

对上一步生成的文件，输出类似：

```text
#1 Layer 1 visible=true opacity=255 bounds=(0,0)-(640,480)
#2 卡片 visible=true opacity=255 bounds=(160,140)-(480,340)
```

## 4. 编辑图层

编辑使用稳定的 `LayerID`。不透明度是 0–255 的字节值，204 约为 80%。

```go
top := layers[len(layers)-1].ID
if err := editor.Rename(top, "主视觉"); err != nil {
	return err
}
if err := editor.SetOpacity(top, 204); err != nil {
	return err
}
```

其他常用操作：`SetVisible`、`SetFillOpacity`、`ReplacePixels`、`AddGroup`、`Move`、`Remove`，见[图层树与编辑](editing.md)。

## 5. 保存为 PSD

`SavePSD` 先写临时文件，同步后原子替换目标路径。未修改的数据尽量保持原字节；`Compression` 只影响被修改的通道（0 原始、1 RLE、2 ZIP、3 预测 ZIP）。

```go
if err := editor.SavePSD(ctx, output, psdkit.SaveOptions{Compression: 1}); err != nil {
	return err
}
```

需要写入内存或网络流时使用 `editor.WritePSD(ctx, writer)`。

## 6. 导出图片

`editor.Composite` 根据当前编辑后的图层树重新合成 RGB8 画面，`psdkit.Encode` 写出 PNG 或 JPEG（JPEG 会把透明区域铺白）。

```go
pixels, err := editor.Composite(ctx)
if err != nil {
	return err
}
file, err := os.Create(preview)
if err != nil {
	return err
}
defer func() { err = errors.Join(err, file.Close()) }()
return psdkit.Encode(file, pixels, "png")
```

> [!WARNING]
> 实时合成只覆盖支持范围内的图层。遇到超出范围的效果、滤镜或组合时返回错误，可用 `errors.Is(err, psdkit.ErrCompositeUnsupported)` 识别，而不是得到一张错误的图片。

## 7. 保存预览与实时重绘

读取别人交付的 PSD 时，常常需要区分两种画面：

```go
source, err := psdkit.OpenDocument("poster.psd")
if err != nil {
	log.Fatal(err)
}
defer func() { _ = source.Close() }()

saved, err := psdkit.DecodeDisplayComposite(ctx, source) // 文件内保存的合成图
if err != nil {
	log.Fatal(err)
}
live, err := psdkit.RenderSource(ctx, source) // 根据图层实时重绘
switch {
case errors.Is(err, psdkit.ErrUnsupported):
	log.Printf("该文档超出实时重绘范围：%v", err)
case err != nil:
	log.Fatal(err)
default:
	fmt.Println(saved.Rect, live.Rect)
}
```

保存预览读得快，但可能是 Photoshop 留下的旧缓存或占位图；实时重绘反映图层的当前状态，但只在支持范围内成功。

## 完整示例

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	ctx := context.Background()
	if err := create(ctx, "poster.psd"); err != nil {
		log.Fatal(err)
	}
	if err := edit(ctx, "poster.psd", "edited.psd", "poster.png"); err != nil {
		log.Fatal(err)
	}
}

// create 新建 640×480 文档，添加一个纯色图层并保存为 PSD。
func create(ctx context.Context, path string) (err error) {
	editor, err := psdkit.CreateDocument(ctx, 640, 480)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, editor.Close()) }()

	card := image.NewNRGBA(image.Rect(0, 0, 320, 200))
	for y := range 200 {
		for x := range 320 {
			card.SetNRGBA(x, y, color.NRGBA{R: 34, G: 228, B: 255, A: 255})
		}
	}
	if _, err := editor.AddRaster(0, 1, "卡片", card, image.Pt(160, 140)); err != nil {
		return err
	}
	return editor.SavePSD(ctx, path)
}

// edit 打开文档，读取图层，修改后另存 PSD 并导出 PNG。
func edit(ctx context.Context, input, output, preview string) (err error) {
	editor, err := psdkit.OpenEditor(input)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, editor.Close()) }()

	layers, err := editor.Layers()
	if err != nil {
		return err
	}
	for _, layer := range layers {
		fmt.Printf("#%d %s visible=%t opacity=%d bounds=%v\n",
			layer.ID, layer.Name, layer.Visible, layer.Opacity, layer.Bounds)
	}
	top := layers[len(layers)-1].ID
	if err := editor.Rename(top, "主视觉"); err != nil {
		return err
	}
	if err := editor.SetOpacity(top, 204); err != nil {
		return err
	}
	if err := editor.SavePSD(ctx, output, psdkit.SaveOptions{Compression: 1}); err != nil {
		return err
	}

	pixels, err := editor.Composite(ctx)
	if err != nil {
		return err
	}
	file, err := os.Create(preview)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	return psdkit.Encode(file, pixels, "png")
}
```

运行后得到 `poster.psd`、`edited.psd` 和 `poster.png`：

```sh
go run .
```

## 接下来

- [PSD/PSB 与按需读取](reading.md)：不解码像素读取结构，按通道和区域解码。
- [合成、重绘与导出](rendering.md)：区域、分块、画板和模板套图。
- [预算、取消与错误](limits.md)：在服务端安全处理用户上传的文件。
