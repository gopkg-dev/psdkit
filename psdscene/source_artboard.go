package psdscene

import (
	"context"

	"github.com/gopkg-dev/psdkit/internal/psdartboard"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func sourceHasArtboards(source *psdfile.Document) bool {
	return psdartboard.HasSource(source)
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
		if view.Data == nil {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.metadata", 0,
				"unrecognized or ambiguous artboard metadata")
		}
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
		if err := engine.addSourceArtboard(ctx, layer, board.Data); err != nil {
			return err
		}
	}
	return nil
}

func (engine *rgbTileRenderer) addSourceArtboard(
	ctx context.Context, layer *psdmodel.Layer, board *psdfile.ArtboardData,
) error {
	owned, cost, err := psdartboard.Prepare(ctx, layer, board, engine.window,
		engine.settings.MaxWorkingBytes-engine.working)
	if err != nil {
		return err
	}
	if err := engine.reserve(cost, "tile.artboard.clip", board.Block.Data.Offset); err != nil {
		return err
	}
	*layer = *owned
	if layer.Hidden {
		return nil
	}
	// This owned rectangle replaces an inactive user mask, so later loading must not overwrite it with source samples.
	engine.maskIDs[layer] = 0
	if *board.BackgroundType == 3 {
		return nil
	}
	background := layer.Children[0]
	engine.nodes[background] = -1
	engine.live[background] = true
	return nil
}
