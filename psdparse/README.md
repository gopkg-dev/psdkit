# Native PSD parser

This package is the eager RGB8 PSD/PSB compatibility reader. Use `psdfile` or the root
`OpenDocument/ReadDocument/ParseDocument` APIs for raw-block preservation, source-precision
channel decoding and explicit source lifetimes.

`ParseFile(path)`, `ParseBytes(data)` and `ParseReader(readerAt, size)` return a `psdmodel.Document`.
The shared model is independent of the renderer; `psdrender.Document` and its layer types remain aliases
for existing integrations. The parser and model depend only on Go's standard library.
This decoded RGB8 model does not yet retain all original blocks or support editing and saving PSD files.
The parser uses only Go's standard library. It reads through a 64 KiB window, keeps decoded layer pixels,
and skips embedded linked-file payloads after recording their identifiers and source filenames.

Supported rendering data retains the frontend's RGB 8-bit subset:

- PSD version 1 or PSB version 2; 3–16 document channels within the original allocation limits.
- Raw, PackBits RLE, ZIP, and ZIP prediction layer channels and saved composite images.
- PSB 64-bit layer/channel lengths, widened RLE row lengths and tagged-block length rules.
- Straight RGBA layers, visibility, opacity, fill opacity, group structure and explicit empty folders.
- Typed composite/RGB Blend If ranges, clipping options and advanced blending booleans.
- Grayscale masks with stored document-coordinate geometry and retained relative-position flags,
  default coverage, inversion and density.
- `SoLd`/`SoLE` placed-object descriptors, legacy `PlLd`, transforms, non-affine corners, source canvas size,
  and the warp metadata needed to distinguish unchanged grids from unsupported deformation.
- Linked-source filenames and version 6–8 user image slices.

The compressed file limit is 500 MiB, each canvas/layer/mask is limited to 20 million pixels and 30,000 pixels
per side, the document permits 500 layer records, and the total decoded canvas/layer/mask budget is 60 million
pixels. Layer records are checked before allocating channel pixels. Embedded files are never opened or fetched.

`psdcompose` implements RGB8 layer-tree reconstruction for groups, deterministic blending, clipping and Blend If.
Remaining features are retained as explicit blocking metadata: adjustments, enabled layer effects,
vector/dual/feathered masks, knockout, channel restrictions, smart filters and artboards.
Empty or entirely disabled object-effect settings are distinguished from actual enabled effects.
Smart-object replacement separately validates supported kinds and warp grids; saved raster reconstruction
does not re-render smart-object contents. Detailed capability boundaries are in the root README.
Unknown extension keys produce warnings; malformed binary structures return errors.

The format implementation was checked against the installed `ag-psd` 31.0.2 reader and the
[Adobe PSD specification](https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/).
`testdata/sample-oracle.json` contains independently extracted frontend metadata and SHA-256 hashes, without
embedded source images or raw smart-object identifiers. Normal Go tests read the local
`../testdata/sample.psd`, check every saved layer/mask/composite byte against that oracle,
and require 14 instances and 9 valid slices. A missing sample is an error.
`PSD_SAMPLE_PATH` can select the same reference sample at another location.
No original application directory or public assets folder is required.

Go must preserve the frontend's floating-point operation order. In particular, composite de-matting uses
`1 / (alpha / 255)`, and projection/compositing explicitly round multiplication before addition to prevent
fused multiply-add from changing boundary pixels.
