package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

const sourceArtboardIssue = "首版暂不支持画板文档"

func sourceHasArtboards(source *psdfile.Document) bool {
	for _, block := range source.AdditionalInfo {
		if block.Key == "artb" || block.Key == "artd" || block.Key == "abdd" {
			return true
		}
	}
	for _, record := range source.Layers {
		for _, block := range record.AdditionalInfo {
			if block.Key == "artb" || block.Key == "artd" || block.Key == "abdd" {
				return true
			}
		}
	}
	return false
}

// prepareSourceArtboards adds retained backgrounds and rectangular clipping to owned live source groups.
// The document canvas, ordinary sibling layers and absolute saved layer coordinates remain intact.
func (engine *rgbTileRenderer) prepareSourceArtboards(ctx context.Context, layers []*psdmodel.Layer) error {
	if !sourceHasArtboards(engine.source) {
		return nil
	}
	available := engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return sceneError(psdfile.ErrLimit, "tile.artboard.metadata", 0, "no artboard metadata budget remains")
	}
	views, err := engine.source.Artboards(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return err
	}
	for _, view := range views {
		if err := engine.reserve(view.Data.MetadataBytes+256,
			"tile.artboard.metadata", view.Data.Block.Data.Offset); err != nil {
			return err
		}
	}
	boards, err := artboardRenderMetadata(views, engine.source)
	if err != nil {
		return err
	}
	if err := engine.reserve(int64(len(layers))*64, "tile.artboard.tree", 0); err != nil {
		return err
	}
	topLevel := make(map[int]*psdmodel.Layer, len(layers))
	for _, layer := range layers {
		topLevel[engine.nodes[layer]] = layer
	}
	for _, board := range boards {
		if err := ctx.Err(); err != nil {
			return err
		}
		layer := topLevel[board.LayerIndex]
		if layer == nil {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.tree", board.Data.Block.Span.Offset,
				"live artboards must identify top-level groups")
		}
		owned := *layer
		owned.Unsupported = make([]string, 0, len(layer.Unsupported))
		for _, issue := range layer.Unsupported {
			if issue != sourceArtboardIssue {
				owned.Unsupported = append(owned.Unsupported, issue)
			}
		}
		if err := artboardRenderLayer(&owned, board.Data.Block.Span.Offset); err != nil {
			return err
		}
		if !layer.Hidden && (layer.Knockout != psdmodel.KnockoutNone || len(layer.ChannelRestrictions) != 0 ||
			layer.VectorDensity != nil || layer.VectorFeather != nil) {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.layer", board.Data.Block.Span.Offset,
				"artboard channel restrictions, knockout or vector mask controls are not supported")
		}
		layer.Unsupported = owned.Unsupported
		if layer.Hidden {
			continue
		}
		if err := engine.addSourceArtboard(ctx, layer, board.Data); err != nil {
			return err
		}
	}
	return nil
}

func (engine *rgbTileRenderer) addSourceArtboard(
	ctx context.Context, layer *psdmodel.Layer, board *psdfile.ArtboardData,
) error {
	bounds := image.Rect(int(board.Rect.Left), int(board.Rect.Top), int(board.Rect.Right), int(board.Rect.Bottom))
	samples := bounds.Intersect(engine.window)
	bytes := int64(samples.Dx()) * int64(samples.Dy()) * 4
	if err := engine.reserve(bytes+512, "tile.artboard.clip", board.Block.Data.Offset); err != nil {
		return err
	}
	coverage := image.NewNRGBA(samples)
	for y := samples.Min.Y; y < samples.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := samples.Min.X; x < samples.Max.X; x++ {
			offset := coverage.PixOffset(x, y)
			coverage.Pix[offset] = 255
		}
	}
	defaultColor := uint8(0)
	layer.Mask = &psdmodel.Mask{Bounds: bounds, SampleBounds: &samples,
		Pixels: coverage, DefaultColor: &defaultColor}
	if *board.BackgroundType == 3 {
		return nil
	}
	if err := engine.reserve(512+int64(len(layer.Children)+1)*8,
		"tile.artboard.background", board.Block.Data.Offset); err != nil {
		return err
	}
	rgb := artboardBackgroundColor(board)
	fill := [3]float64{float64(rgb[0]) / 255, float64(rgb[1]) / 255, float64(rgb[2]) / 255}
	background := &psdmodel.Layer{Name: "Artboard background", Bounds: bounds,
		BlendMode: "normal", FillColor: &fill}
	children := make([]*psdmodel.Layer, len(layer.Children)+1)
	children[0] = background
	copy(children[1:], layer.Children)
	layer.Children = children
	engine.nodes[background] = -1
	engine.live[background] = true
	return nil
}
