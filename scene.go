package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdscene"
)

// SceneOptions bounds live RGB8 rendering and explicit external content resolution.
type SceneOptions = psdscene.Options

// TileOptions bounds per-tile semantic snapshots, output, halo samples and decoder work.
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
type ArtboardRenderOptions = psdscene.ArtboardRenderOptions

// ArtboardImage contains one board's owned pixels and retained saved coordinates.
type ArtboardImage = psdscene.ArtboardImage

// RenderArtboard rebuilds one selected board from its live layers and saved background.
func RenderArtboard(ctx context.Context, source *SourceDocument,
	options ...ArtboardRenderOptions,
) (*image.NRGBA, error) {
	return psdscene.RenderArtboard(ctx, source, options...)
}

// RenderArtboards rebuilds every top-level RGB8 PSD/PSB artboard independently.
func RenderArtboards(ctx context.Context, source *SourceDocument,
	options ...ArtboardRenderOptions,
) ([]ArtboardImage, error) {
	return psdscene.RenderArtboards(ctx, source, options...)
}

// RenderSource rebuilds source layers and embedded PSD/PSB/PNG/JPEG smart-object content.
// It returns owned encoded-RGB pixels in the document or explicitly requested output profile.
func RenderSource(ctx context.Context, source *SourceDocument, options ...SceneOptions) (*image.NRGBA, error) {
	return psdscene.Render(ctx, source, options...)
}

// RenderSourcePlacedLayer rebuilds one original smart-object layer from its embedded source.
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
