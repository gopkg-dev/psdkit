package psdscene

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

// PrecisionOptions bounds source snapshots, compositor buffers and sequential decode work.
// Zero memory and pixel limits inherit the source budgets; nesting defaults to sixty-four levels.
type PrecisionOptions struct {
	MaxWorkingBytes int64
	MaxPixels       int64
	MaxDepth        int
	MaxLayers       int
	NonFinite       psdprecision.NonFinitePolicy
	Region          psdfile.RegionOptions
}

type precisionSourceRenderer struct {
	source                *psdfile.Document
	options               PrecisionOptions
	depth                 uint16
	encoding              psdprecision.Encoding
	working               int64
	nodes                 map[*psdprecision.Layer]int
	canvas                image.Rectangle
	nativeRGB16           bool
	nativeRGB16Normal     bool
	nativeRGB16Difference bool
	activeRGB16Bounds     image.Rectangle
	qualificationScratch  int64
}

// RenderPrecision rebuilds ordinary RGB/Gray16 or RGB/Gray32 layers into owned float64 straight RGBA.
// Eligible flat Normal RGB16 stacks use the 0..32768 integer domain with partial bounds, alpha and opacity.
// Their global active bounding rectangle starts with white RGB at alpha zero; outside it, RGBA zero is math policy.
// This outside policy does not claim native stored samples.
// One to four ordinary nonempty Normal/Difference rasters with first Normal also use the calibrated integers.
// These may have partial or negative-origin bounds; qualification and initialization precede ROI clipping.
// Other qualified opaque stacks retain their existing integer path.
// Remaining supported stacks use source floats.
// RGB32 uses linear values, and finite HDR colors are retained without clamping.
// Source ICC bytes are copied as provenance, and display conversion remains a separate operation.
func RenderPrecision(
	ctx context.Context, source *psdfile.Document, options ...PrecisionOptions,
) (*psdprecision.FloatImage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || len(options) > 1 {
		return nil, sceneError(psdfile.ErrMalformed, "precision.options", 0,
			"provide a source and at most one PrecisionOptions")
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	settings := PrecisionOptions{MaxWorkingBytes: source.Limits().MaxDecodeBytes,
		MaxPixels: source.Limits().MaxDecodePixels, MaxDepth: 64, MaxLayers: 10_000}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxWorkingBytes < 0 || provided.MaxPixels < 0 || provided.MaxDepth < 0 || provided.MaxLayers < 0 ||
			provided.NonFinite > psdprecision.NonFiniteClamp {
			return nil, sceneError(psdfile.ErrMalformed, "precision.options", 0, "invalid budget or non-finite policy")
		}
		if provided.MaxWorkingBytes != 0 {
			settings.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxPixels != 0 {
			settings.MaxPixels = provided.MaxPixels
		}
		if provided.MaxDepth != 0 {
			settings.MaxDepth = provided.MaxDepth
		}
		if provided.MaxLayers != 0 {
			settings.MaxLayers = provided.MaxLayers
		}
		settings.NonFinite, settings.Region = provided.NonFinite, provided.Region
	}
	if settings.MaxDepth > 64 || len(source.Layers) > settings.MaxLayers {
		return nil, sceneError(psdfile.ErrLimit, "precision.layers", 0, "source tree exceeds traversal limits")
	}
	if settings.Region.MaxWorkingBytes < 0 || settings.Region.MaxInflatedBytes < 0 ||
		settings.Region.MaxRLEWorkBytes < 0 || settings.Region.MaxWorkingBytes > source.Limits().MaxDecodeBytes {
		return nil, sceneError(psdfile.ErrLimit, "precision.regionOptions", 0, "invalid region work budget")
	}
	if source.Header.Depth != 16 && source.Header.Depth != 32 ||
		source.Header.ColorMode != 1 && source.Header.ColorMode != 3 {
		return nil, sceneError(psdfile.ErrUnsupported, "precision.header", 0, "high-precision RGB or grayscale required")
	}
	depth := source.Header.Depth
	if source.ActiveLayerSet >= 0 && source.ActiveLayerSet < len(source.LayerSets) {
		depth = source.LayerSets[source.ActiveLayerSet].Depth
	}
	if depth != source.Header.Depth {
		return nil, sceneError(psdfile.ErrUnsupported, "precision.layerSets", 0, "active layer depth differs from header")
	}
	width, height := uint64(source.Header.Width), uint64(source.Header.Height)
	if height != 0 && (width > uint64(settings.MaxPixels)/height ||
		width > uint64(settings.MaxWorkingBytes)/32/height || width > uint64(^uint(0)>>1)/4/height) {
		return nil, sceneError(psdfile.ErrLimit, "precision.canvas", 0, "canvas exceeds output budgets")
	}
	area := int64(width * height)
	engine := precisionSourceRenderer{source: source, options: settings, depth: depth,
		nodes:       make(map[*psdprecision.Layer]int),
		canvas:      image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height)),
		nativeRGB16: depth == 16 && source.Header.ColorMode == 3}
	if depth == 32 {
		engine.encoding = psdprecision.LinearRGB
	}
	if err := engine.reserve(int64(len(source.Layers))*512+512, "precision.metadata", 0); err != nil {
		return nil, err
	}
	layers, err := engine.buildTree(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := engine.prepare(ctx, layers, 0, true)
	if err != nil {
		return nil, err
	}
	if err := engine.qualifyRGB16Normal(ctx, layers); err != nil {
		return nil, err
	}
	engine.planRGB16Qualification()
	if area > (settings.MaxWorkingBytes-engine.working)/32/int64(groups+1) {
		return nil, sceneError(psdfile.ErrLimit, "precision.composite", 0, "output and group buffers exceed budget")
	}
	renderBytes := area * 32 * int64(groups+1)
	if err := engine.reserve(renderBytes, "precision.composite", 0); err != nil {
		return nil, err
	}
	profile, err := engine.profile(ctx)
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, sceneError(psdfile.ErrUnsupported, "precision.layers", 0,
			"a flat saved preview is not a source-layer reconstruction")
	}
	if err := engine.preflightSamples(layers, true); err != nil {
		return nil, err
	}
	if err := engine.qualifyRGB16(ctx, layers); err != nil {
		return nil, err
	}
	if err := engine.load(ctx, layers, true); err != nil {
		return nil, err
	}
	scene := &psdprecision.Scene{Width: engine.canvas.Dx(), Height: engine.canvas.Dy(),
		Encoding: engine.encoding, Layers: layers}
	composeOptions := psdprecision.Options{MaxWorkingBytes: renderBytes, MaxPixels: settings.MaxPixels,
		MaxDepth: settings.MaxDepth, MaxLayers: settings.MaxLayers, NonFinite: settings.NonFinite}
	result, err := engine.compose(ctx, scene, composeOptions)
	if err != nil {
		return nil, precisionSourceError(err)
	}
	result.SourceDepth, result.ICCProfile = depth, profile
	return result, nil
}

