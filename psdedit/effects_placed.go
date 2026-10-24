package psdedit

import (
	"context"
	"image"
	"slices"
	"unsafe"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// effectsPlacedSource owns one current placement and its replacement contents for unlocked source rendering.
type effectsPlacedSource struct {
	prepared psdplaced.Prepared
	settings psdscene.Options
	retained int64
}

func effectsHasPlacedSource(record psdfile.LayerRecord) bool {
	for _, block := range record.AdditionalInfo {
		if block.Key == "SoLd" || block.Key == "SoLE" || block.Key == "PlLd" || block.Key == "plLd" {
			return true
		}
	}
	return false
}

func (e *Editor) effectsPlacedMetadataCost(node *editNode, placed *psdmodel.Placed) int64 {
	amount := smartProjectionMetadataBytes(placed) + 1024
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if smartPlacementBlock(block.Key) {
			payload := node.additional[block.Span.Offset]
			amount += int64(cap(payload) - len(payload))
		}
	}
	return amount
}

func ownEffectsPlacedPolicy(provided *psdscene.Options, budget int64) (psdscene.Options, int64, error) {
	settings := psdscene.Options{}
	if provided != nil {
		settings = *provided
	}
	if settings.MaxWorkingBytes < 0 || settings.MaxDepth < 0 || settings.MaxDocuments < 0 {
		return settings, 0, editError(ErrInvalidEdit, "layers.effects.render", "source limits must be nonnegative")
	}
	if settings.OutputProfile != nil || settings.CachedSpecialLayers || settings.CachedTextLayers ||
		settings.ApproximateText != nil {
		return settings, 0, editError(ErrUnsupported, "layers.effects.render",
			"placed effects require live sources in parent encoding; use explicit OpenType text for source typography")
	}
	if settings.MaxWorkingBytes != 0 {
		budget = min(budget, settings.MaxWorkingBytes)
	}
	amount := int64(unsafe.Sizeof(settings)) + 256
	if settings.Compose.DissolveSeed != nil {
		amount += int64(unsafe.Sizeof(uint64(0)))
		if amount > budget {
			return settings, 0, editError(psdfile.ErrLimit, "layers.effects.render", "owned source policy exceeds budget")
		}
		seed := *settings.Compose.DissolveSeed
		settings.Compose.DissolveSeed = &seed
	}
	filters := psdfilter.Options{}
	if settings.SmartFilters != nil {
		filters = *settings.SmartFilters
	}
	settings.SmartFilters = &filters
	amount += int64(unsafe.Sizeof(filters))
	if settings.ApproximateOpenTypeText != nil {
		owned := *settings.ApproximateOpenTypeText
		featureBytes := int64(unsafe.Sizeof(psdfont.ShapeFeature{}))
		amount += int64(unsafe.Sizeof(owned))
		if owned.Bounds != nil {
			amount += int64(unsafe.Sizeof(image.Rectangle{}))
		}
		if amount > budget || int64(len(owned.Shape.Features)) > (budget-amount)/featureBytes {
			return settings, 0, editError(psdfile.ErrLimit, "layers.effects.render", "source text policy exceeds budget")
		}
		amount += int64(len(owned.Shape.Features)) * featureBytes
		owned.Shape.Features = make([]psdfont.ShapeFeature, len(owned.Shape.Features))
		copy(owned.Shape.Features, settings.ApproximateOpenTypeText.Shape.Features)
		if owned.Bounds != nil {
			bounds := *owned.Bounds
			owned.Bounds = &bounds
		}
		settings.ApproximateOpenTypeText = &owned
	}
	if amount > budget {
		return settings, 0, editError(psdfile.ErrLimit, "layers.effects.render", "owned source policy exceeds budget")
	}
	return settings, amount, nil
}

