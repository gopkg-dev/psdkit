package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type smartPlacementUpdate struct {
	placed *psdmodel.Placed
	blocks map[int64][]byte
	cost   int64
}

func cloneSmartPlacement(original *psdmodel.Placed, width, height int) (*psdmodel.Placed, int64) {
	placed := *original
	placed.Width, placed.Height = width, height
	placed.Transform = slices.Clone(original.Transform)
	placed.NonAffineTransform = slices.Clone(original.NonAffineTransform)
	placed.Unsupported = slices.Clone(original.Unsupported)
	cost := int64(512 + len(placed.ID) + len(placed.Type) + (len(placed.Transform)+len(placed.NonAffineTransform))*8)
	cost += smartMetadataStringBytes(placed.Unsupported)
	if original.Warp == nil {
		return &placed, cost
	}
	warp := *original.Warp
	warp.Values = slices.Clone(original.Warp.Values)
	warp.MeshPoints = slices.Clone(original.Warp.MeshPoints)
	warp.Fields = slices.Clone(original.Warp.Fields)
	warp.Kinds = slices.Clone(original.Warp.Kinds)
	warp.Unsupported = slices.Clone(original.Warp.Unsupported)
	warp.QuiltSlices = slices.Clone(original.Warp.QuiltSlices)
	for index := range warp.QuiltSlices {
		warp.QuiltSlices[index].Values = slices.Clone(original.Warp.QuiltSlices[index].Values)
	}
	if original.Warp.Bounds != nil {
		bounds := *original.Warp.Bounds
		warp.Bounds = &bounds
	}
	placed.Warp = &warp
	cost += int64(512+len(warp.Values)*8+len(warp.MeshPoints)*16) +
		warpMetadataStringBytes(&warp) + warpQuiltMetadataBytes(&warp)
	if width == original.Width && height == original.Height {
		return &placed, cost
	}
	sx, sy := float64(width)/float64(original.Width), float64(height)/float64(original.Height)
	ox, oy := 0.0, 0.0
	if warp.Bounds != nil {
		ox, oy = warp.Bounds.Left, warp.Bounds.Top
		warp.Bounds.Left, warp.Bounds.Top = 0, 0
		warp.Bounds.Right, warp.Bounds.Bottom = float64(width), float64(height)
	}
	for index := range warp.MeshPoints {
		warp.MeshPoints[index].X = (warp.MeshPoints[index].X - ox) * sx
		warp.MeshPoints[index].Y = (warp.MeshPoints[index].Y - oy) * sy
	}
	return &placed, cost
}