func (engine *precisionSourceRenderer) reserve(amount int64, path string, offset int64) error {
	if amount < 0 || amount > engine.options.MaxWorkingBytes-engine.working {
		return sceneError(psdfile.ErrLimit, path, offset, "high-precision snapshots and buffers exceed working budget")
	}
	engine.working += amount
	return nil
}

// qualifyRGB16 scans original bottom-layer alpha independently of the requested output window.
// The fixed 64 KiB scratch ceiling never allocates a full-canvas integer cache.
// Raw and RLE use bounded strips; ZIP decodes a complete alpha plane once only if it fits this ceiling.
// Qualification limits retain the existing float source path, while malformed data and cancellation remain errors.
func (engine *precisionSourceRenderer) qualifyRGB16(ctx context.Context, layers []*psdprecision.Layer) error {
	if engine.nativeRGB16Normal || engine.nativeRGB16Difference {
		return nil
	}
	if !engine.nativeRGB16 {
		return nil
	}
	canvas := image.Rect(0, 0, int(engine.source.Header.Width), int(engine.source.Header.Height))
	scratch := engine.qualificationScratch
	workRemaining := engine.source.Limits().MaxDecodeBytes
	inflatedRemaining := engine.source.Limits().MaxDecodeBytes
	if engine.options.Region.MaxInflatedBytes != 0 {
		inflatedRemaining = engine.options.Region.MaxInflatedBytes
	}
	rleRemaining := int64(256 << 20)
	if engine.options.Region.MaxRLEWorkBytes != 0 {
		rleRemaining = engine.options.Region.MaxRLEWorkBytes
	}
	bottom := true
	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Hidden || *layer.Opacity == 0 {
			continue
		}
		recordIndex := engine.nodes[layer]
		record := engine.source.Layers[recordIndex]
		bounds := record.Bounds.Intersect(canvas)
		if bounds.Empty() {
			continue
		}
		if bottom && (layer.BlendMode != "norm" || *layer.Opacity != 1 || !canvas.In(record.Bounds)) {
			engine.nativeRGB16 = false
			return nil
		}
		isBottom := bottom
		bottom = false
		switch layer.BlendMode {
		case "norm", "mul ", "scrn", "diff", "lddg", "dark", "lite":
		default:
			engine.nativeRGB16 = false
			return nil
		}
		if !isBottom {
			continue
		}
		alphaIndex := -1
		for channelIndex, channel := range record.Channels {
			if channel.ID != -1 {
				continue
			}
			if alphaIndex != -1 {
				return sceneError(psdfile.ErrMalformed, "precision.channels", channel.Data.Offset,
					"duplicate alpha channel")
			}
			alphaIndex = channelIndex
		}
		if alphaIndex == -1 {
			continue
		}
		width, height := int64(bounds.Dx()), int64(bounds.Dy())
		if height > math.MaxInt64/2/width || scratch <= 0 {
			engine.nativeRGB16 = false
			return nil
		}
		work := width * height * 2
		options := engine.options.Region
		options.MaxWorkingBytes = scratch
		channel := record.Channels[alphaIndex]
		rowHeight := int(scratch / (int64(bounds.Dx())*2 + 4) / 2)
		if rowHeight > bounds.Dy() {
			rowHeight = bounds.Dy()
		}
		if channel.Compression >= 2 {
			rowHeight = bounds.Dy()
		}
		if rowHeight <= 0 {
			engine.nativeRGB16 = false
			return nil
		}
		if channel.Compression == 1 {
			countWidth := int64(2)
			if engine.source.Header.Version == 2 {
				countWidth = 4
			}
			tableHeight := int64(channel.Bounds.Dy())
			if tableHeight < 0 || tableHeight > math.MaxInt64/countWidth {
				engine.nativeRGB16 = false
				return nil
			}
			tableBytes := tableHeight * countWidth
			if channel.Data.Length < tableBytes {
				return sceneError(psdfile.ErrMalformed, "precision.rgb16Alpha", channel.Data.Offset,
					"RLE alpha row-count table is truncated")
			}
			strips := (height-1)/int64(rowHeight) + 1
			if strips > 1 && tableBytes > (math.MaxInt64-channel.Data.Length)/(strips-1) {
				engine.nativeRGB16 = false
				return nil
			}
			// Every strip scans the complete table; nonoverlapping row payloads are read at most once.
			rleWork := tableBytes*(strips-1) + channel.Data.Length
			if rleWork > rleRemaining || rleWork > math.MaxInt64-work {
				engine.nativeRGB16 = false
				return nil
			}
			rleRemaining -= rleWork
			work += rleWork
		} else if channel.Compression >= 2 {
			channelWidth, channelHeight := int64(channel.Bounds.Dx()), int64(channel.Bounds.Dy())
			if channelWidth < 0 || channelHeight < 0 || channelWidth != 0 &&
				channelHeight > math.MaxInt64/2/channelWidth {
				engine.nativeRGB16 = false
				return nil
			}
			inflated := channelWidth * channelHeight * 2
			if inflated > inflatedRemaining || channel.Data.Length > math.MaxInt64-inflated {
				engine.nativeRGB16 = false
				return nil
			}
			inflatedRemaining -= inflated
			work = inflated + channel.Data.Length
		}
		if work < 0 || work > workRemaining {
			engine.nativeRGB16 = false
			return nil
		}
		workRemaining -= work
		for top := bounds.Min.Y; top < bounds.Max.Y; top += rowHeight {
			strip := image.Rect(bounds.Min.X, top, bounds.Max.X, min(bounds.Max.Y, top+rowHeight))
			plane, err := engine.source.DecodeChannelRegion(ctx, recordIndex, alphaIndex,
				strip.Sub(channel.Bounds.Min), options)
			localLimit := &psdfile.Error{}
			local := errors.As(err, &localLimit)
			if local && errors.Is(localLimit.Kind, psdfile.ErrLimit) && localLimit.Cause == nil {
				engine.nativeRGB16 = false
				return nil
			}
			if err != nil {
				return err
			}
			for offset := 0; offset < len(plane.Data); offset += 2 {
				if offset&8191 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				if binary.BigEndian.Uint16(plane.Data[offset:offset+2]) != 65535 {
					engine.nativeRGB16 = false
					return nil
				}
			}
		}
	}
	return nil
}

