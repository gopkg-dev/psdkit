# Native master Lightness reference

`native_master_lightness.json` contains independent Photoshop 26.1.0 RGB8,
sRGB grayscale results for every integer master Lightness control from -100
through +100. Colorize is false, master Hue and Saturation are zero, and all
six local adjustment values are zero.

The source archive is the sibling `psdkit-verification` repository:

- `verification/wave13-huesaturation-plain-lightness-independent.json` records
  the original native PNG hashes and independent comparisons.
- `verification/huesaturation-plain-lightness-wave13-review.json` records the
  independent review of the 201 grayscale controls and eight color holdouts.
- `output/wave13-huesaturation-master-lightness-native/` contains the native PNGs.

To reproduce the reference, read `master-lightness-minusN.png` for negative
controls, `master-lightness-N.png` for positive controls, and `baseline.png`
for zero. First verify each encoded file's SHA-256 against the independent
report. Decode with Go's `image/png`, convert each pixel with `color.NRGBAModel`,
verify a 256 by 1 image with equal RGB channels and alpha 255, and hash its
1,024 row-major NRGBA bytes. The JSON retains both the encoded PNG hash and
the decoded pixel hash, together with the source report hash. No expected
pixels are generated from the production Lightness formula.

This reference establishes the RGB8 byte-domain grayscale transfer. The
separate native color and alpha holdouts permit a one-byte RGB difference;
it does not establish exact real arithmetic at arbitrary floating-point
rounding boundaries, mixed controls, Colorize, local ranges, or high bit depths.
