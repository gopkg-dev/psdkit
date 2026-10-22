package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"maps"
	"math"
	"reflect"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// SetEffects edits existing modern RGB effects without changing their families, count or fixed semantics.
// Color and opacity are editable; interior effects also support existing RGB blend modes.
// Shadow distance/local angle/blur and calibrated glow/stroke sizes use strict core validation.
// ColorOverlay also accepts partial source alpha, while geometric effects require an opaque source rectangle.
// Pure ColorOverlay additionally supports ordinary nested leaves and original pixel-mask semantics.
// Global resources, flags, descriptor structure and unsupported source metadata remain unchanged.
// Encoding and owned semantic decoding complete under a peak budget before committing editor overlays.
func (e *Editor) SetEffects(ctx context.Context, id LayerID, desired psdeffects.Set) error {
	return e.SetEffectsWithOptions(ctx, id, desired, EffectsEditOptions{})
}

// EffectsEditOptions selects explicitly approximate rendering when validating an effect edit.
// It does not relax descriptor, source, ownership or transactional budget validation.
type EffectsEditOptions struct {
	// ApproximateDropShadow allows fractional DropShadow parameters on supported raster sources.
	ApproximateDropShadow bool
}

// SetEffectsWithOptions edits existing effects under an explicit rendering policy.
// SaveOptions.ApproximateDropShadow must also be selected when rebuilding an approximate saved preview.
func (e *Editor) SetEffectsWithOptions(
	ctx context.Context, id LayerID, desired psdeffects.Set, options EffectsEditOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return err
	}
	node, parent, position, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return editError(ErrUnsupported, "layers.effects", "effect edits require an existing raster layer")
	}
	expandedSource := parent != nil || e.source.Layers[node.sourceIndex].Mask != nil
	approximateShadow := options.ApproximateDropShadow && effectsPureDropShadow(desired)
	if expandedSource && !effectsPureColorOverlay(desired) && !approximateShadow {
		return editError(ErrUnsupported, "layers.effects", "masked or nested effect edits require a supported effect set")
	}
	block, err := e.effectsEditBlockWithOptions(node, parent, position, expandedSource, approximateShadow)
	if err != nil {
		return err
	}
	if node.effects != nil && effectsParametersEqual(*node.effects, desired) {
		return nil
	}
	limits := e.source.Limits()
	peakRetained := e.ownedBytesLocked(nil)
	available := limits.MaxDecodeBytes - peakRetained
	if available <= 0 {
		return editError(psdfile.ErrLimit, "layers.effects", "retained overlays consume the effect working budget")
	}
	if expandedSource || approximateShadow {
		geometryBudget := min(limits.MaxMetadataBytes, available/8)
		if geometryBudget <= 0 {
			return editError(psdfile.ErrLimit, "layers.effects", "source metadata has no remaining working budget")
		}
		working, err := e.effectsRasterSource(ctx, node, geometryBudget, approximateShadow)
		if err != nil {
			return err
		}
		if working > available {
			return editError(psdfile.ErrLimit, "layers.effects", "source metadata exceeds the working budget")
		}
		available -= working
	}
	sourceBudget := min(limits.MaxMetadataBytes, available/6)
	if sourceBudget <= 0 {
		return editError(psdfile.ErrLimit, "layers.effects", "source effects exceed the peak working budget")
	}
	original, err := e.source.DecodeEffects(ctx, block, psdfile.MetadataOptions{MaxBytes: sourceBudget})
	if err != nil {
		return effectsEditError(err)
	}
	if expandedSource || approximateShadow {
		check := *original
		check.Enabled, check.EffectsVisible = true, true
		if err := psdeffects.Validate(&check); err != nil {
			return effectsEditError(err)
		}
		if !effectsPureColorOverlay(*original) && (!approximateShadow || !effectsPureDropShadow(*original)) {
			return editError(ErrUnsupported, "layers.effects", "source includes other active effect families")
		}
	}
	if err := effectsEditableParametersWithOptions(*original, desired, approximateShadow); err != nil {
		return err
	}
	if effectsNeedOpaqueShape(desired) && !approximateShadow {
		if err := e.effectsOpaqueShape(ctx, node, available); err != nil {
			return err
		}
	}
	if node.effects == nil && effectsParametersEqual(*original, desired) {
		return nil
	}
	patchWorking := int64(len(original.Items))*256 + 4096
	remaining := available - sourceBudget*2 - patchWorking
	if remaining <= 0 {
		return editError(psdfile.ErrLimit, "layers.effects", "effect parameter patch exceeds the peak working budget")
	}
	if err := validateEffectEditCandidateWithOptions(desired, remaining, approximateShadow); err != nil {
		return err
	}
	legacyBudget := min(limits.MaxMetadataBytes, remaining/10)
	legacy, legacyBytes, err := e.effectsLegacyUpdates(ctx, node, *original, desired, legacyBudget)
	if err != nil {
		return err
	}
	remaining -= legacyBytes
	descriptor, err := e.source.DecodeDescriptorContext(ctx,
		psdfile.Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4}, true,
		psdfile.MetadataOptions{MaxBytes: sourceBudget})
	if err != nil {
		return effectsEditError(err)
	}
	objects, err := effectsDescriptorObjects(ctx, descriptor)
	if err != nil {
		return err
	}
	if len(objects) != len(original.Items) {
		return editError(ErrUnsupported, "layers.effects", "ordered effect objects do not match the typed source view")
	}
	for index, object := range objects {
		if err := patchEffectObject(ctx, object, original.Items[index], desired.Items[index]); err != nil {
			return err
		}
	}
	encodeScratch, outputLimit := min(limits.MaxMetadataBytes, remaining/5), min(limits.MaxMetadataBytes, remaining/10)
	if encodeScratch <= 0 || outputLimit <= 0 {
		return editError(psdfile.ErrLimit, "layers.effects", "effect encoding has no remaining working budget")
	}
	encodeLimits := limits
	encodeLimits.MaxMetadataBytes = encodeScratch
	encodedDescriptor, err := e.source.EncodeDescriptor(ctx, descriptor, true,
		psdfile.DescriptorEncodeOptions{Limits: encodeLimits, MaxEncodedBytes: outputLimit})
	if err != nil {
		return effectsEditError(err)
	}
	tailLength := block.Data.Length - 4 - descriptor.Span.Length
	if tailLength < 0 || tailLength > 3 {
		return editError(ErrUnsupported, "layers.effects", "effect tail is not known alignment padding")
	}
	length := int64(len(encodedDescriptor)) + 4 + tailLength
	if length > outputLimit || length > int64(^uint(0)>>1) {
		return editError(psdfile.ErrLimit, "layers.effects", "effect payload exceeds the encoding budget")
	}
	encoded := make([]byte, int(length))
	copy(encoded[4:], encodedDescriptor)
	if tailLength != 0 {
		tail, err := e.source.ReadSpan(psdfile.Span{Offset: descriptor.Span.Offset + descriptor.Span.Length,
			Length: tailLength})
		if err != nil {
			return err
		}
		copy(encoded[4+len(encodedDescriptor):], tail)
	}
	previous := node.additional[block.Span.Offset]
	equal := bytes.Equal(previous, encoded)
	if previous == nil {
		equal, err = equalSourceVector(ctx, e.source, block.Data, encoded)
		if err != nil {
			return err
		}
	}
	if equal {
		return ctx.Err()
	}
	bridgeBudget := min(limits.MaxMetadataBytes,
		available-sourceBudget*2-patchWorking-encodeScratch-int64(cap(encodedDescriptor))-int64(cap(encoded))-legacyBytes)
	if bridgeBudget <= 0 {
		return editError(psdfile.ErrLimit, "layers.effects", "owned effects exceed the peak working budget")
	}
	owned, err := decodeEffectsEditPayload(ctx, encoded, block.Key, limits, bridgeBudget)
	if err != nil {
		return effectsEditError(err)
	}
	owned.GlobalAngle, owned.EffectsVisible = original.GlobalAngle, original.EffectsVisible
	cost := ownedEffectBytes(owned) + int64(cap(encoded)-len(encoded)) + 256
	finalRetained := peakRetained - node.semanticCosts["effects"] - int64(len(previous))
	for offset := range legacy {
		finalRetained -= int64(len(node.additional[offset]))
	}
	if finalRetained < 0 || cost+int64(len(encoded))+legacyBytes > limits.MaxDecodeBytes-finalRetained {
		return editError(psdfile.ErrLimit, "layers.effects", "owned effect overlay exceeds the retained budget")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	node.additional[block.Span.Offset] = encoded
	maps.Copy(node.additional, legacy)
	node.effects = owned
	node.setSemanticCost("effects", cost)
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) effectsLegacyUpdates(
	ctx context.Context, node *editNode, original, desired psdeffects.Set, budget int64,
) (map[int64][]byte, int64, error) {
	updates := make(map[int64][]byte)
	total := int64(0)
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if block.Key != "lrFX" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if block.Data.Length > budget-total {
			return nil, 0, editError(psdfile.ErrLimit, "layers.effects.legacy", "legacy cache exceeds peak working budget")
		}
		data, err := e.source.ReadSpan(block.Data)
		if err != nil {
			return nil, 0, err
		}
		changed, err := patchLegacyEffects(ctx, data, original, desired)
		if err != nil {
			return nil, 0, err
		}
		previous := node.additional[block.Span.Offset]
		if changed || previous != nil && !bytes.Equal(previous, data) {
			updates[block.Span.Offset] = data
			total += int64(cap(data))
		}
	}
	return updates, total, nil
}

