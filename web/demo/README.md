# 浏览器 Playground

覆盖 psdkit 现有能力族的本地工作台。全部目录入口调用真实 Go 函数；常用参数有表单，复杂参数有 JSON 和原 Go 合同。界面与首页、文档共用 `docs/assets/css/tokens.css` 设计令牌和 `docs/_data/icons.json` 图标。

| 区域 | 内容 |
| --- | --- |
| 顶栏 | 文件（打开、Warp 示例、简单示例、新建空白 PSD/PSB）、导出（当前画面 PNG、实时重绘 PNG、可编辑 PSD/PSB）、工作区（释放全部资源、重启引擎）、引擎状态与取消 |
| 左栏 | 图层树（选择、显隐、方向键导航，智能对象单独标记）与已导入资源（按类型筛选、`asset.remove`） |
| 中央 | 画布、缩放（适应 / 25%–400%，`+` `-` `0`）、“保存预览 / 实时重绘”来源切换与标签、执行记录、能力目录、图层数据 |
| 右栏 | 属性（改名、显隐、不透明度、填充、替换像素、智能对象替换内容、文档信息与资源用量）、操作（能力搜索、参数表单、预算、策略、高级 JSON、外链映射）、结果（下载与句柄释放） |

有未导出的编辑时，重置、重启或离开页面会先确认。忙碌时显示耗时并可取消（终止 Worker，释放整个工作区）。错误按 `unsupported`、`resource_limit`、`missing_font`、`worker_timeout` 等代码给出说明，同时保留原始 message、path、feature 与 offset。窄屏下图层、画布、属性、操作四个区域通过底部导航切换。

## 启动

在 psdkit 根目录执行：

```sh
go run ./cmd/psdkit-web-build      # Wasm、配套运行时与示例 PSD（public/wasm、public/samples）
npm --prefix web/demo ci
npm --prefix web/demo run build
go run ./cmd/psdkit-web-server
```

打开 <http://127.0.0.1:8194>。首页和 Playground 共用真实上衣模板 `muban.psd`，默认选中前片智能对象，内置「山野日落」「向阳花园」两款印花。选取印花后点击“替换内容”，通过 `ReplaceContentsWithOptions` 更新同一来源的 5 个衣片实例，保留蒙版、褶皱与放置。示例显式选择数学 Warp 近似，并将无 ICC 的印花按 sRGB 解释，默认严格入口的精度门禁保持。

源素材位于独立的 `gopkg-dev/psdkit-assets` 仓库，放在本仓库旁或用 `PSDKIT_SAMPLE_ASSETS` 指定。输入摘要固定于 `web/samples/manifest.json`，构建器验证输入，并检查两款替换都能重绘。素材缺失或校验失败会明确报错。

可导入 PSD/PSB、PNG/JPEG、TTF/OTF/TTC 和 ICC，普通 Playground 没有上传服务。`dist/` 可静态部署，使用相对 `./wasm/`、`./samples/` 路径，支持 GitHub Pages 子路径；不能用 `file://`。构建同时把 `web/sdk/client.js` 发布到 `dist/sdk/`，供首页按需导入。完整站点预览见 [部署说明](../../docs/deployment.md)。

开发模式为 `npm --prefix web/demo run dev`，端口 5194；只有可选验收接口代理到 8194，库调用不经过 Go 服务。

## 能力对照

