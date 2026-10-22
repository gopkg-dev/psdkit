package psdcompose

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type featherMask struct {
	mask   *psdmodel.Mask
	bounds image.Rectangle
	canvas image.Rectangle
	radius int
	sigma  float64
	path   string
	values []float64
	// unbounded uses mask default-color extension instead of document-edge clamping for shadow source sampling.
	unbounded bool
}

func (plan *composePlan) registerFeather(mask *psdmodel.Mask, path string) error {
	if mask == nil || mask.Disabled || mask.Feather == nil || *mask.Feather == 0 || mask.Pixels == nil {
		return nil
	}
	if mask.Density != nil && *mask.Density == 0 {
		return nil
	}
	if _, exists := plan.feathers[mask]; exists {
		return nil
	}
	requested := *mask.Feather
	sigma := featherSigma(requested)
	kernelBudget := min(plan.options.MaxWorkingBytes, int64(^uint(0)>>1)) / 64
	if requested > plan.options.MaxFeatherRadius || sigma > float64(kernelBudget) {
		return composeError(ErrLimit, path, "mask feather radius exceeds the configured limit")
	}
	radius := int(math.Ceil(sigma * 3))
	canvas := image.Rect(0, 0, plan.document.Width, plan.document.Height)
	bounds := featherBounds(mask.Bounds, canvas, radius).Intersect(plan.window)
	if bounds.Empty() {
		return nil
	}
	plan.feathers[mask] = &featherMask{
		mask: mask, bounds: bounds, canvas: canvas, radius: radius, sigma: sigma, path: path,
	}
	return nil
}

// featherSigma calibrates the pixel-area Gaussian approximation against native mask captures.
// Photoshop's private kernel is not replicated. The continuous subpixel correction
// and broad-radius scale describe one geometry-independent approximation, not an exact noise or filter field.
func featherSigma(radius float64) float64 {
	if radius < .25 {
		return radius
	}
	if radius < .5 {
		return radius + .26*(radius-.25)
	}
	if radius < .75 {
		return radius*1.02 + .055*(.75-radius)/.25
	}
	return radius * 1.02
}

func featherBounds(bounds, canvas image.Rectangle, radius int) image.Rectangle {
	if bounds.Empty() ||
		(bounds.Min.X > canvas.Max.X && uint64(bounds.Min.X)-uint64(canvas.Max.X) > uint64(radius)) ||
		(bounds.Min.Y > canvas.Max.Y && uint64(bounds.Min.Y)-uint64(canvas.Max.Y) > uint64(radius)) ||
		(bounds.Max.X < canvas.Min.X && uint64(canvas.Min.X)-uint64(bounds.Max.X) > uint64(radius)) ||
		(bounds.Max.Y < canvas.Min.Y && uint64(canvas.Min.Y)-uint64(bounds.Max.Y) > uint64(radius)) {
		return image.Rectangle{}
	}
	lowerX, lowerY := max(0, min(canvas.Max.X, bounds.Min.X)), max(0, min(canvas.Max.Y, bounds.Min.Y))
	upperX, upperY := max(0, min(canvas.Max.X, bounds.Max.X)), max(0, min(canvas.Max.Y, bounds.Max.Y))
	return image.Rect(
		max(0, lowerX-radius), max(0, lowerY-radius),
		upperX+min(radius, canvas.Max.X-upperX), upperY+min(radius, canvas.Max.Y-upperY),
	)
}

// featherSupportBounds intersects the expanded input geometry with a sampling target without coordinate overflow.
func featherSupportBounds(bounds, target image.Rectangle, radius int) image.Rectangle {
	if bounds.Empty() || target.Empty() {
		return image.Rectangle{}
	}
	lower := func(value, limit int) int {
		if value <= limit {
			return limit
		}
		return value - int(min(uint64(radius), uint64(value)-uint64(limit)))
	}
	upper := func(value, limit int) int {
		if value >= limit {
			return limit
		}
		return value + int(min(uint64(radius), uint64(limit)-uint64(value)))
	}
	left, top := lower(bounds.Min.X, target.Min.X), lower(bounds.Min.Y, target.Min.Y)
	right, bottom := upper(bounds.Max.X, target.Max.X), upper(bounds.Max.Y, target.Max.Y)
	if left >= right || top >= bottom {
		return image.Rectangle{}
	}
	return image.Rect(left, top, right, bottom)
}

