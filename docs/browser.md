---
title: 浏览器 SDK
description: 在 Web Worker 中运行 psdkit 的 Go WebAssembly，导入本机文件、调用能力目录、管理句柄与预算。
---
# 浏览器 SDK

浏览器端把纯 Go 实现编译为 `js/wasm`，在**独立的 Web Worker** 中执行。主线程只负责界面、文件选择和二进制传输；PSD 解析、编辑、字体、Warp、合成和编码都调用原 Go 库。没有远程渲染服务，也没有用 Canvas 重写算法。

> [!IMPORTANT]
> 文件只在本机内存中处理：页面从站点加载 Wasm 与 Worker 脚本，选择的文档通过 `ArrayBuffer` 转交 Worker，不会上传到任何服务器。

## 构建与部署

```sh
go run ./cmd/psdkit-web-build   # 生成 Wasm、配套 wasm_exec.js、Worker、Go 许可证与 SHA-256 清单
```

输出位于 `web/demo/public/wasm/`。部署时把这个目录放在页面相对的 `./wasm/`，通过 HTTP(S) 提供服务（不能使用 `file://`），`.wasm` 需返回 `application/wasm`。SDK 源码是 `web/sdk/client.js`，类型声明在 `client.d.ts`；Playground 构建时会把它一并发布到 `demo/sdk/client.js`。

| 路径 | 内容 |
| --- | --- |
| `wasm/psdkit.wasm` | Go 编译的引擎 |
| `wasm/wasm_exec.js` | 与构建工具链匹配的 Go 运行时 |
| `wasm/worker.js` | Classic Worker 入口 |
| `wasm/manifest.json` | Go 版本、协议版本与 SHA-256 |
| `wasm/LICENSE-Go.txt` | Go 工具链许可证 |

部署在项目子路径（例如 GitHub Pages 的 `/psdkit/`）时，使用相对地址或显式传入 `workerURL`，不要写死根路径 `/wasm/`。

## 最小接入

```js
import { PsdkitClient, download } from './sdk/client.js';

const engine = new PsdkitClient({ workerURL: './wasm/worker.js' });
await engine.ready();

const imported = await engine.importFile(psdFile, 'document'); // File 对象
const opened = await engine.call('document.open', { args: [{ $asset: imported.data.asset }] });
const { document: source, editor, layers } = opened.data;

const id = layers[0].ID; // LayerID 从 opened.data.layers 或 editor.Tree 读取
await engine.call('editor.Rename', { target: editor, args: [id, '新名称'] });
await engine.call('editor.SetOpacity', { target: editor, args: [id, 204] });

const composed = await engine.call('editor.Composite', { target: editor, args: [{}] });
download(composed.binaries.find((item) => item.mime === 'image/png'), 'preview.png');

const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
download(saved.binaries[0], 'edited.psd');

await engine.call('resource.release', { target: source });
await engine.call('asset.remove', { target: imported.data.asset });
engine.close();
```

复杂文档可能按库的资格规则返回 `unsupported`，此时不会返回伪造的图片。

现有顶层 RGB8 画板也使用相同的 `editor.Composite` / `editor.WritePSD` 调用，无需上传额外合成图。修改子图层后直接调用 `editor.WritePSD`，导出的文件会包含当前画板背景和裁剪生成的预览；导入该文件后可继续编辑。渲染用的临时背景不会成为保存文件中的新图层。

独立画板中的智能对象也从实际来源实时重绘。嵌入来源可直接调用 `render.artboard` / `render.artboards`；外链每次请求提供 `links`，并传入 `Render: {}` 以注入显式来源解析器：

```js
const boards = await engine.call('render.artboards', {
  links: { [sourceID]: sourceAsset },
  args: [{ $ref: source }, { Render: {} }],
});
```