func prepareSmartPlacement(ctx context.Context, source *psdfile.Document, original, current *psdmodel.Placed,
	records []psdfile.SmartObjectRecord, blocks []psdfile.AdditionalBlock,
	dimensions image.Point, budget int64,
) (smartPlacementUpdate, error) {
	var update smartPlacementUpdate
	transformed := current != nil && (!slices.Equal(current.Transform, original.Transform) ||
		!slices.Equal(current.NonAffineTransform, original.NonAffineTransform))
	meshEdited := smartCurrentWarpMeshChanged(original, current)
	resized := original.Width != dimensions.X || original.Height != dimensions.Y
	if resized && original.Warp != nil && original.Warp.Split {
		return update, editError(ErrUnsupported, "smartObject.warp.size",
			"split Warp source resizing requires a separately supported slice-domain update")
	}
	if !resized && !transformed && !meshEdited {
		return update, nil
	}
	if meshEdited {
		if original.Warp == nil || current.Width <= 0 || current.Height <= 0 || transformed {
			return update, editError(ErrUnsupported, "smartObject.warp", "current Warp geometry cannot be carried safely")
		}
		for _, block := range blocks {
			if block.Key == "PlLd" || block.Key == "plLd" {
				return update, editError(ErrUnsupported, "smartObject.warp",
					"edited Warp with legacy placement is unsupported")
			}
		}
	}
	if budget < 1024 {
		return update, editError(psdfile.ErrLimit, "smartObject.metadata", "owned placement exceeds working budget")
	}
	geometry := original
	if meshEdited {
		// Scale the current controls from their current source canvas, never the original control net.
		geometry = current
	}
	update.placed, update.cost = cloneSmartPlacement(geometry, dimensions.X, dimensions.Y)
	if transformed {
		update.placed.Transform = slices.Clone(current.Transform)
		update.placed.NonAffineTransform = slices.Clone(current.NonAffineTransform)
	}
	if update.cost > budget {
		return smartPlacementUpdate{}, editError(psdfile.ErrLimit, "smartObject.metadata", "placement exceeds working budget")
	}
	resizeMetadata := resized
	if meshEdited {
		resizeMetadata = resizeMetadata || !sameSmartWarpBounds(original.Warp.Bounds, update.placed.Warp.Bounds)
	}
	update.blocks = make(map[int64][]byte, len(records))
	sx, sy := float64(dimensions.X)/float64(original.Width), float64(dimensions.Y)/float64(original.Height)
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return smartPlacementUpdate{}, err
		}
		if record.Block.Data.Length > min(source.Limits().MaxMetadataBytes, budget-update.cost) {
			return smartPlacementUpdate{}, editError(
				psdfile.ErrLimit, "smartObject.metadata", "placement payload exceeds budget",
			)
		}
		payload, err := readSmartPayload(ctx, source, record.Block.Data)
		if err != nil {
			return smartPlacementUpdate{}, err
		}
		if resizeMetadata {
			if err := patchSmartDescriptor(record.Descriptor, payload, record.Block.Data,
				original.Width, original.Height, sx, sy); err != nil {
				return smartPlacementUpdate{}, err
			}
		}
		if transformed {
			if err := patchSmartTransformDescriptor(record.Descriptor, payload, record.Block.Data,
				current.Transform); err != nil {
				return smartPlacementUpdate{}, err
			}
		}
		if meshEdited {
			if err := patchSmartWarpMesh(record.Descriptor, payload, record.Block.Data,
				update.placed.Warp.MeshPoints); err != nil {
				return smartPlacementUpdate{}, err
			}
		}
		update.blocks[record.Block.Span.Offset] = payload
		budget -= int64(len(payload))
	}
	for _, block := range blocks {
		if block.Key != "PlLd" && block.Key != "plLd" {
			continue
		}
		// Decoded typed fields and their encoding strings are conservatively reserved separately.
		if block.Data.Length > min(source.Limits().MaxMetadataBytes, budget-update.cost)/33 {
			return smartPlacementUpdate{}, editError(psdfile.ErrLimit, "smartObject.legacy", "legacy warp exceeds budget")
		}
		payload, err := readSmartPayload(ctx, source, block.Data)
		if err != nil {
			return smartPlacementUpdate{}, err
		}
		if resized {
			if err := patchSmartLegacy(ctx, source, payload, block.Data, original.Width, original.Height, sx, sy); err != nil {
				return smartPlacementUpdate{}, err
			}
		}
		if transformed {
			if err := patchSmartTransformLegacy(ctx, source, block, payload, original.Transform,
				current.Transform); err != nil {
				return smartPlacementUpdate{}, err
			}
		}
		update.blocks[block.Span.Offset] = payload
		budget -= int64(len(payload))
	}
	return update, nil
}

func smartCurrentWarpMeshChanged(original, current *psdmodel.Placed) bool {
	if current == nil || current.Warp == nil || current.Warp.Style != "custom" {
		return false
	}
	if original.Warp == nil || current.Width != original.Width || current.Height != original.Height ||
		len(current.Warp.MeshPoints) != len(original.Warp.MeshPoints) ||
		!sameSmartWarpBounds(original.Warp.Bounds, current.Warp.Bounds) {
		return true
	}
	for index, point := range current.Warp.MeshPoints {
		previous := original.Warp.MeshPoints[index]
		if math.Float64bits(point.X) != math.Float64bits(previous.X) ||
			math.Float64bits(point.Y) != math.Float64bits(previous.Y) {
			return true
		}
	}
	return false
}

func sameSmartWarpBounds(first, second *psdmodel.WarpBounds) bool {
	if first == nil || second == nil {
		return first == second
	}
	return first.Pixels == second.Pixels && first.Units == second.Units &&
		math.Float64bits(first.Left) == math.Float64bits(second.Left) &&
		math.Float64bits(first.Top) == math.Float64bits(second.Top) &&
		math.Float64bits(first.Right) == math.Float64bits(second.Right) &&
		math.Float64bits(first.Bottom) == math.Float64bits(second.Bottom)
}

func patchSmartLegacy(ctx context.Context, source *psdfile.Document, payload []byte, span psdfile.Span,
	width, height int, sx, sy float64,
) error {
	header, err := smartLegacyHeader(payload)
	if err != nil {
		return err
	}
	descriptor, err := source.DecodeDescriptorContext(ctx,
		psdfile.Span{Offset: span.Offset + int64(header), Length: span.Length - int64(header)}, true)
	if err != nil {
		return err
	}
	return patchSmartWarp(descriptor, payload, span, width, height, sx, sy)
}

