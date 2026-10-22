package psdkit

import (
	"context"

	"github.com/gopkg-dev/psdkit/psdfont"
)

// OpenTypeFeature explicitly enables or disables a selected optional font feature.
// Required LangSys features execute independently of these optional settings.
type OpenTypeFeature = psdfont.ShapeFeature

// OpenTypeShapeOptions selects Latin OpenType features and bounds shaping work.
// Optional features are disabled by default; Script and Language select one LangSys.
type OpenTypeShapeOptions = psdfont.ShapeOptions

// ShapedGlyph retains a source UTF-16 span and advances and offsets in font design units.
type ShapedGlyph = psdfont.ShapedGlyph

// ShapedRun owns shaped glyph IDs and positions without retaining the source font.
type ShapedRun = psdfont.ShapedRun

// ShapeOpenType executes selected Latin single/ligature substitutions and pair positioning.
// It uses left-to-right horizontal layout; vertical advance adjustments are not applied.
// It does not execute Apple AAT, hinting, complex scripts, or implicit legacy kerning.
// The returned design-unit positions are independent of Photoshop layout and rasterization.
func ShapeOpenType(
	ctx context.Context, face *FontFace, text string, options ...OpenTypeShapeOptions,
) (*ShapedRun, error) {
	return psdfont.Shape(ctx, face, text, options...)
}
