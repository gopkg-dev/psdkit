package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"reflect"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

// PatternFillEditOptions supplies accurate layer artwork for known pattern parameters outside verified sampling.
// Pixels contains straight-alpha RGB8 samples before layer opacity, masks and effects, in current layer bounds.
// The editor owns a copy and validates geometry, not agreement with Photoshop's pattern sampling.
type PatternFillEditOptions struct {
	Pixels *image.NRGBA
}

// SetPatternFill changes an existing PtFl layer's local pattern reference or sampling controls.
// RGB8 tiles support 50, 100 or 200 percent scale, whole-turn angles and bounded integer document phase.
// Single-pixel tiles support every finite positive scale and finite angle and phase.
// Known source scale, angle and phase controls may be replaced even when their original sampling is unsupported.
// Descriptor encodings, original entry order and pattern pixel blocks remain unchanged.
// Recognized zero descriptor alignment is regenerated inside the declared payload for Photoshop compatibility.
// The editor never searches external files or application preset libraries.
func (e *Editor) SetPatternFill(ctx context.Context, id LayerID, fill psdpattern.Fill) error {
	return e.SetPatternFillWithOptions(ctx, id, &fill, PatternFillEditOptions{})
}

// SetPatternFillWithOptions edits an existing local RGB8 pattern fill and optionally replaces its saved raster.
// With Pixels, positive finite scales and finite angles are editable without enabling their live sampling.
// Phase remains integral and bounded, source descriptor layouts and local pattern references remain qualified.
// Pixels must cover the current layer bounds exactly; saving an unsupported live fill needs an explicit composite.
// With no Pixels, the same strict sampling scope as SetPatternFill applies.
func (e *Editor) SetPatternFillWithOptions(
	ctx context.Context, id LayerID, requested *psdpattern.Fill, options PatternFillEditOptions,
) error {
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
	if requested == nil {
		return editError(ErrInvalidEdit, "layers.pattern", "pattern parameters are required")
	}
	fill := *requested
	validate := validatePatternEditSource
	if options.Pixels != nil {
		validate = validatePatternEditMetadata
	}
	if err := validate(fill); err != nil {
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return editError(ErrUnsupported, "layers.pattern", "target is not an existing pattern fill layer")
	}
	var block psdfile.AdditionalBlock
	found := false
	for _, candidate := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if candidate.Key == "SoCo" || candidate.Key == "GdFl" || adjustmentFamilyKey(candidate.Key) ||
			unsupportedAdjustmentKey(candidate.Key) {
			return editError(ErrUnsupported, "layers.pattern", "multiple live layer families are ambiguous")
		}
		if candidate.Key == "PtFl" {
			if found {
				return editError(ErrUnsupported, "layers.pattern", "duplicate pattern fill blocks are ambiguous")
			}
			block, found = candidate, true
		}
	}
	if !found {
		return editError(ErrUnsupported, "layers.pattern", "layer has no original pattern descriptor")
	}
	if options.Pixels == nil && node.pattern != nil && reflect.DeepEqual(fill, *node.pattern) {
		if err := psdpattern.Validate(node.patternTile, fill); err != nil {
			return patternEditError(err)
		}
		return ctx.Err()
	}
	limits := e.source.Limits()
	peakRetained := e.ownedBytesLocked(nil)
	available := limits.MaxDecodeBytes - peakRetained
	pixelBytes := int64(0)
	if options.Pixels != nil {
		pixelBytes, err = e.patternEditPixelBytes(node, options.Pixels, available)
		if err != nil {
			return err
		}
		available -= pixelBytes
		if node.pattern != nil && reflect.DeepEqual(fill, *node.pattern) && node.pixels != nil &&
			textEditPixelsEqual(ctx, node.pixels, options.Pixels) {
			return ctx.Err()
		}
	}
	viewBudget := min(limits.MaxMetadataBytes, available/8)
	if viewBudget <= 0 || block.Data.Length > viewBudget/8 ||
		int64(len(fill.ID))+int64(len(fill.Name)) > available/32 {
		return editError(psdfile.ErrLimit, "layers.pattern", "pattern metadata exceeds the peak working budget")
	}
	original, err := e.source.DecodePatternFill(ctx, block, psdfile.MetadataOptions{MaxBytes: viewBudget})
	if err != nil {
		return patternEditError(err)
	}
	if err := validatePatternEditSource(*original.Fill); err != nil {
		return err
	}
	view, err := e.source.ReadPatternView(ctx, psdfile.MetadataOptions{MaxBytes: viewBudget})
	if err != nil {
		return patternEditError(err)
	}
	previousRecord, err := patternEditRecord(view.Records, original.Fill.ID)
	if err != nil {
		return err
	}
	if _, err := e.source.EstimatePatternTileRGB8(*previousRecord); err != nil {
		return patternEditError(err)
	}
	record, err := patternEditRecord(view.Records, fill.ID)
	if err != nil {
		return err
	}
	if fill.Name == "" {
		fill.Name = record.Name
	} else if fill.Name != record.Name {
		return editError(ErrInvalidEdit, "layers.pattern.name", "name does not match the selected local pattern")
	}
	estimate, err := e.source.EstimatePatternTileRGB8(*record)
	if err != nil {
		return patternEditError(err)
	}
	patchWorking := int64(len(fill.ID)+len(fill.Name))*8 + 4096
	remaining := available - 2*viewBudget - patchWorking
	if remaining <= 0 {
		return editError(psdfile.ErrLimit, "layers.pattern", "pattern patch exceeds the peak working budget")
	}
	tile := node.patternTile
	if node.pattern == nil || node.pattern.ID != fill.ID || tile == nil {
		if estimate.WorkingBytes > remaining {
			return editError(psdfile.ErrLimit, "layers.pattern", "pattern tile exceeds the peak working budget")
		}
		tile, err = e.source.DecodePatternTileRGB8(ctx, *record,
			psdfile.PatternDecodeOptions{MaxWorkingBytes: remaining})
		if err != nil {
			return patternEditError(err)
		}
	}
	if options.Pixels == nil {
		if err := psdpattern.Validate(tile, fill); err != nil {
			return patternEditError(err)
		}
		if node.pattern == nil && reflect.DeepEqual(fill, *original.Fill) {
			return ctx.Err()
		}
	}
	remaining -= estimate.RetainedBytes + 256
	if remaining <= 0 {
		return editError(psdfile.ErrLimit, "layers.pattern", "pattern encoding exceeds the peak working budget")
	}
	patchPatternDescriptor(original.Descriptor, fill)
	encodeScratch, outputLimit := min(limits.MaxMetadataBytes, remaining/4), min(limits.MaxMetadataBytes, remaining/8)
	if encodeScratch <= 0 || outputLimit <= 0 {
		return editError(psdfile.ErrLimit, "layers.pattern", "descriptor encoding exceeds the peak working budget")
	}
	encodeLimits := limits
	encodeLimits.MaxMetadataBytes = encodeScratch
	encoded, err := e.source.EncodeDescriptor(ctx, original.Descriptor, true,
		psdfile.DescriptorEncodeOptions{Limits: encodeLimits, MaxEncodedBytes: outputLimit})
	if err != nil {
		return patternEditError(err)
	}
	// Photoshop's PtFl descriptor reader includes four-byte alignment in the declared block length.
	// The source reader already rejected nonzero or unknown tails; only known zero alignment is rebuilt.
	for len(encoded)%4 != 0 {
		encoded = append(encoded, 0)
	}
	previous := node.additional[block.Span.Offset]
	equal := bytes.Equal(previous, encoded)
	if previous == nil {
		equal, err = equalSourceVector(ctx, e.source, block.Data, encoded)
		if err != nil {
			return err
		}
	}
	backing := int64(cap(encoded))
	bridgeBudget := min(limits.MaxMetadataBytes, remaining-encodeScratch-2*backing-4096)
	if bridgeBudget <= 0 {
		return editError(psdfile.ErrLimit, "layers.pattern", "owned semantic copy exceeds the peak budget")
	}
	owned, err := decodePatternEditPayload(ctx, encoded, limits, bridgeBudget)
	if err != nil {
		return patternEditError(err)
	}
	if err := validate(*owned); err != nil {
		return err
	}
	if owned.Phase != fill.Phase || owned.ID != fill.ID || owned.Name != fill.Name ||
		owned.Scale != fill.Scale || owned.Angle != fill.Angle || owned.Link != fill.Link {
		return editError(ErrUnsupported, "layers.pattern", "source encoding cannot represent the requested parameters")
	}
	if equal && options.Pixels == nil {
		return ctx.Err()
	}
	cost := int64(unsafe.Sizeof(*owned)) + int64(unsafe.Sizeof(*tile)) + int64(cap(tile.Pix)) +
		int64(len(owned.ID)+len(owned.Name))*4 + backing - int64(len(encoded)) + 256
	finalRetained := peakRetained - node.semanticCosts["pattern"] - int64(len(previous))
	if options.Pixels != nil && node.pixels != nil {
		finalRetained -= int64(len(node.pixels.Pix))
	}
	if finalRetained < 0 || cost+int64(len(encoded))+pixelBytes > limits.MaxDecodeBytes-finalRetained {
		return editError(psdfile.ErrLimit, "layers.pattern", "pattern overlay exceeds the retained budget")
	}
	var artwork *image.NRGBA
	if options.Pixels != nil {
		artwork, err = nativeTextCopyImage(ctx, options.Pixels)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	node.additional[block.Span.Offset] = encoded
	node.pattern, node.patternTile = owned, tile
	if artwork != nil {
		node.pixels = artwork
	}
	node.setSemanticCost("pattern", cost)
	e.markChangedLocked(false)
	return nil
}