// planRGB16Qualification fixes scan scratch before window-dependent future buffers are reserved.
// Only already owned metadata and the source-wide ICC copy coexist with the transient scan allocation.
func (engine *precisionSourceRenderer) planRGB16Qualification() {
	if !engine.nativeRGB16 || engine.nativeRGB16Normal || engine.nativeRGB16Difference {
		return
	}
	var profileBytes int64
	for _, resource := range engine.source.Resources {
		if resource.ID == 1039 {
			profileBytes = resource.Data.Length
		}
	}
	scratch := min(int64(64<<10), engine.options.MaxWorkingBytes-engine.working-profileBytes)
	scratch = min(scratch, engine.source.Limits().MaxDecodeBytes)
	if engine.options.Region.MaxWorkingBytes != 0 {
		scratch = min(scratch, engine.options.Region.MaxWorkingBytes)
	}
	engine.qualificationScratch = max(0, scratch)
}

func (engine *precisionSourceRenderer) read(ctx context.Context, span psdfile.Span) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if span.Length < 0 || span.Length > engine.options.MaxWorkingBytes-engine.working ||
		span.Length > engine.source.Limits().MaxReadBytes || uint64(span.Length) > uint64(^uint(0)>>1) {
		return nil, sceneError(psdfile.ErrLimit, "precision.metadata", span.Offset, "metadata read exceeds working budget")
	}
	data := make([]byte, int(span.Length))
	count, err := engine.source.ReadAt(data, span.Offset)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if count != len(data) || err != nil && !errors.Is(err, io.EOF) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, &psdfile.Error{Kind: psdfile.ErrMalformed, Feature: "precision", Path: "precision.metadata",
			Offset: span.Offset, Detail: "original metadata could not be read", Cause: err}
	}
	return data, nil
}

