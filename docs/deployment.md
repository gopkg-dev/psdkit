---
title: 腾讯 COS 网站部署
---
# 腾讯 COS 网站部署

cos.yml 将 Jekyll 生成的首页与文档、React/Vite 构建的 Playground 打包到同一个纯静态站点，发布到普通腾讯云对象存储 COS。域名为 `psdkit.asin1.com`，站点不依赖 GitHub Pages 服务。

| 路径 | 内容 | 源码 |
| --- | --- | --- |
| / | 首页 | `docs/index.html`、`docs/_layouts/home.html` |
| /guide/、/status.html 等 | 使用文档 | `docs/**/*.md`，导航在 `docs/_data/nav.yml` |
| /demo/ | Playground | `web/demo` |
| /demo/wasm/ | Wasm、Worker、配套运行时、清单与 Go 许可 | `cmd/psdkit-web-build` |
| /demo/samples/ | muban.psd、两张上衣印花及校验清单 | `cmd/psdkit-web-samples`、独立 psdkit-assets 仓库 |
| /demo/sdk/ | 浏览器 SDK 源文件，首页按需导入 | `web/sdk` |

首页与文档不使用第三方主题或运行时脚本，样式与脚本位于 `docs/assets/`，设计令牌 `tokens.css` 与图标 `_data/icons.json` 同时被 Playground 导入。

Vite base 和 SDK Worker 默认使用相对路径，可覆盖 PSDKIT_BASE_PATH 或 workerURL。
Wasm 与 wasm_exec.js 必须来自同一 Go 工具链。

## 线上交付与历史核验

