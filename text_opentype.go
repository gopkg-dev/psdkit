package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdscene"
)

// OpenTypeTextKerning selects an explicit kerning policy for source text rendering.
type OpenTypeTextKerning = psdscene.OpenTypeTextKerning

// OpenTypeTextHinting selects an explicit approximate CFF1 source-hint fitting policy.
type OpenTypeTextHinting = psdscene.OpenTypeTextHinting

// OpenTypeTextBluePolicy selects explicit CFF1 source alignment-zone treatment.
type OpenTypeTextBluePolicy = psdscene.OpenTypeTextBluePolicy

const (
	// OpenTypeTextBlueGrid preserves default blue-zone fitting.
	OpenTypeTextBlueGrid = psdscene.OpenTypeTextBlueGrid
	// OpenTypeTextBlueResolvedOutward fits resolvable source zones outward from the device baseline.
	OpenTypeTextBlueResolvedOutward = psdscene.OpenTypeTextBlueResolvedOutward
)

const (
	// OpenTypeTextHintNone preserves default unhinted text rendering.
	OpenTypeTextHintNone = psdscene.OpenTypeTextHintNone
	// OpenTypeTextHintVertical fits source CFF1 horizontal stems and blue zones in device Y.
	OpenTypeTextHintVertical = psdscene.OpenTypeTextHintVertical
	// OpenTypeTextHintFull additionally fits source CFF1 vertical stems in device X.
	OpenTypeTextHintFull = psdscene.OpenTypeTextHintFull
	// OpenTypeTextHintFractional fits stem centers while preserving source fractional widths.
	OpenTypeTextHintFractional = psdscene.OpenTypeTextHintFractional
	// OpenTypeTextHintEdgeFractional fits the nearest stem edge while preserving source fractional widths.
	OpenTypeTextHintEdgeFractional = psdscene.OpenTypeTextHintEdgeFractional
)

// OpenTypeFauxBoldPolicy selects explicit approximate synthetic-weight layout.
type OpenTypeFauxBoldPolicy = psdscene.OpenTypeFauxBoldPolicy

const (
	// OpenTypeFauxBoldOutline retains the original outline-only model and source advances.
	OpenTypeFauxBoldOutline = psdscene.OpenTypeFauxBoldOutline
	// OpenTypeFauxBoldExpandedAdvance adds source-em synthetic advance to each shaped glyph, including spaces.
	// It selects anchored outline expansion and requires approximate single-style typography.
	OpenTypeFauxBoldExpandedAdvance = psdscene.OpenTypeFauxBoldExpandedAdvance
)

// OpenTypeStyleBoundaryPolicy explicitly selects approximate shaping across source style boundaries.
type OpenTypeStyleBoundaryPolicy = psdscene.OpenTypeStyleBoundaryPolicy

// OpenTypeClusterPaintPolicy selects explicit approximate paint ownership for a whole UTF16 glyph cluster.
type OpenTypeClusterPaintPolicy = psdscene.OpenTypeClusterPaintPolicy

const (
	// OpenTypeRejectMixedClusterPaint preserves the default rejection of mixed RGBA glyph clusters.
	OpenTypeRejectMixedClusterPaint = psdscene.OpenTypeRejectMixedClusterPaint
	// OpenTypeFirstStyleClusterPaint paints a cluster using its logical start style's entire RGBA.
	// It requires compatible typography and does not certify Photoshop antialiasing or general native coloring.
	OpenTypeFirstStyleClusterPaint = psdscene.OpenTypeFirstStyleClusterPaint
)

const (
	// OpenTypeSingleStyle preserves the default single-style source contract.
	OpenTypeSingleStyle = psdscene.OpenTypeSingleStyle
	// OpenTypeIsolatedStyleRuns independently shapes and paints each source style.
	OpenTypeIsolatedStyleRuns = psdscene.OpenTypeIsolatedStyleRuns
	// OpenTypeCompatibleStyleRuns permits matching typography to shape across color boundaries.
	OpenTypeCompatibleStyleRuns = psdscene.OpenTypeCompatibleStyleRuns
)

const (
	// OpenTypeTextNone disables optional OpenType kerning.
	OpenTypeTextNone = psdscene.OpenTypeTextNone
	// OpenTypeTextMetrics enables supported OpenType metric pair positioning.
	OpenTypeTextMetrics = psdscene.OpenTypeTextMetrics
)

// OpenTypeTextRenderOptions bounds source metadata, layout and approximate coverage.
// Kerning must explicitly select None or Metrics; Shape selects optional font features.
type OpenTypeTextRenderOptions = psdscene.OpenTypeTextRenderOptions

// RenderOpenTypeTextLayer returns owned approximate source pixels for supported RGB8 point text.
// It verifies the record against source metadata and uses explicit caller-selected fonts and features.
// The default supports single-line Latin at 72ppi, Sharp metadata and no text warp.
// ApproximateTypography also executes supported point paragraphs, tracking, leading, capitalization and FauxBold.
// FauxBold selects outline-only or explicit synthetic advance and anchored outline expansion.
// One style remains the default; explicit StyleBoundaries selects approximate multi-style typography.
// ClusterPaint defaults to rejecting mixed colors; explicit FirstStyle selects the logical start style's entire RGBA.
// Native style-boundary coloring and antialiasing remain unqualified beyond finite reference observations.
// Optional Hinting fits preserved CFF1 source hints without executing TrueType instructions.
// Layer masks, opacity, effects and blending apply separately; native pixel equality is not guaranteed.
func RenderOpenTypeTextLayer(
	ctx context.Context, source *SourceDocument, record TextLayerRecord,
	resolver FontResolver, options OpenTypeTextRenderOptions,
) (*image.NRGBA, error) {
	return psdscene.RenderOpenTypeTextLayer(ctx, source, record, resolver, options)
}
