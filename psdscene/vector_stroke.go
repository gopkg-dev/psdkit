package psdscene

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type vectorStrokeStyle struct {
	fill, stroke [3]float64
	width        float64
}

// prepareVectorStroke draws original solid fill and inside stroke metadata without reading saved RGB samples.
func (engine *rgbTileRenderer) prepareVectorStroke(
	ctx context.Context, record psdfile.LayerRecord, layer *psdmodel.Layer,
) (bool, error) {
	if !engine.settings.ApproximateVectorStroke {
		return false, nil
	}
	var fillBlock, strokeBlock *psdfile.AdditionalBlock
	for index := range record.AdditionalInfo {
		block := &record.AdditionalInfo[index]
		switch block.Key {
		case "vscg":
			if fillBlock != nil {
				return false, vectorStrokeUnsupported(block.Data.Offset, "duplicate vector fill")
			}
			fillBlock = block
		case "vstk":
			if strokeBlock != nil {
				return false, vectorStrokeUnsupported(block.Data.Offset, "duplicate vector stroke")
			}
			strokeBlock = block
		}
	}
	if fillBlock == nil && strokeBlock == nil {
		return false, nil
	}
	if fillBlock == nil || strokeBlock == nil || layer.Group || layer.Children != nil || layer.Placed != nil ||
		layer.FillColor != nil || layer.Gradient != nil || layer.Pattern != nil || layer.Adjustment != nil {
		return false, vectorStrokeUnsupported(record.Span.Offset, "one solid vector fill and stroke must form a leaf")
	}
	mask := layer.VectorMask
	if mask == nil || mask.Disabled || mask.Invert || mask.InitialFill || len(mask.Subpaths) != 1 ||
		!mask.Subpaths[0].Closed || mask.Subpaths[0].Operation != 1 || len(mask.Subpaths[0].Knots) < 3 ||
		len(mask.Unsupported) != 0 || layer.VectorDensity != nil && *layer.VectorDensity != 255 ||
		layer.VectorFeather != nil && *layer.VectorFeather != 0 {
		return false, vectorStrokeUnsupported(record.Span.Offset, "inside stroke requires one ordinary closed vector path")
	}
	available := engine.settings.MaxWorkingBytes - engine.working
	style, metadataBytes, err := decodeVectorStroke(ctx, engine.source, *fillBlock, *strokeBlock, available)
	if err != nil {
		return false, err
	}
	if err := engine.reserve(metadataBytes, "tile.vectorStroke.metadata", record.Span.Offset); err != nil {
		return false, err
	}
	defer func() { engine.working -= metadataBytes }()
	window, err := engine.effectSourceWindow(layer)
	if err != nil {
		return false, err
	}
	// Bounds follow the original path control hull, independent of the saved raster's extent.
	bounds, err := vectorStrokeBounds(mask)
	if err != nil {
		return false, err
	}
	bounds = bounds.Intersect(window)
	pixels, err := rasterVectorStroke(ctx, mask, bounds, style,
		engine.settings.MaxWorkingBytes-engine.working)
	if err != nil {
		return false, err
	}
	if err := engine.reserve(int64(len(pixels.Pix)), "tile.vectorStroke.pixels", record.Span.Offset); err != nil {
		return false, err
	}
	layer.Bounds, layer.Pixels = bounds, pixels
	engine.live[layer] = true
	return true, nil
}

