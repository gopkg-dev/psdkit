---
title: 示例与 Playground
description: 仓库内可直接运行的 Go 示例，以及浏览器 Playground 的使用方式。
---
# 示例与 Playground

## 命令行示例

从仓库根目录运行。读取类示例需要显式提供 `-psd`；先用 `create` 生成一个自有输入即可，无需外部素材。

| 示例 | 用途 | 命令 |
| --- | --- | --- |
| create | 新建 PSD/PSB | `go run ./examples/create -out /tmp/new.psd` |
| index | 原始结构与按需通道 | `go run ./examples/index -psd /tmp/new.psd` |
| inspect | RGB8 结构与智能对象 | `go run ./examples/inspect -psd /path/input.psd` |
| edit | 编辑并保存 | `go run ./examples/edit -psd /tmp/new.psd -out /tmp/edited.psd` |
| composite | RGB8 合成 | `go run ./examples/composite -psd /tmp/new.psd -out /tmp/composite.png` |
| render | 模板套图 | `go run ./examples/render -psd /path/template.psd -print /path/print.png -out /tmp/render.png` |
| color | 源精度显示 / ICC | `go run ./examples/color -psd /tmp/new.psd -out /tmp/display.png` |
| scene | 来源 / 区域 / 画板重绘 | `go run ./examples/scene -help` |
| precision | 16/32 位合成 | `go run ./examples/precision -help` |
| placed-layer | 单个智能对象重绘 | `go run ./examples/placed-layer -help` |
| replace-contents | 替换智能对象内容 | `go run ./examples/replace-contents -help` |

新建的普通文档不含模板智能对象；模板类示例需要符合支持范围的输入。完整参数、预算与显式近似策略通过每个示例的 `-help` 查看。

`edit` 支持的参数：

```sh
go run ./examples/edit -psd /tmp/new.psd -out /tmp/edited.psd \
  -layer 0 -name "主视觉" -opacity 204 -compression zip
```

## 浏览器 Playground

[Playground](../demo/) 把同一份 Go 实现编译为 WebAssembly，在 Web Worker 中运行：

- 首页与 Playground 共用真实上衣样机 `muban.psd`，内置「山野日落」「向阳花园」两款印花，支持 5 个共享衣片联动替换；示例显式采用数学 Warp 近似，模板缺失时明确报错；
- 首页通过小图缩略图选择印花，点选或本地上传后自动替换并重新渲染；支持 PNG/JPEG/WebP，文件不超过 20 MB、2000 万像素。上传图片等比例适配到白色 RGB8 印花画布，用于样机预览；
- 首页在单个 Worker 内缓存最多 3 份印花编辑状态和 16 MiB 的 PNG。重复选择直接恢复相应编辑器与预览，PSD 下载使用当前印花的真实编辑状态；显示 / 隐藏或不透明度变化会使预览失效，已生成的智能图层投影继续复用。新上传会释放上一次本地印花缓存，重启引擎或离开页面释放整个工作区；
- 内置印花的智能对象内容 PSD 在构建时生成并校验，避免浏览器首次使用时重复编码。它们是忽略的站点构建产物；本地上传仍由浏览器内的 Go 引擎生成内容 PSD；
- 选中智能对象可一键“替换内容”：图片经 `raster.buildPSD` 转为可编辑 PSD，再由 `editor.ReplaceContents` 替换真实来源，实时重绘时 Warp 保持不变；
- 可导入本机 PSD/PSB、PNG/JPEG、TTF/OTF/TTC 字体与 ICC，文件不会上传；
- 图层树支持选择、显示 / 隐藏，属性面板可改名、调整不透明度和填充、替换像素、增删图层；
- 画布明确标注“保存预览”或“实时重绘”；
- 能力目录列出全部真实 Go 入口，常用参数使用表单，复杂参数通过 JSON 高级面板传入；
- 结果面板可下载 PNG、PSD/PSB 和原始二进制，并释放资源句柄。

> [!NOTE]
> 浏览器预算默认单次 256 MiB、1600 万像素。大型文档请优先使用原生 Go。

## 本地运行

```sh
make site           # 构建文档、Wasm 与 Playground，组装到 _site/
make site-preview   # 在 http://127.0.0.1:8301/psdkit/ 预览
```