func patchLegacyEffects(ctx context.Context, data []byte, original, desired psdeffects.Set) (bool, error) {
	if len(data) < 4 || binary.BigEndian.Uint16(data) != 0 {
		return false, editError(ErrUnsupported, "layers.effects.legacy", "legacy cache version is unsupported")
	}
	count, offset, changed := int(binary.BigEndian.Uint16(data[2:])), 4, false
	seen := map[string]bool{}
	for range count {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if len(data)-offset < 12 || string(data[offset:offset+4]) != "8BIM" {
			return false, editError(ErrUnsupported, "layers.effects.legacy", "legacy effect record is truncated or unrecognized")
		}
		key := string(data[offset+4 : offset+8])
		length := uint64(binary.BigEndian.Uint32(data[offset+8:]))
		offset += 12
		if length > uint64(len(data)-offset) {
			return false, editError(ErrUnsupported, "layers.effects.legacy", "legacy effect record length is truncated")
		}
		payload := data[offset : offset+int(length)]
		offset += int(length)
		kind := psdeffects.Kind("")
		if key == "dsdw" {
			kind = psdeffects.DropShadow
		}
		if key == "oglw" {
			kind = psdeffects.OuterGlow
		}
		if key == "isdw" {
			kind = psdeffects.InnerShadow
		}
		if key == "iglw" {
			kind = psdeffects.InnerGlow
		}
		if key == "sofi" {
			kind = psdeffects.ColorOverlay
		}
		if kind == "" {
			continue
		}
		matching := -1
		familyChanged, matches := false, 0
		for position, effect := range original.Items {
			if effect.Kind != kind {
				continue
			}
			left, right := effect, desired.Items[position]
			left.Descriptor, right.Descriptor = nil, nil
			familyChanged = familyChanged || !reflect.DeepEqual(left, right)
			matches++
			if matching < 0 {
				matching = position
			}
		}
		if matching < 0 {
			continue
		}
		if matches > 1 && kind != psdeffects.ColorOverlay {
			if familyChanged {
				return false, editError(ErrUnsupported, "layers.effects.legacy",
					"multiple modern effects have one changed legacy family")
			}
			continue
		}
		// Native legacy solid-fill stores the first, topmost modern ColorOverlay instance.
		left, right := original.Items[matching], desired.Items[matching]
		left.Descriptor, right.Descriptor = nil, nil
		if reflect.DeepEqual(left, right) {
			continue
		}
		if seen[key] {
			return false, editError(ErrUnsupported, "layers.effects.legacy",
				"duplicate changed legacy effect family is ambiguous")
		}
		if kind == psdeffects.DropShadow && left.Spread != right.Spread {
			return false, editError(ErrUnsupported, "layers.effects.legacy",
				"drop-shadow spread changes cannot synchronize the legacy intensity field")
		}
		seen[key] = true
		if err := patchLegacyEffect(payload, kind, desired.Items[matching], desired.GlobalAngle); err != nil {
			return false, err
		}
		changed = true
	}
	if offset != len(data) {
		if len(data)-offset > 3 || (4-offset%4)%4 != len(data)-offset {
			return false, editError(ErrUnsupported, "layers.effects.legacy", "legacy cache has unrecognized trailing fields")
		}
		for _, value := range data[offset:] {
			if value != 0 {
				return false, editError(ErrUnsupported, "layers.effects.legacy", "legacy cache has nonzero alignment fields")
			}
		}
	}
	return changed, nil
}

