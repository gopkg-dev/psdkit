---
title: 文字与字体
description: 解析文字图层与 EngineData，显式加载字体，进行排版、塑形、光栅化与受限的文字编辑。
---
# 文字与字体

`psdtext` 解析文字图层（TySh）与 EngineData；`psdfont` 读取调用方提供的 TrueType、CFF1 与 CFF2 字体，提供有限的排版、塑形、Hinting、光栅化和可变字体信息。

> [!IMPORTANT]
> psdkit **不读取系统字体目录，也不自动替换缺失字体**。所有字体都由调用方显式提供；缺字体时返回 `missing_font` / `missing_glyph`，而不是用别的字体顶替。

## 读取文字

| 入口 | 说明 |
| --- | --- |
| `source.ReadLayerMetadata` | 图层中的文字记录与原始描述符 |
| `source.DecodeTextEngine` / `DecodeEngineData` | 解析图层级 EngineData |
| `source.ReadDocumentTextView` / `ReadDocumentTextAssociations` | 文档级文字数据与图层关联 |

## 加载字体与排版

```go
face, err := psdkit.OpenFont(ctx, "fonts/Inter-Regular.ttf")
if err != nil {
	return err
}
resolver := psdkit.FontResolverFunc(func(ctx context.Context, name string) (*psdkit.FontFace, error) {
	if name == "Inter-Regular" {
		return face, nil
	}
	return nil, psdfont.ErrMissingFont
})
style := psdkit.FontStyle{FontName: "Inter-Regular", Size: 48}
run, err := psdkit.LayoutOpenType(ctx, "Hello", style, resolver, psdkit.OpenTypeShapeOptions{})
if err != nil {
	return err
}
coverage, err := psdkit.RasterizeFontRun(ctx, run, image.Rect(0, 0, 320, 80)) // *image.Alpha
```

| 能力 | 入口 |
| --- | --- |
| 解码字体 | `psdfont.Decode`、`psdkit.OpenFont` |
| 排版 / 塑形 | `psdfont.Layout`、`psdkit.LayoutOpenType`、`psdkit.ShapeOpenType`（GSUB / GPOS） |
| 光栅化 / Hinting | `RasterizeFontRun`、`psdfont.HintRun`（CFF1 hint，TrueType 指令不执行） |
| 可变字体 | `Face.VariationAxes`、`NamedVariationInstances`、`VariationMetrics` |
| 文字图层重绘 | `RenderTextLayer`、`RenderOpenTypeTextLayer`，或 `SceneOptions.ApproximateText` |

## 编辑文字

`Editor.SetTextContent(ctx, id, text, options)` 修改**单样式水平点文字**的 Unicode 内容，包括中文、emoji、组合字符、多行及空文本。必须同时提供准确的新图层像素：Photoshop 打开文件时不会自动刷新保存的文字栅格。这个入口保留现有字体与样式，不新增文字排版或字形生成能力。

```go
if err := editor.SetTextContent(ctx, id, "中文标题 😀\n第二行", psdedit.TextEditOptions{
	Pixels: accurateTextPixels,
}); err != nil {
	return err
}
```

`Pixels` 是应用图层不透明度、蒙版与效果之前的 straight-alpha RGB8 图像；其 `Rect` 是新的绝对图层边界。库复制这些像素并校验布局，但不校验画面是否与文字一致。

CRLF 和 LF 统一为 CR，字符与段落范围按 **UTF-16 code unit** 重建：补充平面的 emoji 占两个单位，组合字符保持原码点序列，不做 Unicode 归一化。`Txt ` 与 EngineData 同步保存，未知描述符、字体参数、变换位型及尾部字节保留；PSD/PSB 重开后可继续编辑。

非法 UTF-8 返回 `ErrInvalidEdit`；除 CR/LF 外的控制字符（含 NUL、Tab）和 U+2028/U+2029 行／段落分隔符返回 `ErrUnsupported`。现代 Txt2、多字符样式、不同段落样式、段落文字、竖排和带 Warp 的文字仍不支持。`SetNativeTextState` 可导入由原生应用生成的文字状态。

## 尚未完成

- 完整排版与复杂文字脚本；
- TrueType 字节码执行；
- Photoshop 私有 Sharp 抗锯齿的完整覆盖率。

字形轮廓、字宽、覆盖率与像素一致性分别验证，详见[能力矩阵](../status.md)。
