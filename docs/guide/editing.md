---
title: 图层树与编辑
description: 读取图层树、使用稳定 LayerID 修改属性与层级，并保存为可继续编辑的 PSD/PSB。
---
# 图层树与编辑

`Editor` 持有可变的编辑覆盖层，原始文档保持不可变。所有修改以编辑器会话内稳定的 `LayerID` 定位，保存时写出新的 PSD/PSB；未修改的数据尽量保持原字节。

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

`EditLayerInfo` 是独立拷贝，包含 `ID`、`Parent`、`Name`、`Visible`、`Opacity`、`FillOpacity`、`BlendMode`、`Clipping`、`Bounds`、`Group`、`Expanded`、`SourceIndex`、`PSDID` 和 `Children`。

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
| `SetBlendMode` / `SetClipping` | 混合模式与剪贴属性 |
| `ReplacePixels` | 用同尺寸 RGB8 像素替换普通图层 |
| `ReplaceChannel` | 按原位深替换现有通道 |
| `AddPixelMask` / `RemovePixelMask` | 新增或删除用户像素蒙版，保留矢量蒙版 |
| `AddVectorMask` / `SetVectorMask` / `RemoveVectorMask` | 新增、修改或删除受支持的矢量蒙版，保留独立用户像素蒙版 |
| `AddRaster(parent, position, name, pixels, origin)` | 插入图片图层，父级 `0` 为画布根 |
| `AddGroup(parent, position, name, expanded)` | 新建图层组 |
| `Move(id, parent, position)` / `Remove(id)` | 调整层级或删除 |

更多参数化编辑：`SetSolidFill`、`SetGradientFill`、`SetPatternFillWithOptions`、`SetAdjustment`、`SetEffects`、`SetSmartFilterParameters`、`SetTextContent`、`ReplaceContents` 和 `SetSmartObjectTransform`，分别见[效果、调整与填充](effects.md)、[智能滤镜](filters.md)、[文字与字体](text.md)、[智能对象](smart-objects.md)和[变换编辑](warp.md)。

`SetSmartFilterParameters` 在已有算法与来源支持范围内会自动更新图层像素：空 `SmartFilterEditOptions{}` 使用严格默认子集，需要近似的算法通过 `Render.SmartFilters` 显式开启。参数与像素一起提交，之后可以自动保存、重新打开并继续编辑；仍可通过 `Pixels` 提供调用方已有的准确图像。

## 混合、剪贴与 Blend If

`SetBlendMode(id, mode)` 接受 PSD 四字节模式键（例如 `"mul "`）或常用英文名称（例如 `"multiply"`、`"normal"`）。组可使用 `"pass"` 表示穿透；普通图层不能设置穿透。`SetClipping(id, enabled)` 修改剪贴标记，保存时沿用当前图层顺序和剪贴基底。

`BlendIf(id)` 读取独立拷贝，`SetBlendIf(id, ranges)` 替换完整范围列表。第一个范围是复合灰色，RGB 文档后续依次为红、绿、蓝；缺省范围为完整覆盖。`Source` 对应本图层，`Destination` 对应下方已合成背景。端点值为 0–255，每个黑色或白色拆分端点对必须按升序排列；传 `nil` 或空列表清除全部范围。

{% raw %}
```go
if err := editor.SetBlendMode(id, "multiply"); err != nil {
	return err
}
if err := editor.SetClipping(id, true); err != nil {
	return err
}
if err := editor.SetBlendIf(id, []psdkit.BlendRange{{
	Source:      psdkit.BlendSlider{BlackLow: 0, BlackHigh: 128, WhiteLow: 255, WhiteHigh: 255},
	Destination: psdkit.BlendSlider{BlackLow: 0, BlackHigh: 0, WhiteLow: 255, WhiteHigh: 255},
}}); err != nil {
	return err
}
```
{% endraw %}

`SetBlendMode`、`BlendIf` 和 `SetBlendIf` 也提供带 `Context` 后缀的方法，接受 `ctx` 作为第一个参数。

## 高级混合与蒙版