func patchLegacyEffect(data []byte, kind psdeffects.Kind, effect psdeffects.Effect, globalAngle float64) error {
	if kind == psdeffects.ColorOverlay {
		if len(data) != 34 || binary.BigEndian.Uint32(data) != 2 || string(data[4:8]) != "8BIM" {
			return editError(ErrUnsupported, "layers.effects.legacy", "legacy solid-fill layout is unrecognized")
		}
		if len(effect.BlendMode) != 4 {
			return editError(ErrUnsupported, "layers.effects.legacy", "legacy solid-fill mode is not a four-byte key")
		}
		copy(data[8:12], effect.BlendMode)
		if err := legacyEffectRGB(data[12:22], effect.Color); err != nil {
			return err
		}
		data[22] = byte(math.Round(effect.Opacity * 255))
		return legacyEffectRGB(data[24:34], effect.Color)
	}
	if len(data) < 4 {
		return editError(ErrUnsupported, "layers.effects.legacy", "legacy effect has no version")
	}
	version := binary.BigEndian.Uint32(data)
	if version != 0 && version != 2 {
		return editError(ErrUnsupported, "layers.effects.legacy", "legacy effect version is unsupported")
	}
	colorOffset, opacityOffset, expected := 20, 40, 41
	if kind == psdeffects.OuterGlow || kind == psdeffects.InnerGlow {
		colorOffset, opacityOffset, expected = 12, 31, 32
	}
	if kind == psdeffects.InnerGlow && version == 2 {
		// Version two adds the invert flag before the native RGB color.
		expected++
	}
	if version == 2 {
		expected += 10
	}
	if len(data) != expected {
		return editError(ErrUnsupported, "layers.effects.legacy", "legacy effect has additional unrecognized fields")
	}
	if err := legacyEffectFixed(data[4:8], effect.Size); err != nil {
		return err
	}
	if kind == psdeffects.DropShadow || kind == psdeffects.InnerShadow {
		angle := effect.Angle
		if effect.UseGlobalLight {
			angle = globalAngle
		}
		if err := legacyEffectFixed(data[12:16], angle); err != nil {
			return err
		}
		if err := legacyEffectFixed(data[16:20], effect.Distance); err != nil {
			return err
		}
	}
	if err := legacyEffectRGB(data[colorOffset:colorOffset+10], effect.Color); err != nil {
		return err
	}
	if kind == psdeffects.DropShadow || kind == psdeffects.InnerShadow || kind == psdeffects.InnerGlow {
		blendOffset := colorOffset + 10
		if string(data[blendOffset:blendOffset+4]) != "8BIM" || len(effect.BlendMode) != 4 {
			return editError(ErrUnsupported, "layers.effects.legacy", "legacy effect blend layout is unrecognized")
		}
		copy(data[blendOffset+4:blendOffset+8], effect.BlendMode)
	}
	data[opacityOffset] = byte(math.Round(effect.Opacity * 255))
	if version == 2 {
		if err := legacyEffectRGB(data[expected-10:], effect.Color); err != nil {
			return err
		}
	}
	return nil
}

