# psdkit browser SDK

面向使用者的完整说明见文档网站的 [浏览器 SDK](../../docs/browser.md)；本文件记录协议细节。

纯 Go `js/wasm` 在独立 Web Worker 内执行。主线程只处理界面、文件选择和二进制传输；PSD 解析、编辑、字体、Warp、合成和编码均调用原 Go 库。没有远程渲染服务，也没有用 Canvas 重写算法。

## 构建和接入

在 psdkit 根目录执行：

```sh
shnote --what '构建 Wasm 及配套运行时' --why '使用匹配工具链的 wasm_exec.js' run go run ./cmd/psdkit-web-build
```

将 `web/demo/public/wasm/` 放到页面相对的 `./wasm/`，使用 HTTP(S) 提供服务。它包含 Wasm、Worker、Go 运行时、Go 许可证及 SHA-256 构建清单。SDK 是本目录的 `client.js`，类型在 `client.d.ts`。

```js
import { PsdkitClient, download } from './sdk/client.js';
const engine = new PsdkitClient({ workerURL: './wasm/worker.js' });
await engine.ready();
const catalog = await engine.call('system.capabilities');
const imported = await engine.importFile(psdFile, 'document'); // File.
const opened = await engine.call('document.open', { args: [{ $asset: imported.data.asset }] });
const { document: source, editor } = opened.data;
await engine.call('editor.Rename', { target: editor, args: [1, '新名称'] });
const composed = await engine.call('editor.Composite', { target: editor, args: [{}] });
download(composed.binaries.find((item) => item.mime === 'image/png'));
const saved = await engine.call('editor.WritePSD', { target: editor, args: [{}] });
download(saved.binaries[0], 'edited.psd');
await engine.call('resource.release', { target: source });
await engine.call('asset.remove', { target: imported.data.asset });
engine.close();
```

`LayerID` 从 `opened.data.layers` 或 `editor.Tree` 读取；示例中的 `1` 只适用于该 ID 存在的文档。复杂套图可能按库资格返回 `unsupported`，此时不会返回伪造图片。

## 参数与所有权

- `system.capabilities` 是完整调用目录：Go API、原说明、参数名、类型、示例、schema、目标类型和资格提示。`args` 顺序对应可见参数；Context、Writer、字体和外链解析器由 Go 注入。
- `asset.put` 接收独立二进制；`{"$asset":"a…"}` 引用图片、字体、ICC、PSD 或原始字节。图片入口支持 PNG/JPEG RGB8，保留 PNG alpha=0 的隐藏 RGB。16 位图片不会静默降位；PSD 源精度解码另有入口。
- 类型化返回值包含 `handle/kind/type/value`，后续用 `{"$ref":"r…"}`；`path:["Layer","Placed"]` 或 `path:[0]` 可选择字段或切片项。句柄保留原 Go 类型和精度，可直接链式传递描述符。
- 超过 JS 安全范围的整数表示为 `{"$int64":"9223372036854775807"}`；输入接受相同标签，SDK 也接受 `BigInt`。
- 图像输出 PNG，NRGBA 结果另附 packed straight RGBA；大浮点切片用小端 `.f64le`，不把 HDR 自动量化。原始块、PSD、ZIP 等也是独立 `ArrayBuffer`。
- 完整输入 `ArrayBuffer` 转交 Worker 后会 detached；需要复用请先 `slice(0)`。TypedArray 子视图只复制选中范围。Go 持有输入副本，输出不借用 Go 内存。
- `resource.release` 关闭资源；关闭根文档清理其派生句柄。`asset.remove` 在仍有借用者时拒绝。`system.reset` 释放全部工作区资源。

## 字体、图片与外链

```js
const font = await engine.importFile(fontFile, 'font');
const face = await engine.call('font.decode', { args: [{ $asset: font.data.asset }, {}] });
const run = await engine.call('font.layout', {
  args: ['AV', { FontName: face.data.results[0].value.PostScriptName, Size: 48 }, { OriginY: 60 }],
});
const glyphs = await engine.call('font.raster', {
  args: [{ $ref: run.data.results[0].handle },
    { Min: { X: 0, Y: 0 }, Max: { X: 140, Y: 90 } }, {}],
});
const image = await engine.importFile(imageFile, 'image');
// ReplacePixels / ReplaceContents 等图片参数使用 { $asset: image.data.asset }.
```

不读取系统字体，不自动替换缺失字体。`font.decode` 可显式传 `alias` 对应 PSD 原始 PostScript 名称；字体功能仍遵守原库支持范围。外链通过调用的 `links: { "原始ID或名称": "a…" }` 显式映射，不读取 PSD 中的系统路径或任意 URL。

## 预算与错误

默认资产总量 256 MiB、单次工作预算 256 MiB、16,000,000 像素、128 资产、512 句柄、16 MiB JSON、256 MiB 输出。直接保留的像素/字节缓冲另有累计预算，`system.state` 可查看。Go GC 使用 384 MiB **软**目标，这些不是整个浏览器的硬内存上限；大型文档优先使用原生 Go。

Go deadline 默认 60 秒，可设到 120 秒；SDK watchdog 默认 120 秒，排队时间也计入。强制取消使用 `engine.terminate()`，会丢弃整个工作区和未导出的编辑。`reset()` 启动新 Worker。

错误 Promise 保留 `code/message/path/feature/offset/audit`。主要代码：`unsupported`、`missing_font`、`missing_glyph`、`resource_limit`、`invalid_request`、`canceled`、`worker_timeout`。失败调用清理新增句柄/资产，不声称回滚已提交的编辑。

Scene / 自动内容替换的 quilt 门禁保持。显式网格仅按原库限定执行，不把浏览器与 Go 一致性等同于 Photoshop 零差。

参考：[Go WebAssembly](https://go.dev/wiki/WebAssembly)、[Web Worker](https://developer.mozilla.org/en-US/docs/Web/API/Web_Workers_API/Using_web_workers)。
