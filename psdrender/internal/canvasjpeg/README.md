# Canvas JPEG compatibility

This encoder is derived from Go 1.25.0 `image/jpeg/writer.go` and `fdct.go`.
The Go Authors' BSD license is included in `LICENSE`; original copyright notices are preserved.
This software is based in part on the work of the Independent JPEG Group. Its additional license notice
is retained, unaltered, at the beginning of `fdct.go`.

Local changes retain the browser's baseline 4:2:0 sampling and quality-95 quantization tables, and match
libjpeg's fixed-point RGB conversion and alternating chroma downsampling rounding.
The older Go integer FDCT follows the same IJG transform used by
the reference Canvas encoder; newer Go releases changed DCT rounding and therefore encoded pixels.
No C library, dynamic codec, browser, WASM module, or executable is loaded.
