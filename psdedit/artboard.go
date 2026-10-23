package psdedit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdartboard"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type compositeArtboard struct {
	data *psdfile.ArtboardData
	node *editNode
}

type compositeArtboards map[int]*compositeArtboard

// prepareCompositeArtboardsLocked binds retained board descriptors to stable source identities, not edited order.
// Its reservation covers the owned metadata, validation scratch and binding map until rendering finishes.
func (e *Editor) prepareCompositeArtboardsLocked(
	ctx context.Context, budget int64,
) (compositeArtboards, int64, error) {
	if !psdartboard.HasSource(e.source) {
		return nil, 0, nil
	}
	if budget <= 0 {
		return nil, 0, editError(psdfile.ErrLimit, "save.artboards", "no artboard metadata budget remains")
	}
	views, err := e.source.Artboards(ctx, psdfile.MetadataOptions{MaxBytes: budget})
	if err != nil {
		return nil, 0, err
	}
	cost := int64(0)
	for _, view := range views {
		if view.Data == nil || view.Data.MetadataBytes > budget-cost-256 {
			return nil, 0, editError(psdfile.ErrLimit, "save.artboards", "owned artboard metadata exceeds budget")
		}
		cost += view.Data.MetadataBytes + 256
	}
	if budget-cost < 256 || int64(len(views)) > (budget-cost-256)/256 {
		return nil, 0, editError(psdfile.ErrLimit, "save.artboards", "artboard bindings exceed budget")
	}
	cost += int64(len(views))*256 + 256
	records, err := psdartboard.ValidateMetadata(views, e.source)
	if err != nil {
		return nil, 0, err
	}
	boards := make(compositeArtboards, len(records))
	for _, record := range records {
		boards[record.LayerIndex] = &compositeArtboard{data: record.Data}
	}
	var visit func([]*editNode, bool) error
	visit = func(nodes []*editNode, topLevel bool) error {
		for _, node := range nodes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if board := boards[node.sourceIndex]; board != nil {
				if board.node != nil || !topLevel || !node.group ||
					node.bounds != e.source.Layers[node.sourceIndex].Bounds {
					return editError(ErrUnsupported, "save.artboards.structure",
						"original artboards must remain unique top-level groups with unchanged saved geometry")
				}
				board.node = node
			}
			if err := visit(node.children, false); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(e.roots, true); err != nil {
		return nil, 0, err
	}
	for _, board := range boards {
		if board.node == nil {
			return nil, 0, editError(ErrUnsupported, "save.artboards.structure",
				"removing an original artboard requires updating its document-level metadata")
		}
	}
	return boards, cost, nil
}

func (boards compositeArtboards) prepare(
	ctx context.Context, node *editNode, layer *psdmodel.Layer, canvas image.Rectangle, budget int64,
) (*psdmodel.Layer, int64, error) {
	board := boards[node.sourceIndex]
	if board == nil {
		return layer, 0, nil
	}
	if board.node != node {
		return nil, 0, editError(ErrUnsupported, "save.artboards.structure", "artboard source identity changed")
	}
	return psdartboard.Prepare(ctx, layer, board.data, canvas, budget)
}

// validateArtboardStructureLocked also protects explicit-preview writes from broken count and nesting metadata.
// It reads only indexed tags and the edited tree, preserving unknown unchanged artboard payloads verbatim.
func (e *Editor) validateArtboardStructureLocked(ctx context.Context) error {
	if !e.structureChanged || !psdartboard.HasSource(e.source) {
		return nil
	}
	for index, source := range e.source.Layers {
		board := false
		for _, block := range source.AdditionalInfo {
			board = board || block.Key == "artb" || block.Key == "abdd"
		}
		if !board {
			continue
		}
		found := false
		for _, node := range e.roots {
			if err := ctx.Err(); err != nil {
				return err
			}
			if node.sourceIndex == index && node.group && node.bounds == source.Bounds {
				if found {
					return editError(ErrUnsupported, "save.artboards.structure", "duplicate original artboard identity")
				}
				found = true
			}
		}
		if !found {
			return editError(ErrUnsupported, "save.artboards.structure",
				"removing, nesting or changing an original artboard's geometry requires metadata editing")
		}
	}
	return ctx.Err()
}
