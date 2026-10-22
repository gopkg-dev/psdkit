package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type patternSceneSources struct {
	records []psdfile.PatternRecord
	tiles   map[string]*image.NRGBA
	loaded  bool
}

func (engine *renderer) renderPattern(
	ctx context.Context, source *psdfile.Document, layer *psdmodel.Layer, content *patternSceneSources,
) error {
	if layer.Pattern == nil {
		return nil
	}
	if !content.loaded {
		available := engine.options.MaxWorkingBytes - engine.working
		if available <= 0 {
			return sceneError(psdfile.ErrLimit, "pattern.metadata", 0, "no pattern metadata budget remains")
		}
		view, err := source.ReadPatternView(ctx, psdfile.MetadataOptions{MaxBytes: available})
		if err != nil {
			return err
		}
		if err := engine.reserve(view.MetadataBytes+int64(len(view.Records))*128, "pattern.metadata", 0); err != nil {
			return err
		}
		content.records, content.loaded = view.Records, true
	}
	var selected *psdfile.PatternRecord
	for index := range content.records {
		record := &content.records[index]
		if record.ID != layer.Pattern.ID {
			continue
		}
		if selected != nil {
			return sceneError(psdfile.ErrUnsupported, "pattern.reference", record.Body.Offset,
				"duplicate local pattern reference is ambiguous")
		}
		selected = record
	}
	if selected == nil || len(layer.Pattern.Unsupported) != 0 {
		return sceneError(psdfile.ErrUnsupported, "pattern.reference", 0,
			"local pattern reference is absent or has unknown sampling semantics")
	}
	tile := content.tiles[layer.Pattern.ID]
	if tile == nil {
		estimate, err := source.EstimatePatternTileRGB8(*selected)
		if err != nil {
			return err
		}
		if err := engine.reserve(estimate.WorkingBytes, "pattern.tile", selected.Body.Offset); err != nil {
			return err
		}
		decoded, err := source.DecodePatternTileRGB8(ctx, *selected,
			psdfile.PatternDecodeOptions{MaxWorkingBytes: estimate.WorkingBytes})
		if err != nil {
			return err
		}
		if int64(len(decoded.Pix)) != estimate.RetainedBytes {
			return sceneError(psdfile.ErrMalformed, "pattern.tile", selected.Body.Offset,
				"decoded tile storage differs from its source estimate")
		}
		engine.working -= estimate.WorkingBytes - estimate.RetainedBytes
		tile = decoded
		content.tiles[layer.Pattern.ID] = tile
	}
	layer.PatternTile = tile
	return nil
}
