---
title: 合成、重绘与导出
description: 区分保存预览与实时重绘，按整图、图层组、区域、分块和画板输出 PNG/JPEG。
---
# 合成、重绘与导出

psdkit 提供三类画面来源，它们回答的是不同的问题：

| 来源 | 入口 | 适用 |
| --- | --- | --- |
| **保存预览** | `DecodeDisplayComposite` / `DecodeDisplayRegion` | 快速显示文件内保存的合成图；可能是旧缓存 |
| **RGB8 图层合成** | `Composite` / `CompositeRegion`、`Editor.Composite` | 已解码的 RGB8 图层树，或编辑后的文档 |
| **来源实时重绘** | `RenderSource` / `RenderSourceTile` / `VisitSourceTiles` | 从原始文档重建，包括嵌入的智能对象内容 |

## 支持范围

RGB8 核心合成覆盖支持范围内的组、剪贴、26 种确定性混合模式、Opacity/Fill、像素蒙版、部分 Knockout、通道限制和 Blend If。具体图层和组合仍需要通过资格检查，超出范围返回包装了 `ErrUnsupported` 的错误。

混合模式、剪贴、Blend If、高级混合以及像素蒙版可通过 `Editor` 修改并保存；`AddPixelMask` / `RemovePixelMask` 支持新增、删除用户像素蒙版，包括新图层和组，见[混合与蒙版编辑](editing.md#混合剪贴与-blend-if)。蒙版变动会立即参与 RGB8 合成，保存重开后保留相同覆盖效果。编辑后的 `Editor.Composite` 和默认保存预览使用相同的合成资格检查；参数可编辑不表示所有 Knockout 或高级组合都能自动重绘。

实时智能对象来源可与显式 `ApproximateDropShadow` 下的纯阴影或 Normal 阴影＋颜色叠加组合；来源像素、父层像素蒙版、不透明度与效果分开计算，整图与区域包含必要的画布外阴影来源。编辑器自动预览会严格重建当前嵌入来源，不沿用上次 `EffectsEditOptions.Render` 的外链或近似策略；其余情况需显式准确合成图，见[智能对象效果边界](effects.md#智能对象当前来源与效果)。

10×10／13×13 均匀 custom Warp 已接入整图、区域、分块访问、单实例和候选来源渲染，各次调用须显式 `ApproximateCustomWarp: true`；外链仍每次映射。保持已认证仿射放置、均匀切片及原边界／±0.6 guard，数学近似不会放开透视、不规则 quilt 或折叠。保存已有编辑像素与重新从来源重绘仍是不同入口，详见[均匀网格范围](warp.md#编辑-10101313-均匀网格)。

已有横向 Arc Lower 预设也已接入整图、区域、分块访问和单实例来源重绘，限仿射、零透视、4×2、有限且绝对值小于100的 bend，并通过无折叠认证。包括 bend 为0，每次仍须显式开启 `ApproximateCustomWarp`，外链逐次映射；缺少策略不回退旧缓存。参数编辑、保存重开和继续编辑沿用同一数学模型，来源替换及其他预设仍拒绝，详见[预设来源重绘](warp.md#预设的整图与区域来源重绘)。

```go
pixels, err := psdkit.RenderSource(ctx, source)
if errors.Is(err, psdkit.ErrUnsupported) {
	// 回退到保存预览，并在界面上标明来源
	pixels, err = psdkit.DecodeDisplayComposite(ctx, source)
}
if err != nil {
	return err
}
```

> [!IMPORTANT]
> 回退到保存预览时，请在产品界面上标明画面来源。保存预览不反映编辑，也可能与当前图层不一致。

## 独立图层组合成

`editor.CompositeGroup(ctx, groupID)` 在透明背景上独立合成指定图层组，保留组自身的蒙版、透明度和子图层顺序，
不包含组外的祖先与兄弟图层。输出自动裁去透明边框，返回矩形仍保留文档坐标；空组返回 1×1 透明图。
调用不修改编辑或保存修订，并沿用整图合成的资格检查、预算与取消机制。

## 区域、分块与画板

| 目标 | 入口 |
| --- | --- |
| 实时区域 | `RenderSourceTile(ctx, source, bounds)` |
| 分块遍历 | `VisitSourceTiles(ctx, source, bounds, size, visit)`，逐块返回独立像素 |
| 高位深受限合成 | `RenderSourcePrecision` / `RenderSourcePrecisionTile` / `VisitSourcePrecisionTiles` |
| 单个智能对象 | `RenderSourcePlacedLayer` |
| 画板 | `RenderArtboard` / `RenderArtboards`，受支持的顶层 RGB8 画板 |
| 模板套图 | `NewRenderer` / `Inspect` / `examples/render` |

区域坐标、通道局部矩形、图层 `Bounds` 与画板裁剪可能位于不同坐标域，请按类型注释传入。

顶层 RGB8 画板自身可携带明确禁用的像素／矢量蒙版及关闭的效果集合。原矩形裁剪和背景语义保持不变，禁用的黑色蒙版样本不会覆盖画板裁剪。未知整体语义、活动蒙版或效果仍按原有边界拒绝。

现有顶层 RGB8 画板已支持编辑器自动合成与保存。`Editor.Composite`、`Write` / `Save` 会按当前编辑树生成完整画布预览，无需额外提供 `CompositeImage`，包括白、黑、透明和自定义背景、画板外普通图层及多个画板的裁剪。

子图层可修改像素、名称、可见性和不透明度，也可新增、删除、重排或在现有画板之间移动。画板可以在根级重排或显隐。`CompositeGroup` 选择画板时包含其背景和裁剪；选择普通子组时仍不继承祖先画板，保持独立图层组预览语义。

裁剪蒙版和背景只加入临时渲染快照，writer 不会新增虚拟图层或改写原蒙版采样。画板自身几何、删除或嵌套需要同步编辑画板元数据，当前拒绝；提供显式合成图也不能绕过这些结构限制。活动画板自身蒙版／效果及高位深完整语义仍保持原有边界。

### 独立画板中的智能对象

`RenderArtboard` / `RenderArtboards` 从可见智能对象的实际来源重建像素，不用已保存的实例栅格或合成预览代替。默认支持严格范围内的嵌入来源；外链通过 `ArtboardRenderOptions.Render` 显式提供 `ExternalResolver` 或 `ExternalRasterResolver`，仍不读取 PSD 中的任意路径或 URL。

```go
boards, err := psdkit.RenderArtboards(ctx, source, psdkit.ArtboardRenderOptions{
	Render: &psdkit.SceneOptions{
		ExternalRasterResolver: resolveSelectedRaster,
	},
})
```

`resolveSelectedRaster` 只返回调用方已选定的 PNG/JPEG/受支持扁平 TIFF 字节；外链 PSD/PSB 使用返回文档的 `ExternalResolver`。同次输出内共享来源只解析一次，各实例的变换、滤镜和父蒙版仍独立。单画板选择仍使用物理 `LayerIndex`，不会解析未选择画板中的来源；隐藏图层或画板不触发来源解析。结果图像的起点为零，`ArtboardImage.Bounds` 保留原画板坐标，各画板不会混入其他画板或画板外普通层。

近似始终显式：`Render.ApproximatePlacedProjection`、`Render.ApproximateCustomWarp`、`Render.SmartFilters` 等只选择声明范围。外层 `ArtboardRenderOptions.Compose` 控制当前画板及其图层的合成，`Render.Compose` 控制递归来源内部的合成；两者不会自动复制或互相开启近似。

外层 `MaxWorkingBytes` 包含来源快照、实时原型、各实例缓存及全部输出，`Render.MaxWorkingBytes` 非零时进一步限制来源的工作存储。未知来源、缺少外链映射、缺少近似策略、预算、取消或关闭失败均不返回部分画板。为保持实时来源语义，拒绝 `Render.CachedSpecialLayers`、`CachedTextLayers`、`OutputProfile` 以及旧 `ApproximateText`；支持范围内的实时 OpenType 仍须显式字体与策略。

来源替换、画板内子层透明度等编辑后可直接自动保存 PSD/PSB，重新打开再独立输出和继续编辑；保存与写入外部来源文件仍是两次独立操作。已验证 PSD/PSB 四种压缩与两轮编辑，外链重开后必须再次提供明确来源映射。

## 显式近似

少数高级特性只能近似重绘，需要调用方在 `SceneOptions` 或对应选项中明确开启：

| 选项 | 范围 |
| --- | --- |
| `ApproximateVectorStroke` | 原始矢量路径的有界实心内描边 |
| `ApproximateCustomWarp` | 自定义单片、仿射均匀10×10／13×13网格及已认证横向 Arc Lower 预设的数学投影；预设包括零 bend 也逐次显式选择 |
| `ApproximatePlacedProjection` | 透明边框透视放置的全画布双线性采样 |
| `ApproximateText` / `ApproximateOpenTypeText` | 显式字体的拉丁文字渲染 |
| `SmartFilters` | 选择实时滤镜算法与近似策略 |

这些选项只代表各自声明的范围，**不能为了得到画面而全部打开**。近似结果不等于 Photoshop 像素兼容。

## 编码与输出

原生 Go 的 `psdcompose.Render` / `RenderRegion` 将像素合成划分为 32 行的小任务，默认最多使用 4 个 Go 处理器；`Options.Workers: 1` 可强制串行。并行任务写入独立行块，共享只读准备数据，工作内存不足时减少任务数。像素公式、分辨率和采样精度保持不变。`js/wasm` 保持单线程，goroutine 不会带来浏览器多核加速。

浏览器 SDK 仅需要 PNG 时，可以使用 `engine.call('editor.Composite', { target: editor, args: [{}], imageOutput: 'png' })`，省去重复的原始 RGBA 传输。默认仍同时返回 PNG 与 RGBA，PNG 始终采用无损编码。

```go
file, err := os.Create("out.png")
if err != nil {
	return err
}
defer func() { err = errors.Join(err, file.Close()) }()
return psdkit.Encode(file, pixels, "png") // 或 "jpeg"，质量 95
```

- JPEG 不保留透明度，会铺白；请明确业务的背景策略。
- `PrepareOutput` 负责尺寸与裁片，`ValidateOptions` 先校验参数。
- 原始通道与高位深结果不要隐式转成 RGB8。
- 渲染器使用期间，输入图像保持只读；需要修改时先复制。

```sh
go run ./examples/create -out /tmp/new.psd
go run ./examples/composite -psd /tmp/new.psd -out /tmp/composite.png
go run ./examples/scene -help
```