func decodeVectorStroke(
	ctx context.Context, source *psdfile.Document, fillBlock, strokeBlock psdfile.AdditionalBlock, budget int64,
) (vectorStrokeStyle, int64, error) {
	var style vectorStrokeStyle
	length := fillBlock.Data.Length + strokeBlock.Data.Length
	if length < 0 || length > (math.MaxInt64-(4<<10))/64 {
		return style, 0, sceneError(psdfile.ErrLimit, "tile.vectorStroke.metadata", fillBlock.Data.Offset,
			"descriptor metadata size overflows")
	}
	metadataBytes := length*64 + 4<<10
	if budget <= metadataBytes {
		return style, 0, sceneError(psdfile.ErrLimit, "tile.vectorStroke.metadata", fillBlock.Data.Offset,
			"owned descriptors exceed remaining working budget")
	}
	if fillBlock.Data.Length < 8 {
		return style, 0, sceneError(psdfile.ErrMalformed, "tile.vectorStroke.fill", fillBlock.Data.Offset,
			"vector fill header is truncated")
	}
	var signature [4]byte
	if _, err := source.ReadAt(signature[:], fillBlock.Data.Offset); err != nil {
		return style, 0, err
	}
	if string(signature[:]) != "SoCo" {
		return style, 0, vectorStrokeUnsupported(fillBlock.Data.Offset, "only solid vector fill content is supported")
	}
	fillSpan := psdfile.Span{Offset: fillBlock.Data.Offset + 4, Length: fillBlock.Data.Length - 4}
	fill, err := source.DecodeDescriptorContext(ctx, fillSpan, true, psdfile.MetadataOptions{MaxBytes: metadataBytes / 2})
	if err != nil {
		return style, 0, err
	}
	stroke, err := source.DecodeDescriptorContext(ctx, strokeBlock.Data, true,
		psdfile.MetadataOptions{MaxBytes: metadataBytes / 2})
	if err != nil {
		return style, 0, err
	}
	if fill.Span != fillSpan || stroke.Span != strokeBlock.Data {
		return style, 0, vectorStrokeUnsupported(fillBlock.Data.Offset, "trailing vector descriptor fields")
	}
	style.fill, err = vectorStrokeColor(fill, "null", fillBlock.Data.Offset)
	if err != nil {
		return style, 0, err
	}
	style.stroke, style.width, err = vectorStrokeParameters(stroke, strokeBlock.Data.Offset)
	return style, metadataBytes, err
}

func vectorStrokeParameters(descriptor *psdfile.Descriptor, offset int64) ([3]float64, float64, error) {
	var color [3]float64
	if descriptor == nil || descriptor.ClassID != "strokeStyle" {
		return color, 0, vectorStrokeUnsupported(offset, "unknown stroke descriptor class")
	}
	seen := make(map[string]bool, len(descriptor.Entries))
	width := 0.0
	for _, entry := range descriptor.Entries {
		if seen[entry.Key] {
			return color, 0, vectorStrokeUnsupported(offset, "duplicate stroke parameter")
		}
		seen[entry.Key] = true
		valid := false
		switch entry.Key {
		case "strokeStyleVersion":
			valid = entry.Type == "long" && entry.Value == int32(2)
		case "strokeEnabled", "fillEnabled":
			valid = entry.Type == "bool" && entry.Value == true
		case "strokeStyleScaleLock", "strokeStyleStrokeAdjust":
			valid = entry.Type == "bool" && entry.Value == false
		case "strokeStyleMiterLimit":
			valid = entry.Type == "doub" && entry.Value == float64(100)
		case "strokeStyleResolution":
			valid = entry.Type == "doub" && entry.Value == float64(72)
		case "strokeStyleLineWidth":
			width, valid = vectorStrokeUnit(entry, "#Pxl")
			valid = valid && width > 0 && width <= 64
		case "strokeStyleOpacity":
			value, unitOK := vectorStrokeUnit(entry, "#Prc")
			valid = unitOK && value == 100
		case "strokeStyleLineDashOffset":
			value, unitOK := vectorStrokeUnit(entry, "#Pnt")
			valid = unitOK && value == 0
		case "strokeStyleLineDashSet":
			list, listOK := entry.Value.([]psdfile.DescriptorValue)
			valid = entry.Type == "VlLs" && listOK && len(list) == 0
		case "strokeStyleBlendMode":
			valid = vectorStrokeEnum(entry, "BlnM", "Nrml")
		case "strokeStyleLineAlignment":
			valid = vectorStrokeEnum(entry, "strokeStyleLineAlignment", "strokeStyleAlignInside")
		case "strokeStyleLineCapType":
			valid = vectorStrokeEnum(entry, "strokeStyleLineCapType", "strokeStyleButtCap")
		case "strokeStyleLineJoinType":
			valid = vectorStrokeEnum(entry, "strokeStyleLineJoinType", "strokeStyleMiterJoin")
		case "strokeStyleContent":
			object, objectOK := entry.Value.(*psdfile.Descriptor)
			if objectOK && entry.Type == "Objc" {
				var err error
				color, err = vectorStrokeColor(object, "solidColorLayer", offset)
				if err != nil {
					return color, 0, err
				}
				valid = true
			}
		}
		if !valid {
			return color, 0, vectorStrokeUnsupported(offset, "unsupported stroke parameter: "+entry.Key)
		}
	}
	if len(seen) != 16 {
		return color, 0, vectorStrokeUnsupported(offset, "stroke descriptor lacks required parameters")
	}
	return color, width, nil
}