func legacyEffectFixed(data []byte, value float64) error {
	encoded := math.Round(value * 65536)
	if math.IsNaN(encoded) || math.IsInf(encoded, 0) || encoded < math.MinInt32 || encoded > math.MaxInt32 {
		return editError(ErrUnsupported, "layers.effects.legacy", "effect number exceeds legacy signed 16.16 range")
	}
	binary.BigEndian.PutUint32(data, uint32(int32(encoded)))
	return nil
}

func legacyEffectRGB(data []byte, rgb [3]float64) error {
	if binary.BigEndian.Uint16(data) != 0 {
		return editError(ErrUnsupported, "layers.effects.legacy", "legacy native color space is not RGB")
	}
	for index, component := range rgb {
		binary.BigEndian.PutUint16(data[2+index*2:], uint16(math.Round(component*65535)))
	}
	return nil
}

func (e *Editor) effectsEditBlockWithOptions(
	node, parent *editNode, position int, allowPixelMask, approximateShadow bool,
) (psdfile.AdditionalBlock, error) {
	record := e.source.Layers[node.sourceIndex]
	if record.BlendMode != "norm" && (!approximateShadow || record.BlendMode != "mul ") ||
		record.Clipping != 0 || record.Mask != nil && !allowPixelMask ||
		node.fillColor != nil ||
		node.gradient != nil || node.adjustment != nil || node.placed != nil || node.vectorMask != nil {
		return psdfile.AdditionalBlock{}, editError(ErrUnsupported, "layers.effects",
			"effect source is not an ordinary Normal raster")
	}
	for _, channel := range record.Channels {
		if channel.ID != -1 && (!allowPixelMask || record.Mask == nil || channel.ID != -2) &&
			(channel.ID < 0 || channel.ID > 2) {
			return psdfile.AdditionalBlock{}, editError(ErrUnsupported, "layers.effects",
				"extra source channels are uncalibrated")
		}
	}
	if record.BlendRanges.Length > 4 {
		length := record.BlendRanges.Length - 4
		if length > 512 || length%4 != 0 {
			return psdfile.AdditionalBlock{}, editError(ErrUnsupported, "layers.effects",
				"blend-range structure is uncalibrated")
		}
		var slider [4]byte
		for offset := int64(4); offset < record.BlendRanges.Length; offset += 4 {
			count, err := e.source.ReadAt(slider[:], record.BlendRanges.Offset+offset)
			if count != len(slider) || err != nil && !errors.Is(err, io.EOF) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return psdfile.AdditionalBlock{}, err
			}
			if slider != [4]byte{0, 0, 255, 255} {
				return psdfile.AdditionalBlock{}, editError(ErrUnsupported, "layers.effects", "custom Blend If is uncalibrated")
			}
		}
	}
	siblings := e.roots
	if parent != nil {
		siblings = parent.children
	}
	if position+1 < len(siblings) {
		next := siblings[position+1]
		if next.sourceIndex >= 0 && e.source.Layers[next.sourceIndex].Clipping != 0 {
			return psdfile.AdditionalBlock{}, editError(ErrUnsupported, "layers.effects",
				"effect target is a clipping-chain base")
		}
	}
	var block psdfile.AdditionalBlock
	found := false
	for _, candidate := range record.AdditionalInfo {
		switch candidate.Key {
		case "lfx2", "lmfx":
			if found {
				return block, editError(ErrUnsupported, "layers.effects", "multiple modern effect blocks are ambiguous")
			}
			block, found = candidate, true
		case "SoLd", "SoLE", "PlLd", "plLd":
			if !approximateShadow {
				return block, editError(ErrUnsupported, "layers.effects", "source has a placed layer family")
			}
		case "vmsk", "vsms", "SoCo", "GdFl", "PtFl", "TySh", "tySh", "Txt2":
			return block, editError(ErrUnsupported, "layers.effects", "source has an additional live layer family")
		case "brst", "FEid", "FXid", "PxSc", "PxSD":
			return block, editError(ErrUnsupported, "layers.effects", "source has uncalibrated advanced raster semantics")
		case "tsly", "knko":
			if candidate.Data.Length < 1 || candidate.Data.Length > 4 {
				return block, editError(ErrUnsupported, "layers.effects", "advanced flag payload has unknown fields")
			}
			var value [4]byte
			count, err := e.source.ReadAt(value[:candidate.Data.Length], candidate.Data.Offset)
			if count != int(candidate.Data.Length) || err != nil && !errors.Is(err, io.EOF) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return block, err
			}
			if candidate.Key == "tsly" && value[0] == 0 || candidate.Key == "knko" && value[0] != 0 {
				return block, editError(ErrUnsupported, "layers.effects", "alternate transparency or knockout is uncalibrated")
			}
		}
	}
	if !found {
		return block, editError(ErrUnsupported, "layers.effects", "layer has no original modern effects descriptor")
	}
	return block, nil
}