func (engine *precisionSourceRenderer) buildTree(ctx context.Context) ([]*psdprecision.Layer, error) {
	var root []*psdprecision.Layer
	stack := []*[]*psdprecision.Layer{&root}
	for index, v := range slices.Backward(engine.source.Layers) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record := v
		if record.Divider == 3 {
			if len(stack) == 1 {
				return nil, sceneError(psdfile.ErrMalformed, "precision.groups", record.Span.Offset, "unbalanced group end")
			}
			slices.Reverse(*stack[len(stack)-1])
			stack = stack[:len(stack)-1]
			continue
		}
		if record.Divider > 3 {
			return nil, sceneError(psdfile.ErrUnsupported, "precision.groups", record.Span.Offset, "unknown group divider")
		}
		opacity := float64(record.Opacity) / 255
		layer := &psdprecision.Layer{Name: record.Name, Bounds: record.Bounds, BlendMode: record.BlendMode,
			Opacity: &opacity, Hidden: record.Flags&2 != 0, Clipping: record.Clipping != 0}
		parent := stack[len(stack)-1]
		*parent = append(*parent, layer)
		engine.nodes[layer] = index
		if record.Divider == 1 || record.Divider == 2 {
			layer.Children = make([]*psdprecision.Layer, 0)
			stack = append(stack, &layer.Children)
			if len(stack) > engine.options.MaxDepth {
				return nil, sceneError(psdfile.ErrLimit, "precision.groups", record.Span.Offset, "group nesting exceeds limit")
			}
		}
	}
	if len(stack) != 1 {
		return nil, sceneError(psdfile.ErrMalformed, "precision.groups", 0, "unbalanced group start")
	}
	slices.Reverse(root)
	return root, nil
}

func (engine *precisionSourceRenderer) prepare(
	ctx context.Context, layers []*psdprecision.Layer, groups int, ancestor bool,
) (int, error) {
	peak := groups
	for index, layer := range layers {
		record := engine.source.Layers[engine.nodes[layer]]
		visible := ancestor && !layer.Hidden && *layer.Opacity != 0
		if visible {
			if err := engine.semantics(ctx, record, layer); err != nil {
				return 0, err
			}
			visible = *layer.Opacity != 0
			if layer.Children != nil && (layer.Clipping ||
				index+1 < len(layers) && layers[index+1].Clipping ||
				layer.BlendMode == "pass" && len(layer.BlendRanges) != 0) {
				return 0, sceneError(psdfile.ErrUnsupported, "precision.clipping", record.Span.Offset,
					"group clipping or own pass-group Blend If requires high-depth calibration")
			}
		}
		nextGroups := groups
		if layer.Children != nil && visible && (layer.BlendMode != "pass" || *layer.Opacity != 1 ||
			layer.Mask != nil && !layer.Mask.Disabled) {
			nextGroups++
		}
		if layer.Children != nil {
			childPeak, err := engine.prepare(ctx, layer.Children, nextGroups, visible)
			if err != nil {
				return 0, err
			}
			peak = max(peak, childPeak)
		}
	}
	return peak, nil
}