`sourceID` 为文档中的来源标识，`sourceAsset` 为已导入来源资产 ID。单个画板可传 `{ LayerIndex: physicalIndex, Render: {} }`，这里是源文件物理索引。投影、Warp 或滤镜近似放在 `Render` 内逐次显式选择，例如 `{ Render: { SmartFilters: { ApproximateGaussian: true } } }`。缺少映射或策略不会回退到缓存；当前画板 `Compose` 与递归来源 `Render.Compose` 的范围见[画板重绘](guide/rendering.md#独立画板中的智能对象)。

智能滤镜参数编辑在已有算法范围内也会自动更新像素。`stack` 为该层读取并修改后的现有滤镜栈：

```js
await engine.call('editor.SetSmartFilterParameters', {
  target: editor,
  args: [id, stack, {}], // 默认子集；不需要 Pixels。
});
const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
```

非零高斯模糊等近似须显式传入 `{ Render: { SmartFilters: { ApproximateGaussian: true } } }`。外链来源每次调用都须传 `links` 映射，并至少提供 `{ Render: {} }` 以注入来源解析器。保存重开后仍可按相同方式继续编辑；近似选项不会因文件中存在滤镜而自动开启。完整边界见[智能滤镜](guide/filters.md)。

替换智能对象来源也会同步重算每个共享实例的当前滤镜。`replacementAsset` 为已导入的新来源资产 ID；严格子集可直接用 `editor.ReplaceContents`，需要近似时使用下列直接位于 `SmartContentsOptions` 的策略：

```js
await engine.call('editor.ReplaceContentsWithOptions', {
  target: editor,
  args: [id, { $asset: replacementAsset }, {
    SmartFilters: { ApproximateGaussian: true },
  }],
});
const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
```

`EmbedLinkedContentsWithOptions` / `UpdateLinkedContentsWithOptions` 使用相同的 `SmartFilters` 字段，外链仍由每次请求的 `links` 显式映射。重新导入后可继续替换或编辑滤镜，近似每次明确传入；不会重复应用滤镜或烘焙父层蒙版与效果。

普通 Normal 栅格层上的无羽化活动矢量／双蒙版颜色叠加，可直接调用 `editor.SetEffects`。活动像素、矢量或双蒙版上的阴影及阴影＋颜色叠加，使用 `editor.SetEffectsWithOptions` 的 `{ ApproximateDropShadow: true }`，并在 `editor.Composite`、`editor.WritePSD` 各自的选项中同样显式开启。无羽化时保持已有受支持路径；包含羽化时，矢量几何限单整数矩形、矢量半径为 0／0.5／1.5／3，像素蒙版沿用已有羽化模型，矢量羽化为 0 时也可参与矩形上的活动像素羽化组合。`lmgm`／`vmgm` 保留各自的塑形或最终隐藏阶段，覆盖区域外及画布外的阴影来源。导出后重新导入可继续修改几何、羽化和已有效果，每次仍须显式选择近似；完整边界见[图层效果](guide/effects.md)。

单整数矩形矢量蒙版的 0.5／1.5／3 像素羽化也可与纯颜色叠加一起使用 `editor.SetEffects`，要求普通 RGB8 Normal 栅格且无活动像素蒙版。无需提供 `CompositeImage` 或开启新近似，`editor.WritePSD` 自动生成当前像素；重新导入后可继续编辑。纯颜色叠加单独使用时仍拒绝活动双蒙版羽化，未校准矢量几何、半径和高级阴影组合继续拒绝。

RGB8 智能对象的当前来源也支持纯阴影及 Normal 阴影＋颜色叠加：调用 `editor.SetEffectsWithOptions` 时传 `{ ApproximateDropShadow: true }`，在 `editor.Composite` 和 `editor.WritePSD` 中分别再次传入。严格可重建的嵌入来源及当前来源替换可以直接保存，重新导入后继续编辑；父蒙版、Fill／Opacity 和效果不会烘焙进原始投影像素，各共享实例保留独立效果。

外链效果编辑须在本次调用同时传 `links` 与 `{ ApproximateDropShadow: true, Render: {} }`，来源近似放入 `Render`，父阴影近似不自动传给子来源。`Render` 不会保留给下一次保存；外链自动保存前须通过 `editor.EmbedLinkedContentsWithOptions` 和显式映射转嵌入，或提供准确 `CompositeImage`。需要额外来源近似或字体时，也须提供准确合成图。活动矢量蒙版、纯智能对象颜色叠加的单独参数编辑和其他高级组合仍拒绝，详见[图层效果](guide/effects.md#智能对象当前来源与效果)。

已有普通无采样默认覆盖用户蒙版时，`editor.AddVectorMask` 可直接新增矢量几何。`editor.WritePSD` 不会生成蒙版采样通道；重新导入后仍可用 `editor.SetPixelMask` 修改 `DefaultColor`、密度、反相等属性，并继续 `SetVectorMask`／`RemoveVectorMask`。生成覆盖载体没有真实用户蒙版时仍拒绝按默认覆盖编辑，自动保存仍受 RGB8 合成范围限制。

支持范围内的文字内容编辑也可以自动更新像素。先通过 `font.decode` 显式注册字体，名称须匹配该文字层的 PostScript 名称，再逐次选择文字近似策略：

```js
const font = await engine.importFile(fontFile, 'font');
await engine.call('font.decode', { args: [{ $asset: font.data.asset }, {}] });
await engine.call('editor.SetTextContent', {
  target: editor,
  args: [id, 'Hello', { Render: { ApproximateText: { Samples: 4 } } }],
});
const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
```

无需提供 `Pixels`；Go 注入已注册字体解析器，但不会读取系统字体或自动选择近似。OpenType 可使用 `{ Render: { ApproximateOpenTypeText: { Kerning: 1 } } }`，其中 `1` 为 None、`2` 为 Metrics；已支持的多行排版还须显式开启 `ApproximateTypography: true`。重新导入后继续编辑时仍须保留或重新注册字体，并再次传入策略；释放字体后不会沿用先前字体或旧像素。适用的 RGB8、72ppi、单样式水平点文字等边界见[文字与字体](guide/text.md#自动更新像素)。

### Warp 控制点编辑

已有 4×4 custom 单片的控制点可用 `editor.SetSmartObjectWarp` 编辑。`points` 为当前目标的16个源像素坐标点；嵌入来源的严格单片可传空选项：

```js
await engine.call('editor.SetSmartObjectWarp', { target: editor, args: [id, points, {}] });
const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
```

4×4 数学 projective Warp 逐次传 `{ Render: { ApproximateCustomWarp: true } }`；外链调用同时传 `links` 和 `Render: {}`。保存重开后可继续编辑，共享来源和其他实例保持，单片当前网格在后续来源替换和尺寸变化时仍保留。普通受支持组合保存已生成实例像素，不自动开启近似；带有需重新解析来源的活动效果时，仍遵守准确合成图边界，详见[Warp 控制点编辑](guide/warp.md#编辑-44-自定义-warp-控制点)。

已有 10×10／13×13 均匀网格同样使用 `editor.SetSmartObjectWarp`，`points` 长度为100／169。每次编辑传 `{ Render: { ApproximateCustomWarp: true } }`，外链同时传 `links`；随后 `editor.WritePSD` 可保存已经生成的实例像素。重新导入后继续编辑仍须再次选择近似与映射。`render.source`／`render.placed`／`render.tile` 的选项也分别传 `ApproximateCustomWarp: true`；缺少时不读取旧缓存补画。

网格阶数、切片和 ±0.6 guard 保留，同尺寸来源替换会携带当前网格（`SmartContentsOptions.ApproximateCustomWarp: true` 也需显式选择）。均匀网格改变来源尺寸、透视网格、不规则 quilt 及折叠继续拒绝，见[均匀网格编辑边界](guide/warp.md#编辑-10101313-均匀网格)。

### Warp 预设参数编辑

`editor.SetSmartObjectWarpPreset` 编辑已有横向 Arc Lower 的 bend 值。每次传入 `{ Render: { ApproximateCustomWarp: true } }`，即使目标值为0也一样；外链同时提供本次 `links` 映射：

```js
await engine.call('editor.SetSmartObjectWarpPreset', {
  target: editor,
  args: [id, { Value: 25 }, { Render: { ApproximateCustomWarp: true } }],
  // 外链时另传 links: { [sourceID]: sourceAsset }。
});
const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
```

参数和新实例像素一起提交，保留共享来源、其他实例及预设描述符未知字段；重新导入后仍可显式选择近似与映射继续编辑。`render.source`／`render.placed`／`render.tile` 也已接通同一预设几何，每次直接在各渲染选项中传 `ApproximateCustomWarp: true`，外链同时提供本次 `links`：

```js
const preview = await engine.call('render.source', {
  args: [{ $ref: source }, { ApproximateCustomWarp: true }],
  // 外链时另传 links: { [sourceID]: sourceAsset }。
});
```

整图与区域来源重绘不使用旧实例栅格，保存重新导入后也从当前参数与实际来源生成像素。包括 bend 为0，每次仍须选择近似与外链映射；参数编辑时的 `Render` 不会成为后续渲染的默认策略。范围限已有横向 `warpArcLower`、4×2、仿射、零透视且无折叠；其他预设、竖向旋转及来源替换继续拒绝。该数学采样不声明 Photoshop 原生像素一致，详见[预设参数编辑边界](guide/warp.md#编辑-warp-预设控制参数)与[来源重绘](guide/warp.md#预设的整图与区域来源重绘)。

## 能力目录

`system.capabilities` 返回完整调用目录：Go API、原注释、参数名、类型、示例、JSON schema、目标类型以及精度策略提示。`args` 的顺序对应可见参数；`Context`、`io.Writer`、字体与外链解析器由 Go 注入，不需要传入。

```js
const catalog = (await engine.call('system.capabilities')).data;
const op = catalog.find((item) => item.id === 'editor.SetOpacity');
// { id, group, label, goAPI, description, targetKind: 'editor', parameters: [...] , policy? }
```

| 前缀 | 作用 |
| --- | --- |
| `document.*` | 打开、新建、原始结构、按需解码、Descriptor |
| `editor.*` | 图层编辑、合成、写出 PSD/PSB |
| `preview.*` / `render.*` | 保存预览、实时重绘、区域、分块、画板 |
| `font.*` / `text.*` | 字体解码、排版、塑形、光栅化、文字图层 |
| `warp.*` | Warp 编译与投影 |
| `effects.*` / `adjustment.*` / `filters.*` | 效果、调整、智能滤镜 |
| `vector.*` / `gradient.*` / `pattern.*` | 矢量与填充 |
| `color.*` / `image.*` / `raster.*` | 色彩、编码、图片转 PSD |
| `system.*` / `asset.*` / `resource.*` | 工作区、资产与生命周期 |

## 参数与返回值

- **资产**：`importFile(file, kind)` 或 `asset.put` 导入二进制；之后用 `{"$asset": "a…"}` 引用图片、字体、ICC、PSD 或原始字节。图片入口支持 PNG/JPEG RGB8，保留 PNG alpha=0 的隐藏 RGB；16 位图片不会被静默降位。
- **句柄**：类型化返回值包含 `handle / kind / type / value`，后续用 `{"$ref": "r…"}` 引用；`path: ["Layer", "Placed"]` 或 `path: [0]` 选择字段或切片项。句柄保留原 Go 类型和精度，可直接链式传递。
- **大整数**：超过 JS 安全范围的整数表示为 `{"$int64": "9223372036854775807"}`；输入接受同样的标签，也接受 `BigInt`。
- **二进制**：图像输出 PNG，NRGBA 结果另附 packed straight RGBA；大浮点切片使用小端 `.f64le`，不自动量化 HDR。原始块、PSD、ZIP 等都是独立的 `ArrayBuffer`。
- **所有权**：完整输入 `ArrayBuffer` 转交 Worker 后会被 detach，需要复用请先 `slice(0)`；TypedArray 子视图只复制选中范围。Go 持有输入副本，输出不借用 Go 内存。

## 生命周期

| 调用 | 作用 |
| --- | --- |
| `resource.release` | 关闭资源；关闭根文档会清理它的派生句柄 |
| `asset.remove` | 删除资产；仍有借用者时拒绝 |
| `system.state` | 列出资产、句柄、已用字节与预算 |
| `system.reset` | 释放全部工作区资源 |
| `engine.terminate()` | 强制终止 Worker，丢弃整个工作区与未导出的编辑 |
| `engine.reset()` | 启动新的 Worker 并等待就绪 |

## 字体、图片与外链

```js
const font = await engine.importFile(fontFile, 'font');
const face = await engine.call('font.decode', { args: [{ $asset: font.data.asset }, {}] });
const run = await engine.call('font.layout', {
  args: ['AV', { FontName: face.data.results[0].value.PostScriptName, Size: 48 }, { OriginY: 60 }],
});
const glyphs = await engine.call('font.raster', {
  args: [{ $ref: run.data.results[0].handle }, { Min: { X: 0, Y: 0 }, Max: { X: 140, Y: 90 } }, {}],
});
```

不读取系统字体，不自动替换缺失字体。`font.decode` 可以通过 `alias` 对应 PSD 中原始的 PostScript 名称。外链通过调用参数 `links: { "原始 ID 或名称": "a…" }` 显式映射，不读取 PSD 中的系统路径或任意 URL。

外链 PNG/JPEG/受支持的扁平 TIFF 现在可直接用于实时重绘。导入来源资产后，`render.source`、`render.tile` 和 `render.placed` 会注入栅格 resolver；每次调用均须提供映射，并传入选项对象：

```js
const live = await engine.call('render.source', {
  args: [{ $ref: source }, {}],
  links: { [sourceID]: externalImageAsset },
});
```

更换映射会读取新来源；缺少映射不会使用上次调用的映射或文件里的旧缓存。保存并重开父 PSD/PSB 后，同样可以显式映射、实时重绘并继续编辑。

## 预算与超时

| 预算 | 默认 |
| --- | --- |
| 资产总量 / 单次工作 | 256 MiB / 256 MiB |
| 单次像素 | 16,000,000 |
| 资产 / 句柄数量 | 128 / 512 |
| JSON / 输出 | 16 MiB / 256 MiB |

直接保留的像素与二进制缓冲另有累计预算，可以通过 `system.state` 查看。Go GC 使用 384 MiB 的**软**目标，这些都不是整个浏览器的硬内存上限；大型文档优先使用原生 Go。

Go deadline 默认 60 秒，可设到 120 秒（`timeoutMS`）；SDK 看门狗默认 120 秒，排队时间也计入，超时后终止 Worker 并以 `worker_timeout` 拒绝所有待处理调用。

## 错误

错误 Promise 保留 `code / message / path / feature / offset / audit`：

| code | 含义 |
| --- | --- |
| `unsupported` | 超出支持范围 |
| `missing_font` / `missing_glyph` | 缺字体或字形 |
| `resource_limit` | 超出预算 |
| `invalid_request` | 参数、句柄或资产无效 |
| `canceled` | 超过 Go deadline |
| `worker_timeout` | SDK 看门狗超时，工作区已释放 |
| `worker_error` | Worker 或 Wasm 运行时错误 |

失败的调用会清理本次新增的句柄与资产，但不声称回滚已提交的编辑。

参考：[Go WebAssembly](https://go.dev/wiki/WebAssembly)、[Web Workers](https://developer.mozilla.org/en-US/docs/Web/API/Web_Workers_API/Using_web_workers)。