// effectsPureColorOverlay retains inactive source metadata while excluding other enabled effect families.
func effectsPureColorOverlay(set psdeffects.Set) bool {
	colorOverlay := false
	for _, effect := range set.Items {
		if !effect.Enabled {
			continue
		}
		if effect.Kind == psdeffects.ColorOverlay {
			colorOverlay = true
		} else if effect.Opacity != 0 {
			return false
		}
	}
	return colorOverlay
}

func (e *Editor) effectsRasterSource(
	ctx context.Context, node *editNode, budget int64, allowOriginalPlaced bool,
) (int64, error) {
	view, err := e.source.ReadLayerMetadata(ctx, node.sourceIndex, psdfile.MetadataOptions{MaxBytes: budget})
	if err != nil {
		return 0, err
	}
	layer := view.Layer
	if layer == nil || view.Divider != 0 || len(view.BlockingIssues) != 0 || len(layer.Unsupported) != 0 ||
		layer.Group || layer.Placed != nil && !allowOriginalPlaced || layer.FillColor != nil || layer.Gradient != nil ||
		layer.Pattern != nil || layer.Adjustment != nil || layer.VectorMask != nil ||
		layer.VectorDensity != nil || layer.VectorFeather != nil || len(layer.ChannelRestrictions) != 0 ||
		layer.Knockout != psdmodel.KnockoutNone ||
		layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		return 0, editError(ErrUnsupported, "layers.effects", "source has unsupported raster or mask semantics")
	}
	if layer.Mask != nil && (view.PixelMaskChannel != -2 || len(layer.Mask.Unsupported) != 0) {
		return 0, editError(ErrUnsupported, "layers.effects", "source pixel mask representation is unsupported")
	}
	return view.WorkingBytes, nil
}

