package psdscene

import (
	"context"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func (engine *renderer) textRecords(
	ctx context.Context, source *psdfile.Document,
) (map[int]psdfile.TextLayerRecord, error) {
	if engine.options.ApproximateText == nil {
		return nil, nil
	}
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "text.metadata", 0, "no text metadata budget remains")
	}
	view, err := source.ReadTextLayerView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(view.MetadataBytes+int64(len(view.Records))*128, "text.metadata", 0); err != nil {
		return nil, err
	}
	result := make(map[int]psdfile.TextLayerRecord)
	for _, record := range view.Records {
		if record.LayerSetIndex != source.ActiveLayerSet {
			continue
		}
		if _, exists := result[record.LayerIndex]; exists {
			return nil, sceneError(psdfile.ErrUnsupported, "text.metadata", record.Block.Data.Offset,
				"duplicate text blocks have ambiguous live semantics")
		}
		result[record.LayerIndex] = record
	}
	return result, nil
}

func (engine *renderer) renderText(
	ctx context.Context, source *psdfile.Document, index int, layer *psdmodel.Layer,
	records map[int]psdfile.TextLayerRecord,
) (bool, error) {
	record, exists := records[index]
	if !exists {
		return false, nil
	}
	if layer.Placed != nil || layer.Children != nil || layer.FillColor != nil || layer.Gradient != nil ||
		layer.Adjustment != nil || layer.Pattern != nil {
		return false, sceneError(psdfile.ErrUnsupported, "text.layer", record.Block.Data.Offset,
			"text and another live source share the same physical layer")
	}
	settings := *engine.options.ApproximateText
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return false, sceneError(psdfile.ErrLimit, "text.render", record.Block.Data.Offset,
			"no glyph rendering budget remains")
	}
	if settings.MaxWorkingBytes == 0 || settings.MaxWorkingBytes > available {
		settings.MaxWorkingBytes = available
	}
	pixels, err := RenderTextLayer(ctx, source, record, engine.options.FontResolver, settings)
	if err != nil {
		return false, err
	}
	if err := engine.reserve(int64(len(pixels.Pix)), "text.pixels", record.Block.Data.Offset); err != nil {
		return false, err
	}
	layer.Pixels, layer.Bounds = pixels, pixels.Rect
	return true, nil
}
