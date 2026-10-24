package psdcompose

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type vectorCoverage struct {
	path     string
	base     bool
	bounds   image.Rectangle
	pixels   *image.Alpha
	feathers map[float64]*vectorFeatherCoverage
}

type vectorFeatherCoverage struct {
	path             string
	sigma            float64
	radius           int
	canvas, expanded image.Rectangle
	values           []float64
}

func (plan *composePlan) registerVector(layer *psdmodel.Layer, path string) error {
	mask := layer.VectorMask
	if mask == nil || mask.Disabled || (layer.VectorDensity != nil && *layer.VectorDensity == 0) {
		return nil
	}
	if _, exists := plan.vectors[mask]; !exists {
		plan.vectors[mask] = &vectorCoverage{path: path, bounds: plan.window}
	}
	cache := plan.vectors[mask]
	requested := float64(0)
	if layer.VectorFeather != nil {
		requested = *layer.VectorFeather
	}
	if requested == 0 {
		cache.base = true
		if !plan.window.Empty() && plan.effects[layer] != nil && plan.dropShadowShapesWithVector(layer) {
			// Shadow shape can depend on vector coverage outside the requested output, including off-canvas artwork.
			cache.bounds = cache.bounds.Union(layer.Bounds)
		}
		return nil
	}
	if requested > plan.options.MaxFeatherRadius {
		return composeError(ErrLimit, path, "vector feather radius exceeds the configured limit")
	}
	shadow := plan.effects[layer] != nil && plan.effects[layer].approximateDropShadow &&
		calibratedShadowVectorFeather(layer)
	if requested != .5 && requested != 1.5 && requested != 3 || !vectorFeatherGeometry(mask) ||
		layer.Group || layer.Children != nil || layer.Mask != nil && !layer.Mask.Disabled && !shadow ||
		layer.Clipping || layer.Effects != nil && !calibratedVectorFeatherOverlay(layer) && !shadow ||
		layer.Placed != nil || layer.FillColor != nil || layer.Adjustment != nil || layer.Gradient != nil ||
		layer.Pattern != nil || layer.PatternTile != nil ||
		layer.BlendMode != "normal" && layer.BlendMode != "norm" && layer.BlendMode != "" ||
		layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		return composeError(ErrUnsupported, path,
			"vector feather is calibrated only at .5/1.5/3 for one integer rectangle on an ordinary normal raster layer")
	}
	if cache.feathers == nil {
		cache.feathers = make(map[float64]*vectorFeatherCoverage)
	}
	canvas := plan.window
	if !canvas.Empty() && shadow && plan.dropShadowShapesWithVector(layer) {
		// Feathered shadow shapes sample the complete raster source, including artwork outside the output or canvas.
		canvas = canvas.Union(layer.Bounds)
	}
	if existing := cache.feathers[requested]; existing != nil {
		canvas = canvas.Union(existing.canvas)
	}
	// One geometry-independent finite support is calibrated against the native rectangle controls.
	radius := int(math.Ceil(requested * 2.25))
	expanded, err := vectorFeatherExpanded(canvas, radius, path)
	if err != nil {
		return err
	}
	if cache.feathers[requested] == nil {
		cache.feathers[requested] = &vectorFeatherCoverage{path: path, sigma: featherSigma(requested),
			radius: radius}
	}
	cache.feathers[requested].canvas, cache.feathers[requested].expanded = canvas, expanded
	return nil
}

func vectorFeatherExpanded(canvas image.Rectangle, radius int, path string) (image.Rectangle, error) {
	if _, _, err := composeDimensions(canvas, path+".bounds"); err != nil {
		return image.Rectangle{}, err
	}
	integerLimit := int(^uint(0) >> 1)
	if canvas.Min.X < -integerLimit-1+radius || canvas.Min.Y < -integerLimit-1+radius ||
		canvas.Max.X > integerLimit-radius || canvas.Max.Y > integerLimit-radius {
		return image.Rectangle{}, composeError(ErrLimit, path, "expanded vector geometry overflows coordinates")
	}
	expanded := canvas.Inset(-radius)
	if _, _, err := composeDimensions(expanded, path+".expanded"); err != nil {
		return image.Rectangle{}, err
	}
	return expanded, nil
}

