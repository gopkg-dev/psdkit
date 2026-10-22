package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfont"
)

// FontStyle supplies an exact font name and pixel-size character style.
type FontStyle = psdfont.Style

// FontLayoutOptions bounds owned outlines and positions and selects a pixel baseline.
type FontLayoutOptions = psdfont.LayoutOptions

// FontRun owns approximate unhinted glyph outlines and pixel positions.
type FontRun = psdfont.Run

// FontRasterOptions bounds unhinted coverage and optionally selects an approximate FauxBold outline union.
type FontRasterOptions = psdfont.RasterOptions

// LayoutOpenType places a single-line Latin run using explicitly selected OpenType features.
// Tracking must be zero and FontStyle.Kerning false; OpenTypeShapeOptions alone controls kerning.
// It does not reproduce hinting or Photoshop antialiasing and leaves scene text rendering unchanged.
func LayoutOpenType(
	ctx context.Context, text string, style FontStyle, resolver FontResolver,
	shapeOptions OpenTypeShapeOptions, options ...FontLayoutOptions,
) (*FontRun, error) {
	return psdfont.LayoutOpenType(ctx, text, style, resolver, shapeOptions, options...)
}

// RasterizeFontRun returns owned grayscale coverage in the supplied absolute pixel rectangle.
// It applies the existing unhinted approximation without text color, warps or Photoshop antialias modes.
func RasterizeFontRun(
	ctx context.Context, run *FontRun, bounds image.Rectangle, options ...FontRasterOptions,
) (*image.Alpha, error) {
	return psdfont.RasterizeRun(ctx, run, bounds, options...)
}
