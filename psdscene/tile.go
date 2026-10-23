package psdscene

import (
	"context"
	"errors"
	"image"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

// TileOptions bounds owned regional samples, metadata, output and decoder scratch.
// Zero budgets inherit source decode limits; MaxDepth defaults to 64 and MaxLayers to 10000.
// MaxPixels bounds each requested tile; the complete parent canvas does not determine that allocation.
// Explicit OpenType recursion also bounds each required complete child prototype frame with this pixel limit.
// Region bounds compressed-stream validation independently of output storage.
type TileOptions struct {
	MaxWorkingBytes int64
	MaxPixels       int64
	MaxDepth        int
	MaxLayers       int
	Region          psdfile.RegionOptions
	Compose         psdcompose.Options
	// ApproximatePlacedProjection selects explicitly approximate sampling of transparent-border projective sources.
	// Default projection and original placement metadata remain unchanged.
	ApproximatePlacedProjection bool
	NonFinite                   psdprecision.NonFinitePolicy
	// CachedSpecialLayers explicitly selects saved text, stroke, gradient and pattern layer rasters.
	CachedSpecialLayers bool
	// CachedTextLayers explicitly selects saved text rasters while keeping other artwork sources live.
	CachedTextLayers bool
	// ApproximateVectorStroke explicitly selects solid inside stroke rasterization from original vector paths.
	ApproximateVectorStroke bool
	// ApproximateCustomWarp explicitly selects the mathematical cubic-plus-homography custom warp model.
	ApproximateCustomWarp bool
	// CachedPlacedLayers explicitly selects saved placed-layer rasters instead of resolving live source contents.
	// Live failures never fall back to this policy automatically.
	CachedPlacedLayers bool
	// ExternalResolver supplies explicitly selected external PSD/PSB contents.
	ExternalResolver psdfile.LinkedDocumentResolver
	// ExternalRasterResolver supplies explicit external PNG/JPEG/TIFF bytes, without opening saved paths.
	ExternalRasterResolver LinkedRasterResolver
	// MaxDocuments bounds decoded placed prototypes and defaults to 64.
	MaxDocuments int
	// AssumeUntaggedSRGB matches Scene's explicit policy for required tagged/untagged content conversion.
	AssumeUntaggedSRGB bool
	// CenteredJPEGChroma explicitly selects centered embedded-JPEG chroma reconstruction.
	CenteredJPEGChroma bool
	// SmartFilters explicitly selects live algorithms and approximation policy.
	SmartFilters *psdfilter.Options
	// OutputProfile requests encoded RGB8 output in this owned profile, preserving nil as document encoding.
	OutputProfile []byte
	ownedProfile  bool
	// ApproximateOpenTypeText selects explicit Latin OpenType source glyphs instead of saved text rasters.
	// Bounds limits only glyph artwork; output remains the requested absolute tile rectangle.
	// Original placed PSD/PSB contents inherit features/resolver/limits; Bounds applies only to this document.
	// This policy keeps live placed contents ahead of CachedPlacedLayers and never falls back after live errors.
	ApproximateOpenTypeText *OpenTypeTextRenderOptions
	FontResolver            psdfont.FontResolver
	ownedOpenTypeText       bool
	openTypeTraversal       *openTypeTraversal
	openTypeDepth           int
	sourceRenderer          *renderer
	sourceDepth             int
}

func tileConfiguration(
	ctx context.Context, source *psdfile.Document, rectangle image.Rectangle, options []TileOptions, checkPixels bool,
) (context.Context, TileOptions, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ctx, TileOptions{}, err
	}
	if source == nil || len(options) > 1 {
		return ctx, TileOptions{}, sceneError(psdfile.ErrMalformed, "tile.options", 0,
			"provide a source and at most one TileOptions")
	}
	liveOpenType := len(options) == 1 && options[0].ApproximateOpenTypeText != nil
	if !liveOpenType {
		if _, err := source.ReadAt(nil, 0); err != nil {
			return ctx, TileOptions{}, err
		}
	}
	settings := TileOptions{MaxWorkingBytes: source.Limits().MaxDecodeBytes,
		MaxPixels: source.Limits().MaxDecodePixels, MaxDepth: 64, MaxLayers: 10_000, MaxDocuments: 64}
	if len(options) != 0 {
		provided := options[0]
		settings.openTypeTraversal, settings.openTypeDepth = provided.openTypeTraversal, provided.openTypeDepth
		settings.sourceRenderer, settings.sourceDepth = provided.sourceRenderer, provided.sourceDepth
		if provided.MaxWorkingBytes < 0 || provided.MaxPixels < 0 || provided.MaxDepth < 0 || provided.MaxLayers < 0 ||
			provided.NonFinite > psdprecision.NonFiniteClamp || provided.Region.MaxWorkingBytes < 0 ||
			provided.Region.MaxInflatedBytes < 0 || provided.Region.MaxRLEWorkBytes < 0 || provided.MaxDocuments < 0 {
			return ctx, TileOptions{}, sceneError(psdfile.ErrMalformed, "tile.options", 0, "invalid tile budgets or policy")
		}
		if provided.MaxWorkingBytes != 0 {
			settings.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxPixels != 0 {
			settings.MaxPixels = provided.MaxPixels
		}
		if provided.MaxDepth != 0 {
			settings.MaxDepth = provided.MaxDepth
		}
		if provided.MaxLayers != 0 {
			settings.MaxLayers = provided.MaxLayers
		}
		settings.Region, settings.Compose, settings.NonFinite = provided.Region, provided.Compose, provided.NonFinite
		settings.ApproximatePlacedProjection = provided.ApproximatePlacedProjection
		settings.CachedSpecialLayers, settings.CachedPlacedLayers = provided.CachedSpecialLayers, provided.CachedPlacedLayers
		settings.CachedTextLayers = provided.CachedTextLayers
		settings.ApproximateVectorStroke = provided.ApproximateVectorStroke
		settings.ApproximateCustomWarp = provided.ApproximateCustomWarp
		settings.ExternalResolver, settings.AssumeUntaggedSRGB = provided.ExternalResolver, provided.AssumeUntaggedSRGB
		settings.ExternalRasterResolver = provided.ExternalRasterResolver
		settings.CenteredJPEGChroma = provided.CenteredJPEGChroma
		if provided.MaxDocuments != 0 {
			settings.MaxDocuments = provided.MaxDocuments
		}
		if provided.SmartFilters != nil {
			owned := *provided.SmartFilters
			settings.SmartFilters = &owned
		}
		if provided.OutputProfile != nil {
			if int64(len(provided.OutputProfile)) > 4<<20 ||
				int64(len(provided.OutputProfile)) >= settings.MaxWorkingBytes {
				return ctx, TileOptions{}, sceneError(psdfile.ErrLimit, "tile.color", 0, "owned output profile exceeds budget")
			}
			if provided.ownedProfile {
				settings.OutputProfile = provided.OutputProfile
			} else {
				settings.OutputProfile = append([]byte{}, provided.OutputProfile...)
			}
			settings.ownedProfile = true
		}
		if provided.ApproximateOpenTypeText != nil {
			settings.FontResolver = provided.FontResolver
			if provided.ownedOpenTypeText {
				settings.ApproximateOpenTypeText = provided.ApproximateOpenTypeText
			} else {
				owned, _, err := ownOpenTypeTextOptions(provided.ApproximateOpenTypeText,
					settings.MaxWorkingBytes-int64(cap(settings.OutputProfile)))
				if err != nil {
					return ctx, TileOptions{}, err
				}
				settings.ApproximateOpenTypeText = owned
			}
			settings.ownedOpenTypeText = true
		}
	}
	if liveOpenType {
		canvas := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
		if source.Header.Depth != 8 || source.Header.ColorMode != 3 {
			return ctx, TileOptions{}, sceneError(psdfile.ErrUnsupported, "tile.text.header", 0,
				"explicit OpenType source text requires RGB8")
		}
		if bounds := settings.ApproximateOpenTypeText.Bounds; bounds != nil &&
			(bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y || !bounds.In(canvas)) {
			return ctx, TileOptions{}, sceneError(psdfile.ErrMalformed, "tile.text.bounds", 0,
				"glyph source bounds must lie in the complete document canvas")
		}
		if _, err := source.ReadAt(nil, 0); err != nil {
			return ctx, TileOptions{}, err
		}
	}
	if settings.MaxDepth > 64 || len(source.Layers) > settings.MaxLayers ||
		settings.Region.MaxWorkingBytes > source.Limits().MaxDecodeBytes {
		return ctx, TileOptions{}, sceneError(psdfile.ErrLimit, "tile.options", 0, "tile traversal or decoder limit exceeded")
	}
	if rectangle.Min.X < 0 || rectangle.Min.Y < 0 || rectangle.Max.X < rectangle.Min.X ||
		rectangle.Max.Y < rectangle.Min.Y || rectangle.Max.X > int(source.Header.Width) ||
		rectangle.Max.Y > int(source.Header.Height) {
		return ctx, TileOptions{}, sceneError(psdfile.ErrMalformed, "tile.bounds", 0,
			"tile must lie inside the absolute document canvas")
	}
	if checkPixels && rectangle.Dy() != 0 && (int64(rectangle.Dx()) > settings.MaxPixels/int64(rectangle.Dy()) ||
		int64(rectangle.Dx()) > int64(^uint(0)>>1)/32/int64(rectangle.Dy())) {
		return ctx, TileOptions{}, sceneError(psdfile.ErrLimit, "tile.bounds", 0, "tile exceeds pixel or platform limits")
	}
	if len(source.Layers) == 0 {
		return ctx, TileOptions{}, sceneError(psdfile.ErrUnsupported, "tile.layers", 0,
			"source-layer tile composition requires a layer tree")
	}
	return ctx, settings, nil
}

// VisitTiles rebuilds an RGB8 source window one tile at a time in row-major order.
// Each callback receives an owned image in absolute document coordinates; returning an error stops immediately.
// Storage retained by callbacks belongs to the caller and is outside the per-tile working budget.
func VisitTiles(
	ctx context.Context, source *psdfile.Document, rectangle image.Rectangle, tileSize image.Point,
	visit func(*image.NRGBA) error, options ...TileOptions,
) error {
	ctx, settings, err := tileConfiguration(ctx, source, rectangle, options, false)
	if err != nil {
		return err
	}
	if tileSize.X <= 0 || tileSize.Y <= 0 || visit == nil {
		return sceneError(psdfile.ErrMalformed, "tile.visitor", 0, "provide positive tile dimensions and a callback")
	}
	// The outer window is a traversal range, so only individual tiles consume MaxPixels.
	for y := rectangle.Min.Y; y < rectangle.Max.Y; {
		bottom := y + min(tileSize.Y, rectangle.Max.Y-y)
		for x := rectangle.Min.X; x < rectangle.Max.X; {
			right := x + min(tileSize.X, rectangle.Max.X-x)
			pixels, err := RenderTile(ctx, source, image.Rect(x, y, right, bottom), settings)
			if err != nil {
				return err
			}
			if err := visit(pixels); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			x = right
		}
		y = bottom
	}
	return ctx.Err()
}

// VisitPrecisionTiles rebuilds RGB/Gray16 or RGB/Gray32 windows into owned source-domain tiles.
// It never decodes a merged preview and does not allocate the complete requested traversal window.
func VisitPrecisionTiles(
	ctx context.Context, source *psdfile.Document, rectangle image.Rectangle, tileSize image.Point,
	visit func(*psdprecision.RegionImage) error, options ...TileOptions,
) error {
	if len(options) == 1 && options[0].ApproximateOpenTypeText != nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		return sceneError(psdfile.ErrUnsupported, "tile.text.precision", 0,
			"explicit OpenType source text is available only in RGB8 tile composition")
	}
	ctx, settings, err := tileConfiguration(ctx, source, rectangle, options, false)
	if err != nil {
		return err
	}
	if tileSize.X <= 0 || tileSize.Y <= 0 || visit == nil {
		return sceneError(psdfile.ErrMalformed, "tile.visitor", 0, "provide positive tile dimensions and a callback")
	}
	for y := rectangle.Min.Y; y < rectangle.Max.Y; {
		bottom := y + min(tileSize.Y, rectangle.Max.Y-y)
		for x := rectangle.Min.X; x < rectangle.Max.X; {
			right := x + min(tileSize.X, rectangle.Max.X-x)
			pixels, err := RenderPrecisionTile(ctx, source, image.Rect(x, y, right, bottom), settings)
			if err != nil {
				return err
			}
			if err := visit(pixels); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			x = right
		}
		y = bottom
	}
	return ctx.Err()
}

func tileComposeError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := psdfile.ErrMalformed
	if errors.Is(err, psdcompose.ErrUnsupported) {
		kind = psdfile.ErrUnsupported
	} else if errors.Is(err, psdcompose.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "tile", Path: "tile.render", Detail: err.Error(), Cause: err}
}
