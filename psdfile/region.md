# 按区域保留原始样本

`DecodeChannelRegion(ctx, layerIndex, channelIndex, rectangle)` 读取一个图层通道的小块。
矩形采用通道自身坐标，`(0,0)` 对应 `ChannelRecord.Bounds.Min`；蒙版使用自己的通道尺寸。
矩形必须完整位于通道内，不会自动裁切。返回 `Plane` 的宽高是矩形宽高，Data 独立拥有。

```go
tile, err := source.DecodeChannelRegion(ctx, 0, 0, image.Rect(256, 128, 512, 384))
```

`DecodeCompositeChannelRegion` 使用同一规则读取保存的合成图通道。
两种入口都保留 1/8/16/32 位；16/32 位仍为大端原始字节，NaN payload 和符号零不会转换。
1 位样本重新打包为 MSB-first，输出每行末尾未用的位清零。

Raw 只读取目标行的目标字节段。RLE 扫描完整行计数表以验证载荷边界，只保留目标行计数；
它读取目标行的 PackBits 控制字和所需 literal 字节段，跳过其他行与无关 literal。
未选中 RLE 行的 PackBits 内容不会解码，Raw 的未选中字节也不会访问。
非空 ZIP 请求流式读取整个解压结果并校验长度、校验和及尾部，只保留当前行和目标块。
ZIP prediction 逐行还原，32 位同时保留一行字节重排工作区。空矩形不读取任何像素载荷。

`MaxDecodePixels` 约束目标矩形；它不会因为完整源图超出像素预算而拒绝小块。
`MaxDecodeBytes` 在读取前约束输出、位图重打包、目标 RLE 行计数、ZIP 行工作区和压缩读取缓冲。
`RegionOptions.MaxWorkingBytes` 可逐次压低此工作内存预算，不能提高源文档预算。
Go 标准库 zlib/flate 还使用与图像尺寸无关的内部固定工作区。
一个源行本身超出 ZIP 工作区预算时仍会拒绝，ZIP 也仍需要按序解压前面及后面的行。
取消、关闭和原始 ReaderAt 错误会传播，失败不返回部分 Plane。

工作量另有独立限制：`MaxInflatedBytes` 默认采用源文档的 `MaxDecodeBytes`，计算整个 ZIP 流的
解压长度，包括合成图未选中通道。超过时需显式提高，例如 `RegionOptions{MaxInflatedBytes: 1 << 30}`。
`MaxRLEWorkBytes` 默认 256 MiB，计算完整行计数表与选中压缩行字节的保守工作量上界。
这些限额约束扫描/解压时间的规模，提高它们不会提高内存预算；长解压仍应提供可取消的 Context。

`NewRegionCache` 为同一只读源文档提供有界 LRU，默认保留 64 MiB 样本、256 个矩形：

```go
cache, err := psdfile.NewRegionCache(source, psdfile.RegionCacheOptions{
    MaxBytes: 32 << 20,
    MaxEntries: 128,
})
tile, err := cache.DecodeChannelRegion(ctx, 0, 0, image.Rect(256, 128, 512, 384))
```

命中也返回独立副本。超过缓存容量的单块可读取但不保留；并发冷请求可以各自解码。
缓存的 `DecodeOptions` 固定这一缓存所有冷请求的区域工作预算，命中无需重复解压。
缓存容量约束保留样本，源解码工作区及调用方返回值分别受源预算和调用方生命周期约束。
`Stats` 报告命中、未命中和当前样本字节；`Clear` 清理保留块但不重置累计计数。
关闭缓存不关闭借用的文档；文档关闭后，缓存命中也返回 `ErrClosed`。
