---
title: 版本发布
description: 推送版本 Tag 后自动检查、验证 Go 模块引用并生成中文 GitHub Release。
---
# 版本发布

主仓库的 [Release 工作流](https://github.com/gopkg-dev/psdkit/actions/workflows/release.yml) 监听 `v*` Tag。
维护者将准备好的提交合并到 `main`，再推送一个新的版本 Tag，即可自动完成发版。
不需要额外的 PAT、AI 服务或第三方发布平台；发布权限来自 GitHub Actions 内置令牌。

## 已发布版本与交付核验

当前文档对应 [v0.3.0](https://github.com/gopkg-dev/psdkit/releases/tag/v0.3.0)，新增能力与升级范围见[本版说明](releases/v0.3.0.md)，准确边界以[固定 Tag 的矩阵](https://github.com/gopkg-dev/psdkit/blob/v0.3.0/docs/status.md)为准。每个新版本都通过下述 Tag 工作流执行跨平台检查、公共模块引用验证和中文 Release 发布。

2026-10-24 对 `v0.2.0` 的[历史交付核验](releases/verification-2026-10-24.md)保留了当时的提交、CI／Pages／Release 运行、公共代理安装和浏览器保存重开证据。这是固定时点的记录，不能代表后续 `main`、线上部署或新版本的支持范围。

## 发布步骤

先确认本地 `main` 已与远端同步、版本变更已检查，再创建并推送 Tag。例如未来的补丁版本（以下仅为示例）：

```sh
git switch main
git pull --ff-only origin main
git tag -a v0.3.1 -m "psdkit v0.3.1"
git push origin refs/tags/v0.3.1
```

示例中的版本号需要按实际变更调整。工作流依次执行：

1. 检查版本格式、`go.mod` 模块路径，以及 Tag 提交是否已进入 `main`，固定提交 SHA 和 Tag 对象。
2. 复用 CI：Linux、macOS、Windows 的竞态测试与 vet，golangci-lint 与格式检查，浏览器 SDK 测试、Wasm/Playground 构建、仓库检查和发布脚本测试。
3. 在仓库外创建全新的 Go 项目与缓存，执行 `go get github.com/gopkg-dev/psdkit@版本号`。
4. 从 `proxy.golang.org` 下载，通过 `sum.golang.org` 校验；检查精确版本、校验和和无本地 `replace`，实际编译运行 PSD/PSB 创建、保存重开、重复原位保存、图层组合成与像素检查。
5. 生成中文说明，再次确认远端 Tag 没有变化，创建 GitHub Release。只有这个最终任务有仓库写权限。

运行结果、引用验证与发布说明预览都在 Actions 的 Summary 中。Go 库通过 Tag 分发，
GitHub 自动提供源码 ZIP/tar，无需单独上传平台可执行文件。

## 中文发布说明

默认按上一版本到当前 Tag 的提交生成说明，将功能、修复、性能、文档、测试、构建等变更分组，
附上提交链接、完整比较链接、精确安装命令、Go 版本、检查记录和此版本的能力矩阵。
提交标题保留原文，建议使用简短中文 Conventional Commits，例如 `fix(render): 修复透明图层合成`。
`!` 或提交正文中的 `BREAKING CHANGE:` / `BREAKING-CHANGE:` 会归入兼容性变更。

重要版本可在打 Tag **之前**提交 `docs/releases/vX.Y.Z.md`，写入需要强调的中文升级说明。
该文件作为发布说明的开头补充，不需要 YAML front matter；其余内容仍自动生成。
正式版本与上一正式版本比较，覆盖这次预发布周期的全部变更；预发布版本与上一可达版本比较。

## 版本格式和失败重跑

- 接受 `v0.1.1`、`v1.0.0` 和 `v0.2.0-rc.1` 等完整语义版本；预发布会自动标记为 Pre-release，不设为 Latest。
- 不接受省略补丁号、数字前导零和 `+` 构建元数据。当前模块路径支持 v0/v1；v2 需要先迁移模块路径及引用验证程序。
- 公共代理暂未同步时最多尝试 8 次，每次命令最多 60 秒，间隔从 15 秒增加到最多 60 秒；不回退本地源码或 direct。
- 检查或引用验证失败时不会创建正式 Release。可以在 Actions 中 **Re-run failed jobs** 或 **Re-run all jobs**。
- 重跑会保留已发布 Release 的说明和状态；同一 Tag 的运行串行执行。已有草稿可在检查通过后发布。
- 已推送的 Tag 本身就可被 Go 下载，即使工作流失败也不能隐藏它。**不要移动、删除或复用已公开版本的 Tag**；代码修复使用新版本。
- 若只是代理、网络或检查环境暂时失败，重跑原工作流；若必须修改该 Tag 中的代码或工作流，提交修复后使用新 Tag。

GitHub Pages 仍由 `main` 推送触发，独立于库版本 Release。

## 手动验证已有版本

在 Actions → Release → **Run workflow** 中选择 `main`，输入已存在的 Tag，例如 `v0.2.0`。
该入口运行同样的代码检查、公共模块引用验证和中文说明预览，**不会创建或修改 Release**。
检查与构建针对输入 Tag 的提交；发布工具取自所选工作流版本，因此也能验证接入自动化前的版本。

本地可先运行发布脚本测试、预览版本说明，或验证公开模块：

```sh
make release-test
RELEASE_TAG=v0.2.0 node scripts/release/release.mjs notes
RELEASE_TAG=v0.2.0 node scripts/release/consumer.mjs
```

预览需要包含 Tag 和 `origin/main` 的完整 Git 历史；引用验证需要 Node.js 24、
目标版本要求的 Go 工具链，以及可连接公共代理和校验和服务的网络。
本地脚本不会创建 Tag，也不会上传版本。
