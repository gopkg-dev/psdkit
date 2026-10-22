package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

// RenderPrecisionTile rebuilds an RGB/Gray16 or RGB/Gray32 source-layer window at its original color precision.
// HDR colors and ICC provenance remain unchanged; Bounds retains absolute document coordinates.
// Layer and mask output samples are decoded only at intersections.
// Flat Normal and limited Normal/Difference RGB16 eligibility use whole-source metadata before ROI clipping.
// Raw/RLE channel payload reads remain regional; ZIP retains its complete compressed-stream validation.
// Other RGB16 stacks use bounded alpha qualification.
// The complete canvas never contributes to output allocation budgets.
func RenderPrecisionTile(
	ctx context.Context, source *psdfile.Document, rectangle image.Rectangle, options ...TileOptions,
) (*psdprecision.RegionImage, error) {
	if len(options) == 1 && options[0].ApproximateOpenTypeText != nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		return nil, sceneError(psdfile.ErrUnsupported, "tile.text.precision", 0,
			"explicit OpenType source text is available only in RGB8 tile composition")
	}
	ctx, settings, err := tileConfiguration(ctx, source, rectangle, options, true)
	if err != nil {
		return nil, err
	}
	if settings.OutputProfile != nil {
		return nil, sceneError(psdfile.ErrUnsupported, "tile.color", 0,
			"high-precision tile output requires a separate display or profile conversion")
	}
	if source.Header.Depth != 16 && source.Header.Depth != 32 ||
		source.Header.ColorMode != 1 && source.Header.ColorMode != 3 {
		return nil, sceneError(psdfile.ErrUnsupported, "tile.header", 0, "high-precision RGB or grayscale required")
	}
	depth := source.Header.Depth
	if source.ActiveLayerSet >= 0 && source.ActiveLayerSet < len(source.LayerSets) {
		depth = source.LayerSets[source.ActiveLayerSet].Depth
	}
	if depth != source.Header.Depth {
		return nil, sceneError(psdfile.ErrUnsupported, "tile.layerSets", 0, "active layer depth differs from header")
	}
	engine := precisionSourceRenderer{source: source, depth: depth, canvas: rectangle,
		nativeRGB16: depth == 16 && source.Header.ColorMode == 3,
		nodes:       make(map[*psdprecision.Layer]int), options: PrecisionOptions{
			MaxWorkingBytes: settings.MaxWorkingBytes, MaxPixels: settings.MaxPixels,
			MaxDepth: settings.MaxDepth, MaxLayers: settings.MaxLayers,
			NonFinite: settings.NonFinite, Region: settings.Region,
		}}
	if depth == 32 {
		engine.encoding = psdprecision.LinearRGB
	}
	metadata := int64(len(source.Layers))*512 + 512
	if err := engine.reserve(metadata, "tile.metadata", 0); err != nil {
		return nil, err
	}
	layers, err := engine.buildTree(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := engine.prepare(ctx, layers, 0, true)
	if err != nil {
		return nil, err
	}
	if err := engine.qualifyRGB16Normal(ctx, layers); err != nil {
		return nil, err
	}
	engine.planRGB16Qualification()
	area := int64(rectangle.Dx()) * int64(rectangle.Dy())
	if area > (settings.MaxWorkingBytes-engine.working)/32/int64(groups+1) {
		return nil, sceneError(psdfile.ErrLimit, "tile.composite", 0, "tile output and group buffers exceed budget")
	}
	renderBytes := area*32*int64(groups+1) + metadata
	if err := engine.reserve(renderBytes, "tile.composite", 0); err != nil {
		return nil, err
	}
	profile, err := engine.profile(ctx)
	if err != nil {
		return nil, err
	}
	if err := engine.preflightSamples(layers, true); err != nil {
		return nil, err
	}
	if err := engine.qualifyRGB16(ctx, layers); err != nil {
		return nil, err
	}
	if err := engine.load(ctx, layers, true); err != nil {
		return nil, err
	}
	scene := &psdprecision.Scene{Width: int(source.Header.Width), Height: int(source.Header.Height),
		Encoding: engine.encoding, Layers: layers}
	result, err := engine.composeRegion(ctx, scene, rectangle, psdprecision.Options{
		MaxWorkingBytes: renderBytes, MaxPixels: settings.MaxPixels, MaxDepth: settings.MaxDepth,
		MaxLayers: settings.MaxLayers, NonFinite: settings.NonFinite,
	})
	if err != nil {
		return nil, precisionSourceError(err)
	}
	result.Image.SourceDepth, result.Image.ICCProfile = depth, profile
	return result, nil
}