| 能力 | 入口 / 使用方式 |
| --- | --- |
| PSD/PSB 索引、所有原始块与通道区间 | 导入；`document.structure`、`ReadSpan/CopySpan`；无需解码像素 |
| 按需、区域、通道、源精度解码 | `document.Decode*`、嵌入 TIFF、区域 LRU Cache |
| 新建 PSD/PSB、原始格式保存 | `document.create` 版本 1/2、`editor.Write/WritePSD`、顶部导出 |
| 图层与组编辑 | `editor.*` 增删、移动、改名、可见性、Opacity、Fill、像素/通道；Tree / Changes |
| 智能对象和外链 | 源索引、嵌入 PSD/PSB/TIFF/图片、共享替换、显式映射、嵌入和保留外链更新 |
| 仿射、透视、单片和网格 Warp | `warp.*` 编译、Forward / Inverse / Project；显式均匀 10×10 / 13×13 |
| 整图、区域、画板与套图 | `render.*`、`renderer.*`、`editor.Composite`；分块 visitor 适配 ZIP |
| 保存预览 | `preview.saved/region`，画面明确标记来源 |
| 字体与文字 | `font.*` 字形、排版、塑形、Hinting、可变字体；`text.*` 实时文字、文字/EngineData 解析与受控编辑 |
| 矢量、纯色、渐变与图案 | `vector.* / gradient.* / pattern.*` 和对应 source 解码、Editor 参数编辑 |
| 混合、组、剪贴、蒙版与 Blend If | 原 Go 合成器执行，选项按原类型 JSON 传入 |
| 效果、调整与智能滤镜 | `effects.* / adjustment.* / filters.*`，包括源参数解析及 Editor 参数修改 |
| ICC、16/32 位与 HDR | `color.*`、`render.precision*`，原始通道和 `.f64le`，显式显示转换 |
| PNG/JPEG、裁片、图片转 PSD | `image.* / raster.*`；所有二进制均可下载 |
| 原始描述符 | `document.DecodeDescriptor* / EncodeDescriptor`；类型化句柄可链式引用 |
| 生命周期、预算、错误与取消 | `system.* / asset.* / resource.release`；终止 Worker 清空会话 |

这是现有能力的展示，并非“所有 Photoshop 功能已实现”。操控变形、尚未开放的几何/效果组合等仍明确拒绝。每个入口的限制由 Go 库执行，错误和路径直接显示。

文件 API 的浏览器等价物：Open/Read/Parse → 导入和 `document.open/Decode`；OpenFont → `font.decode`；Save → Writer 二进制下载；ReaderAt → 内存索引；Close → `resource.release`。VisitSourceTiles / VisitSourcePrecisionTiles → ZIP。根包字体、矢量、图案、滤镜、几何别名对应子包实现；EmptyImage 的空文档用途由 `document.create` 覆盖。

## 冻结的 66 份 PSD 验收

此为历史本地验收，不是公开 Pages 功能。需要外部归档以及本地 fixture 服务，
并使用 `VITE_PSDKIT_VERIFICATION=1 npm --prefix web/demo run build` 显式构建内部回归面板。

此前实际 Photoshop ReplaceContents 强制重算、保存重开的 oracle 已保留。以下模式只开放清单内文件：

```sh
shnote --what '启动冻结集验收' --why '对照浏览器、原生 Go 和 Photoshop' run \
  go run ./cmd/psdkit-web-server -fixtures -out output/wasm-browser-new
```

`-out` 必须是新目录。点击“浏览器回归”运行 66 例和字体管线。每例实际在 Worker 导入目标 PSD 与未变形原图、读取 placed 元数据、投影、导出 PNG/PSD、重开 PSD。Go 服务复算像素、原始块、通道和输入 SHA，保存逐例结果及汇总。

投影使用禁止读取父文档保存像素的 ReaderAt 守卫；PSD 导出合法复制原始块，分别统计。58 份已支持样本必须与冻结本机 Go **原始 RGBA 完全相等**，8 份反例须明确拒绝。PS 指标采用相同绝对文档框与可见并集预乘 RGBA，不做对齐、容差或重采样修饰。

历史结果已移至外部 `psdkit-verification/output/wasm-browser-v3/`，字体 PNG 另与本机 Go 逐像素比较。浏览器与原生 Go 零差只证明平台迁移没有新增算法差异；既有 Photoshop C0 / Sharp 等差异和自动 quilt 门禁继续保留。

SDK、所有权和预算见 [SDK 文档](../sdk/README.md)。