func smartLegacyHeader(payload []byte) (int, error) {
	if len(payload) < 9 || string(payload[:4]) != "plcL" || binary.BigEndian.Uint32(payload[4:8]) != 3 {
		return 0, editError(ErrUnsupported, "smartObject.legacy", "legacy placed-layer version is unsupported")
	}
	start := 9 + int(payload[8])
	header := start + 16 + 64 + 4
	if len(payload)-4 < header {
		return 0, editError(psdfile.ErrMalformed, "smartObject.legacy", "legacy placed-layer header is incomplete")
	}
	if binary.BigEndian.Uint32(payload[start+12:]) != 2 || binary.BigEndian.Uint32(payload[header-4:]) != 0 {
		return 0, editError(ErrUnsupported, "smartObject.legacy", "legacy source type or warp version is unsupported")
	}
	if binary.BigEndian.Uint32(payload[header:]) != 16 {
		return 0, editError(ErrUnsupported, "smartObject.legacy", "legacy descriptor version is unsupported")
	}
	return header, nil
}

func readSmartPayload(ctx context.Context, source *psdfile.Document, span psdfile.Span) ([]byte, error) {
	if span.Length < 0 || span.Length > math.MaxInt {
		return nil, editError(psdfile.ErrLimit, "smartObject.metadata", "placement length is invalid")
	}
	data := make([]byte, int(span.Length))
	chunk := min(int64(64<<10), source.Limits().MaxReadBytes)
	for offset := int64(0); offset < span.Length; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(span.Length, offset+chunk)
		count, err := source.ReadAt(data[offset:end], span.Offset+offset)
		if count != int(end-offset) || err != nil && !errors.Is(err, io.EOF) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		offset = end
	}
	return data, ctx.Err()
}

func patchSmartDescriptor(descriptor *psdfile.Descriptor, payload []byte, span psdfile.Span,
	width, height int, sx, sy float64,
) error {
	if descriptor == nil {
		return editError(psdfile.ErrMalformed, "smartObject.metadata", "placement descriptor is absent")
	}
	if err := validateSmartDuplicates(descriptor.Entries, payload, span, "Sz  ", "warp", "quiltWarp"); err != nil {
		return err
	}
	sizes := 0
	for _, entry := range descriptor.Entries {
		switch entry.Key {
		case "Sz  ":
			size, valid := entry.Value.(*psdfile.Descriptor)
			if !valid || size == nil || entry.Type != "Objc" && entry.Type != "GlbO" {
				return editError(ErrUnsupported, "smartObject.size", "size descriptor has an unsupported layout")
			}
			foundWidth, foundHeight := false, false
			if err := validateSmartDuplicates(size.Entries, payload, span, "Wdth", "Hght"); err != nil {
				return err
			}
			for _, field := range size.Entries {
				var old, desired float64
				switch field.Key {
				case "Wdth":
					old, desired, foundWidth = float64(width), math.Round(float64(width)*sx), true
				case "Hght":
					old, desired, foundHeight = float64(height), math.Round(float64(height)*sy), true
				default:
					continue
				}
				value, valid := smartScalar(field)
				if !valid || value != old {
					return editError(ErrUnsupported, "smartObject.size", "duplicate prototype sizes are inconsistent")
				}
				if err := patchSmartScalar(payload, span, field, desired); err != nil {
					return err
				}
			}
			if !foundWidth || !foundHeight {
				return editError(ErrUnsupported, "smartObject.size", "prototype size is incomplete")
			}
			sizes++
		case "warp":
			warp, valid := entry.Value.(*psdfile.Descriptor)
			if !valid || warp == nil || entry.Type != "Objc" && entry.Type != "GlbO" {
				return editError(ErrUnsupported, "smartObject.warp", "warp descriptor has an unsupported layout")
			}
			if err := patchSmartWarp(warp, payload, span, width, height, sx, sy); err != nil {
				return err
			}
		case "quiltWarp":
			return editError(ErrUnsupported, "smartObject.warp", "quilt dimensions cannot be updated")
		}
	}
	if sizes == 0 {
		return editError(ErrUnsupported, "smartObject.size", "prototype size is absent")
	}
	return nil
}

