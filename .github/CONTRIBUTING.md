# 贡献指南

需要 Go 1.27.3；构建 Demo 还需要 Node.js 24。

```sh
make check
make race
make web
```

CI 在 Linux、macOS、Windows 执行 Go 测试与 vet，另检查 lint 和浏览器构建。
Go 库测试不依赖外部素材；浏览器构建会检出演示素材仓库，见 [测试说明](../docs/testing.md)。

## 代码规范

- `make fmt` 使用 gofmt/gci，`make lint` 使用仓库固定版本及 `.golangci.yml`。
- Go 库只依赖标准库。
- 先检查错误再执行主逻辑，使用 `errors.Is` / `errors.AsType` 处理可识别错误。
- 按需采用 slices、maps、整数 range 和 `new(value)` 等现代写法。
- 公共注释说明所有权、坐标、位深、预算、取消和不支持条件。
- 不把缓存图、静默降位或显式近似包装成完整原生支持。

nil Context 是现有库允许的输入，相关回归测试特意保留，仅测试忽略 SA1012。
测试 fixture 的字节缓冲写入允许省略错误检查；生产代码仍检查 errcheck。
PSD Descriptor 的 `doub`、`clas` 标记不属于拼写错误。

## 测试与提交

优先以 Go 生成最小输入，断言可观察行为；像素修复需要独立计算或可信 oracle。
小型 fixture 跟随包，超过 1 MiB 的外部数据、真实模板和原生应用记录独立存放。
新增素材说明来源和许可，不提交个人文件、商业字体、生成结果或构建产物。

修改导出函数注释后运行 `go run ./cmd/psdkit-web-docs` 同步浏览器 API 目录。
提交说明包含问题、行为和验证结果。涉及浏览器桥接需实际打开 Demo。
当前尚未发布 v1，公共 API 仍可能调整。

## 发布版本

合并到 main 后推送新的版本 Tag，Release 工作流会自动运行全部 CI、验证公共 Go 模块引用，
并创建带中文说明的 GitHub Release。预发布、失败重跑和只读验收入口见[版本发布](../docs/releases.md)。
