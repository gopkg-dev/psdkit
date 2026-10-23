# LZW reference fixtures

These synthetic RGB8/RGBA8 images contain no user artwork. A fixed 32-bit LCG
generates the expected samples; LibTIFF 4.7.2 encodes the LZW streams independently
of psdkit. The generator uses Pillow 12.3.0 to write the uncompressed input and
verify the decoded reference output. Runtime tests require neither dependency.

Regenerate using `python3 psdfile/testdata/generate_lzw_fixtures.py` with Pillow and
`tiffcp` available. The larger fixtures cross the 9/10/11/12-bit dictionary-width
boundaries and clear the dictionary; the RGBA fixtures also use horizontal
prediction and multiple strips. Both TIFF byte orders are represented.

The implementation follows TIFF 6.0 sections 13–14, including clear code 256,
EOI 257, MSB-first byte packing and the decoder width changes after entries
510, 1022 and 2046. It does not implement historical nonstandard LSB-first LZW.

Specification: [TIFF 6.0](https://www.itu.int/itudoc/itu-t/com16/tiff-fx/docs/tiff6.pdf).