func (plan *composePlan) reserveVectorCaches() error {
	area := int64(plan.window.Dx()) * int64(plan.window.Dy())
	if area == 0 {
		return nil
	}
	for _, cache := range plan.vectors {
		if cache.base {
			width, height, err := composeDimensions(cache.bounds, cache.path+".bounds")
			if err != nil {
				return err
			}
			remaining := plan.options.MaxWorkingBytes - plan.workingBytes
			if height != 0 && int64(width) > remaining/int64(height) {
				return composeError(ErrLimit, cache.path, "vector coverage caches exceed working budget")
			}
			plan.workingBytes += int64(width) * int64(height)
		}
		for _, feather := range cache.feathers {
			width, height, err := composeDimensions(feather.canvas, feather.path+".bounds")
			if err != nil {
				return err
			}
			remaining := plan.options.MaxWorkingBytes - plan.workingBytes
			if remaining < 256 || height != 0 && int64(width) > (remaining-256)/8/int64(height) {
				return composeError(ErrLimit, feather.path, "vector feather caches exceed working budget")
			}
			plan.workingBytes += int64(width)*int64(height)*8 + 256
		}
	}
	return nil
}

func (plan *composePlan) prepareVectors(ctx context.Context) error {
	area := int64(plan.window.Dx()) * int64(plan.window.Dy())
	if area == 0 {
		return nil
	}
	for mask, cache := range plan.vectors {
		if cache.base {
			baseArea := int64(cache.bounds.Dx()) * int64(cache.bounds.Dy())
			pixels, err := psdvector.Rasterize(ctx, mask, cache.bounds,
				psdvector.Options{MaxWorkingBytes: plan.options.MaxWorkingBytes - plan.workingBytes + baseArea})
			if err != nil {
				return vectorComposeError(err, cache.path)
			}
			cache.pixels = pixels
		}
		for _, feather := range cache.feathers {
			if err := feather.prepare(ctx, mask, plan.options.MaxWorkingBytes-plan.workingBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func (plan *composePlan) vectorAlpha(layer *psdmodel.Layer, x, y int) float64 {
	mask := layer.VectorMask
	if mask == nil || mask.Disabled || (layer.VectorDensity != nil && *layer.VectorDensity == 0) {
		return 1
	}
	cache := plan.vectors[mask]
	if cache == nil {
		return 0
	}
	value := float64(0)
	if layer.VectorFeather != nil && *layer.VectorFeather != 0 {
		feather := cache.feathers[*layer.VectorFeather]
		if feather == nil || feather.values == nil || !image.Pt(x, y).In(feather.canvas) {
			return 0
		}
		value = feather.values[(y-feather.canvas.Min.Y)*feather.canvas.Dx()+x-feather.canvas.Min.X]
	} else {
		if cache.pixels == nil {
			return 0
		}
		value = float64(cache.pixels.AlphaAt(x, y).A) / 255
	}
	if layer.VectorDensity != nil {
		value = 1 - (1-value)*float64(*layer.VectorDensity)/255
	}
	return value
}

func vectorFeatherGeometry(mask *psdvector.Mask) bool {
	if mask.Version != 3 || mask.Flags&^uint32(7) != 0 || len(mask.Unsupported) != 0 {
		return false
	}
	if len(mask.Subpaths) == 0 {
		return mask.HasInitialFill
	}
	if len(mask.Subpaths) != 1 {
		return false
	}
	path := mask.Subpaths[0]
	if !path.Closed || path.Operation != 1 || len(path.Knots) != 4 || path.Unknown1 > 1 || path.Unknown2 != 0 {
		return false
	}
	for _, value := range path.Reserved {
		if value != 0 {
			return false
		}
	}
	for index, knot := range path.Knots {
		point := knot.Anchor
		if point.X != knot.Preceding.X || point.Y != knot.Preceding.Y ||
			point.X != knot.Leaving.X || point.Y != knot.Leaving.Y ||
			math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) ||
			math.Trunc(point.X) != point.X || math.Trunc(point.Y) != point.Y ||
			math.Abs(point.X) > 1<<40 || math.Abs(point.Y) > 1<<40 {
			return false
		}
		next := path.Knots[(index+1)%4].Anchor
		opposite := path.Knots[(index+2)%4].Anchor
		if (point.X == next.X) == (point.Y == next.Y) || point.X == opposite.X || point.Y == opposite.Y {
			return false
		}
	}
	return true
}

func (feather *vectorFeatherCoverage) scratchBytes(limit int64) (int64, error) {
	width, height := int64(feather.expanded.Dx()), int64(feather.expanded.Dy())
	if width <= 0 || height <= 0 || width > limit/5/height {
		return 0, composeError(ErrLimit, feather.path, "expanded vector geometry exceeds the working budget")
	}
	bytes := width * height * 5
	canvasWidth := int64(feather.canvas.Dx())
	kernel := (2*int64(feather.radius) + 1) * 8
	geometry := width + 24*128 + 64 + 4*64
	if kernel > limit-bytes || geometry > limit-bytes-kernel ||
		height > (limit-bytes-kernel-geometry)/8/canvasWidth {
		return 0, composeError(ErrLimit, feather.path, "vector feather convolution scratch exceeds the budget")
	}
	return bytes + canvasWidth*height*8 + kernel + geometry, nil
}

func (plan *composePlan) checkVectorFeatherBudget() error {
	if plan.window.Empty() {
		return nil
	}
	remaining := plan.options.MaxWorkingBytes - plan.workingBytes
	for _, cache := range plan.vectors {
		for _, feather := range cache.feathers {
			if _, err := feather.scratchBytes(remaining); err != nil {
				return err
			}
		}
	}
	return nil
}

func (feather *vectorFeatherCoverage) prepare(ctx context.Context, mask *psdvector.Mask, remaining int64) error {
	scratch, err := feather.scratchBytes(remaining)
	if err != nil {
		return err
	}
	area := int64(feather.expanded.Dx()) * int64(feather.expanded.Dy())
	geometry := int64(feather.expanded.Dx()) + 24*128 + 64 + 4*64
	// Future convolution storage is withheld while the independent geometry rasterizer allocates its own scratch.
	raw, err := psdvector.Rasterize(ctx, mask, feather.expanded,
		psdvector.Options{MaxWorkingBytes: remaining - scratch + area + geometry})
	if err != nil {
		return vectorComposeError(err, feather.path)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, feather.expanded.Dx(), feather.expanded.Dy()))
	for row := range feather.expanded.Dy() {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := range feather.expanded.Dx() {
			offset := row*pixels.Stride + column*4
			pixels.Pix[offset] = raw.Pix[row*raw.Stride+column]
		}
	}
	// The convolution's complete source domain is explicitly rasterized, including inverse/default outside coverage.
	borrowed := &psdmodel.Mask{Bounds: feather.expanded, Pixels: pixels}
	working := &featherMask{mask: borrowed, bounds: feather.canvas, canvas: feather.canvas,
		radius: feather.radius, sigma: feather.sigma, path: feather.path}
	if err := working.prepare(ctx); err != nil {
		return err
	}
	feather.values = working.values
	return nil
}

func vectorComposeError(err error, path string) error {
	kind := ErrInvalid
	if errors.Is(err, psdvector.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdvector.ErrLimit) {
		kind = ErrLimit
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &Error{Kind: kind, Path: path, Detail: err.Error()}
}