线上交付以[最近成功的 COS 工作流](https://github.com/gopkg-dev/psdkit/actions/workflows/cos.yml)、站点根路径的 `deployment.json` 及公开运行时清单为准。`deployment.json` 记录源码提交、公开文件数量、字节数、整站摘要和完成时间；仅当全部文件上传成功后才更新。

2026-10-24 对 `v0.2.0` 的历史核验固定于 [Pages 运行 39885197289](https://github.com/gopkg-dev/psdkit/actions/runs/39885197289)，部署提交为 `dd482b7a0e6b508561e1c35dbcf38f9c4355e339`。当时已实测页面、运行时文件及浏览器保存重开；固定链接、资源校验和与完整步骤见[历史交付记录](releases/verification-2026-10-24.md)。该记录保留当时使用 Pages 的事实，不代表现在的托管方式。

线上内容来自实际部署的提交。本地预览更新不表示线上已同步；须以 COS 发布完成记录和公共域名实测结果确认。

## 配置与后续部署

1. 使用广州地域（`ap-guangzhou`）的网站专用普通 COS 存储桶 `psdkit-site-<APPID>`，设置公有读私有写，并开启静态网站，索引文档为 `index.html`。轻量对象存储（Lighthouse 版）不支持自定义源站域名，不能替代此步骤。
2. 在 COS 的「域名与传输管理 → 自定义源站域名」绑定 `psdkit.asin1.com`，源站类型选择静态网站源站，配置有效的 HTTPS 证书。
3. 在 GitHub 仓库或 `cos-production` 环境的 Secrets 中设置 `COS_SECRET_ID`、`COS_SECRET_KEY`；在 Variables 中设置 `COS_BUCKET`（含 APPID 的完整桶名）、`COS_REGION`。使用仅允许目标桶对象写入的凭据，密钥不写入源码、文档或构建产物。
4. 审核源码提交并推送到 gopkg-dev/psdkit 的 `main`，或在 `main` 手动运行 Tencent COS Website 工作流。PR 只检查和构建；其他分支的手动运行也不会部署。
5. 首次迁移先上传并验证 COS 源站，随后把子域名 CNAME 切换到 COS 控制台提供的静态网站源站目标；验证公共域名后，再关闭仓库 Settings → Pages。原 DNS 记录在验收前保留为回退依据。
6. 检查首页、`/guide/`、`/demo/`、SDK、Worker、Wasm 和样例下载；在 Playground 中验证导入、编辑、导出及导出文件重新打开。

工作流使用锁定依赖的 Ruby/Jekyll 构建文档，并通过普通 Actions artifact 传递 `_site`；不使用 Pages 构建、上传或部署动作，也不申请 `pages:write` 或 `id-token:write` 权限。
部署地址为 [首页与文档](https://psdkit.asin1.com/)
和 [Playground](https://psdkit.asin1.com/demo/)。

项目单独绑定 `psdkit.asin1.com`，Jekyll 的 `baseurl` 为空，文档与 Demo 从域名根路径加载。DNS 或代理配置只调整该子域名；不修改其他主机的 DNS、SSL 模式或重定向。

### 文件与缓存约定

`scripts/deploy-cos.py` 只发布已构建的完整公开站点，拒绝符号链接、隐藏路径和常见私密文件，并检查首页、文档、Playground、SDK、运行时及清单是否齐备。`CNAME` 和 `.nojekyll` 是 Pages 专用文件，不上传到 COS。

上传保持同源目录结构，先传资源，后传 HTML，最后更新 `deployment.json`。脚本不删除 COS 中的旧文件，旧版带哈希资源可继续供已打开页面使用。失败时工作流报错，不更新发布完成记录；如需回退，应重新发布上一份完整构建产物。分文件上传不是整站原子切换，固定名称运行时更新期间仍存在短暂版本交错窗口。

- `.js` 使用 JavaScript MIME，`.wasm` 使用 `application/wasm`。
- `.psd.gz` 使用 `application/gzip`，**不设置 `Content-Encoding: gzip`**，由 SDK 校验压缩原始字节后自行解压。
- Vite 带内容哈希的 `demo/assets/` 文件长期缓存；HTML、Worker、Wasm、清单和其余固定路径使用 `Cache-Control: no-cache`，避免旧运行时与新页面混用。
- 全站必须提供 HTTPS：样例校验依赖安全上下文中的 `crypto.subtle`。如启用 CDN 或其他代理，应保留上述响应头和压缩字节，不覆盖固定路径的缓存策略。

### 本地发布

先使用下面的方式构建整站，再预检上传范围：

```sh
python3 scripts/deploy-cos.py --site _site --dry-run
```

发布前安装 `scripts/cos-requirements.txt`，将前述四个 COS 配置项放入环境变量，再运行 `python3 scripts/deploy-cos.py --site _site --revision <完整源码提交SHA>`。脚本也支持可选的 `COS_SESSION_TOKEN` 临时凭据。不要把密钥直接拼在命令行参数中。

## 本地构建与预览

将 `gopkg-dev/psdkit-assets` 放在本仓库旁，或设置 `PSDKIT_SAMPLE_ASSETS=/path/to/psdkit-assets`。
CI 和 COS 工作流从 [演示素材仓库](https://github.com/gopkg-dev/psdkit-assets) 检出到 `.sample-assets/`。
原始输入 SHA-256 固定在 `web/samples/manifest.json`，构建输出不进入核心 Git 仓库。

```sh
bundle install --gemfile docs/Gemfile   # 首次：安装 Jekyll（可设 BUNDLE_PATH=vendor/bundle）
make site                               # docs → _site/，Playground → _site/demo/
make site-preview                       # http://127.0.0.1:8301/psdkit/
```

本地默认保留 `/psdkit/` 子路径，通过 `SITE_BASE_PATH` 覆盖；线上直接读取 `docs/_config.yml`。
要按生产域名的根路径预览，运行 `make site SITE_BASE_PATH=`，再运行 `make site-preview SITE_BASE_PATH=`。
只预览 Playground 时可在 `make web` 后运行 `go run ./cmd/psdkit-web-server`（站点根路径）。
公开站点不提供 __fixtures 服务、文件上传、Photoshop 服务或系统字体。