`AdvancedBlending(ctx, id)` 读取高级混合状态，`SetAdvancedBlending(ctx, id, options)` 按字段更新。`AdvancedBlendingOptions` 包含 `Knockout`、`BlendClipped`、`BlendInterior`、`MaskAsGlobal`、`VectorMaskAsGlobal`、`TransparencyShapes` 和 `ChannelRestrictions`。指针字段传 `nil` 保留当前值；显式指针可设置 `false` 或 `KnockoutNone`。通道限制列表传 `nil` 保留，传空列表清除；RGB 通道编号为 0、1、2。

`PixelMask(ctx, id)` 读取现有像素蒙版属性，`SetPixelMask(ctx, id, settings)` 替换属性。推荐先读取再改动所需字段：

```go
mask, err := editor.PixelMask(ctx, id)
if err != nil {
	return err
}
mask.Disabled = false
mask.Density = new(uint8(192))
if err := editor.SetPixelMask(ctx, id, mask); err != nil {
	return err
}
```

`PixelMaskSettings` 包含启用状态、相对位置、反相、默认覆盖色，以及像素和矢量蒙版的密度、羽化参数。`SetPixelMask` 中密度和羽化的 `nil` **恢复默认值**，不表示保留；默认密度为 255，羽化为 0。此方法保持蒙版几何和通道尺寸不变。

单一普通像素蒙版即使没有 `-2` 样本通道，也可以仅靠 `DefaultColor` 提供默认覆盖。没有 generated coverage 或另一份用户蒙版元数据时，`PixelMask` / `SetPixelMask` 支持读取与编辑这种蒙版；也可通过 `AddVectorMask` 新增明确矢量几何。预览按默认覆盖与矢量覆盖相乘，保存重开后仍不新增 `-2`／`-3` 通道，默认值、反相、密度、羽化、禁用状态、原边界和尾部字节均保留。

重开后修改 `DefaultColor` 仍改变整个无采样覆盖场，不会因新增矢量蒙版而变成边界内固定采样。常量覆盖场的像素蒙版羽化不会生成新的采样；矢量羽化及效果组合仍遵守各自支持范围。[PSD 格式中的蒙版来源标记](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/)将普通用户蒙版与由其他数据渲染的覆盖区分；后者没有真实用户蒙版时，不能用 `PixelMask` 当作默认覆盖编辑。

### 新增与删除像素蒙版

`AddPixelMask(ctx, id, bounds, plane, settings...)` 为没有用户像素蒙版的图层新增蒙版，支持原有图层、新建图层和组。`bounds` 使用文档坐标；`plane` 的宽高必须与边界一致，位深必须匹配文档，行布局必须紧凑。8 位覆盖值 0 表示隐藏、255 表示显示。库复制传入像素，并自动选择通道 `-2` 或 `-3`，无需填写 `plane.ID`。

省略 `settings` 时，蒙版边界外的 `DefaultColor` 为 255。显式传入设置则使用完整值，包括值为 0 的 `DefaultColor`；只允许一份设置。新增时 `VectorDensity`、`VectorFeather` 为 `nil` 会继承已有共享矢量参数，之后可通过 `SetPixelMask` 清除。

下面为 RGB8 文档的首个图层添加一个两像素蒙版，保存后重新打开并读取设置。示例要求该图层尚无用户像素蒙版，且不改变图层结构：

```go
layers, err := editor.Layers()
if err != nil {
	return err
}
if len(layers) == 0 {
	return fmt.Errorf("文档没有图层")
}
id := layers[0].ID
if err := editor.AddPixelMask(ctx, id, image.Rect(0, 0, 2, 1), psdkit.Plane{
	Width: 2, Height: 1, Depth: 8, RowBytes: 2,
	Data: []byte{0, 255},
}); err != nil {
	return err
}
if err := editor.SavePSD(ctx, "masked.psd"); err != nil {
	return err
}
reopened, err := psdkit.OpenEditor("masked.psd")
if err != nil {
	return err
}
defer func() { _ = reopened.Close() }()
reopenedLayers, err := reopened.Layers()
if err != nil {
	return err
}
if len(reopenedLayers) == 0 {
	return fmt.Errorf("重新打开的文档没有图层")
}
mask, err := reopened.PixelMask(ctx, reopenedLayers[0].ID)
if err != nil {
	return err
}
_ = mask // 可继续使用 SetPixelMask 修改读取到的设置。
```