func vectorStrokeColor(descriptor *psdfile.Descriptor, class string, offset int64) ([3]float64, error) {
	var color [3]float64
	if descriptor == nil || descriptor.ClassID != class || len(descriptor.Entries) != 1 ||
		descriptor.Entries[0].Key != "Clr " || descriptor.Entries[0].Type != "Objc" {
		return color, vectorStrokeUnsupported(offset, "solid fill descriptor is not an unambiguous RGB object")
	}
	rgb, valid := descriptor.Entries[0].Value.(*psdfile.Descriptor)
	if !valid || rgb == nil || rgb.ClassID != "RGBC" || len(rgb.Entries) != 3 {
		return color, vectorStrokeUnsupported(offset, "solid fill uses unsupported color components")
	}
	seen := [3]bool{}
	for _, entry := range rgb.Entries {
		component := -1
		switch entry.Key {
		case "Rd  ":
			component = 0
		case "Grn ":
			component = 1
		case "Bl  ":
			component = 2
		}
		value, numberOK := entry.Value.(float64)
		if component < 0 || seen[component] || entry.Type != "doub" || !numberOK ||
			math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 255 {
			return color, vectorStrokeUnsupported(offset, "solid fill has invalid or duplicate RGB components")
		}
		seen[component], color[component] = true, value
	}
	return color, nil
}

func vectorStrokeEnum(entry psdfile.DescriptorEntry, category, value string) bool {
	enumeration, valid := entry.Value.(psdfile.DescriptorEnum)
	return entry.Type == "enum" && valid && enumeration.TypeID == category && enumeration.ValueID == value
}

func vectorStrokeUnit(entry psdfile.DescriptorEntry, unit string) (float64, bool) {
	number, valid := entry.Value.(psdfile.DescriptorUnit)
	if !valid || entry.Type != "UntF" || number.Unit != unit {
		return 0, false
	}
	value, valid := number.Number.(float64)
	return value, valid && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func vectorStrokeUnsupported(offset int64, detail string) error {
	return sceneError(psdfile.ErrUnsupported, "tile.vectorStroke", offset, detail)
}

func vectorStrokeBounds(mask *psdvector.Mask) (image.Rectangle, error) {
	left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, path := range mask.Subpaths {
		for _, knot := range path.Knots {
			for _, point := range []psdvector.Point{knot.Preceding, knot.Anchor, knot.Leaving} {
				if math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) ||
					math.Abs(point.X) > 1<<30 || math.Abs(point.Y) > 1<<30 {
					return image.Rectangle{}, sceneError(psdfile.ErrMalformed, "tile.vectorStroke.bounds",
						mask.Span.Offset, "path contains an invalid document coordinate")
				}
				left, top = min(left, point.X), min(top, point.Y)
				right, bottom = max(right, point.X), max(bottom, point.Y)
			}
		}
	}
	return image.Rect(int(math.Floor(left)), int(math.Floor(top)),
		int(math.Ceil(right)), int(math.Ceil(bottom))), nil
}

