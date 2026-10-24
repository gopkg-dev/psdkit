package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdscene"
)

// SceneOptions bounds live RGB8 rendering and explicit external content resolution.
// Certified horizontal Arc Lower presets require explicit ApproximateCustomWarp, including a zero bend.
type SceneOptions = psdscene.Options

// LinkedRasterResolver supplies explicitly selected external PNG/JPEG/TIFF source bytes for live rendering.
// Returned bytes remain caller-owned and immutable until the rendering call returns.
type LinkedRasterResolver = psdscene.LinkedRasterResolver

// TileOptions bounds per-tile semantic snapshots, output, halo samples and decoder work.
// Certified preset sampling requires explicit ApproximateCustomWarp and the current call's external source mappings.
type TileOptions = psdscene.TileOptions

// RenderSourceTile rebuilds an RGB8 window without allocating the complete source canvas.
// The returned owned image retains absolute document coordinates.
func RenderSourceTile(ctx context.Context, source *SourceDocument, bounds image.Rectangle,
	options ...TileOptions,
) (*image.NRGBA, error) {
	return psdscene.RenderTile(ctx, source, bounds, options...)
}

// VisitSourceTiles rebuilds a source window one owned tile at a time in row-major order.
// Pixels retained by the callback are caller storage and are outside the per-tile budget.
func VisitSourceTiles(ctx context.Context, source *SourceDocument, bounds image.Rectangle, size image.Point,
	visit func(*image.NRGBA) error, options ...TileOptions,
) error {
	return psdscene.VisitTiles(ctx, source, bounds, size, visit, options...)
}

// ArtboardRenderOptions bounds RGB8 artboard renders and optionally selects one physical board.
// Render supplies live placed-source resolvers and explicit source approximation policies.
type ArtboardRenderOptions = psdscene.ArtboardRenderOptions

// ArtboardImage contains one board's owned pixels and retained saved coordinates.
type ArtboardImage = psdscene.ArtboardImage

// RenderArtboard rebuilds one selected board from its live layers and saved background.
// Visible smart objects use actual contents; external sources and approximations require explicit Render policies.
func RenderArtboard(ctx context.Context, source *SourceDocument,
	options ...ArtboardRenderOptions,
) (*image.NRGBA, error) {
	return psdscene.RenderArtboard(ctx, source, options...)
}

// RenderArtboards rebuilds every top-level RGB8 PSD/PSB artboard independently.
// Shared live contents are resolved once per call while each board retains its own background and crop.
func RenderArtboards(ctx context.Context, source *SourceDocument,
	options ...ArtboardRenderOptions,
) ([]ArtboardImage, error) {
	return psdscene.RenderArtboards(ctx, source, options...)
}

// RenderSource rebuilds source layers and embedded or explicitly resolved PSD/PSB/PNG/JPEG/TIFF contents.
// It returns owned encoded-RGB pixels in the document or explicitly requested output profile.
func RenderSource(ctx context.Context, source *SourceDocument, options ...SceneOptions) (*image.NRGBA, error) {
	return psdscene.Render(ctx, source, options...)
}

// RenderSourcePlacedLayer rebuilds one original smart-object layer from embedded or explicitly resolved contents.
// The owned result retains placement, masks and effects for subsequent composition.
func RenderSourcePlacedLayer(ctx context.Context, source *SourceDocument, layerIndex int,
	options ...SceneOptions,
) (*Layer, error) {
	return psdscene.RenderPlacedLayer(ctx, source, layerIndex, options...)
}

// ReadSourceRGBProfile returns owned saved ICC bytes without guessing an untagged working space.
func ReadSourceRGBProfile(ctx context.Context, source *SourceDocument, maxBytes int64) ([]byte, error) {
	return psdscene.ReadSourceRGBProfile(ctx, source, maxBytes)
}