func validatePatternEditMetadata(fill psdpattern.Fill) error {
	if err := validatePatternEditSource(fill); err != nil {
		return err
	}
	if fill.Phase.X != math.Trunc(fill.Phase.X) || fill.Phase.Y != math.Trunc(fill.Phase.Y) {
		return editError(ErrUnsupported, "layers.pattern.phase", "phase must use whole document pixels")
	}
	if math.Abs(fill.Phase.X) > 1<<40 || math.Abs(fill.Phase.Y) > 1<<40 {
		return editError(ErrInvalidEdit, "layers.pattern.phase", "phase exceeds exact pixel coordinate precision")
	}
	return nil
}

func validatePatternEditSource(fill psdpattern.Fill) error {
	if fill.ID == "" || len(fill.Unsupported) != 0 {
		return editError(ErrUnsupported, "layers.pattern", "known parameters and a local pattern reference are required")
	}
	if math.IsNaN(fill.Scale) || math.IsInf(fill.Scale, 0) || fill.Scale <= 0 ||
		math.IsNaN(fill.Angle) || math.IsInf(fill.Angle, 0) || math.IsNaN(fill.Phase.X) ||
		math.IsInf(fill.Phase.X, 0) || math.IsNaN(fill.Phase.Y) || math.IsInf(fill.Phase.Y, 0) {
		return editError(ErrInvalidEdit, "layers.pattern", "positive finite scale and finite angle and phase are required")
	}
	return nil
}

