package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// FloatImage owns high-precision straight RGBA samples without display quantization.
type FloatImage = psdprecision.FloatImage

// FloatRegion preserves an absolute source rectangle and owned high-precision output.
type FloatRegion = psdprecision.RegionImage

// RenderSourcePrecisionTile reconstructs RGB/Gray16 or RGB/Gray32 in an absolute source window.
func RenderSourcePrecisionTile(ctx context.Context, source *SourceDocument, bounds image.Rectangle,
	options ...TileOptions,
) (*FloatRegion, error) {
	return psdscene.RenderPrecisionTile(ctx, source, bounds, options...)
}

// VisitSourcePrecisionTiles traverses a source window without retaining the complete output canvas.
func VisitSourcePrecisionTiles(ctx context.Context, source *SourceDocument, bounds image.Rectangle, size image.Point,
	visit func(*FloatRegion) error, options ...TileOptions,
) error {
	return psdscene.VisitPrecisionTiles(ctx, source, bounds, size, visit, options...)
}

// PrecisionOptions bounds ordinary source-precision layer composition and explicit non-finite policies.
type PrecisionOptions = psdscene.PrecisionOptions

// RenderSourcePrecision rebuilds supported ordinary RGB/Gray16 or RGB/Gray32 layer trees.
// It preserves finite HDR values, source encoding and ICC bytes without applying a display transform.
func RenderSourcePrecision(ctx context.Context, source *SourceDocument,
	options ...PrecisionOptions,
) (*FloatImage, error) {
	return psdscene.RenderPrecision(ctx, source, options...)
}