func (engine *precisionSourceRenderer) semantics(
	ctx context.Context, record psdfile.LayerRecord, layer *psdprecision.Layer,
) error {
	fillOpacity := uint8(255)
	modernEffects := false
	for _, block := range record.AdditionalInfo {
		modernEffects = modernEffects || block.Key == "lfx2" || block.Key == "lmfx"
	}
	if record.Clipping > 1 || record.Flags&0xc0 != 0 {
		return sceneError(psdfile.ErrUnsupported, "precision.layers", record.Span.Offset,
			"unknown clipping or rendering flags")
	}
	if record.BlendRanges.Length > 4 {
		data, err := engine.read(ctx, psdfile.Span{Offset: record.BlendRanges.Offset + 4,
			Length: record.BlendRanges.Length - 4})
		if err != nil {
			return err
		}
		if len(data)%8 != 0 {
			return sceneError(psdfile.ErrMalformed, "precision.blendIf", record.BlendRanges.Offset, "invalid range length")
		}
		identity := psdprecision.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
		custom := false
		for position := 0; position < len(data); position += 4 {
			slider := psdprecision.BlendSlider{BlackLow: data[position], BlackHigh: data[position+1],
				WhiteLow: data[position+2], WhiteHigh: data[position+3]}
			if slider.BlackLow > slider.BlackHigh || slider.WhiteLow > slider.WhiteHigh {
				return sceneError(psdfile.ErrMalformed, "precision.blendIf", record.BlendRanges.Offset,
					"unordered split thresholds")
			}
			if position >= 32 && slider != identity {
				return sceneError(psdfile.ErrUnsupported, "precision.blendIf", record.BlendRanges.Offset,
					"non-RGB Blend If channel")
			}
			custom = custom || slider != identity
		}
		if custom {
			if err := engine.reserve(int64(len(data))*2, "precision.blendIf", record.BlendRanges.Offset); err != nil {
				return err
			}
			layer.BlendRanges = make([]psdprecision.BlendRange, len(data)/8)
			for index := range layer.BlendRanges {
				position := index * 8
				layer.BlendRanges[index] = psdprecision.BlendRange{
					Source: psdprecision.BlendSlider{BlackLow: data[position], BlackHigh: data[position+1],
						WhiteLow: data[position+2], WhiteHigh: data[position+3]},
					Destination: psdprecision.BlendSlider{BlackLow: data[position+4], BlackHigh: data[position+5],
						WhiteLow: data[position+6], WhiteHigh: data[position+7]},
				}
			}
		}
	}
	for _, block := range record.AdditionalInfo {
		switch block.Key {
		case "lsct", "lsdk":
			if block.Data.Length >= 12 {
				data, err := engine.read(ctx, psdfile.Span{Offset: block.Data.Offset + 4, Length: 8})
				if err != nil {
					return err
				}
				if string(data[:4]) != "8BIM" {
					return sceneError(psdfile.ErrMalformed, "precision.group", block.Data.Offset, "invalid group signature")
				}
				layer.BlendMode = string(data[4:8])
			}
		case "iOpa", "knko", "tsly", "clbl":
			data, err := engine.read(ctx, psdfile.Span{Offset: block.Data.Offset, Length: min(1, block.Data.Length)})
			if err != nil {
				return err
			}
			if len(data) != 1 {
				return sceneError(psdfile.ErrMalformed, "precision.layerOptions", block.Data.Offset, "empty layer option")
			}
			if block.Key == "iOpa" {
				fillOpacity = data[0]
			} else if block.Key == "clbl" {
				if data[0] > 1 {
					return sceneError(psdfile.ErrMalformed, "precision.layerOptions", block.Data.Offset,
						"invalid blend-clipped flag")
				}
				grouped := data[0] != 0
				layer.BlendClipped = &grouped
			} else if block.Key == "knko" && data[0] != 0 || block.Key == "tsly" && data[0] != 1 {
				return sceneError(psdfile.ErrUnsupported, "precision.layerOptions", block.Data.Offset,
					"knockout or disabled transparency shaping is unavailable")
			}
		case "lfx2", "lmfx":
			if block.Data.Length > (engine.options.MaxWorkingBytes-engine.working)/64 {
				return sceneError(psdfile.ErrLimit, "precision.effects", block.Data.Offset, "effect descriptor exceeds budget")
			}
			set, err := engine.source.DecodeEffects(ctx, block)
			if err != nil {
				return err
			}
			if set.Enabled && set.EffectsVisible {
				if len(set.Unsupported) != 0 {
					return sceneError(psdfile.ErrUnsupported, "precision.effects", block.Data.Offset, "unknown active effects")
				}
				for _, effect := range set.Items {
					if effect.Enabled && effect.Present && effect.Shown && effect.Opacity != 0 {
						return sceneError(psdfile.ErrUnsupported, "precision.effects", block.Data.Offset, "active layer effects")
					}
				}
			}
		case "lrFX":
			if !modernEffects {
				return sceneError(psdfile.ErrUnsupported, "precision.layers.semantic", block.Data.Offset,
					"legacy effects without an authoritative modern descriptor")
			}
		case "TySh", "tySh", "SoLd", "SoLE", "PlLd", "SoCo", "GdFl", "PtFl", "vmsk", "vsms", "vscg", "vstk",
			"lfxs", "brst", "FEid", "FXid", "PxSc", "PxSD", "curv", "levl", "brit", "blnc", "hue2",
			"hue ", "nvrt", "thrs", "post", "grdm", "selc", "mixr", "vibA", "expA", "blwh", "phfl", "CgEd":
			return sceneError(psdfile.ErrUnsupported, "precision.layers.semantic", block.Data.Offset,
				"active special layer semantics require a separate high-precision renderer")
		}
	}
	if record.Flags&0x20 != 0 && len(layer.BlendRanges) == 0 &&
		(layer.BlendClipped == nil || *layer.BlendClipped) {
		return sceneError(psdfile.ErrUnsupported, "precision.layers", record.Span.Offset,
			"unrecognized extended rendering flag without known blending options")
	}
	if layer.BlendMode == "lddg" && engine.encoding == psdprecision.EncodedRGB && fillOpacity != 255 {
		return sceneError(psdfile.ErrUnsupported, "precision.fillOpacity", record.Span.Offset,
			"RGB16 Linear Dodge special Fill semantics are not calibrated")
	}
	if layer.BlendMode == "diff" && fillOpacity != 255 {
		return sceneError(psdfile.ErrUnsupported, "precision.fillOpacity", record.Span.Offset,
			"Difference special Fill semantics are not calibrated")
	}
	*layer.Opacity *= float64(fillOpacity) / 255
	if record.Mask != nil {
		mask, err := engine.maskMetadata(record)
		if err != nil {
			return err
		}
		layer.Mask = mask
	}
	if layer.Children != nil || layer.Clipping || layer.Mask != nil && !layer.Mask.Disabled ||
		len(layer.BlendRanges) != 0 || fillOpacity != 255 {
		engine.nativeRGB16 = false
	}
	if !psdprecision.SupportsBlend(layer.BlendMode, engine.encoding) &&
		(layer.Children == nil || layer.BlendMode != "pass") {
		return sceneError(psdfile.ErrUnsupported, "precision.blendMode", record.Span.Offset,
			"blend mode is unavailable in the calibrated source color domain")
	}
	if engine.depth == 16 && layer.Children != nil && layer.BlendMode == "pass" &&
		(*layer.Opacity != 1 || layer.Mask != nil && !layer.Mask.Disabled) {
		return sceneError(psdfile.ErrUnsupported, "precision.passGroup", record.Span.Offset,
			"RGB16 partial or masked pass-through groups are not calibrated to native integer rounding")
	}
	if layer.Children != nil {
		switch layer.BlendMode {
		case "dark", "lite", "diff", "smud":
			return sceneError(psdfile.ErrUnsupported, "precision.groupBlend", record.Span.Offset,
				"this group blend mode is not yet calibrated against native high-depth groups")
		}
	}
	return nil
}