func (e *Editor) effectsOpaqueShape(ctx context.Context, node *editNode, budget int64) error {
	check := func(data []byte, step int) error {
		for offset := step - 1; offset < len(data); offset += step {
			if offset&4095 == step-1 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if data[offset] != 255 {
				return editError(ErrUnsupported, "layers.effects", "controlled effect edits require opaque source shape alpha")
			}
		}
		return nil
	}
	if plane, exists := node.planes[-1]; exists {
		return check(plane.Data, 1)
	}
	if node.pixels != nil {
		for row := 0; row < node.pixels.Rect.Dy(); row++ {
			data := node.pixels.Pix[row*node.pixels.Stride : row*node.pixels.Stride+node.pixels.Rect.Dx()*4]
			if err := check(data, 4); err != nil {
				return err
			}
		}
		return nil
	}
	for index, channel := range e.source.Layers[node.sourceIndex].Channels {
		if channel.ID != -1 {
			if channel.ID < 0 || channel.ID > 2 {
				return editError(ErrUnsupported, "layers.effects", "extra source channels have uncalibrated semantics")
			}
			continue
		}
		plane, err := e.source.DecodeChannelRegion(ctx, node.sourceIndex, index,
			image.Rect(0, 0, channel.Bounds.Dx(), channel.Bounds.Dy()), psdfile.RegionOptions{MaxWorkingBytes: budget})
		if err != nil {
			return err
		}
		return check(plane.Data, 1)
	}
	return nil
}

func effectsParametersEqual(left, right psdeffects.Set) bool {
	if left.ObjectVersion != right.ObjectVersion || left.DescriptorVersion != right.DescriptorVersion ||
		left.Enabled != right.Enabled || left.EffectsVisible != right.EffectsVisible || left.Scale != right.Scale ||
		left.GlobalAngle != right.GlobalAngle || !reflect.DeepEqual(left.ModifyingCount, right.ModifyingCount) ||
		!reflect.DeepEqual(left.Unsupported, right.Unsupported) || len(left.Items) != len(right.Items) {
		return false
	}
	for index, effect := range left.Items {
		other := right.Items[index]
		effect.Descriptor, other.Descriptor = nil, nil
		if !reflect.DeepEqual(effect, other) {
			return false
		}
	}
	return true
}

func effectsEditableParametersWithOptions(original, desired psdeffects.Set, approximateShadow bool) error {
	if len(original.Unsupported) != 0 {
		return editError(ErrUnsupported, "layers.effects", "source effects contain unsupported descriptor semantics")
	}
	projected := desired
	projected.Items = original.Items
	if !effectsParametersEqual(original, projected) {
		return editError(ErrUnsupported, "layers.effects", "global resources, master flags, scale and effect count are fixed")
	}
	if len(original.Items) != len(desired.Items) {
		return editError(ErrUnsupported, "layers.effects", "effect families and instance counts must remain unchanged")
	}
	check := desired
	check.Enabled, check.EffectsVisible = true, true
	if err := psdeffects.Validate(&check); err != nil {
		return effectsEditError(err)
	}
	for index, source := range original.Items {
		wanted := desired.Items[index]
		allowed := source
		if len(source.Unsupported) == 0 && (source.Kind == psdeffects.DropShadow ||
			source.Kind == psdeffects.OuterGlow || source.Kind == psdeffects.InnerShadow ||
			source.Kind == psdeffects.InnerGlow || source.Kind == psdeffects.Stroke || source.Kind == psdeffects.ColorOverlay) {
			allowed.Color, allowed.Opacity = wanted.Color, wanted.Opacity
			if source.Kind == psdeffects.ColorOverlay || source.Kind == psdeffects.InnerShadow ||
				source.Kind == psdeffects.InnerGlow {
				allowed.BlendMode = wanted.BlendMode
			}
			if source.Kind == psdeffects.DropShadow || source.Kind == psdeffects.InnerShadow {
				allowed.Distance, allowed.Angle = wanted.Distance, wanted.Angle
			}
			if source.Kind == psdeffects.DropShadow || source.Kind == psdeffects.InnerShadow ||
				source.Kind == psdeffects.InnerGlow ||
				source.Kind == psdeffects.OuterGlow || source.Kind == psdeffects.Stroke {
				allowed.Size = wanted.Size
			}
			if approximateShadow && source.Kind == psdeffects.DropShadow {
				allowed.Spread = wanted.Spread
				if wanted.BlendMode == "norm" || wanted.BlendMode == "mul " {
					allowed.BlendMode = wanted.BlendMode
				}
			}
		}
		allowed.Descriptor, wanted.Descriptor = nil, nil
		if !reflect.DeepEqual(allowed, wanted) {
			return editError(ErrUnsupported, "layers.effects", "an effect family, fixed flag or unsupported parameter changed")
		}
	}
	return nil
}

func effectsNeedOpaqueShape(set psdeffects.Set) bool {
	for _, effect := range set.Items {
		if effect.Enabled && effect.Kind != psdeffects.ColorOverlay {
			return true
		}
	}
	return false
}