func (plan *composePlan) reserveFeatherCaches() error {
	if err := plan.registerShadowFeathers(); err != nil {
		return err
	}
	for _, caches := range []map[*psdmodel.Mask]*featherMask{plan.feathers, plan.shadowFeathers} {
		for _, feather := range caches {
			width, height := int64(feather.bounds.Dx()), int64(feather.bounds.Dy())
			remaining := plan.options.MaxWorkingBytes - plan.workingBytes
			if height > remaining/8/width {
				return composeError(ErrLimit, feather.path, "feather caches exceed the working budget")
			}
			plan.workingBytes += width * height * 8
		}
	}
	return nil
}

// registerShadowFeathers collects complete source dependencies before any cache storage is reserved.
// Source-shaping masks use their default-color extension without changing ordinary or final-stage mask fields.
func (plan *composePlan) registerShadowFeathers() error {
	for layer, cache := range plan.effects {
		mask := layer.Mask
		if !cache.approximateDropShadow || !plan.dropShadowShapesWithMask(layer) || mask.Pixels == nil ||
			mask.Feather == nil || *mask.Feather == 0 || mask.Density != nil && *mask.Density == 0 {
			continue
		}
		sigma := featherSigma(*mask.Feather)
		kernelBudget := min(plan.options.MaxWorkingBytes, int64(^uint(0)>>1)) / 64
		if *mask.Feather > plan.options.MaxFeatherRadius || sigma > float64(kernelBudget) {
			return composeError(ErrLimit, cache.path, "source-mask feather exceeds the configured limit")
		}
		radius := int(math.Ceil(sigma * 3))
		bounds := featherSupportBounds(mask.Bounds, layer.Bounds, radius)
		if bounds.Empty() {
			continue
		}
		if existing := plan.shadowFeathers[mask]; existing != nil {
			existing.bounds = existing.bounds.Union(bounds)
			continue
		}
		if plan.shadowFeathers == nil {
			plan.shadowFeathers = make(map[*psdmodel.Mask]*featherMask)
		}
		plan.shadowFeathers[mask] = &featherMask{mask: mask, bounds: bounds, radius: radius, sigma: sigma,
			path: cache.path + ".source-mask.feather", unbounded: true}
	}
	return nil
}

func (plan *composePlan) checkFeatherBudget() error {
	working := plan.workingBytes
	allocationLimit := min(plan.options.MaxWorkingBytes, int64(^uint(0)>>1))
	var largestScratch int64
	for _, caches := range []map[*psdmodel.Mask]*featherMask{plan.feathers, plan.shadowFeathers} {
		for _, feather := range caches {
			width, height := int64(feather.bounds.Dx()), int64(feather.bounds.Dy())
			if height > allocationLimit/8/width {
				return composeError(ErrLimit, feather.path, "feather caches exceed the working budget")
			}
			_, rowCount := feather.horizontalRows()
			rows := int64(rowCount)
			kernel := (2*int64(feather.radius) + 1) * 8
			if kernel > allocationLimit || rows > (allocationLimit-kernel)/8/width {
				return composeError(ErrLimit, feather.path, "feather convolution scratch exceeds the working budget")
			}
			largestScratch = max(largestScratch, width*rows*8+kernel)
		}
	}
	if largestScratch > plan.options.MaxWorkingBytes-working {
		return composeError(ErrLimit, "document.masks.feather", "feather caches and convolution scratch exceed the budget")
	}
	return plan.checkVectorFeatherBudget()
}