func (engine *precisionSourceRenderer) maskMetadata(record psdfile.LayerRecord) (*psdprecision.Mask, error) {
	metadata := record.Mask
	bounds, flags, outside := metadata.Bounds, metadata.Flags, metadata.DefaultColor
	if flags&8 != 0 {
		if !metadata.HasReal {
			return nil, nil
		}
		bounds, flags, outside = metadata.RealBounds, metadata.RealFlags, metadata.RealDefaultColor
	} else if metadata.HasReal && metadata.RealFlags&2 == 0 {
		return nil, sceneError(psdfile.ErrUnsupported, "precision.mask", metadata.Span.Offset, "two active pixel masks")
	}
	mask := &psdprecision.Mask{Bounds: bounds.Intersect(engine.canvas), Default: float64(outside) / 255,
		Invert: flags&4 != 0, Disabled: flags&2 != 0}
	if metadata.UserDensity != nil {
		density := float64(*metadata.UserDensity) / 255
		mask.Density = &density
		mask.Disabled = mask.Disabled || density == 0
	}
	if mask.Disabled {
		return mask, nil
	}
	if flags&0xe0 != 0 || metadata.ParameterFlags&0xf0 != 0 ||
		metadata.UserFeather != nil && *metadata.UserFeather != 0 {
		return nil, sceneError(psdfile.ErrUnsupported, "precision.mask", metadata.Span.Offset,
			"high-precision feather or unknown mask options are unavailable")
	}
	return mask, nil
}

