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

`Editor.SetTextContent` 只修改**单样式拉丁点文字**，并且必须同时提供准确的新图层像素——Photoshop 打开文件时不会自动刷新保存的文字栅格。现代 Txt2 文字、多样式、段落文字和带 Warp 的文字返回 `ErrUnsupported`。`SetNativeTextState` 导入由原生应用生成的文字状态。

## 尚未完成

- 完整排版与复杂文字脚本；
- TrueType 字节码执行；
- Photoshop 私有 Sharp 抗锯齿的完整覆盖率。

字形轮廓、字宽、覆盖率与像素一致性分别验证，详见[能力矩阵](../status.md)。
