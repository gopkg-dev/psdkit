package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"unsafe"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type preparedPlacement struct {
	layer     *psdmodel.Layer
	placement psdfile.SmartObjectRecord
	contents  []byte
	fileType  string
}

func preparedPlacementOptions(prepared psdplaced.Prepared, options Options) error {
	if prepared.Candidate == nil || len(prepared.Candidate.Layers) != 1 ||
		prepared.Candidate.Header.Depth != 8 || prepared.Candidate.Header.ColorMode != 3 {
		return sceneError(psdfile.ErrMalformed, "placedLayer.candidate", 0,
			"provide one indexed RGB8 candidate layer")
	}
	if options.OutputProfile != nil || options.CachedSpecialLayers || options.CachedTextLayers {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.policy", 0,
			"candidate artwork must retain parent encoding and live source rendering")
	}
	if prepared.Contents == nil {
		if prepared.FileType != "" {
			return sceneError(psdfile.ErrMalformed, "placedLayer.candidate.contents", 0,
				"a content type requires replacement bytes")
		}
		return nil
	}
	if prepared.FileType != "8BPS" && prepared.FileType != "8BPB" && externalRasterFormat(prepared.FileType) == "" {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.contents", 0,
			"candidate contents must use PSD/PSB/PNG/JPEG/TIFF")
	}
	if int64(len(prepared.Contents)) > prepared.Source.Limits().MaxFileBytes {
		return sceneError(psdfile.ErrLimit, "placedLayer.candidate.contents", 0,
			"candidate contents exceed the original source file limit")
	}
	return nil
}

func preparedPlacementIdentity(record psdfile.SmartObjectRecord) (string, error) {
	var placed string
	seen := make(map[string]bool)
	for _, entry := range record.Descriptor.Entries {
		switch entry.Key {
		case "Idnt", "Type", "Sz  ", "Trnf", "nonAffineTransform", "placed", "warp", "quiltWarp", "filterFX":
			if seen[entry.Key] {
				return "", sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.identity", entry.Data.Offset,
					"duplicate placement identity, geometry or filter fields are ambiguous")
			}
			seen[entry.Key] = true
		}
		if entry.Key == "placed" {
			value, valid := entry.Value.(string)
			if !valid || entry.Type != "TEXT" {
				return "", sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.identity", entry.Data.Offset,
					"filter-mask placement identity must be retained text")
			}
			placed = value
		}
	}
	return placed, nil
}

func (engine *rgbTileRenderer) installPreparedPlacement(
	ctx context.Context, prepared psdplaced.Prepared, layer *psdmodel.Layer,
) error {
	if err := engine.reserve(int64(unsafe.Sizeof(preparedPlacement{}))+1024,
		"placedLayer.candidate.metadata", 0); err != nil {
		return err
	}
	original, err := engine.placement(ctx, engine.source.Layers[prepared.LayerIndex], layer)
	if err != nil {
		return err
	}
	originalID, err := preparedPlacementIdentity(original)
	if err != nil {
		return err
	}
	available := engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return sceneError(psdfile.ErrLimit, "placedLayer.candidate.metadata", 0, "no candidate decoding budget remains")
	}
	candidateRecord := prepared.Candidate.Layers[0]
	if candidateRecord.Divider != 0 || len(candidateRecord.AdditionalInfo) != 1 ||
		(candidateRecord.AdditionalInfo[0].Key != "SoLd" && candidateRecord.AdditionalInfo[0].Key != "SoLE") {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.metadata", 0,
			"candidate layer must contain one modern placement block")
	}
	placements, err := prepared.Candidate.ReadSmartObjectView(ctx, psdfile.MetadataOptions{
		MaxBytes: min(available, engine.source.Limits().MaxMetadataBytes),
	})
	if err != nil {
		return err
	}
	if err := engine.reserve(placements.MetadataBytes, "placedLayer.candidate.metadata", 0); err != nil {
		return err
	}
	if len(placements.Records) != 1 || placements.Records[0].LayerIndex != 0 ||
		placements.Records[0].LayerSetIndex != prepared.Candidate.ActiveLayerSet || placements.Records[0].SourceType != 2 {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.metadata", 0,
			"candidate must identify one active modern raster placement")
	}
	placement := placements.Records[0]
	candidateID, err := preparedPlacementIdentity(placement)
	if err != nil {
		return err
	}
	if placement.SourceID != original.SourceID || candidateID != originalID {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.identity", placement.Block.Data.Offset,
			"candidate must retain original content and filter-mask identities")
	}
	available = engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return sceneError(psdfile.ErrLimit, "placedLayer.candidate.metadata", 0, "no candidate layer budget remains")
	}
	view, err := prepared.Candidate.ReadLayerMetadata(ctx, 0, psdfile.MetadataOptions{
		MaxBytes: min(available, engine.source.Limits().MaxMetadataBytes),
	})
	if err != nil {
		return err
	}
	if err := engine.reserve(view.MetadataBytes, "placedLayer.candidate.metadata", 0); err != nil {
		return err
	}
	if view.Layer == nil || view.Layer.Placed == nil || view.Layer.Placed.Type != "raster" ||
		view.Layer.Placed.ID != placement.SourceID || len(view.Layer.Unsupported) != 0 || len(view.BlockingIssues) != 0 {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.metadata", 0,
			"candidate contains unsupported layer-source metadata")
	}
	if err := engine.linkedRecords(ctx); err != nil {
		return err
	}
	linked, exists := engine.linked[placement.SourceID]
	if !exists {
		return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.contents", 0,
			"original content reference is absent")
	}
	if prepared.Contents == nil {
		if linked.Kind != "liFD" && (linked.Kind != "liFE" ||
			engine.settings.ExternalResolver == nil && !engine.legacy.resolvesExternalRaster(linked)) {
			return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.contents", 0,
				"external original contents require an explicit resolver")
		}
		if linked.Kind == "liFD" && (!linked.HasData || linked.Data.Length == 0) {
			return sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.contents", 0,
				"embedded original contents are absent")
		}
	}
	layer.Placed = view.Layer.Placed
	// Parent-layer styling is applied only when composing the edited layer, never in its saved artwork.
	layer.Mask, layer.VectorMask, layer.Effects = nil, nil, nil
	layer.VectorDensity, layer.VectorFeather, layer.Opacity, layer.FillOpacity = nil, nil, nil, nil
	layer.MaskAsGlobal, layer.VectorMaskAsGlobal = nil, nil
	engine.maskIDs[layer] = 0
	// Both documents are borrowed; a recursive resolver must not transfer either one back as an owned child.
	engine.legacy.active[prepared.Candidate] = true
	engine.prepared = &preparedPlacement{layer: layer, placement: placement,
		contents: prepared.Contents, fileType: prepared.FileType}
	return ctx.Err()
}