func validateEffectEditCandidateWithOptions(desired psdeffects.Set, budget int64, approximateShadow bool) error {
	// Source geometry, pixel-mask representation and any required opaque source shape are qualified separately.
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	pixels.Pix[3] = 255
	desired.Enabled, desired.EffectsVisible = true, true
	layer := &psdmodel.Layer{Bounds: pixels.Rect, Pixels: pixels, BlendMode: "norm", Effects: &desired}
	document := &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer}}
	if err := psdcompose.Validate(document, psdcompose.Options{
		MaxWorkingBytes: budget, ApproximateDropShadow: approximateShadow,
	}); err != nil {
		return effectsEditError(err)
	}
	return nil
}

func effectsDescriptorObjects(ctx context.Context, descriptor *psdfile.Descriptor) ([]*psdfile.Descriptor, error) {
	var objects []*psdfile.Descriptor
	for _, entry := range descriptor.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch value := entry.Value.(type) {
		case *psdfile.Descriptor:
			objects = append(objects, value)
		case []psdfile.DescriptorValue:
			for _, item := range value {
				object, valid := item.Value.(*psdfile.Descriptor)
				if !valid {
					return nil, editError(ErrUnsupported, "layers.effects", "effect list has an unrecognized object")
				}
				objects = append(objects, object)
			}
		}
	}
	return objects, nil
}

func patchEffectObject(ctx context.Context, descriptor *psdfile.Descriptor, original, desired psdeffects.Effect) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if original.Color != desired.Color {
		color := gradientObject(descriptor, "Clr ")
		if color == nil || color.ClassID != "RGBC" {
			return editError(ErrUnsupported, "layers.effects.color", "source RGB color object is absent")
		}
		for channel, key := range []string{"Rd  ", "Grn ", "Bl  "} {
			if err := patchEffectNumber(color, key, desired.Color[channel]*255); err != nil {
				return err
			}
		}
	}
	if original.BlendMode != desired.BlendMode {
		identifier := effectBlendIdentifier(desired.BlendMode)
		if identifier == "" {
			return editError(ErrUnsupported, "layers.effects", "effect blend mode has no supported descriptor identifier")
		}
		found := false
		for index := range descriptor.Entries {
			entry := &descriptor.Entries[index]
			if entry.Key != "Md  " {
				continue
			}
			if found {
				return editError(ErrUnsupported, "layers.effects", "duplicate effect blend mode is ambiguous")
			}
			enumeration, valid := entry.Value.(psdfile.DescriptorEnum)
			if !valid || enumeration.TypeID != "BlnM" {
				return editError(ErrUnsupported, "layers.effects", "effect blend enumeration is unrecognized")
			}
			enumeration.ValueID = identifier
			entry.Value, found = enumeration, true
		}
		if !found {
			return editError(ErrUnsupported, "layers.effects", "source effect blend mode is absent")
		}
	}
	for _, parameter := range []struct {
		key           string
		before, after float64
	}{
		{"Opct", original.Opacity * 100, desired.Opacity * 100},
		{"Dstn", original.Distance, desired.Distance}, {"lagl", original.Angle, desired.Angle},
		{"blur", original.Size, desired.Size},
		{"Ckmt", original.Spread * 100, desired.Spread * 100},
	} {
		if parameter.before == parameter.after {
			continue
		}
		key := parameter.key
		if key == "blur" && original.Kind == psdeffects.Stroke {
			key = "Sz  "
		}
		if err := patchEffectNumber(descriptor, key, parameter.after); err != nil {
			return err
		}
	}
	return nil
}

func effectBlendIdentifier(mode string) string {
	switch mode {
	case "norm":
		return "Nrml"
	case "mul ":
		return "Mltp"
	case "scrn":
		return "Scrn"
	case "over":
		return "Ovrl"
	case "dark":
		return "Drkn"
	case "lite":
		return "Lghn"
	case "hLit":
		return "HrdL"
	case "sLit":
		return "SftL"
	case "diff":
		return "Dfrn"
	case "lddg":
		return "linearDodge"
	case "lbrn":
		return "linearBurn"
	case "idiv":
		return "CBrn"
	case "div ":
		return "CDdg"
	case "dkCl":
		return "darkerColor"
	case "lgCl":
		return "lighterColor"
	case "vLit":
		return "vividLight"
	case "lLit":
		return "linearLight"
	case "pLit":
		return "pinLight"
	case "hMix":
		return "hardMix"
	case "smud":
		return "Xclu"
	case "hue ":
		return "H   "
	case "sat ":
		return "Strt"
	case "colr":
		return "Clr "
	case "lum ":
		return "Lmns"
	case "fsub":
		return "blendSubtraction"
	case "fdiv":
		return "blendDivide"
	}
	return ""
}