func (plan *composePlan) prepareFeathers(ctx context.Context) error {
	for _, caches := range []map[*psdmodel.Mask]*featherMask{plan.feathers, plan.shadowFeathers} {
		for _, feather := range caches {
			if err := feather.prepare(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (feather *featherMask) prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	width, height := feather.bounds.Dx(), feather.bounds.Dy()
	kernel := featherKernel(feather.sigma, feather.radius)
	rowStart, rows := feather.horizontalRows()
	horizontal := make([]float64, width*rows)
	for row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		y := rowStart + row
		for column := range width {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			x := feather.bounds.Min.X + column
			value := float64(0)
			for index, weight := range kernel {
				value += weight * featherRaw(feather.mask, feather.sampleX(x+index-feather.radius), y)
			}
			horizontal[row*width+column] = value
		}
	}
	feather.values = make([]float64, width*height)
	for row := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := range width {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			value := float64(0)
			for index, weight := range kernel {
				sourceY := feather.sampleY(feather.bounds.Min.Y + row + index - feather.radius)
				sample := featherDefault(feather.mask)
				if sourceY >= rowStart && sourceY < rowStart+rows {
					sample = horizontal[(sourceY-rowStart)*width+column]
				}
				value += weight * sample
			}
			feather.values[row*width+column] = composeClamp(value)
		}
	}
	return nil
}

func (feather *featherMask) horizontalRows() (int, int) {
	bounds := composeMaskSampleBounds(feather.mask)
	lower := max(bounds.Min.Y, feather.bounds.Min.Y-feather.radius)
	upper := min(bounds.Max.Y, feather.bounds.Max.Y+feather.radius)
	return lower, max(0, upper-lower)
}

func featherKernel(sigma float64, radius int) []float64 {
	weights := make([]float64, radius*2+1)
	if sigma < 1e-3 {
		weights[radius] = 1
		return weights
	}
	total := float64(0)
	for index := range weights {
		distance := math.Abs(float64(index - radius))
		// Integrating source and destination pixel areas reconstructs a triangular sampling footprint.
		weights[index] = max(0, featherIntegral(sigma, distance+1)-
			2*featherIntegral(sigma, distance)+featherIntegral(sigma, distance-1))
		total += weights[index]
	}
	for index := range weights {
		weights[index] /= total
	}
	return weights
}

func featherIntegral(sigma, value float64) float64 {
	standardized := value / sigma
	return sigma*math.Exp(-.5*standardized*standardized)/math.Sqrt(2*math.Pi) +
		value*.5*(1+math.Erf(standardized/math.Sqrt2))
}

func (feather *featherMask) sampleX(value int) int {
	if feather.unbounded {
		return value
	}
	if feather.mask.Bounds.Min.X >= feather.canvas.Min.X && feather.mask.Bounds.Max.X <= feather.canvas.Max.X {
		return max(feather.canvas.Min.X, min(feather.canvas.Max.X-1, value))
	}
	return value
}

func (feather *featherMask) sampleY(value int) int {
	if feather.unbounded {
		return value
	}
	if feather.mask.Bounds.Min.Y >= feather.canvas.Min.Y && feather.mask.Bounds.Max.Y <= feather.canvas.Max.Y {
		return max(feather.canvas.Min.Y, min(feather.canvas.Max.Y-1, value))
	}
	return value
}

func featherRaw(mask *psdmodel.Mask, x, y int) float64 {
	if bounds := composeMaskSampleBounds(mask); mask.Pixels != nil && image.Pt(x, y).In(bounds) {
		offset := (y-bounds.Min.Y)*mask.Pixels.Stride + (x-bounds.Min.X)*4
		return float64(mask.Pixels.Pix[offset]) / 255
	}
	return featherDefault(mask)
}

func featherDefault(mask *psdmodel.Mask) float64 {
	if mask.DefaultColor != nil {
		return float64(*mask.DefaultColor) / 255
	}
	return 1
}

func (plan *composePlan) mask(mask *psdmodel.Mask, x, y int) float64 {
	return sampleFeatherMask(plan.feathers[mask], mask, x, y)
}

func (plan *composePlan) shadowMask(mask *psdmodel.Mask, x, y int) float64 {
	return sampleFeatherMask(plan.shadowFeathers[mask], mask, x, y)
}

func sampleFeatherMask(feather *featherMask, mask *psdmodel.Mask, x, y int) float64 {
	if feather == nil {
		return composeMask(mask, x, y)
	}
	value := featherRaw(mask, x, y)
	if image.Pt(x, y).In(feather.bounds) {
		offset := (y-feather.bounds.Min.Y)*feather.bounds.Dx() + x - feather.bounds.Min.X
		value = feather.values[offset]
	}
	if mask.Invert {
		value = 1 - value
	}
	if mask.Density != nil {
		value = 1 - (1-value)*float64(*mask.Density)/255
	}
	return value
}
