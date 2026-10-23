package psdedit

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdfile"
)

// SetAdjustment edits an existing RGB8 adjustment layer without changing its family.
// It owns encoded parameters and semantic views, retaining all other layer blocks and metadata.
// Non-neutral Color Balance, legacy Brightness/Contrast and Photo Filter require an explicit save composite.
// Modern CgEd-dependent adjustments and unrecognized source semantics are rejected before committing overlays.
func (e *Editor) SetAdjustment(ctx context.Context, id LayerID, adjustment psdadjust.Adjustment) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return editError(ErrUnsupported, "layers.adjustment", "target is not an existing adjustment layer")
	}
	var blocks []psdfile.AdditionalBlock
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if unsupportedAdjustmentKey(block.Key) {
			return editError(ErrUnsupported, "layers.adjustment", "source includes unsupported adjustment semantics")
		}
		if adjustmentFamilyKey(block.Key) {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return editError(ErrUnsupported, "layers.adjustment", "layer has no supported adjustment parameters")
	}
	limits := e.source.Limits()
	peakRetained := e.ownedBytesLocked(nil)
	retained := peakRetained - node.semanticCosts["adjustment"]
	for _, block := range blocks {
		retained -= int64(len(node.additional[block.Span.Offset]))
	}
	const transformWorking = int64(16 << 10)
	if retained < 0 || transformWorking > limits.MaxDecodeBytes-peakRetained {
		return editError(psdfile.ErrLimit, "layers.adjustment", "adjustment transform exceeds the working budget")
	}
	updates := make(map[int64][]byte, len(blocks))
	total := int64(0)
	changed := false
	var semantic *psdadjust.Adjustment
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return err
		}
		peak := peakRetained + total + ownedAdjustmentBytes(semantic)
		decodeBudget := min(limits.MaxMetadataBytes, limits.MaxDecodeBytes-peak-transformWorking)
		if decodeBudget <= 0 {
			return editError(psdfile.ErrLimit, "layers.adjustment", "source adjustment exceeds the working budget")
		}
		current, currentCost := node.additional[block.Span.Offset], int64(0)
		if current == nil {
			if block.Data.Length > decodeBudget/8 {
				return editError(psdfile.ErrLimit, "layers.adjustment", "source adjustment exceeds the working budget")
			}
			current, err = e.source.ReadSpan(block.Data)
			if err != nil {
				return err
			}
			currentCost = int64(len(current))
		}
		original, err := psdadjust.Decode(ctx, block.Key, current, psdadjust.DecodeOptions{
			SourceOffset: block.Data.Offset, MaxBytes: decodeBudget,
		})
		if err != nil {
			return adjustmentEditError(err)
		}
		if original.Kind != adjustment.Kind {
			return editError(ErrUnsupported, "layers.adjustment.kind", "adjustment layer family cannot be changed")
		}
		if _, err := psdadjust.Compile(*original, psdadjust.CompileOptions{
			ApproximateHueSaturation: original.Kind == psdadjust.HueSaturation,
		}); err != nil && (!errors.Is(err, psdadjust.ErrUnsupported) || !metadataAdjustmentEditable(original)) {
			return adjustmentEditError(err)
		}
		originalCost := ownedAdjustmentBytes(original)
		encodeBudget := min(limits.MaxMetadataBytes, limits.MaxDecodeBytes-peak-currentCost-originalCost)
		if encodeBudget <= 0 {
			return editError(psdfile.ErrLimit, "layers.adjustment", "parameter encoding exceeds the working budget")
		}
		desired := adjustment
		desired.Key, desired.Version, desired.ExtraVersion = original.Key, original.Version, original.ExtraVersion
		desired.Raw, desired.Span = original.Raw, original.Span
		if desired.HueSaturation != nil && original.HueSaturation != nil {
			parameters := *desired.HueSaturation
			parameters.Padding, parameters.Unknown = original.HueSaturation.Padding, original.HueSaturation.Unknown
			desired.HueSaturation = &parameters
		}
		encoded, err := psdadjust.Encode(ctx, desired, psdadjust.EncodeOptions{MaxBytes: encodeBudget})
		if err != nil {
			return adjustmentEditError(err)
		}
		if int64(len(encoded)) > limits.MaxDecodeBytes-retained-transformWorking-total {
			return editError(psdfile.ErrLimit, "layers.adjustment", "encoded adjustment overrides exceed the working budget")
		}
		total += int64(len(encoded))
		updates[block.Span.Offset] = encoded
		changed = changed || !bytes.Equal(encoded, current)
		semanticBudget := min(limits.MaxMetadataBytes,
			limits.MaxDecodeBytes-peak-currentCost-originalCost-int64(len(encoded))-transformWorking)
		if semanticBudget <= 0 {
			return editError(psdfile.ErrLimit, "layers.adjustment", "owned adjustment copy exceeds the working budget")
		}
		semantic, err = psdadjust.Decode(ctx, original.Key, encoded, psdadjust.DecodeOptions{
			MaxBytes: semanticBudget,
		})
		if err != nil {
			return adjustmentEditError(err)
		}
	}
	if !changed {
		return nil
	}
	cost := ownedAdjustmentBytes(semantic)
	if cost > limits.MaxDecodeBytes-retained-total {
		return editError(psdfile.ErrLimit, "layers.adjustment", "owned adjustment metadata exceeds the working budget")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	maps.Copy(node.additional, updates)
	node.adjustment = semantic
	node.setSemanticCost("adjustment", cost)
	e.markChangedLocked(false)
	return nil
}