func (engine *rgbTileRenderer) preparedPrototype(
	ctx context.Context, placement psdfile.SmartObjectRecord,
) (*image.NRGBA, error) {
	profile, err := engine.readProfile(ctx)
	if err != nil {
		return nil, err
	}
	depth := 2
	if engine.settings.sourceDepth != 0 {
		depth = engine.settings.sourceDepth + 1
	}
	if engine.settings.ApproximateOpenTypeText != nil {
		depth = engine.settings.openTypeDepth + 1
		if err := engine.checkOpenTypeContent(depth); err != nil {
			return nil, err
		}
		engine.legacy.documents = engine.settings.openTypeTraversal.documents
		defer func() { engine.settings.openTypeTraversal.documents = engine.legacy.documents }()
	}
	engine.legacy.working = engine.working
	if depth > engine.settings.MaxDepth || engine.legacy.documents >= engine.settings.MaxDocuments {
		return nil, sceneError(psdfile.ErrLimit, "placedLayer.candidate.contents", 0,
			"recursive original-source limits exceeded")
	}
	available := min(engine.settings.MaxWorkingBytes-engine.working, engine.source.Limits().MaxDecodeBytes)
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "placedLayer.candidate.contents", 0, "no source decoding budget remains")
	}
	var pixels *image.NRGBA
	if engine.prepared.fileType == "8BPS" || engine.prepared.fileType == "8BPB" {
		pixels, err = engine.preparedDocumentPrototype(ctx, profile, depth, available)
	} else {
		maxPixels := engine.source.Limits().MaxDecodePixels
		if engine.settings.ApproximateOpenTypeText != nil {
			maxPixels = min(maxPixels, engine.settings.MaxPixels)
		}
		var decoded *psdfile.LinkedRaster
		decoded, err = psdfile.DecodeRasterContents(ctx, engine.prepared.contents, psdfile.LinkedRasterOptions{
			MaxPixels: maxPixels, MaxWorkingBytes: available, CenteredJPEGChroma: engine.settings.CenteredJPEGChroma,
		})
		if err == nil && decoded.Format != externalRasterFormat(engine.prepared.fileType) {
			err = sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.contents", 0,
				"candidate bytes do not match their retained file type")
		}
		if err == nil {
			engine.legacy.documents++
			err = engine.legacy.reserve(int64(cap(decoded.Pixels.Pix))+int64(cap(decoded.ICCProfile)),
				"placedLayer.candidate.contents", 0)
		}
		if err == nil {
			pixels, err = engine.legacy.convertProfile(ctx, decoded.Pixels, &sceneProfile{data: decoded.ICCProfile}, profile)
			engine.legacy.working -= int64(cap(decoded.ICCProfile))
		}
	}
	if err != nil {
		return nil, err
	}
	engine.working = engine.legacy.working
	engine.contents[placement.SourceID] = pixels
	return pixels, ctx.Err()
}

func (engine *rgbTileRenderer) preparedDocumentPrototype(
	ctx context.Context, destination *sceneProfile, depth int, available int64,
) (*image.NRGBA, error) {
	data := engine.prepared.contents
	expected := uint16(1)
	if engine.prepared.fileType == "8BPB" {
		expected = 2
	}
	if len(data) < 6 || !bytes.Equal(data[:4], []byte("8BPS")) || binary.BigEndian.Uint16(data[4:6]) != expected {
		return nil, sceneError(psdfile.ErrUnsupported, "placedLayer.candidate.contents", 0,
			"candidate document bytes do not match their retained file type")
	}
	limits := engine.source.Limits()
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, available)
	limits.MaxDecodeBytes = min(limits.MaxDecodeBytes, available)
	child, err := psdfile.Parse(data, psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, err
	}
	defer func() { _ = child.Close() }()
	indexBytes := child.IndexMetadataBytes()
	if err := engine.legacy.reserve(indexBytes, "placedLayer.candidate.index", 0); err != nil {
		return nil, err
	}
	var pixels *image.NRGBA
	if engine.settings.ApproximateOpenTypeText != nil && len(child.Layers) != 0 {
		engine.working = engine.legacy.working
		pixels, err = engine.renderOpenTypeChild(ctx, child, depth, destination)
		engine.legacy.documents = engine.settings.openTypeTraversal.documents
	} else {
		pixels, err = engine.legacy.render(ctx, child, depth, destination)
	}
	engine.legacy.working -= indexBytes
	return pixels, err
}