func (engine *precisionSourceRenderer) profile(ctx context.Context) ([]byte, error) {
	var span psdfile.Span
	for _, resource := range engine.source.Resources {
		if resource.ID == 1039 {
			span = resource.Data
		}
	}
	profile, err := engine.read(ctx, span)
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(int64(len(profile)), "precision.profile", span.Offset); err != nil {
		return nil, err
	}
	return profile, nil
}

func (engine *precisionSourceRenderer) load(ctx context.Context, layers []*psdprecision.Layer, ancestor bool) error {
	for _, layer := range layers {
		visible := ancestor && !layer.Hidden && *layer.Opacity != 0
		if !visible {
			continue
		}
		record := engine.source.Layers[engine.nodes[layer]]
		if layer.Children != nil {
			if err := engine.load(ctx, layer.Children, visible); err != nil {
				return err
			}
		} else {
			bounds := record.Bounds.Intersect(engine.canvas)
			area := int64(bounds.Dx()) * int64(bounds.Dy())
			if area != 0 {
				layer.Bounds = bounds
				layer.Pixels = &psdprecision.FloatImage{Width: bounds.Dx(), Height: bounds.Dy(), Encoding: engine.encoding,
					SourceDepth: engine.depth, Pix: make([]float64, int(area)*4)}
				if err := engine.loadPixels(ctx, engine.nodes[layer], record, layer.Pixels, bounds); err != nil {
					return err
				}
			}
		}
		if layer.Mask != nil && !layer.Mask.Disabled {
			if err := engine.loadMask(ctx, engine.nodes[layer], record, layer.Mask); err != nil {
				return err
			}
		}
	}
	return nil
}

func (engine *precisionSourceRenderer) preflightSamples(layers []*psdprecision.Layer, ancestor bool) error {
	for _, layer := range layers {
		visible := ancestor && !layer.Hidden && *layer.Opacity != 0
		if !visible {
			continue
		}
		record := engine.source.Layers[engine.nodes[layer]]
		if layer.Children != nil {
			if err := engine.preflightSamples(layer.Children, visible); err != nil {
				return err
			}
		} else {
			bounds := record.Bounds.Intersect(engine.canvas)
			area := int64(bounds.Dx()) * int64(bounds.Dy())
			if err := engine.reserve(area*32, "precision.layers.pixels", record.Span.Offset); err != nil {
				return err
			}
		}
		if layer.Mask != nil && !layer.Mask.Disabled {
			area := int64(layer.Mask.Bounds.Dx()) * int64(layer.Mask.Bounds.Dy())
			if err := engine.reserve(area*8, "precision.mask.samples", record.Mask.Span.Offset); err != nil {
				return err
			}
		}
	}
	return nil
}

func (engine *precisionSourceRenderer) regionOptions() (psdfile.RegionOptions, error) {
	remaining := min(engine.options.MaxWorkingBytes-engine.working, engine.source.Limits().MaxDecodeBytes)
	if remaining <= 0 {
		return psdfile.RegionOptions{}, sceneError(psdfile.ErrLimit, "precision.samples", 0,
			"no sample decode budget remains")
	}
	options := engine.options.Region
	if options.MaxWorkingBytes == 0 || options.MaxWorkingBytes > remaining {
		options.MaxWorkingBytes = remaining
	}
	return options, nil
}

func (engine *precisionSourceRenderer) channel(
	ctx context.Context, layerIndex, channelIndex int, rectangle image.Rectangle,
) (*psdfile.Plane, error) {
	options, err := engine.regionOptions()
	if err != nil {
		return nil, err
	}
	return engine.source.DecodeChannelRegion(ctx, layerIndex, channelIndex, rectangle, options)
}

func (engine *precisionSourceRenderer) loadPixels(
	ctx context.Context, index int, record psdfile.LayerRecord, pixels *psdprecision.FloatImage, bounds image.Rectangle,
) error {
	components := 3
	if engine.source.Header.ColorMode == 1 {
		components = 1
	}
	indices := [4]int{-1, -1, -1, -1}
	for channelIndex, channel := range record.Channels {
		if channel.ID != -1 && (channel.ID < 0 || int(channel.ID) >= components) {
			continue
		}
		component := int(channel.ID)
		if channel.ID == -1 {
			component = 3
		}
		if indices[component] != -1 {
			return sceneError(psdfile.ErrMalformed, "precision.channels", channel.Data.Offset, "duplicate color/alpha channel")
		}
		indices[component] = channelIndex
	}
	for component := 0; component < components; component++ {
		if indices[component] < 0 {
			return sceneError(psdfile.ErrMalformed, "precision.channels", record.Span.Offset,
				"required color channel is missing")
		}
	}
	for pixel := 0; pixel < pixels.Width*pixels.Height; pixel++ {
		pixels.Pix[pixel*4+3] = 1
	}
	for component, channelIndex := range indices {
		if channelIndex < 0 {
			continue
		}
		plane, err := engine.channel(ctx, index, channelIndex, bounds.Sub(record.Bounds.Min))
		if err != nil {
			return err
		}
		for pixel := 0; pixel < pixels.Width*pixels.Height; pixel++ {
			if pixel&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			value, err := engine.sample(plane, pixel, component == 3)
			if err != nil {
				return err
			}
			pixels.Pix[pixel*4+component] = value
			if components == 1 && component == 0 {
				pixels.Pix[pixel*4+1], pixels.Pix[pixel*4+2] = value, value
			}
		}
	}
	return nil
}

