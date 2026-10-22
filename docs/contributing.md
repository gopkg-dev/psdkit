---
title: 参与贡献
description: 开发环境、检查命令、代码约定与提交要求。
---
# 参与贡献

欢迎提交问题、测试用例与改进。完整约定见仓库中的 [CONTRIBUTING.md](https://github.com/gopkg-dev/psdkit/blob/main/.github/CONTRIBUTING.md)，安全问题请按 [SECURITY.md](https://github.com/gopkg-dev/psdkit/blob/main/.github/SECURITY.md) 私下报告。

## 开发环境

- Go **1.27.3**；
- 构建 Playground 需要 Node.js 24（`.node-version`）；
- 本地构建文档需要 Ruby 与 Bundler（`bundle install --gemfile docs/Gemfile`）。

```sh
make check       # 测试、vet、lint、格式检查、SDK 与发布脚本测试、仓库检查
make race        # Go 竞态检测
make web         # Wasm 与 Playground
make site        # 文档 + Playground 统一静态站点
```

## 原则

- 核心库只依赖 Go 标准库。
- 先检查错误再执行主逻辑，用 `errors.Is` / `errors.AsType` 处理可识别错误。
- 公共注释说明所有权、坐标、位深、预算、取消和不支持条件。
- **不把缓存图、静默降位或显式近似包装成完整原生支持。**
- 修改导出函数注释后运行 `go run ./cmd/psdkit-web-docs`，同步浏览器能力目录。

## 测试与素材

优先用 Go 生成最小输入并断言可观察行为；像素修复需要独立计算或可信 oracle。超过 1 MiB 的外部数据、真实模板和原生应用记录独立存放，见[测试与素材](testing.md)。新增素材需要说明来源和许可，不提交个人文件、商业字体或构建产物。

## 文档与能力状态

- 文档源文件是 `docs/` 下的 Markdown，`_site/` 是生成结果。
- [能力矩阵](status.md)的数据只维护在 `docs/status.md` 的 front matter 中，首页与矩阵页都从这里渲染。能力描述必须以源码、测试和运行时 `system.capabilities` 为准。
- 涉及浏览器桥接的修改需要实际打开 Playground 验证。

当前尚未发布 v1，公共 API 仍可能调整。项目采用 [MIT 许可](third-party-notices.md)。

维护者发布新版本时，推送版本 Tag 即可触发检查、独立项目引用验证和中文 GitHub Release，详见[版本发布](releases.md)。