`LayerID` 不保证跨编辑器或保存重开后保持相同。真实业务重新打开文档后，应重新读取图层列表或树并选择目标图层，再使用该编辑器返回的 ID；上例仅因图层结构未变而再次选择首个图层。

`RemovePixelMask(ctx, id)` 删除用户像素蒙版的元数据和通道；没有用户蒙版时不产生修改。删除保留矢量路径和生成的矢量覆盖通道；实际存在矢量蒙版时，也保留其共享密度、羽化参数，没有矢量蒙版的闲置向量参数则随像素蒙版一起删除。删除后可重新调用 `AddPixelMask`，例如改变蒙版边界；对已有用户蒙版直接新增会返回 `ErrInvalidEdit`。没有用户蒙版时，`PixelMask` 和 `SetPixelMask` 返回 `ErrUnsupported`；不明确的双用户蒙版布局也返回 `ErrUnsupported`。

```go
if err := editor.RemovePixelMask(ctx, id); err != nil {
	return err
}
if err := editor.SavePSD(ctx, "unmasked.psd"); err != nil {
	return err
}
```

普通栅格层的已有蒙版通道可用 `ReplaceChannel` 按原位深替换内容：通常为 `-2`，与生成的矢量覆盖并存时真实用户蒙版为 `-3`。源通道的这一替换入口不支持组、智能对象或填充层；修改源 `-3` 后的自动 RGB8 预览仍返回 `ErrUnsupported`，保存需提供准确合成图。通过 `AddPixelMask` 新增的蒙版通道也可用 `ReplaceChannel` 更新，包括新图层和组；删除后需先重新新增。

### 新增、修改与删除矢量蒙版

`AddVectorMask(ctx, id, mask)` 为没有矢量蒙版的图层或组添加独立复制的 `psdvector.Mask`，其画布宽高须与文档一致。已有蒙版使用 `SetVectorMask` 修改；`RemoveVectorMask(ctx, id)` 删除矢量几何及其生成覆盖，保留独立的用户像素蒙版。不存在矢量蒙版时删除不产生修改，删除后可重新新增。

新增、修改、禁用、删除及再次新增均支持 PSD/PSB 保存重开。新增不会创建栅格覆盖通道；删除会清除不再属于任何矢量蒙版的密度／羽化参数。

禁用像素／矢量蒙版后，受支持的图层效果可直接实时重绘并自动保存；禁用像素蒙版也允许与受限矩形矢量羽化共存。重开保留禁用状态、样本及几何，重新启用时会再次检查活动组合的支持范围，详见[效果、调整与填充](effects.md)。

活动矢量／双蒙版羽化与阴影现支持普通 RGB8 Normal 栅格、单整数矩形矢量几何及 0.5／1.5／3 矢量羽化，像素蒙版沿用已有羽化模型；纯阴影及阴影＋颜色叠加均须在 `SetEffectsWithOptions`、`Composite` 和保存时逐次显式开启 `ApproximateDropShadow`。编辑几何、羽化或效果后可直接保存 PSD/PSB、重开和继续编辑，不需额外合成图，原始图层与蒙版样本保持；纯颜色叠加单独使用的双蒙版羽化及其余未校准组合仍拒绝。

生成的矢量覆盖与真实 `-3` 用户蒙版并存时，删除矢量蒙版会移除生成覆盖，将用户蒙版迁移为普通 `-2` 通道。未修改的用户样本保留原压缩字节，用户参数、边界和后续编辑继续有效。已有无样本默认覆盖用户蒙版时，可以直接新增、修改、删除并再次新增矢量蒙版，默认覆盖保持独立；删除用户蒙版后，剩余矢量参数载体不会被误认成新的用户蒙版。重复矢量块和不明确的双蒙版布局仍拒绝。

### 浏览器调用

浏览器能力目录会公开这些入口。例如 `engine.call('editor.SetBlendMode', { target: editor, args: [id, 'multiply'] })`；`ctx` 由 bridge 注入，不放入 `args`。JSON 使用 Go 导出字段名；高级混合中省略字段或传 `null` 表示保留，像素蒙版设置则传完整读取结果后修改。