func patchSmartWarp(warp *psdfile.Descriptor, payload []byte, span psdfile.Span,
	width, height int, sx, sy float64,
) error {
	if err := validateSmartDuplicates(warp.Entries, payload, span, "bounds", "customEnvelopeWarp", "warpStyle",
		"warpRotate", "warpValue", "warpPerspective", "warpPerspectiveOther", "uOrder", "vOrder"); err != nil {
		return err
	}
	ox, oy := 0.0, 0.0
	for _, entry := range warp.Entries {
		if entry.Key != "bounds" {
			continue
		}
		bounds, valid := entry.Value.(*psdfile.Descriptor)
		if valid && bounds != nil {
			for _, field := range bounds.Entries {
				if field.Key == "Left" {
					ox, _ = smartScalar(field)
				}
				if field.Key == "Top " {
					oy, _ = smartScalar(field)
				}
			}
		}
	}
	for _, entry := range warp.Entries {
		switch entry.Key {
		case "bounds":
			bounds, valid := entry.Value.(*psdfile.Descriptor)
			if !valid || bounds == nil || entry.Type != "Objc" && entry.Type != "GlbO" {
				return editError(ErrUnsupported, "smartObject.warp.bounds", "warp bounds have an unsupported layout")
			}
			var found [4]bool
			if err := validateSmartDuplicates(bounds.Entries, payload, span, "Left", "Top ", "Rght", "Btom"); err != nil {
				return err
			}
			for _, field := range bounds.Entries {
				var index int
				var desired float64
				switch field.Key {
				case "Left":
					index = 0
				case "Top ":
					index = 1
				case "Rght":
					index, desired = 2, math.Round(float64(width)*sx)
				case "Btom":
					index, desired = 3, math.Round(float64(height)*sy)
				default:
					continue
				}
				_, valid := smartScalar(field)
				if !valid {
					return editError(ErrUnsupported, "smartObject.warp.bounds", "warp bounds must contain finite pixels")
				}
				if unit, ok := field.Value.(psdfile.DescriptorUnit); ok && unit.Unit != "#Pxl" {
					return editError(ErrUnsupported, "smartObject.warp.bounds", "warp bounds use unsupported units")
				}
				if err := patchSmartScalar(payload, span, field, desired); err != nil {
					return err
				}
				found[index] = true
			}
			if found != [4]bool{true, true, true, true} {
				return editError(ErrUnsupported, "smartObject.warp.bounds", "warp bounds are incomplete")
			}
		case "customEnvelopeWarp":
			envelope, valid := entry.Value.(*psdfile.Descriptor)
			if !valid || envelope == nil || entry.Type != "Objc" && entry.Type != "GlbO" {
				return editError(ErrUnsupported, "smartObject.warp.mesh", "custom envelope has an unsupported layout")
			}
			if err := patchSmartEnvelope(envelope, payload, span, sx, sy, ox, oy); err != nil {
				return err
			}
		}
	}
	return nil
}

func patchSmartEnvelope(envelope *psdfile.Descriptor, payload []byte, span psdfile.Span,
	sx, sy, ox, oy float64,
) error {
	if err := validateSmartDuplicates(envelope.Entries, payload, span, "meshPoints"); err != nil {
		return err
	}
	meshes := 0
	for _, entry := range envelope.Entries {
		if entry.Key != "meshPoints" {
			continue
		}
		mesh, valid := entry.Value.(*psdfile.DescriptorObjectArray)
		if !valid || mesh == nil || entry.Type != "ObAr" {
			return editError(ErrUnsupported, "smartObject.warp.mesh", "mesh is not an object array")
		}
		var found [2]bool
		if err := validateSmartDuplicates(mesh.Entries, payload, span, "Hrzn", "Vrtc"); err != nil {
			return err
		}
		for _, column := range mesh.Entries {
			index, ratio, origin := 0, sx, ox
			switch column.Key {
			case "Hrzn":
			case "Vrtc":
				index, ratio, origin = 1, sy, oy
			default:
				continue
			}
			array, valid := column.Value.(psdfile.DescriptorUnitArray)
			if !valid || column.Type != "UnFl" || array.Unit != "#Pxl" || len(array.Numbers) != 16 {
				return editError(ErrUnsupported, "smartObject.warp.mesh", "mesh needs sixteen pixel coordinates per axis")
			}
			data, err := smartNumericSpan(payload, span, array.NumbersData, 128)
			if err != nil {
				return err
			}
			for offset, value := range array.Numbers {
				desired := (value - origin) * ratio
				if !smartFinite(desired) {
					return editError(ErrUnsupported, "smartObject.warp.mesh", "scaled mesh is nonfinite")
				}
				binary.BigEndian.PutUint64(data[offset*8:], math.Float64bits(desired))
			}
			found[index] = true
		}
		if found != [2]bool{true, true} {
			return editError(ErrUnsupported, "smartObject.warp.mesh", "mesh axes are incomplete")
		}
		meshes++
	}
	if meshes == 0 {
		return editError(ErrUnsupported, "smartObject.warp.mesh", "mesh is absent")
	}
	return nil
}

