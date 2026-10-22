---
title: 测试与素材
---
# 测试、预算和外部素材

```sh
go test ./...
go test -race ./...
go vet ./...
make lint
npm --prefix web/demo test
```

默认测试采用 Go 生成输入和小型 testdata，不需要 Photoshop、外部服务或大 PSD。
大型 oracle 不存在时对应测试明确 Skip；显式配置的素材缺失则失败。

```sh
PSDKIT_TESTDATA_DIR=/path/to/psdkit-testdata PSDKIT_REQUIRE_CORPUS=1 go test ./...
```

外部目录镜像源码相对路径，例如 psdfont/testdata/opentype/layout_oracle.json。
本地默认查找同级 ../psdkit-testdata，PSD_SAMPLE_PATH 可单独指定私有 PSD。
manifest.json 记录大素材大小、SHA-256 与分发状态。未获授权样例被 Git 忽略，不公开分发。

各入口分别配置加载、工作、解码和输出预算；区域读取不表示 ZIP 可以跳过完整校验。
Context 取消和资源限额属于操作约束，不是整个进程 RSS 保证。

## 渲染性能检查

```sh
go test ./psdwarp -run '^$' -bench 'BenchmarkWarpRaster(Blocks|RowScan)$' -benchmem
```

基准同时保留逐行扫描参考和分块候选筛选，回归测试逐像素核对透明图、透视弯曲与裁剪坐标。
浏览器端应分别测量图片转 PSD、智能对象替换和 `editor.Composite`（含预览编码），
将首次引擎下载与初始化时间单独记录；比较前后须使用同一模板、图片、分辨率和运行环境。

通过 errors.Is 检查不支持、超限或取消，保留结构化 path、feature、offset。
浏览器错误保留 unsupported、resource_limit、missing_font、canceled 等 code。
历史原生验收位于 psdkit-verification；Go/浏览器一致性不等于新一轮 Photoshop 精度验收。
