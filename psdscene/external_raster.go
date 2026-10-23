package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// LinkedRasterResolver supplies encoded PNG/JPEG/TIFF contents for an explicitly selected external source.
// The supplied slice is borrowed and must remain immutable until the requesting render call returns.
// Its length is charged while decoding; returned pixels and ICC data do not alias the encoded input.
// The library never opens a saved external path or uses an external record's cached contents automatically.
type LinkedRasterResolver func(context.Context, psdfile.LinkedFileRecord) ([]byte, error)

func (engine *renderer) resolvesExternalRaster(record psdfile.LinkedFileRecord) bool {
	return record.Kind == "liFE" && engine.options.ExternalRasterResolver != nil &&
		externalRasterFormat(record.FileType) != ""
}

func externalRasterFormat(fileType string) string {
	switch fileType {
	case "PNGf":
		return "png"
	case "JPEG":
		return "jpeg"
	case "TIFF":
		return "tiff"
	}
	return ""
}

func (engine *renderer) hasExternalRaster(ctx context.Context, source *psdfile.Document) (bool, error) {
	if engine.options.ExternalRasterResolver == nil || len(source.Layers) == 0 {
		return false, nil
	}
	hasLinks := func(blocks []psdfile.AdditionalBlock) bool {
		for _, block := range blocks {
			switch block.Key {
			case "lnkD", "lnk2", "lnk3", "lnkE":
				return true
			}
		}
		return false
	}
	found := hasLinks(source.AdditionalInfo)
	for _, set := range source.LayerSets {
		for _, layer := range set.Layers {
			found = found || hasLinks(layer.AdditionalInfo)
		}
	}
	if !found {
		return false, nil
	}
	visiblePlacement := false
	for _, layer := range source.Layers {
		if layer.Flags&2 != 0 || layer.Opacity == 0 {
			continue
		}
		for _, block := range layer.AdditionalInfo {
			visiblePlacement = visiblePlacement || block.Key == "SoLd" || block.Key == "SoLE"
		}
	}
	if !visiblePlacement {
		return false, nil
	}
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return false, sceneError(psdfile.ErrLimit, "smartObject.externalRaster.metadata", 0,
			"no linked metadata budget remains")
	}
	view, err := source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return false, err
	}
	count := 0
	for _, record := range view.Records {
		if engine.resolvesExternalRaster(record) {
			count++
		}
	}
	if count == 0 {
		return false, nil
	}
	available -= view.MetadataBytes
	if available <= 64 || int64(count) > (available-64)/128 {
		return false, sceneError(psdfile.ErrLimit, "smartObject.externalRaster.metadata", 0,
			"external source identities exceed the remaining metadata budget")
	}
	available -= int64(count)*128 + 64
	if available <= 0 {
		return false, sceneError(psdfile.ErrLimit, "smartObject.externalRaster.metadata", 0,
			"no placement metadata budget remains")
	}
	identities := make(map[string]struct{}, count)
	for _, record := range view.Records {
		if engine.resolvesExternalRaster(record) {
			identities[record.ID] = struct{}{}
		}
	}
	placements, err := source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return false, err
	}
	for _, placement := range placements.Records {
		if placement.LayerSetIndex != source.ActiveLayerSet || placement.SourceType != 2 ||
			placement.LayerIndex < 0 || placement.LayerIndex >= len(source.Layers) {
			continue
		}
		layer := source.Layers[placement.LayerIndex]
		if _, exists := identities[placement.SourceID]; exists && layer.Flags&2 == 0 && layer.Opacity != 0 {
			return true, nil
		}
	}
	return false, nil
}

func sameLinkedContentRecord(first, second psdfile.LinkedFileRecord) bool {
	if first.Kind != second.Kind || first.Data != second.Data {
		return false
	}
	// External records can have no Data span; distinct metadata must never collapse to a shared ID.
	return first.Kind != "liFE" || first.Body == second.Body && first.Block.Span == second.Block.Span
}

func (engine *renderer) renderExternalRaster(
	ctx context.Context, source *psdfile.Document, record psdfile.LinkedFileRecord,
	destination *sceneProfile, depth int,
) (*image.NRGBA, error) {
	return engine.renderExternalRasterLimited(ctx, source, record, destination, depth, source.Limits().MaxDecodePixels)
}

func (engine *renderer) renderExternalRasterLimited(
	ctx context.Context, source *psdfile.Document, record psdfile.LinkedFileRecord,
	destination *sceneProfile, depth int, maxPixels int64,
) (*image.NRGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !engine.resolvesExternalRaster(record) {
		return nil, sceneError(psdfile.ErrUnsupported, "smartObject.externalRaster", record.Span.Offset,
			"external PNG/JPEG/TIFF contents require an explicit raster resolver")
	}
	if depth > engine.options.MaxDepth || engine.documents >= engine.options.MaxDocuments || maxPixels <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.externalRaster", record.Span.Offset,
			"recursive content limits exceeded")
	}
	limits := source.Limits()
	available := min(engine.options.MaxWorkingBytes-engine.working, limits.MaxDecodeBytes)
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.externalRaster", record.Span.Offset,
			"no external raster decoding budget remains")
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	data, err := engine.options.ExternalRasterResolver(ctx, record)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	encodedBytes := int64(len(data))
	if encodedBytes > limits.MaxFileBytes || encodedBytes >= available {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.externalRaster", record.Span.Offset,
			"encoded external contents exceed the source file or remaining decoding budget")
	}
	decoded, err := psdfile.DecodeRasterContents(ctx, data, psdfile.LinkedRasterOptions{
		MaxPixels: min(maxPixels, limits.MaxDecodePixels), MaxWorkingBytes: available - encodedBytes,
		CenteredJPEGChroma: engine.options.CenteredJPEGChroma,
	})
	if err != nil {
		return nil, err
	}
	if decoded.Format != externalRasterFormat(record.FileType) {
		return nil, sceneError(psdfile.ErrUnsupported, "smartObject.externalRaster.format", record.Span.Offset,
			"resolved encoding differs from the retained raster file type")
	}
	engine.documents++
	retained := int64(cap(decoded.Pixels.Pix)) + int64(cap(decoded.ICCProfile))
	if err := engine.reserve(retained, "smartObject.externalRaster", record.Span.Offset); err != nil {
		return nil, err
	}
	pixels, err := engine.convertProfile(ctx, decoded.Pixels, &sceneProfile{data: decoded.ICCProfile}, destination)
	if err != nil {
		return nil, err
	}
	engine.working -= int64(cap(decoded.ICCProfile))
	return pixels, nil
}