func (e *Editor) prepareEffectsPlacedSourceLocked(
	ctx context.Context, node *editNode, effects *psdeffects.Set, provided *psdscene.Options, budget int64,
) (*effectsPlacedSource, error) {
	settings, policyBytes, err := ownEffectsPlacedPolicy(provided, budget)
	if err != nil {
		return nil, err
	}
	if settings.MaxWorkingBytes != 0 {
		budget = min(budget, settings.MaxWorkingBytes)
	}
	block, err := e.smartFilterEditBlock(node)
	if err != nil {
		return nil, err
	}
	limits := e.source.Limits()
	payload := node.additional[block.Span.Offset]
	length := block.Data.Length
	if payload != nil {
		length = int64(len(payload))
	}
	if budget <= policyBytes || length > min(limits.MaxMetadataBytes, (budget-policyBytes)/16) {
		return nil, editError(psdfile.ErrLimit, "layers.effects.source", "current placement exceeds candidate budget")
	}
	if payload == nil {
		payload, err = readSmartPayload(ctx, e.source, block.Data)
		if err != nil {
			return nil, err
		}
	} else {
		payload = slices.Clone(payload)
	}
	candidate, _, err := smartFilterEditSource(ctx, payload, block.Key, limits, budget-policyBytes)
	if err != nil {
		return nil, err
	}
	stage := &effectsPlacedSource{settings: settings,
		prepared: psdplaced.Prepared{Source: e.source, Candidate: candidate, LayerIndex: node.sourceIndex,
			Effects: effects, EffectsApproximateDropShadow: true}}
	stage.retained = policyBytes + int64(cap(payload)+len(payload)+len(payload)%2+111) + candidate.IndexMetadataBytes()
	if stage.retained >= budget {
		_ = candidate.Close()
		return nil, editError(psdfile.ErrLimit, "layers.effects.source", "candidate consumes the metadata budget")
	}
	identity := ""
	if node.placed != nil {
		identity = node.placed.ID
	} else {
		view, readErr := candidate.ReadLayerMetadata(ctx, 0,
			psdfile.MetadataOptions{MaxBytes: budget - stage.retained})
		if readErr != nil || view.Layer == nil || view.Layer.Placed == nil {
			_ = candidate.Close()
			if readErr != nil {
				return nil, readErr
			}
			return nil, editError(ErrUnsupported, "layers.effects.source", "current placement has no live source")
		}
		identity = view.Layer.Placed.ID
		stage.retained += view.MetadataBytes
	}
	if replacement := e.linkedOverlays[identity]; replacement != nil {
		if len(replacement.data) == 0 || !supportedSmartContentsType(replacement.fileType) {
			_ = candidate.Close()
			return nil, editError(ErrUnsupported, "layers.effects.source", "current replacement source is unsupported")
		}
		if int64(len(replacement.data)) > budget-stage.retained {
			_ = candidate.Close()
			return nil, editError(psdfile.ErrLimit, "layers.effects.source", "replacement source exceeds candidate budget")
		}
		stage.prepared.Contents = slices.Clone(replacement.data)
		stage.prepared.FileType = replacement.fileType
		stage.retained += int64(cap(stage.prepared.Contents))
	}
	if stage.retained >= budget {
		_ = candidate.Close()
		return nil, editError(psdfile.ErrLimit, "layers.effects.source", "candidate consumes the source rendering budget")
	}
	stage.settings.MaxWorkingBytes = budget - stage.retained
	return stage, nil
}

func (stage *effectsPlacedSource) render(ctx context.Context) (*psdmodel.Layer, error) {
	return psdscene.RenderPreparedPlacedLayer(ctx, stage.prepared, stage.settings)
}

func effectsPlacedPixels(layer *psdmodel.Layer) (*image.NRGBA, error) {
	if layer == nil || layer.Pixels == nil || layer.Placed == nil || layer.Bounds.Size() != layer.Pixels.Rect.Size() ||
		!layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		return nil, editError(ErrUnsupported, "layers.effects.source", "fresh projected artwork is required")
	}
	owned := *layer.Pixels
	owned.Rect = image.Rect(0, 0, layer.Bounds.Dx(), layer.Bounds.Dy())
	return &owned, nil
}
