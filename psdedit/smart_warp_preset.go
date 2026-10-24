package psdedit

import (
	"context"
	"math"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
)

// SmartWarpPresetParameters changes the existing bend of a supported native Warp preset.
// Value is the native percentage scalar; the preset type, direction and coordinate schema remain unchanged.
type SmartWarpPresetParameters struct {
	Value float64
}

// SetSmartObjectWarpPreset changes the bend of one existing horizontal Arc Lower raster placement.
// Native 4x2 affine geometry, zero perspective and an absolute bend below 100 are required.
// Render.ApproximateCustomWarp must be explicitly enabled, including for a zero bend.
// Explicit external resolvers and source policies apply to this edit only; shared peer placements remain unchanged.
// Unsupported presets, schemas or resolver results commit neither parameters nor replacement artwork.
func (e *Editor) SetSmartObjectWarpPreset(
	ctx context.Context, id LayerID, parameters SmartWarpPresetParameters, options SmartWarpEditOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	stage, err := e.prepareSmartWarpCandidateLocked(ctx, id, options.Render, true,
		func(descriptor *psdfile.Descriptor, payload []byte, span psdfile.Span) error {
			return patchSmartWarpPreset(descriptor, payload, span, parameters)
		})
	e.mu.Unlock()
	if err != nil {
		return err
	}
	return e.applySmartWarpStage(ctx, stage)
}

// patchSmartWarpPreset changes only the existing scalar bytes without re-encoding its descriptor or tail.
func patchSmartWarpPreset(
	descriptor *psdfile.Descriptor, payload []byte, span psdfile.Span, parameters SmartWarpPresetParameters,
) error {
	if !smartFinite(parameters.Value) || math.Abs(parameters.Value) >= 100 {
		return editError(ErrInvalidEdit, "smartObject.warp.preset.value",
			"provide a finite bend strictly between -100 and 100")
	}
	if descriptor == nil {
		return editError(ErrUnsupported, "smartObject.warp.preset", "one existing native preset descriptor is required")
	}
	if err := psdplaced.ValidatePresetDescriptor(descriptor); err != nil {
		return editError(ErrUnsupported, "smartObject.warp.preset", "one unambiguous supported native preset is required")
	}
	var warp *psdfile.Descriptor
	for _, entry := range descriptor.Entries {
		if entry.Key != "warp" && entry.Key != "quiltWarp" {
			continue
		}
		candidate, valid := entry.Value.(*psdfile.Descriptor)
		if entry.Key != "warp" || warp != nil || !valid || candidate == nil ||
			entry.Type != "Objc" && entry.Type != "GlbO" {
			return editError(ErrUnsupported, "smartObject.warp.preset", "one unambiguous native Warp descriptor is required")
		}
		warp = candidate
	}
	if warp == nil {
		return editError(ErrUnsupported, "smartObject.warp.preset", "an existing native Warp descriptor is required")
	}
	var value *psdfile.DescriptorEntry
	for index := range warp.Entries {
		entry := &warp.Entries[index]
		if entry.Key != "warpValue" {
			continue
		}
		current, valid := entry.Value.(float64)
		if value != nil || !valid || entry.Type != "doub" || !smartFinite(current) || math.Abs(current) >= 100 {
			return editError(ErrUnsupported, "smartObject.warp.preset.value", "one supported native double bend is required")
		}
		value = entry
	}
	if value == nil {
		return editError(ErrUnsupported, "smartObject.warp.preset.value", "the existing native bend is absent")
	}
	return patchSmartScalar(payload, span, *value, parameters.Value)
}
