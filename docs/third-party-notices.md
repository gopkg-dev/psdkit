---
title: 许可与第三方声明
description: psdkit 的 MIT 许可，以及第三方代码、运行时与参考资料的来源和许可。
---
# 许可与第三方声明

## 项目许可

psdkit 自有代码采用 [MIT 许可](https://github.com/gopkg-dev/psdkit/blob/main/LICENSE)：可以自由使用、复制、修改、合并、发布、分发、再许可和销售，只需在副本中保留版权声明与许可声明。软件按“原样”提供，不附带任何担保。

下列第三方代码及素材保留各自许可。

## 网站与浏览器运行时

浏览器 Playground 使用 React、React DOM 和 Scheduler（MIT），完整运行时声明在
`web/demo/public/THIRD-PARTY-LICENSES.txt`，静态构建会一并复制。
首页与文档网站不引入第三方运行时脚本；示意动画与图形均为本项目自有的 CSS / SVG，随 MIT 发布。
文档由 Jekyll 生成（构建工具，不进入发布产物）。
Vite 及其 React 插件仅作为构建工具使用；版本和依赖许可在 npm lockfile / 各包 LICENSE 中保留。
Go Wasm 运行时直接复制自构建工具链，并同时携带该工具链的 `LICENSE-Go.txt`。
验收用 BridgeFixture 字体由本库 Go 源码生成，不分发系统或商业字体。

## 规范与参考实现

格式与预测/Descriptor 语义的实现参考以下公开规范及开源项目，库本身不依赖其运行时：

- [Adobe PSD/PSB 与 Action Descriptor 规范](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/)。
- [psd-tools 压缩实现](https://github.com/psd-tools/psd-tools/blob/main/src/psd_tools/compression/__init__.py)。
- [ag-psd Descriptor 实现](https://github.com/Agamnentzar/ag-psd/blob/master/src/descriptor.ts)。

P3 的混合与合成数学参考公开规范，并通过本机 Photoshop 生成的自有小样本校准：

- [W3C Compositing and Blending Level 1](https://www.w3.org/TR/compositing-1/)。
- [Adobe 混合模式说明](https://helpx.adobe.com/photoshop/using/blending-modes.html)。
- [psd-tools 混合算法说明](https://psd-tools.readthedocs.io/en/stable/_modules/psd_tools/composite/blend.html)。

外部 `psdkit-verification/verification` 下的 Python 检查使用可选的 psd-tools/Pillow 作为独立解析和像素对照工具。
`output/p3-native` 小样本由验收脚本在本机 Photoshop 创建；它们不增加生产库的运行时依赖。

## JPEG 编码器

`psdrender/internal/canvasjpeg` 改编自 Go 1.25.0 的 `image/jpeg`。
原版权头、[BSD 许可证](https://github.com/gopkg-dev/psdkit/blob/main/psdrender/internal/canvasjpeg/LICENSE)和
[修改说明](https://github.com/gopkg-dev/psdkit/blob/main/psdrender/internal/canvasjpeg/README.md)随源码保留。
Independent JPEG Group 的原始版权和授权声明保留在 `fdct.go` 文件开头。

This software is based in part on the work of the Independent JPEG Group.

## Canvas 数值兼容

`internal/canvaspix` 保留原实现的 Canvas 八位预乘与读回量化行为。

## PSD 测试样本

网页上衣示例的 `muban.psd` 由用户提供，原文件保存在独立的 `psdkit-assets/garment/`，
构建时校验摘要并按原字节复制。两款示例印花「山野日落」「向阳花园」为本项目生成的原创图片。
这些演示素材与核心源码分开保存。

README 展示图 `docs/assets/img/readme-hero.webp` 使用上述模板及两款印花的真实 Go 渲染结果排版，
沿用网站配色与标志；成衣效果采用示例声明的显式数学 Warp 近似。该轻量预览不包含原始 PSD 文件。

私有样本位于外部 `psdkit-testdata/testdata/sample.psd`，被该仓库 Git 忽略，不随核心库分发。
它是 Lingtu 官方样例的本地副本，
用于本地解析及渲染兼容验证。样例中的照片、作品及其他内容仍归原权利人所有，
不属于依赖代码的许可证范围。此本地副本不表示取得公开再分发该样例的授权。

`psdparse/testdata/sample-oracle.json` 记录独立读取的元数据与像素摘要，
不包含嵌入源图片或原始智能对象标识符。

## CFF StandardEncoding 表

`psdfont/cff_standard_encoding.go` 的完整 code/name/SID 数据转换自 Adobe AFDKO 的
`stdenc1.h`、`stdenc2.h` 和 `stdstr1.h`，固定提交为
[`c53bdc6d489942d48c9d83d22ed02e2973d40637`](https://github.com/adobe-type-tools/afdko/tree/c53bdc6d489942d48c9d83d22ed02e2973d40637/c/shared/resource)。
原表的 Copyright 2014 Adobe Systems Incorporated 版权头随 Go 表保留；修改仅为 Go 字面量及槽分类标记。
[AFDKO 原许可](https://github.com/gopkg-dev/psdkit/blob/main/psdfont/LICENSE-AFDKO)、[Apache 2.0 全文](https://github.com/gopkg-dev/psdkit/blob/main/psdfont/LICENSE-Apache-2.0.txt)和
[来源与修改说明](https://github.com/gopkg-dev/psdkit/blob/main/psdfont/NOTICE)随此表分发。这些声明只描述该表的来源，不为整个库另选许可证。

## Source Han Sans 验收字体

`output/fonts/source-han-2.005R` 包含从 Adobe 官方
[Source Han Sans 2.005R](https://github.com/adobe-fonts/source-han-sans/releases/tag/2.005R)
取得的未修改 `SourceHanSansCN-Medium`、`SourceHanSansCN-Bold` 和 `SourceHanSansCN-Heavy` OTF，
用于用户提供的 `test.psd` 的精确字体名称验收，不自动安装或替换系统字体。
字体采用 SIL Open Font License 1.1；原 LICENSE.txt 和版本、来源、SHA-256 清单
随外部 `psdkit-verification/output/fonts/source-han-2.005R/` 本地验收资源保留。
生产库继续只读取调用方明确提供的字体，不依赖这些测试资源或联网下载。
本次 Photoshop 原生对照将同一字节的三个字体安装为
用户字体目录中的 `psdkit-SourceHanSansCN-{Medium,Bold,Heavy}-2.005R.otf`，
用于 Photoshop 26.1 与 Go 同版字体验收；生产库不安装字体，也不依赖此系统目录。

## CFF1 hint 拟合数学参考

`psdfont/hint.go` 和 `hint_map.go` 的有限网格模型用 Go 独立编写；
源数据含义参考 Adobe Type1/Type2/CFF 规范。
保留宽度的最近边缘对齐规则参考 FreeType 2.14.3 中公开的 Adobe CF2 算法，
固定提交为 [`0a0221a1347e2f1e07c395263540026e9a0aa7c7`](https://github.com/freetype/freetype/tree/0a0221a1347e2f1e07c395263540026e9a0aa7c7)。
未移植其 C 源码、暗化或 counter 等距实现；原项目许可声明可在
[FreeType LICENSE](https://github.com/freetype/freetype/blob/0a0221a1347e2f1e07c395263540026e9a0aa7c7/LICENSE.TXT)核对。
这些算法参考不代表 Photoshop 使用相同实现，也不将输出标为原生逐像素相等。
