package psdscene

import (
	"context"
	"errors"
	"image"
	"math"
	"slices"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type sceneSmartFilter struct {
	view        *psdfile.SmartFilterStack
	operation   psdfilter.Stack
	cost        int64
	opaqueFacts []bool
}

func (engine *renderer) prepareSmartFilters(
	ctx context.Context, placement psdfile.SmartObjectRecord, layer *psdmodel.Layer,
) (*sceneSmartFilter, error) {
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.filters", placement.Block.Data.Offset,
			"no typed filter budget remains")
	}
	view, err := psdfile.DecodeSmartFilters(ctx, placement, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return nil, err
	}
	if view == nil || !view.Enabled {
		return nil, nil
	}
	active := false
	for _, item := range view.Filters {
		if item.Operation.Enabled && item.Operation.Opacity != 0 && !item.ParametersKnown {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.filters.parameters", placement.Block.Data.Offset,
				"the active filter has unknown algorithm parameters")
		}
		active = active || item.Operation.Enabled && item.Operation.Opacity != 0
	}
	if !active {
		layer.Placed.Unsupported = slices.DeleteFunc(layer.Placed.Unsupported, func(reason string) bool {
			return reason == "暂不支持智能滤镜"
		})
		return nil, nil
	}
	if engine.options.SmartFilters == nil {
		return nil, sceneError(psdfile.ErrUnsupported, "smartObject.filters", placement.Block.Data.Offset,
			"live smart filters require an explicit rendering policy")
	}
	if !view.ValidAtPosition {
		return nil, sceneError(psdfile.ErrUnsupported, "smartObject.filters.position", placement.Block.Data.Offset,
			"the filter stack is not valid at its retained placement")
	}
	cost := view.MetadataBytes + int64(len(view.Filters))*int64(unsafe.Sizeof(psdfilter.Filter{})) +
		int64(unsafe.Sizeof(sceneSmartFilter{}))
	if err := engine.reserve(cost, "smartObject.filters", placement.Block.Data.Offset); err != nil {
		return nil, err
	}
	result := &sceneSmartFilter{view: view, cost: cost, operation: psdfilter.Stack{Enabled: view.Enabled,
		Filters: make([]psdfilter.Filter, len(view.Filters))}}
	for index, item := range view.Filters {
		result.operation.Filters[index] = item.Operation
	}
	// Remove only the legacy parser's filter blocker; all unrelated placement blockers remain active.
	layer.Placed.Unsupported = slices.DeleteFunc(layer.Placed.Unsupported, func(reason string) bool {
		return reason == "暂不支持智能滤镜"
	})
	return result, nil
}

func (engine *renderer) applySmartFilters(
	ctx context.Context, source *psdfile.Document, layer *psdmodel.Layer, filters *sceneSmartFilter,
	canvas image.Rectangle,
) error {
	if filters == nil {
		return nil
	}
	defer func() { engine.working -= filters.cost }()
	area := int64(canvas.Dx()) * int64(canvas.Dy())
	if err := engine.reserve(area*4, "smartObject.filterInput", 0); err != nil {
		return err
	}
	input := image.NewNRGBA(canvas)
	intersection := canvas.Intersect(layer.Bounds)
	for y := intersection.Min.Y; y < intersection.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		inputOffset := input.PixOffset(intersection.Min.X, y)
		pixelOffset := layer.Pixels.PixOffset(layer.Pixels.Rect.Min.X+intersection.Min.X-layer.Bounds.Min.X,
			layer.Pixels.Rect.Min.Y+y-layer.Bounds.Min.Y)
		length := intersection.Dx() * 4
		copy(input.Pix[inputOffset:inputOffset+length], layer.Pixels.Pix[pixelOffset:pixelOffset+length])
	}
	mask, maskBounds, err := engine.smartFilterMask(ctx, source, filters.view, canvas)
	if err != nil {
		return err
	}
	options := *engine.options.SmartFilters
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return sceneError(psdfile.ErrLimit, "smartObject.filterOutput", 0, "no filter output budget remains")
	}
	if options.MaxWorkingBytes == 0 || options.MaxWorkingBytes > available {
		options.MaxWorkingBytes = available
	}
	estimate, err := psdfilter.WorkingEstimate(input, filters.operation, options)
	if err != nil {
		return sceneFilterError(err)
	}
	if err := engine.reserve(estimate.WorkingBytes, "smartObject.filterOutput", 0); err != nil {
		return err
	}
	var filtered *image.NRGBA
	if filters.opaqueFacts == nil {
		filtered, err = psdfilter.Apply(ctx, input, filters.operation, options)
	} else {
		filtered, err = psdfilter.ApplyRegion(ctx, input, filters.operation, filters.opaqueFacts, options)
	}
	if err != nil {
		return sceneFilterError(err)
	}
	engine.working -= estimate.WorkingBytes - estimate.RetainedBytes
	if filters.view.MaskEnable {
		outside := float64(0)
		if filters.view.MaskExtendWithWhite {
			outside = 1
		}
		for y := canvas.Min.Y; y < canvas.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := canvas.Min.X; x < canvas.Max.X; x++ {
				weight := outside
				if image.Pt(x, y).In(maskBounds) {
					weight = float64(mask.Data[(y-maskBounds.Min.Y)*mask.RowBytes+x-maskBounds.Min.X]) / 255
				}
				position := filtered.PixOffset(x, y)
				blendSmartFilterMask(filtered.Pix[position:position+4], input.Pix[position:position+4], weight)
			}
		}
	}
	engine.working -= area * 4
	if mask != nil {
		engine.working -= int64(len(mask.Data))
	}
	engine.working -= int64(len(layer.Pixels.Pix))
	layer.Pixels, layer.Bounds = filtered, canvas
	return nil
}

