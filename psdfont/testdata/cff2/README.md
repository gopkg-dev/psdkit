# CFF2 instance controls

These fonts are self-generated QA assets. The Go implementation uses only the standard library.
The manifest records each exact font byte hash and size. The tests load only relative package paths.

The glyph corpus covers CFF2 EOF, empty charstrings, FDSelect format 4, Private `vsindex` inheritance,
513 operands and all existing path operators. Instance cases cover fixed 16.16 normalization,
F2DOT14 coordinates, avar 1/2, HVAR, MVAR, GDEF variation indices and GSUB/GPOS FeatureVariations.
The raw HVAR metrics remain distinct from precise selected metrics. Integer Shape/legacy layout
positions use one final `floor(value + .5)` projection; horizontal GPOS YAdvance is ignored.

The avar 2 precision profile retains segment-mapped 16.16 coordinates and uses an F2DOT14 snapshot
for all cross-axis scalars. The final vector commits after every axis delta is evaluated. This matches
the pinned HarfBuzz profile in the shared research; published OFF 2026 status and later correction
proposals are recorded separately in verification. FontTools intermediate-precision differences remain.

CFF2 matrix qualification uses the declared positive-uniform reciprocal-nearest-head profile. The
original matrix and source bytes remain owned. HVAR maps accept formats 0 and 1. CFF2 vstore's
65535 length sentinel accepts only references inside its CFF2 table. Missing STAT is accepted for
local metadata compatibility controls; those fonts are not presented as complete variable-font
conformance samples. The Source corpus has separate STAT-qualified derived controls.

Explicit RSB maps use original hmtx values and encoded unvaried outline operands. A second VM
shares point, contour, visit, byte and operation quotas; its blend scalars are zero. Normalized zero
and the selected default user instance can each contain nonzero contributions and are separate domains.

Hint operands are checked under a strict profile without executing hinting. HM requirements,
stem order, edge widths, mask unused bits and counter constraints are preserved. Active overlap
or unmanaged overlap rejects; that error policy does not claim hinted rasterizer behavior.

The initial PairPos format 1 self-fixture and implementation incorrectly used PairPos-relative
variation offsets. Those bytes, initial passes and the later causal failure remain in verification.
Current format 1 offsets are PairSet-relative; format 2 offsets remain PairPos-relative.
The raw-baseline constant-region counterexample and the original unquantized 110-row metric
oracle failure also remain separately recorded. No tolerance was increased.

These controls do not establish Photoshop glyph antialiasing, complex-script shaping, hinting,
automatic AAT, CFF2 writing or a process-wide CPU/RSS bound.

Primary format references: [CFF2](https://learn.microsoft.com/en-us/typography/opentype/spec/cff2),
[variation overview](https://learn.microsoft.com/en-us/typography/opentype/spec/otvaroverview),
[common variation formats](https://learn.microsoft.com/en-us/typography/opentype/spec/otvarcommonformats),
[GPOS](https://learn.microsoft.com/en-us/typography/opentype/spec/gpos).