func metadataAdjustmentEditable(adjustment *psdadjust.Adjustment) bool {
	switch adjustment.Kind {
	case psdadjust.ColorBalance, psdadjust.PhotoFilter:
		return true
	case psdadjust.BrightnessContrast:
		parameters := adjustment.BrightnessContrast
		return parameters != nil && parameters.Legacy && !parameters.LabOnly
	}
	return false
}

func unsupportedAdjustmentKey(key string) bool {
	switch key {
	case "vibA", "blwh", "clrL", "grdm", "selc", "CgEd":
		return true
	}
	return false
}

func adjustmentFamilyKey(key string) bool {
	switch key {
	case "nvrt", "thrs", "post", "levl", "curv", "expA", "brit", "blnc", "mixr", "hue2", "hue ", "phfl":
		return true
	}
	return false
}

func adjustmentEditError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalidEdit
	if errors.Is(err, psdadjust.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdadjust.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.adjustment", Detail: err.Error(), Cause: err}
}

func ownedAdjustmentBytes(adjustment *psdadjust.Adjustment) int64 {
	if adjustment == nil {
		return 0
	}
	length := int64(unsafe.Sizeof(*adjustment)) + int64(len(adjustment.Raw))
	length += int64(cap(adjustment.Levels)) * int64(unsafe.Sizeof(psdadjust.LevelChannel{}))
	for _, curves := range [][]psdadjust.CurveChannel{adjustment.Curves, adjustment.LegacyCurves} {
		length += int64(cap(curves)) * int64(unsafe.Sizeof(psdadjust.CurveChannel{}))
		for _, curve := range curves {
			length += int64(cap(curve.Points))*int64(unsafe.Sizeof(psdadjust.CurvePoint{})) + int64(cap(curve.LUT))
		}
	}
	if adjustment.Exposure != nil {
		length += int64(unsafe.Sizeof(*adjustment.Exposure))
	}
	if adjustment.BrightnessContrast != nil {
		length += int64(unsafe.Sizeof(*adjustment.BrightnessContrast))
	}
	if adjustment.ColorBalance != nil {
		length += int64(unsafe.Sizeof(*adjustment.ColorBalance))
	}
	if adjustment.ChannelMixer != nil {
		length += int64(unsafe.Sizeof(*adjustment.ChannelMixer))
	}
	if adjustment.HueSaturation != nil {
		length += int64(unsafe.Sizeof(*adjustment.HueSaturation)) + int64(cap(adjustment.HueSaturation.Unknown))
	}
	if adjustment.PhotoFilter != nil {
		length += int64(unsafe.Sizeof(*adjustment.PhotoFilter))
	}
	return length
}