// rasterVectorStroke uses fractional disk erosion as an explicit approximation of an inside solid stroke.
// The existing compositor applies the outer vector coverage once; owned RGB stores its conditional mixture.
func rasterVectorStroke(
	ctx context.Context, mask *psdvector.Mask, bounds image.Rectangle, style vectorStrokeStyle, budget int64,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mask == nil || bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y ||
		int64(bounds.Min.X) < -(1<<30) || int64(bounds.Min.Y) < -(1<<30) ||
		int64(bounds.Max.X) > 1<<30 || int64(bounds.Max.Y) > 1<<30 {
		return nil, sceneError(psdfile.ErrMalformed, "tile.vectorStroke.bounds", 0,
			"inside stroke requires a valid path and bounded document rectangle")
	}
	if bounds.Empty() {
		return image.NewNRGBA(bounds), nil
	}
	if !isFiniteStrokeWidth(style.width) {
		return nil, vectorStrokeUnsupported(mask.Span.Offset, "inside stroke width is outside its finite model")
	}
	margin := int(math.Ceil(style.width)) + 1
	sourceBounds := bounds.Inset(-margin)
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if budget <= 0 || width > budget/4/height {
		return nil, sceneError(psdfile.ErrLimit, "tile.vectorStroke.pixels", mask.Span.Offset,
			"owned inside stroke output exceeds remaining working budget")
	}
	outputBytes := width * height * 4
	sourceWidth, sourceHeight := int64(sourceBounds.Dx()), int64(sourceBounds.Dy())
	if sourceWidth > (budget-outputBytes)/sourceHeight {
		return nil, sceneError(psdfile.ErrLimit, "tile.vectorStroke.pixels", mask.Span.Offset,
			"output and stroke source coverage exceed remaining working budget")
	}
	// This finite work limit rejects an expensive request before scanning its neighborhoods.
	kernelWidth := int64(2*margin + 1)
	work := kernelWidth * kernelWidth
	if width > 100_000_000/work/height {
		return nil, sceneError(psdfile.ErrLimit, "tile.vectorStroke.work", mask.Span.Offset,
			"inside stroke coverage neighborhoods exceed the work limit")
	}
	alpha, err := psdvector.Rasterize(ctx, mask, sourceBounds,
		psdvector.Options{MaxWorkingBytes: budget - outputBytes})
	if err != nil {
		kind := psdfile.ErrMalformed
		if errors.Is(err, psdvector.ErrUnsupported) {
			kind = psdfile.ErrUnsupported
		} else if errors.Is(err, psdvector.ErrLimit) {
			kind = psdfile.ErrLimit
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, sceneError(kind, "tile.vectorStroke.path", mask.Span.Offset, err.Error())
	}
	result := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&63 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			outer := float64(alpha.AlphaAt(x, y).A)
			inner := outer
			for dy := -margin; dy <= margin && inner > 0; dy++ {
				for dx := -margin; dx <= margin; dx++ {
					weight := min(1.0, max(0.0, style.width+1-math.Hypot(float64(dx), float64(dy))))
					if weight == 0 {
						continue
					}
					candidate := 255 - (255-float64(alpha.AlphaAt(x+dx, y+dy).A))*weight
					inner = min(inner, candidate)
				}
			}
			fillWeight := 0.0
			if outer != 0 {
				fillWeight = inner / outer
			}
			offset := result.PixOffset(x, y)
			for component := range 3 {
				value := style.stroke[component] + (style.fill[component]-style.stroke[component])*fillWeight
				result.Pix[offset+component] = byte(math.Round(value))
			}
			result.Pix[offset+3] = 255
		}
	}
	return result, nil
}

func isFiniteStrokeWidth(width float64) bool {
	return !math.IsNaN(width) && !math.IsInf(width, 0) && width > 0 && width <= 64
}