func (e *Editor) patternEditPixelBytes(node *editNode, pixels *image.NRGBA, budget int64) (int64, error) {
	if pixels.Rect != node.bounds {
		return 0, editError(ErrInvalidEdit, "layers.pattern.pixels", "artwork must match the current layer bounds exactly")
	}
	var colors [3]int
	alpha := 0
	for _, channel := range e.source.Layers[node.sourceIndex].Channels {
		if channel.ID >= 0 && channel.ID < 3 {
			colors[channel.ID]++
		} else if channel.ID == -1 {
			alpha++
		}
	}
	if colors != [3]int{1, 1, 1} || alpha > 1 {
		return 0, editError(ErrUnsupported, "layers.pattern.pixels", "unambiguous source RGB channels are required")
	}
	for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		switch block.Key {
		case "TySh", "tySh", "SoLd", "SoLE", "PlLd", "PxSc", "PxSD":
			return 0, editError(ErrUnsupported, "layers.pattern.pixels", "incompatible semantic source block")
		}
	}
	length, err := textEditPixelBytes(pixels, e.source.Limits(), budget)
	if err != nil {
		kind := ErrInvalidEdit
		if errors.Is(err, psdfile.ErrLimit) {
			kind = psdfile.ErrLimit
		}
		return 0, &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.pattern.pixels",
			Detail: "invalid or over-budget supplied layer artwork", Cause: err}
	}
	return length, nil
}

func patternEditRecord(records []psdfile.PatternRecord, id string) (*psdfile.PatternRecord, error) {
	var found *psdfile.PatternRecord
	for index := range records {
		if records[index].ID == id {
			if found != nil {
				return nil, editError(ErrUnsupported, "layers.pattern.reference", "local pattern reference is ambiguous")
			}
			found = &records[index]
		}
	}
	if found == nil {
		return nil, editError(ErrUnsupported, "layers.pattern.reference", "pattern is absent from local source blocks")
	}
	return found, nil
}

func patchPatternDescriptor(descriptor *psdfile.Descriptor, fill psdpattern.Fill) {
	pattern := gradientObject(descriptor, "Ptrn")
	gradientEntry(pattern, "Idnt", "TEXT", fill.ID, nil)
	gradientEntry(pattern, "Nm  ", "TEXT", fill.Name, "")
	gradientUnit(descriptor, "Scl ", "#Prc", fill.Scale, 100)
	gradientUnit(descriptor, "Angl", "#Ang", fill.Angle, 0)
	link := "Lnkd"
	if gradientFind(descriptor, "Algn") != nil {
		link = "Algn"
	}
	gradientEntry(descriptor, link, "bool", fill.Link, true)
	phase := gradientObject(descriptor, "phase")
	if phase == nil && fill.Phase == (psdpattern.Phase{}) {
		return
	}
	if phase == nil {
		phase = &psdfile.Descriptor{ClassID: "Pnt "}
		gradientEntry(descriptor, "phase", "Objc", phase, nil)
	}
	for index, key := range []string{"Hrzn", "Vrtc"} {
		value := []float64{fill.Phase.X, fill.Phase.Y}[index]
		if entry := gradientFind(phase, key); entry != nil && entry.Type != "doub" {
			gradientUnit(phase, key, "#Pxl", value, 0)
		} else {
			gradientEntry(phase, key, "doub", value, nil)
		}
	}
}

