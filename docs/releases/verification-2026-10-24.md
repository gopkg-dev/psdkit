---
title: 2026-10-24 发布交付核验
description: v0.2.0 的远端提交、CI、Release、Pages、公共代理安装和浏览器保存重开验收记录。
---
# 2026-10-24 发布交付核验

核验日期为 2026-10-24（Asia/Shanghai）。本记录固定验证当时的公开交付 **v0.2.0**；“最新版本”、远端提交、线上资源和哈希均指该核验时点。此次历史核验没有创建或移动 Tag，也没有触发发布或部署。后续版本与部署见[版本发布](../releases.md)，不改写本记录中的既有证据。

## 远端与固定版本

| 对象 | 核验结果 |
| --- | --- |
| 仓库 | [gopkg-dev/psdkit](https://github.com/gopkg-dev/psdkit)，公开仓库，默认分支 `main`；本地 origin 指向同一仓库 |
| main 提交 | [`dd482b7a0e6b508561e1c35dbcf38f9c4355e339`](https://github.com/gopkg-dev/psdkit/commit/dd482b7a0e6b508561e1c35dbcf38f9c4355e339) |
| 最新 Tag | `v0.2.0`；annotated Tag 对象 `b18595581a660bc1e0ac986736ddb86b727062db`，解引用到上述提交；远端另有 `v0.1.0` |
| 最新稳定 Release | [v0.2.0](https://github.com/gopkg-dev/psdkit/releases/tag/v0.2.0)，非草稿、非预发布，target 与 Tag 提交一致 |
| 发布时间 | 2026-10-23 01:44:08 UTC，即北京时间 09:44:08 |
| 分发方式 | Go 模块和 GitHub 源码 ZIP/tar；没有另附平台可执行文件，这与 Go 库的发布方式一致 |

## GitHub Actions 运行证据

以下均为核验时最新的对应工作流，状态为 `completed / success`，head SHA 均为上述固定提交。时间使用 UTC。

| 工作流 | 固定运行链接 | 运行时间 | 核对内容 |
| --- | --- | --- | --- |
| CI | [39885197274](https://github.com/gopkg-dev/psdkit/actions/runs/39885197274) | 2026-10-23 01:39:06–01:43:12 | 6 个 job 全成功：Linux、macOS、Windows 分别执行 race 与 vet；lint；SDK/Wasm/Playground 构建与仓库检查；发布脚本测试 |
| GitHub Pages | [39885197289](https://github.com/gopkg-dev/psdkit/actions/runs/39885197289) | 2026-10-23 01:39:06–01:41:31 | build、deploy 均成功；检查、Jekyll、浏览器构建、静态站点组装、上传与部署步骤通过 |
| Release v0.2.0 | [39885238536](https://github.com/gopkg-dev/psdkit/actions/runs/39885238536) | 2026-10-23 01:39:32–01:44:11 | prepare、6 个复用 CI 检查、consumer、publish 共 9 个 job 成功；公共代理消费验证及中文说明发布成功 |

## 公共安装与使用

在 macOS arm64 上使用 Go `go1.27.3`、Node `v24.22.1`，运行仓库已有 `scripts/release/consumer.mjs`。脚本在仓库外创建全新模块、模块缓存和构建缓存，固定 `proxy.golang.org` 与 `sum.golang.org`，禁用工作区、私有代理旁路和本地 `replace`，最后清理临时目录。

```sh
RELEASE_TAG=v0.2.0 \
RELEASE_SHA=dd482b7a0e6b508561e1c35dbcf38f9c4355e339 \
node scripts/release/consumer.mjs
```

结果：

```text
all modules verified
dependency: github.com/gopkg-dev/psdkit v0.2.0; local replace: none
format 1: PSD/PSB round-trip, repeated save and group composition OK (64x48)
format 2: PSD/PSB round-trip, repeated save and group composition OK (64x48)
```

验证包含真实编译运行、新建文档、图层组与彩色图层、全图和单组像素与边界、PSD/PSB 写出、重新打开后改名、两次原位保存和再次合成。安装版本、源码和 go.mod 校验和均检查通过。公共代理的 `@latest` 同时返回 `v0.2.0`，来源 Hash 与上述提交一致，未使用当前本地新增代码替代公开模块。

## Pages 实际资源

[首页](https://psdkit.asin1.com/)、[指南](https://psdkit.asin1.com/guide/)、[能力矩阵](https://psdkit.asin1.com/status.html)、[任务面板](https://psdkit.asin1.com/tasks.html)、[Playground](https://psdkit.asin1.com/demo/) 和 [SDK](https://psdkit.asin1.com/demo/sdk/client.js) 均实际返回 HTTP 200；资源从域名根路径加载。公开 [manifest.json](https://psdkit.asin1.com/demo/wasm/manifest.json) 标注 `go1.27.3`、协议 1、目标 `js/wasm`。逐个下载后校验如下：

| 文件 | 字节数 | SHA-256 |
| --- | --- | --- |
| `psdkit.wasm` | 15139018 | `715678023d82529b6046c1d6e50bf81b8c4cd1f26b9b360ab3a6a57c669d4dbd` |
| `wasm_exec.js` | 16992 | `0c949f4996f9a89698e4b5c586de32249c3b69b7baadb64d220073cc04acba14` |
| `worker.js` | 1772 | `e59651c2e241d40ab36b07246d363b4d90222b7bdd610e4d32a18c60c9e9481c` |

三文件均与清单一致。Wasm 返回 `application/wasm`，脚本返回 JavaScript MIME。HTTP／资源检查时间为 2026-10-23 17:32 UTC，即北京时间 2026-10-24 01:32。

## 线上浏览器操作

在 Codex 内置浏览器打开真实公共域名的 Playground，Go Worker 启动完成，能力目录显示 241 个入口，上衣模板与两张印花载入成功。随后执行：

1. 新建 64×48 的透明 RGB8 PSD。
2. 将图层改名为“交付核验-第一次编辑”，导出并实际下载 PSD。
3. 重新导入该下载文件，确认图层名与文档尺寸保留。
4. 改名为“交付核验-重开后继续编辑”，再次导出并实际下载 PSD。
5. 独立读取两份下载文件，核对图层名、64×48 尺寸和全透明像素；浏览器控制台没有 warning 或 error。

线上浏览器验证的是已部署版本。PSD/PSB 两种格式及非透明像素的检查由上面的独立公共消费程序覆盖。此项是交付冒烟验收，不替代全部浏览器、移动端和无障碍人工验收；窄屏图标菜单的可访问名称仍需在相应任务中补齐。

## 版本边界与后续

`v0.2.0` 的支持范围以[固定 Tag 的能力矩阵](https://github.com/gopkg-dev/psdkit/blob/v0.2.0/docs/status.md)与[发布说明](https://github.com/gopkg-dev/psdkit/releases/tag/v0.2.0)为准。当前本地已验收的文字自动像素、独立画板实时来源、来源替换同步重算滤镜、默认覆盖上新增矢量蒙版、矩形羽化与颜色叠加等后续改动不属于该 Tag。

本次仅修正本地说明与任务状态，没有推送当前开发工作区、发新版本或重新部署 Pages。任务面板将本项计为完成；矩阵中的图像能力大类保持原有状态。性能基线、完整移动端／无障碍验收和公开素材分发审核仍是独立未完成任务。