func (engine *precisionSourceRenderer) compose(
	ctx context.Context, scene *psdprecision.Scene, options psdprecision.Options,
) (*psdprecision.FloatImage, error) {
	if engine.nativeRGB16Normal || engine.nativeRGB16Difference {
		return engine.composeRGB16Normal(ctx, scene, engine.canvas)
	}
	if engine.nativeRGB16 {
		result, err := psdprecision.RenderRGB16OverOpaque(ctx, scene, options)
		if !errors.Is(err, psdprecision.ErrUnsupported) {
			return result, err
		}
	}
	return psdprecision.Render(ctx, scene, options)
}

func (engine *precisionSourceRenderer) composeRegion(
	ctx context.Context, scene *psdprecision.Scene, rectangle image.Rectangle, options psdprecision.Options,
) (*psdprecision.RegionImage, error) {
	if engine.nativeRGB16Normal || engine.nativeRGB16Difference {
		result, err := engine.composeRGB16Normal(ctx, scene, rectangle)
		if err != nil {
			return nil, err
		}
		return &psdprecision.RegionImage{Bounds: rectangle, Image: result}, nil
	}
	if engine.nativeRGB16 {
		result, err := psdprecision.RenderRGB16OverOpaqueRegion(ctx, scene, rectangle, options)
		if !errors.Is(err, psdprecision.ErrUnsupported) {
			return result, err
		}
	}
	return psdprecision.RenderRegion(ctx, scene, rectangle, options)
}

func (engine *precisionSourceRenderer) loadMask(
	ctx context.Context, index int, record psdfile.LayerRecord, mask *psdprecision.Mask,
) error {
	id := int16(-2)
	bounds := record.Mask.Bounds
	if record.Mask.Flags&8 != 0 && record.Mask.HasReal {
		id, bounds = -3, record.Mask.RealBounds
	}
	area := int64(mask.Bounds.Dx()) * int64(mask.Bounds.Dy())
	mask.Values = make([]float64, int(area))
	if area == 0 {
		return nil
	}
	for channelIndex, channel := range record.Channels {
		if channel.ID != id {
			continue
		}
		plane, err := engine.channel(ctx, index, channelIndex, mask.Bounds.Sub(bounds.Min))
		if err != nil {
			return err
		}
		for pixel := range mask.Values {
			if pixel&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			value, err := engine.sample(plane, pixel, true)
			if err != nil {
				return err
			}
			mask.Values[pixel] = value
		}
		return nil
	}
	return sceneError(psdfile.ErrMalformed, "precision.mask.samples", record.Mask.Span.Offset,
		"pixel mask channel missing")
}

func (engine *precisionSourceRenderer) sample(plane *psdfile.Plane, pixel int, coverage bool) (float64, error) {
	var value float64
	if plane.Depth == 16 {
		value = float64(binary.BigEndian.Uint16(plane.Data[pixel*2:pixel*2+2])) / 65535
	} else {
		value = float64(math.Float32frombits(binary.BigEndian.Uint32(plane.Data[pixel*4 : pixel*4+4])))
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		switch engine.options.NonFinite {
		case psdprecision.NonFiniteReject:
			return 0, precisionSourceError(psdprecision.ErrNonFinite)
		case psdprecision.NonFiniteZero:
			value = 0
		case psdprecision.NonFiniteClamp:
			if math.IsInf(value, 1) {
				value = 1
			} else {
				value = 0
			}
		}
	}
	if coverage && (value < 0 || value > 1) {
		return 0, precisionSourceError(psdprecision.ErrInvalid)
	}
	return value, nil
}

func precisionSourceError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := psdfile.ErrMalformed
	if errors.Is(err, psdprecision.ErrUnsupported) {
		kind = psdfile.ErrUnsupported
	} else if errors.Is(err, psdprecision.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "precision", Path: "precision.render", Detail: err.Error(), Cause: err}
}