func decodePatternEditPayload(
	ctx context.Context, payload []byte, limits psdfile.Limits, budget int64,
) (*psdpattern.Fill, error) {
	padding := (4 - len(payload)%4) % 4
	length := int64(len(payload)) + int64(padding) + 63
	if length > budget/4 || length > math.MaxUint32 || uint64(length) > uint64(^uint(0)>>1) {
		return nil, editError(psdfile.ErrLimit, "layers.pattern", "encoded semantic envelope exceeds the peak budget")
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
	copy(data[46:], "8BIMPtFl")
	binary.BigEndian.PutUint32(data[54:], uint32(len(payload)))
	copy(data[58:], payload)
	limits.MaxMetadataBytes, limits.MaxDecodeBytes = min(limits.MaxMetadataBytes, budget), budget
	source, err := psdfile.Read(gradientEditReader{ctx: ctx, reader: bytes.NewReader(data)}, length,
		psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	remaining := budget - length - source.IndexMetadataBytes()
	if remaining <= 0 || len(source.AdditionalInfo) != 1 {
		return nil, editError(psdfile.ErrLimit, "layers.pattern", "semantic view exceeds the remaining budget")
	}
	view, err := source.DecodePatternFill(ctx, source.AdditionalInfo[0], psdfile.MetadataOptions{MaxBytes: remaining})
	if err != nil {
		return nil, err
	}
	return view.Fill, nil
}

func patternEditError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalidEdit
	if errors.Is(err, psdpattern.ErrUnsupported) || errors.Is(err, psdfile.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdpattern.ErrLimit) || errors.Is(err, psdfile.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.pattern", Detail: err.Error(), Cause: err}
}

type patternEditorSources struct {
	records []psdfile.PatternRecord
	tiles   map[string]*image.NRGBA
	loaded  bool
}

func (e *Editor) decodeCompositePatternLocked(
	ctx context.Context, layer *psdmodel.Layer, content *patternEditorSources, remaining *int64,
) error {
	if !content.loaded {
		metadataLimit := min(e.source.Limits().MaxMetadataBytes, *remaining/2)
		if metadataLimit <= 0 {
			return editError(psdfile.ErrLimit, "save.pattern", "no local pattern metadata budget remains")
		}
		view, err := e.source.ReadPatternView(ctx, psdfile.MetadataOptions{MaxBytes: metadataLimit})
		if err != nil {
			return patternEditError(err)
		}
		cost := view.MetadataBytes + int64(len(view.Records))*128 + 128
		if cost > *remaining {
			return editError(psdfile.ErrLimit, "save.pattern", "local pattern metadata exceeds the remaining budget")
		}
		*remaining -= cost
		content.records, content.loaded = view.Records, true
		content.tiles = make(map[string]*image.NRGBA)
	}
	record, err := patternEditRecord(content.records, layer.Pattern.ID)
	if err != nil {
		return err
	}
	tile := content.tiles[layer.Pattern.ID]
	if tile == nil {
		estimate, err := e.source.EstimatePatternTileRGB8(*record)
		if err != nil {
			return patternEditError(err)
		}
		if estimate.WorkingBytes+int64(unsafe.Sizeof(image.NRGBA{})) > *remaining {
			return editError(psdfile.ErrLimit, "save.pattern", "local pattern tile exceeds the remaining peak budget")
		}
		tile, err = e.source.DecodePatternTileRGB8(ctx, *record,
			psdfile.PatternDecodeOptions{MaxWorkingBytes: estimate.WorkingBytes})
		if err != nil {
			return patternEditError(err)
		}
		if int64(len(tile.Pix)) != estimate.RetainedBytes {
			return editError(psdfile.ErrMalformed, "save.pattern", "local pattern tile differs from its source estimate")
		}
		*remaining -= estimate.RetainedBytes + int64(unsafe.Sizeof(*tile))
		content.tiles[layer.Pattern.ID] = tile
	}
	if err := psdpattern.Validate(tile, *layer.Pattern); err != nil {
		return patternEditError(err)
	}
	layer.PatternTile = tile
	return nil
}