```js
await engine.call('editor.AddPixelMask', {
  target: editor,
  args: [id, { Min: { X: 0, Y: 0 }, Max: { X: 2, Y: 1 } }, {
    Width: 2, Height: 1, Depth: 8, RowBytes: 2, Data: 'AP8=',
  }],
});
await engine.call('editor.WritePSD', { target: editor, args: [] });
await engine.call('editor.RemovePixelMask', { target: editor, args: [id] });
```

示例中 `Data` 为两个覆盖字节 `0, 255` 的 Base64；也可传上传后的二进制资产引用。浏览器中省略最后的设置参数同样使用 `DefaultColor: 255`；传 `null` 或 `{}` 则明确使用零值设置，`DefaultColor` 为 0。新增、修改、删除后的保存预览均来自当前编辑状态，PSD/PSB 格式沿用源文档。高位深及其他不支持自动合成的组合，需按下节提供准确的替代合成图。

矢量接口分别为 `editor.AddVectorMask`、`editor.SetVectorMask` 和 `editor.RemoveVectorMask`。解析返回的 `Mask` 可直接作为参数；`Header`、`Records.Raw` 等固定字节数组接受 Base64 或等长数值数组，非法编码或长度会在编辑前拒绝。

## 保存

### 画板自动合成与保存

现有顶层 RGB8 画板内的受支持图层编辑，现在可以直接自动保存，不再要求额外传入 `CompositeImage`：

```go
if err := editor.ReplacePixels(childID, pixels); err != nil {
	return err
}
if err := editor.SavePSD(ctx, "edited.psd"); err != nil {
	return err
}
```

默认保存会重新计算画板背景、矩形裁剪和完整画布预览；PSD/PSB 格式沿用源文件。重新打开后，通过 `Tree` / `Layers` 获取当前编辑器的图层句柄，即可继续编辑并保存。支持子层增删重排、跨现有画板移动和根级画板重排；删除或嵌套画板、修改其自身几何尚未开放。具体边界见[画板渲染](rendering.md#区域分块与画板)。

### 保存策略

```go
if err := editor.SavePSD(ctx, "edited.psd", psdkit.SaveOptions{Compression: 1}); err != nil {
	return err
}
```

- `SavePSD` / `Save` 写临时文件、同步后原子替换；`WritePSD` / `Write` 写入任意 `io.Writer`。
- 格式沿用原 Header，更换扩展名**不会**转换 PSD/PSB。
- `Compression` 只作用于修改过的通道：0 原始、1 RLE、2 ZIP、3 预测 ZIP。
- 混合、剪贴、Blend If 以及蒙版新增、修改、删除会使合成缓存失效；保存默认按修改后的图层重建预览，未修改的原始数据仍保留。

> [!NOTE]
> 能读取并原样保存某种高级图层，不代表能实时重绘它。编辑后的自动预览受[合成能力](rendering.md)限制。`SetTextContent` 在已有文字算法范围内可通过 `Render` 和显式字体自动更新源像素，近似须逐次开启；超出该范围的受支持文字内容编辑仍需提供准确 `Pixels`，详见[文字编辑](text.md#编辑文字)。

部分 Knockout 和高级组合虽可编辑参数，但无法自动合成，保存会返回 `ErrUnsupported`。调用方已有准确的编辑后合成图时，可通过 `SaveOptions.Composite` 或 `CompositeImage` 提供；库只校验尺寸与格式，不验证画面内容，也不会把旧保存预览冒充修改后的结果。

受支持的 pass-through 组 Blend If 现在允许组或子层携带明确关闭的效果集合。可继续修改 `SetBlendIf` 和不透明度，自动保存、重开后效果元数据仍保留；集合级歧义、重新启用的效果或其他高级组合继续拒绝，详见[效果范围](effects.md)。

单个非 Normal 栅格子层的受限穿透组 Blend If 还可与明确禁用的像素／矢量蒙版共存，组和子层上的禁用覆盖均视为 1。只有对应矢量蒙版已明确禁用时，才忽略它的密度／羽化控制；孤立参数和未知语义仍拒绝。已有 Normal 路径的舍入与资格保持不变，重新启用蒙版后恢复原限制。

业务中需要追踪关闭错误时，用命名返回值配合 `errors.Join`：

```go
defer func() { err = errors.Join(err, editor.Close()) }()
```
