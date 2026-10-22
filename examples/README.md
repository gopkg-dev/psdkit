# 运行示例

从仓库根目录运行，读取类示例显式提供 `-psd`。历史大样本不在公开核心仓库中。

| 示例 | 用途 | 命令 |
| --- | --- | --- |
| create | 不依赖素材的新建 | `go run ./examples/create -out /tmp/new.psd` |
| index | 原始结构/按需通道 | `go run ./examples/index -psd /tmp/new.psd` |
| inspect | RGB8 结构和智能对象 | `go run ./examples/inspect -psd /path/input.psd` |
| edit | 编辑保存 | `go run ./examples/edit -psd /tmp/new.psd -out /tmp/edited.psd` |
| composite | RGB8 合成 | `go run ./examples/composite -psd /tmp/new.psd -out /tmp/composite.png` |
| render | 模板套图 | `go run ./examples/render -psd /path/template.psd -print /path/print.png -out /tmp/render.png` |
| color | 源精度显示/ICC | `go run ./examples/color -psd /tmp/new.psd -out /tmp/display.png` |
| scene | Source/区域/画板重绘 | `go run ./examples/scene -help` |
| precision | 16/32 位合成 | `go run ./examples/precision -help` |
| placed-layer | 单智能对象重绘 | `go run ./examples/placed-layer -help` |
| replace-contents | 真实内容替换 | `go run ./examples/replace-contents -help` |

新建普通文档不含模板智能对象；模板示例需要符合支持范围的输入。
完整参数、预算和显式近似策略通过每个示例的 `-help` 查看。
