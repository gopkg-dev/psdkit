---
title: 图层树与编辑
description: 读取图层树、使用稳定 LayerID 修改属性与层级，并保存为可继续编辑的 PSD/PSB。
---
# 图层树与编辑

`Editor` 持有可变的编辑覆盖层，原始文档保持不可变。所有修改以稳定的 `LayerID` 定位，保存时写出新的 PSD/PSB；未修改的数据尽量保持原字节。

## 打开编辑器

```go
editor, err := psdkit.OpenEditor("input.psd")
if err != nil {
	return err
}
defer func() { _ = editor.Close() }()
```

| 入口 | 说明 |
| --- | --- |
| `OpenEditor(path)` | 打开文件并拥有它 |
| `NewEditor(source)` | 借用已打开的 `SourceDocument`；关闭编辑器不会关闭该源 |
| `CreateDocument(ctx, w, h)` | 新建透明 RGB8 文档；`CreateOptions{Version: 2}` 选择 PSB |

## 读取图层树

```go
tree, err := editor.Tree()
if err != nil {
	return err
}
var walk func(nodes []psdkit.EditLayerInfo, depth int)
walk = func(nodes []psdkit.EditLayerInfo, depth int) {
	for i := len(nodes) - 1; i >= 0; i-- { // 同级自下而上排列，倒序即面板顺序
		layer := nodes[i]
		fmt.Printf("%s#%d %s group=%t\n", strings.Repeat("  ", depth), layer.ID, layer.Name, layer.Group)
		walk(layer.Children, depth+1)
	}
}
walk(tree, 0)
```

`EditLayerInfo` 是独立拷贝，包含 `ID`、`Parent`、`Name`、`Visible`、`Opacity`、`FillOpacity`、`Bounds`、`Group`、`Expanded`、`SourceIndex`、`PSDID` 和 `Children`。

| 方法 | 返回 |
| --- | --- |
| `Tree` | 带 `Children` 的图层树 |
| `Layers` | 扁平列表 |
| `Layer(id)` | 单个图层 |
| `Changes` | 当前修订号、是否有未保存修改、是否改动合成或结构 |

## 修改属性与层级

```go
layers, err := editor.Layers()
if err != nil {
	return err
}
if len(layers) == 0 {
	return errors.New("document has no editable layers")
}
id := layers[0].ID
if err := editor.Rename(id, "Artwork"); err != nil {
	return err
}
if err := editor.SetOpacity(id, 204); err != nil { // 0–255
	return err
}
```

| 操作 | 说明 |
| --- | --- |
| `Rename` / `SetVisible` / `SetOpacity` | 基础属性 |
| `SetFillOpacity` | 独立设置 0–255 填充不透明度 |
| `ReplacePixels` | 用同尺寸 RGB8 像素替换普通图层 |
| `ReplaceChannel` | 按原位深替换现有通道 |
| `AddRaster(parent, position, name, pixels, origin)` | 插入图片图层，父级 `0` 为画布根 |
| `AddGroup(parent, position, name, expanded)` | 新建图层组 |
| `Move(id, parent, position)` / `Remove(id)` | 调整层级或删除 |

更多参数化编辑：`SetSolidFill`、`SetGradientFill`、`SetPatternFill`、`SetVectorMask`、`SetAdjustment`、`SetEffects`、`SetSmartFilterParameters`、`SetTextContent`、`ReplaceContents`，分别见[效果、调整与填充](effects.md)、[智能滤镜](filters.md)、[文字与字体](text.md)和[智能对象](smart-objects.md)。

## 保存

```go
if err := editor.SavePSD(ctx, "edited.psd", psdkit.SaveOptions{Compression: 1}); err != nil {
	return err
}
```

- `SavePSD` / `Save` 写临时文件、同步后原子替换；`WritePSD` / `Write` 写入任意 `io.Writer`。
- 格式沿用原 Header，更换扩展名**不会**转换 PSD/PSB。
- `Compression` 只作用于修改过的通道：0 原始、1 RLE、2 ZIP、3 预测 ZIP。

> [!NOTE]
> 能读取并原样保存某种高级图层，不代表能实时重绘它。编辑后的自动预览受[合成能力](rendering.md)限制；部分高级编辑需要调用方同时提供准确像素，例如 `SetTextContent`。

业务中需要追踪关闭错误时，用命名返回值配合 `errors.Join`：

```go
defer func() { err = errors.Join(err, editor.Close()) }()
```