func (engine *renderer) smartFilterMask(
	ctx context.Context, source *psdfile.Document, filters *psdfile.SmartFilterStack, canvas image.Rectangle,
) (*psdfile.Plane, image.Rectangle, error) {
	if !filters.MaskEnable {
		return nil, image.Rectangle{}, nil
	}
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, image.Rectangle{}, sceneError(psdfile.ErrLimit, "smartObject.filterMask", 0,
			"no mask indexing budget remains")
	}
	view, err := source.ReadFilterCacheView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	if err := engine.reserve(view.MetadataBytes, "smartObject.filterMask", 0); err != nil {
		return nil, image.Rectangle{}, err
	}
	defer func() { engine.working -= view.MetadataBytes }()
	var selected *psdfile.FilterCacheArray
	for _, record := range view.Records {
		if record.ID != filters.PlacementID {
			continue
		}
		if selected != nil || record.Mask == nil {
			return nil, image.Rectangle{}, sceneError(psdfile.ErrUnsupported, "smartObject.filterMask", 0,
				"filter mask is absent or its cache identifier is ambiguous")
		}
		selected = record.Mask
	}
	if selected == nil || selected.Depth != 8 {
		return nil, image.Rectangle{}, sceneError(psdfile.ErrUnsupported, "smartObject.filterMask", 0,
			"an enabled mask requires one retained RGB8 filter-mask array")
	}
	bounds := canvas.Intersect(selected.Bounds)
	if bounds.Empty() {
		return nil, image.Rectangle{}, nil
	}
	available = min(engine.options.MaxWorkingBytes-engine.working, source.Limits().MaxDecodeBytes)
	if available <= 0 {
		return nil, image.Rectangle{}, sceneError(psdfile.ErrLimit, "smartObject.filterMask", selected.Data.Offset,
			"no mask decoding budget remains")
	}
	plane, err := source.DecodeFilterCacheRegion(ctx, *selected, bounds.Sub(selected.Bounds.Min),
		psdfile.RegionOptions{MaxWorkingBytes: available})
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	if err := engine.reserve(int64(len(plane.Data)), "smartObject.filterMask", selected.Data.Offset); err != nil {
		return nil, image.Rectangle{}, err
	}
	return plane, bounds, nil
}

func blendSmartFilterMask(filtered, original []byte, weight float64) {
	if weight == 1 {
		return
	}
	if weight == 0 {
		copy(filtered, original)
		return
	}
	alpha := float64(filtered[3])*weight + float64(original[3])*(1-weight)
	for component := range 3 {
		premultiplied := float64(filtered[component])*float64(filtered[3])*weight +
			float64(original[component])*float64(original[3])*(1-weight)
		value := float64(0)
		if alpha != 0 {
			value = premultiplied / alpha
		}
		filtered[component] = uint8(math.Round(value))
	}
	filtered[3] = uint8(math.Round(alpha))
}

func sceneFilterError(cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	kind := psdfile.ErrMalformed
	if errors.Is(cause, psdfilter.ErrUnsupported) {
		kind = psdfile.ErrUnsupported
	} else if errors.Is(cause, psdfilter.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "scene", Path: "smartObject.filters",
		Detail: "live smart-filter execution failed", Cause: cause}
}