func smartScalar(entry psdfile.DescriptorEntry) (float64, bool) {
	var value float64
	switch number := entry.Value.(type) {
	case float64:
		value = number
	case int32:
		value = float64(number)
	case int64:
		value = float64(number)
	case psdfile.DescriptorUnit:
		switch scalar := number.Number.(type) {
		case float64:
			value = scalar
		case float32:
			value = float64(scalar)
		default:
			return 0, false
		}
	default:
		return 0, false
	}
	return value, smartFinite(value)
}

func smartFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validateSmartDuplicates(entries []psdfile.DescriptorEntry, payload []byte, block psdfile.Span,
	keys ...string,
) error {
	first := make(map[string]psdfile.DescriptorEntry, len(keys))
	for _, entry := range entries {
		if !slices.Contains(keys, entry.Key) {
			continue
		}
		previous, exists := first[entry.Key]
		if !exists {
			first[entry.Key] = entry
			continue
		}
		a, err := smartNumericSpan(payload, block, previous.Data, previous.Data.Length)
		if err != nil {
			return err
		}
		b, err := smartNumericSpan(payload, block, entry.Data, entry.Data.Length)
		if err != nil {
			return err
		}
		if previous.Type != entry.Type || !bytes.Equal(a, b) {
			return editError(ErrUnsupported, "smartObject.metadata", "duplicate placement fields are ambiguous")
		}
	}
	return nil
}

func smartNumericSpan(payload []byte, block, field psdfile.Span, length int64) ([]byte, error) {
	if length < 0 || field.Offset < block.Offset || field.Length != length ||
		field.Offset-block.Offset > int64(len(payload))-length {
		return nil, editError(psdfile.ErrMalformed, "smartObject.metadata", "numeric span is outside its placement block")
	}
	start := field.Offset - block.Offset
	return payload[start : start+length], nil
}

func patchSmartScalar(payload []byte, block psdfile.Span, entry psdfile.DescriptorEntry, desired float64) error {
	if !smartFinite(desired) {
		return editError(ErrUnsupported, "smartObject.metadata", "scaled coordinate is nonfinite")
	}
	field, kind, length := entry.Data, entry.Type, int64(8)
	if kind == "UntF" || kind == "UnFl" {
		unit, valid := entry.Value.(psdfile.DescriptorUnit)
		if !valid {
			return editError(psdfile.ErrMalformed, "smartObject.metadata", "unit coordinate has an invalid value")
		}
		field = unit.NumberData
		if kind == "UnFl" {
			length = 4
		}
	} else if kind == "long" {
		length = 4
	} else if kind != "doub" && kind != "comp" {
		return editError(ErrUnsupported, "smartObject.metadata", "coordinate numeric encoding is unsupported")
	}
	data, err := smartNumericSpan(payload, block, field, length)
	if err != nil {
		return err
	}
	switch kind {
	case "doub", "UntF":
		binary.BigEndian.PutUint64(data, math.Float64bits(desired))
	case "UnFl":
		converted := float32(desired)
		if math.IsInf(float64(converted), 0) {
			return editError(ErrUnsupported, "smartObject.metadata", "scaled coordinate exceeds float32")
		}
		binary.BigEndian.PutUint32(data, math.Float32bits(converted))
	case "long":
		if desired < math.MinInt32 || desired > math.MaxInt32 || desired != math.Trunc(desired) {
			return editError(ErrUnsupported, "smartObject.metadata", "scaled coordinate exceeds exact int32")
		}
		binary.BigEndian.PutUint32(data, uint32(int32(desired)))
	case "comp":
		if desired < -0x1p63 || desired >= 0x1p63 || desired != math.Trunc(desired) {
			return editError(ErrUnsupported, "smartObject.metadata", "scaled coordinate exceeds exact int64")
		}
		binary.BigEndian.PutUint64(data, uint64(int64(desired)))
	}
	return nil
}
