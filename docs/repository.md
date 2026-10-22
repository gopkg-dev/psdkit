---
title: 仓库结构
---
# 仓库结构与拆分

| 位置 | 用途 |
| --- | --- |
| 根包 | github.com/gopkg-dev/psdkit 统一入口 |
| psdfile / psdparse / psdmodel | 原始文档、RGB8 兼容解析和模型 |
| psdedit | 受控编辑与 writer |
| psdcompose / psdscene / psdrender | 合成、来源重绘和模板输出 |
| psdfont / psdtext | 字体与文字 |
| 其他 psd* | 色彩、位深、Warp、效果、滤镜、矢量和填充 |
| internal / examples | 内部实现、测试辅助与小型示例 |
| cmd / psdweb / web | Wasm、协议、示例文档生成、构建与 Playground |
| docs / .github | 首页、使用文档与 CI/Pages |

根目录仅保留 README.md 与 LICENSE 两份说明文件。贡献和安全说明位于 .github/；
路线图、第三方说明和其他使用文档集中在 docs/，由 GitHub Pages 发布。

以下目录与核心源码分开保存：

- **psdkit-testdata**：约 80 MiB 大型 oracle 与私有样本，镜像路径，附校验清单。
- **psdkit-verification**：原 verification、原 output 及旧 README/路线图，原文件完整保留。
- **[psdkit-assets](https://github.com/gopkg-dev/psdkit-assets)**：网页示例使用的原始上衣模板和两张原创印花，独立于核心源码分发；CI 与 Pages 构建时检出。

普通 Go 库测试不依赖这些目录，网站构建需要演示素材仓库。
私有测试 PSD 与原生验收输出继续留在本地，不随公开仓库分发。
核心仓库保留小型单元测试，不为减少行数将紧密依赖的生产包拆成多个 module。
超过 1 MiB 的外部语料、生成图像和重复冻结源码不进入核心仓库。
读取示例显式传 -psd，或先用 examples/create 生成自有输入。
