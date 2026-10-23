package psdartboard

import (
	"context"
	"image"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// HasSource reports original artboard markers without decoding payloads or reading samples.
func HasSource(source *psdfile.Document) bool {
	if source == nil {
		return false
	}
	for _, block := range source.AdditionalInfo {
		if block.Key == "artb" || block.Key == "artd" || block.Key == "abdd" {
			return true
		}
	}
	for _, record := range source.Layers {
		for _, block := range record.AdditionalInfo {
			if block.Key == "artb" || block.Key == "artd" || block.Key == "abdd" {
				return true
			}
		}
	}
	return false
}

// Prepare builds an owned artboard container, rectangular mask and optional background for a rendering window.
// ValidateMetadata and top-level source identity must be checked before calling this function.
// The layer, its children and original mask/effect metadata are never modified; descendant pointers remain borrowed.
// Returned slices, generated coverage and the synthetic background are owned independently of the input layer.
// Positive budget bounds every additional retained allocation; the returned cost must remain charged during rendering.
// Hidden artboards retain visibility and children, but do not allocate coverage or add a background.
// Failure returns no layer or retained cost, including cancellation after allocations have begun.
func Prepare(
	ctx context.Context, layer *psdmodel.Layer, board *psdfile.ArtboardData, window image.Rectangle, budget int64,
) (*psdmodel.Layer, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if board == nil {
		return nil, 0, renderError(psdfile.ErrMalformed, "artboard.metadata", 0, "artboard metadata is required")
	}
	if len(board.Unsupported) != 0 {
		return nil, 0, renderError(psdfile.ErrUnsupported, "artboard.metadata", board.Block.Data.Offset,
			"unrecognized or ambiguous artboard metadata")
	}
	if err := validateRectangle(board, psdmodel.MaxDimension, psdmodel.MaxDimension); err != nil {
		return nil, 0, err
	}
	if err := ValidateLayer(layer, board.Block.Span.Offset); err != nil {
		return nil, 0, err
	}
	if window.Max.X < window.Min.X || window.Max.Y < window.Min.Y {
		return nil, 0, renderError(psdfile.ErrMalformed, "artboard.window", board.Block.Data.Offset,
			"rendering window must have nonnegative dimensions")
	}
	if budget <= 0 {
		return nil, 0, renderError(psdfile.ErrLimit, "artboard.snapshot", board.Block.Data.Offset,
			"no owned artboard snapshot budget remains")
	}
	bounds := image.Rect(int(board.Rect.Left), int(board.Rect.Top), int(board.Rect.Right), int(board.Rect.Bottom))
	samples := bounds.Intersect(window)
	background := !layer.Hidden && *board.BackgroundType != 3
	childCount := len(layer.Children)
	if background {
		if childCount == int(^uint(0)>>1) {
			return nil, 0, renderError(psdfile.ErrLimit, "artboard.background", board.Block.Data.Offset,
				"artboard child count overflows")
		}
		childCount++
	}
	cost := int64(unsafe.Sizeof(psdmodel.Layer{}))
	charge := func(count, size int64) bool {
		if cost > budget || count < 0 || size <= 0 || count > (budget-cost)/size {
			return false
		}
		cost += count * size
		return true
	}
	if !charge(int64(len(layer.Unsupported)), int64(unsafe.Sizeof(""))) ||
		!charge(int64(childCount), int64(unsafe.Sizeof((*psdmodel.Layer)(nil)))) {
		return nil, 0, renderError(psdfile.ErrLimit, "artboard.snapshot", board.Block.Data.Offset,
			"owned group metadata exceeds the remaining budget")
	}
	if !layer.Hidden {
		overhead := int64(unsafe.Sizeof(psdmodel.Mask{}) + unsafe.Sizeof(image.NRGBA{}) +
			unsafe.Sizeof(image.Rectangle{}) + unsafe.Sizeof(uint8(0)))
		area := int64(samples.Dx()) * int64(samples.Dy())
		if area > int64(^uint(0)>>1)/4 || !charge(1, overhead) || !charge(area, 4) {
			return nil, 0, renderError(psdfile.ErrLimit, "artboard.clip", board.Block.Data.Offset,
				"owned artboard clipping coverage exceeds the remaining budget")
		}
	}
	if background && !charge(1, int64(unsafe.Sizeof(psdmodel.Layer{})+unsafe.Sizeof([3]float64{}))) {
		return nil, 0, renderError(psdfile.ErrLimit, "artboard.background", board.Block.Data.Offset,
			"owned artboard background exceeds the remaining budget")
	}
	owned := *layer
	if layer.Unsupported != nil {
		owned.Unsupported = make([]string, 0, len(layer.Unsupported))
		for _, issue := range layer.Unsupported {
			if issue != Issue {
				owned.Unsupported = append(owned.Unsupported, issue)
			}
		}
	}
	if layer.Children != nil || background {
		owned.Children = make([]*psdmodel.Layer, childCount)
		start := 0
		if background {
			start = 1
			rgb := BackgroundColor(board)
			fill := [3]float64{float64(rgb[0]) / 255, float64(rgb[1]) / 255, float64(rgb[2]) / 255}
			owned.Children[0] = &psdmodel.Layer{Name: "Artboard background", Bounds: bounds,
				BlendMode: "normal", FillColor: &fill}
		}
		copy(owned.Children[start:], layer.Children)
	}
	if !layer.Hidden {
		coverage := image.NewNRGBA(samples)
		for y := samples.Min.Y; y < samples.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			for x := samples.Min.X; x < samples.Max.X; x++ {
				coverage.Pix[coverage.PixOffset(x, y)] = 255
			}
		}
		owned.Mask = &psdmodel.Mask{Bounds: bounds, SampleBounds: &samples,
			Pixels: coverage, DefaultColor: new(uint8(0))}
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return &owned, cost, nil
}
