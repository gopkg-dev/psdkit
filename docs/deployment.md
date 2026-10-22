---
title: GitHub Pages 部署
---
# GitHub Pages 部署

pages.yml 将 Jekyll 生成的首页与文档、React/Vite 构建的 Playground 打包到同一个纯静态站点。

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

## 首次启用

1. 审核源码提交并推送到 gopkg-dev/psdkit 的 main。
2. GitHub Settings → Pages → Source 选择 GitHub Actions。
3. 运行 GitHub Pages 工作流，按仓库策略批准 github-pages 环境。
4. 检查文档、Demo、Worker、Wasm 在子路径正常加载，验证导入、编辑与下载。

PR 只构建；主分支和手动触发部署。部署作业单独具有 pages:write 与 id-token:write，
构建只有源码读取权限。部署地址为 [首页与文档](https://psdkit.asin1.com/)
和 [Playground](https://psdkit.asin1.com/demo/)。

当前项目单独绑定 `psdkit.asin1.com`，DNS 的 CNAME 指向 `gopkg-dev.github.io`，不修改账号首页域名。
线上 Jekyll 的 `baseurl` 为空，文档与 Demo 从域名根路径加载。

## 本地构建与预览

将 `gopkg-dev/psdkit-assets` 放在本仓库旁，或设置 `PSDKIT_SAMPLE_ASSETS=/path/to/psdkit-assets`。
CI 和 Pages 工作流从 [演示素材仓库](https://github.com/gopkg-dev/psdkit-assets) 检出到 `.sample-assets/`。
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
