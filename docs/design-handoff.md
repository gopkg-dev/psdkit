---
title: 设计规范
description: 首页、文档与 Playground 共用的品牌、色彩、字体、图标与交互约定。
---
# 设计规范

首页、文档网站和 Playground 共用一套设计令牌与交互语言。设计概念为 **“图层信号”**：深墨色画布作为基底，电光青与蓝紫作为“选区与信号”色，配合 Photoshop 原生语汇——透明棋盘格、选区蚂蚁线、变换控制点和图层标签——表达“用代码操控 PSD 文档”。

## 共享来源

| 内容 | 位置 | 使用方 |
| --- | --- | --- |
| 设计令牌 | `docs/assets/css/tokens.css` | Jekyll 页面直接引用；Playground 由 Vite 导入 |
| 图标路径 | `docs/_data/icons.json` | Jekyll `icon.html` include；Playground `icons.jsx` 导入 |
| 品牌标识 | `docs/_includes/logo.html`、`docs/assets/img/favicon.svg` | 全站 |
| 能力状态 | `docs/status.md` front matter | 能力矩阵页与首页 |
| 示例文档 | `cmd/psdkit-web-samples`（构建时生成） | Playground 与首页真实引擎演示 |

## 色彩

| 令牌 | 值 | 用途 |
| --- | --- | --- |
| `--bg` | `#070a12` | 深色基底 |
| `--surface` / `--surface-2` | `#0f1522` / `#141b2b` | 面板、卡片 |
| `--brand-cyan` | `#22e4ff` | 主强调、焦点、选区 |
| `--brand-violet` | `#8b7bff` | 次强调、近似策略 |
| `--state-done / partial / none` | 绿 / 琥珀 / 红 | 能力状态 |

文档提供浅色阅读主题（跟随系统，可手动切换），浅色下强调色加深以保证对比度。霓虹、发光与模糊只用于首页的视觉焦点，不出现在正文或工具面板中。

## 字体与排版

- 正文使用系统 UI 字体栈（PingFang SC / 微软雅黑等），代码与 API 名使用等宽字体栈。
- 首页标题使用超大字号与紧凑字距；文档正文 16px、行高 1.85，最大行宽约 820px。

## 交互约定

- 所有可交互元素具有可见焦点环（`--focus`）。
- 动效表达图层关系与处理过程，统一使用 `--ease-out` 曲线；`prefers-reduced-motion` 下关闭视差与过渡。
- 概念动画、真实 Go 计算结果和引擎加载状态使用不同标签区分：概念演示标注“示意”，真实输出标注“Go Wasm 输出”。
- “保存预览”与“实时重绘”在 Playground 画布上使用不同颜色的来源标签。

## 技术约定

| 约定 | 要求 |
| --- | --- |
| 部署 | GitHub Pages 纯静态；`psdkit.asin1.com` 的首页与文档在 `/`，Playground 在 `/demo/`；本地默认 `/psdkit/` 前缀 |
| Worker | 相对 `./wasm/worker.js` 或显式 `workerURL`，不写死根路径 |
| 执行 | 真实 Go Wasm，不用 Canvas 重写算法 |
| 文件 | 本机导入，不添加上传服务 |
| 能力 | 保留 unsupported、精度策略与全部预算限制 |
| 内部验收 | 公开站点隐藏依赖 `__fixtures` 的回归入口 |
| 协议 | 保留二进制通道、typed 句柄、超大整数与资源释放 |