func patchEffectNumber(descriptor *psdfile.Descriptor, key string, value float64) error {
	var entry *psdfile.DescriptorEntry
	for index := range descriptor.Entries {
		if descriptor.Entries[index].Key != key {
			continue
		}
		if entry != nil {
			return editError(ErrUnsupported, "layers.effects", "duplicate numeric parameter is ambiguous")
		}
		entry = &descriptor.Entries[index]
	}
	if entry == nil {
		return editError(ErrUnsupported, "layers.effects", "source numeric parameter is absent")
	}
	switch typed := entry.Value.(type) {
	case float64:
		entry.Value = value
	case float32:
		entry.Value = float32(value)
	case psdfile.DescriptorUnit:
		if typed.Width == 32 {
			typed.Number = float32(value)
		} else {
			typed.Number = value
		}
		entry.Value = typed
	case int32:
		if value != math.Trunc(value) || value < math.MinInt32 || value > math.MaxInt32 {
			return editError(ErrUnsupported, "layers.effects", "numeric change cannot retain the source integer encoding")
		}
		entry.Value = int32(value)
	default:
		return editError(ErrUnsupported, "layers.effects", "source numeric representation is not editable")
	}
	return nil
}

func decodeEffectsEditPayload(
	ctx context.Context, payload []byte, key string, limits psdfile.Limits, budget int64,
) (*psdeffects.Set, error) {
	padding := (4 - len(payload)%4) % 4
	length := int64(len(payload)) + int64(padding) + 63
	if length > budget/4 || length > math.MaxUint32 || uint64(length) > uint64(^uint(0)>>1) {
		return nil, editError(psdfile.ErrLimit, "layers.effects", "semantic envelope exceeds the peak working budget")
	}
	data := make([]byte, int(length))
	copy(data, "8BPS")
	binary.BigEndian.PutUint16(data[4:], 1)
	binary.BigEndian.PutUint16(data[12:], 3)
	binary.BigEndian.PutUint32(data[14:], 1)
	binary.BigEndian.PutUint32(data[18:], 1)
	binary.BigEndian.PutUint16(data[22:], 8)
	binary.BigEndian.PutUint16(data[24:], 3)
	binary.BigEndian.PutUint32(data[34:], uint32(20+len(payload)+padding))
	copy(data[46:], "8BIM"+key)
	binary.BigEndian.PutUint32(data[54:], uint32(len(payload)))
	copy(data[58:], payload)
	limits.MaxMetadataBytes, limits.MaxDecodeBytes = min(limits.MaxMetadataBytes, budget), budget
	source, err := psdfile.Read(gradientEditReader{ctx: ctx, reader: bytes.NewReader(data)}, length,
		psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	available := budget - length - source.IndexMetadataBytes()
	if available <= 0 || len(source.AdditionalInfo) != 1 {
		return nil, editError(psdfile.ErrLimit, "layers.effects", "owned semantic view exceeds the remaining budget")
	}
	return source.DecodeEffects(ctx, source.AdditionalInfo[0], psdfile.MetadataOptions{MaxBytes: available})
}

func ownedEffectBytes(set *psdeffects.Set) int64 {
	if set == nil {
		return 0
	}
	var descriptorCost func(*psdeffects.Descriptor) int64
	var valueCost func(any) int64
	valueCost = func(value any) int64 {
		switch typed := value.(type) {
		case *psdeffects.Descriptor:
			return descriptorCost(typed)
		case []psdeffects.Value:
			cost := int64(cap(typed)) * 256
			for _, item := range typed {
				cost += valueCost(item.Value)
			}
			return cost
		case string:
			return int64(len(typed))
		case psdeffects.Enum:
			return int64(len(typed.TypeID) + len(typed.ValueID))
		case psdeffects.Unit:
			return int64(len(typed.Unit))
		}
		return 0
	}
	descriptorCost = func(descriptor *psdeffects.Descriptor) int64 {
		if descriptor == nil {
			return 0
		}
		cost := int64(512+len(descriptor.Name)+len(descriptor.ClassID)) + int64(cap(descriptor.Entries))*256
		for _, entry := range descriptor.Entries {
			cost += int64(len(entry.Key)*2+len(entry.Type)) + valueCost(entry.Value)
		}
		return cost
	}
	return 512 + int64(cap(set.Items))*1024 + descriptorCost(set.Descriptor)
}

func effectsEditError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalidEdit
	if errors.Is(err, psdcompose.ErrUnsupported) || errors.Is(err, psdeffects.ErrUnsupported) ||
		errors.Is(err, psdfile.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdcompose.ErrLimit) || errors.Is(err, psdeffects.ErrLimit) ||
		errors.Is(err, psdfile.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.effects", Detail: err.Error(), Cause: err}
}
