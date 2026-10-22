package psdvector

import (
	"context"
	"fmt"
	"image"
	"math"
	"slices"
)

type vectorLine struct {
	x1, y1, x2, y2 float64
	component      int
}

type vectorComponent struct {
	operation int16
}

type vectorCrossing struct {
	x         float64
	component int
	winding   int
}

type rasterPlan struct {
	ctx         context.Context
	mask        *Mask
	options     Options
	rule        FillRule
	lines       []vectorLine
	components  []vectorComponent
	lineLimit   int
	combineOnly bool
}

// Rasterize creates an independently owned alpha mask in absolute document coordinates.
// Curves are adaptively flattened, then each pixel averages a fixed 4 by 4 or 8 by 8 sample grid.
// Open contours are implicitly closed for filling; this entry point does not draw path strokes.
// Absolute output coordinates must lie within plus or minus 2^40 pixels to preserve subpixel precision.
// Input metadata and geometry must remain read-only for the duration of the call.
func Rasterize(
	ctx context.Context, mask *Mask, bounds image.Rectangle, options ...Options,
) (*image.Alpha, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := prepareRaster(ctx, mask, bounds, options)
	if err != nil {
		return nil, err
	}
	result := image.NewAlpha(bounds)
	if mask.Disabled || len(mask.Subpaths) == 0 {
		filled := mask.InitialFill != mask.Invert
		if mask.Disabled {
			filled = true
		}
		for row := range bounds.Dy() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if filled {
				for column := range bounds.Dx() {
					result.Pix[row*result.Stride+column] = 255
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result, nil
	}
	if plan.options.AreaCoverage {
		return rasterizeArea(plan, bounds, result)
	}
	counts := make([]uint8, bounds.Dx())
	winding := make([]int, len(plan.components))
	crossings := make([]vectorCrossing, 0, len(plan.lines))
	samples := plan.options.Samples
	if plan.options.NoAntialias {
		samples = 1
	}
	for row := range bounds.Dy() {
		clear(counts)
		for sample := range samples {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			clear(winding)
			activeComponents := 0
			crossings = crossings[:0]
			y := float64(bounds.Min.Y+row) + (float64(sample)+0.5)/float64(samples)
			for _, line := range plan.lines {
				direction := 0
				if line.y1 <= y && y < line.y2 {
					direction = 1
				} else if line.y2 <= y && y < line.y1 {
					direction = -1
				}
				if direction != 0 {
					x := line.x1 + (y-line.y1)/(line.y2-line.y1)*(line.x2-line.x1)
					crossings = append(crossings, vectorCrossing{x: x, component: line.component, winding: direction})
				}
			}
			slices.SortFunc(crossings, func(left, right vectorCrossing) int {
				if left.x < right.x {
					return -1
				}
				if left.x > right.x {
					return 1
				}
				return 0
			})
			previous := float64(bounds.Min.X)
			for index := 0; index < len(crossings); {
				position := crossings[index].x
				inside := activeComponents != 0
				if !plan.combineOnly {
					inside = plan.inside(winding)
				}
				if inside != mask.Invert {
					addSampleSpan(counts, samples, bounds.Min.X, previous, position)
				}
				for index < len(crossings) && crossings[index].x == position {
					crossing := crossings[index]
					previousInside := winding[crossing.component] != 0
					if plan.rule == EvenOdd {
						previousInside = winding[crossing.component]%2 != 0
					}
					winding[crossing.component] += crossing.winding
					if plan.combineOnly {
						currentInside := winding[crossing.component] != 0
						if plan.rule == EvenOdd {
							currentInside = winding[crossing.component]%2 != 0
						}
						if currentInside && !previousInside {
							activeComponents++
						} else if previousInside && !currentInside {
							activeComponents--
						}
					}
					index++
				}
				previous = position
			}
			inside := activeComponents != 0
			if !plan.combineOnly {
				inside = plan.inside(winding)
			}
			if inside != mask.Invert {
				addSampleSpan(counts, samples, bounds.Min.X, previous, float64(bounds.Max.X))
			}
		}
		denominator := uint32(samples * samples)
		for column, count := range counts {
			value := byte((uint32(count)*255 + denominator/2) / denominator)
			if plan.options.CoverageLevels16 {
				value = coverageLevel16(float64(count), float64(denominator))
			}
			result.Pix[row*result.Stride+column] = value
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// RasterizeNRGBA returns coverage in all three color channels with an opaque alpha channel.
// Its working budget includes both the temporary alpha mask and the returned grayscale image.
func RasterizeNRGBA(
	ctx context.Context, mask *Mask, bounds image.Rectangle, options ...Options,
) (*image.NRGBA, error) {
	configuration, err := rasterConfiguration(options)
	if err != nil {
		return nil, err
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if width <= 0 || height <= 0 || width > configuration.MaxWorkingBytes/4/height {
		return nil, vectorError(ErrLimit, "vectorMask.bounds", 0, "grayscale output exceeds the working budget")
	}
	configuration.MaxWorkingBytes -= width * height * 4
	if configuration.MaxWorkingBytes == 0 {
		return nil, vectorError(ErrLimit, "vectorMask.bounds", 0, "no budget remains for vector coverage")
	}
	alpha, err := Rasterize(ctx, mask, bounds, configuration)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := image.NewNRGBA(bounds)
	for row := range bounds.Dy() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range bounds.Dx() {
			value, offset := alpha.Pix[row*alpha.Stride+column], row*result.Stride+column*4
			result.Pix[offset], result.Pix[offset+1], result.Pix[offset+2], result.Pix[offset+3] = value, value, value, 255
		}
	}
	return result, nil
}

func prepareRaster(
	ctx context.Context, mask *Mask, bounds image.Rectangle, options []Options,
) (*rasterPlan, error) {
	configuration, err := rasterConfiguration(options)
	if err != nil {
		return nil, err
	}
	if mask == nil || mask.Width <= 0 || mask.Height <= 0 || mask.Width > 300_000 || mask.Height > 300_000 ||
		bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y {
		return nil, vectorError(ErrMalformed, "vectorMask", 0, "invalid mask dimensions or output rectangle")
	}
	// This range preserves subpixel sample positions after integer document coordinates become float64.
	coordinateLimit := int64(1) << 40
	if int64(bounds.Min.X) < -coordinateLimit || int64(bounds.Min.Y) < -coordinateLimit ||
		int64(bounds.Max.X) > coordinateLimit || int64(bounds.Max.Y) > coordinateLimit {
		return nil, vectorError(ErrMalformed, "vectorMask.bounds", mask.Span.Offset,
			"output coordinates exceed the supported subpixel precision range")
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if width <= 0 || height <= 0 || width > int64(^uint(0)>>1)/4/height ||
		width > int64(^uint(0)>>1)/int64(configuration.Samples) || width > configuration.MaxWorkingBytes/height {
		return nil, vectorError(ErrLimit, "vectorMask.bounds", mask.Span.Offset, "output exceeds the working budget")
	}
	rule := mask.FillRule
	if configuration.FillRule != nil {
		rule = *configuration.FillRule
	}
	if rule != EvenOdd && rule != NonZero {
		return nil, vectorError(ErrUnsupported, "vectorMask.fillRule", mask.Span.Offset, "unknown contour fill rule")
	}
	rowBytes := width
	if configuration.AreaCoverage {
		if width > int64(^uint(0)>>1)/8 {
			return nil, vectorError(ErrLimit, "vectorMask.bounds", mask.Span.Offset,
				"area scanline storage exceeds the platform allocation limit")
		}
		rowBytes = width * 8
	}
	baseBytes := width*height + rowBytes + int64(configuration.MaxDepth)*128
	if baseBytes > configuration.MaxWorkingBytes ||
		int64(len(mask.Subpaths)) > (configuration.MaxWorkingBytes-baseBytes)/64 {
		return nil, vectorError(ErrLimit, "vectorMask.subpaths", mask.Span.Offset,
			"path bookkeeping exceeds the working budget")
	}
	baseBytes += int64(len(mask.Subpaths)) * 64
	plan := &rasterPlan{
		ctx: ctx, mask: mask, options: configuration, rule: rule,
		components:  make([]vectorComponent, 0, len(mask.Subpaths)),
		lineLimit:   int(min(int64(configuration.MaxSegments), (configuration.MaxWorkingBytes-baseBytes)/64)),
		combineOnly: true,
	}
	if !mask.Disabled && (mask.Flags&^uint32(7) != 0 || len(mask.Unsupported) != 0) {
		return nil, vectorError(ErrUnsupported, "vectorMask.records", mask.Span.Offset, "path has retained unknown semantics")
	}
	if mask.Version != 0 && mask.Version != 3 {
		return nil, vectorError(ErrUnsupported, "vectorMask.version", mask.Span.Offset, "unknown vector-mask version")
	}
	for index, subpath := range mask.Subpaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := fmt.Sprintf("vectorMask.subpaths[%d]", index)
		if !mask.Disabled {
			if subpath.Operation < -1 || subpath.Operation > 3 || subpath.Unknown1 > 1 ||
				subpath.Unknown2 != 0 || !zeroBytes(subpath.Reserved[:]) {
				return nil, vectorError(ErrUnsupported, path, subpath.Span.Offset, "unknown path combination metadata")
			}
			if subpath.Operation == -1 && len(plan.components) == 0 {
				return nil, vectorError(ErrUnsupported, path, subpath.Span.Offset, "continuation has no preceding component")
			}
		}
		for _, knot := range subpath.Knots {
			for _, point := range []Point{knot.Preceding, knot.Anchor, knot.Leaving} {
				if !finitePoint(point) {
					return nil, vectorError(ErrMalformed, path, subpath.Span.Offset, "Bezier point is not a bounded finite coordinate")
				}
			}
		}
		if mask.Disabled {
			continue
		}
		if subpath.Operation != -1 {
			plan.components = append(plan.components, vectorComponent{operation: subpath.Operation})
			if subpath.Operation != 1 {
				plan.combineOnly = false
			}
		}
		component := len(plan.components) - 1
		for knotIndex := 1; knotIndex < len(subpath.Knots); knotIndex++ {
			previous, current := subpath.Knots[knotIndex-1], subpath.Knots[knotIndex]
			err := plan.flatten(previous.Anchor, previous.Leaving, current.Preceding, current.Anchor, component, 0)
			if err != nil {
				return nil, err
			}
		}
		if len(subpath.Knots) != 0 {
			first, last := subpath.Knots[0], subpath.Knots[len(subpath.Knots)-1]
			if subpath.Closed {
				if err := plan.flatten(last.Anchor, last.Leaving, first.Preceding, first.Anchor, component, 0); err != nil {
					return nil, err
				}
			} else if err := plan.line(last.Anchor, first.Anchor, component); err != nil {
				return nil, err
			}
		}
	}
	steps := max(int64(1), int64(len(plan.lines)))
	components := max(int64(1), int64(len(plan.components)))
	if plan.combineOnly {
		// Pure union updates one membership count per crossing, independent of the number of components.
		components = 1
	}
	verticalSamples := int64(configuration.Samples)
	if configuration.AreaCoverage {
		verticalSamples = int64(configuration.AreaSamples)
	} else if configuration.NoAntialias {
		verticalSamples = 1
	}
	if height > configuration.MaxScanlineWork/verticalSamples/steps/components {
		return nil, vectorError(ErrLimit, "vectorMask.scanlines", mask.Span.Offset,
			"scanline work exceeds the configured budget")
	}
	return plan, nil
}

func rasterConfiguration(options []Options) (Options, error) {
	configuration := Options{
		Samples: 8, Tolerance: 1.0 / 64, MaxWorkingBytes: 256 << 20,
		MaxSegments: 1_000_000, MaxDepth: 24, MaxScanlineWork: 100_000_000,
	}
	if len(options) > 1 {
		return configuration, vectorError(ErrMalformed, "options", 0, "at most one raster option value is accepted")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.Samples < 0 || provided.AreaSamples < 0 || provided.Tolerance < 0 || math.IsNaN(provided.Tolerance) ||
			math.IsInf(provided.Tolerance, 0) || provided.MaxWorkingBytes < 0 || provided.MaxSegments < 0 ||
			provided.MaxDepth < 0 || provided.MaxScanlineWork < 0 {
			return configuration, vectorError(ErrMalformed, "options", 0, "raster options contain invalid budgets or tolerance")
		}
		if provided.Samples != 0 {
			configuration.Samples = provided.Samples
		}
		if provided.Tolerance != 0 {
			configuration.Tolerance = provided.Tolerance
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxSegments != 0 {
			configuration.MaxSegments = provided.MaxSegments
		}
		if provided.MaxDepth != 0 {
			configuration.MaxDepth = provided.MaxDepth
		}
		if provided.MaxScanlineWork != 0 {
			configuration.MaxScanlineWork = provided.MaxScanlineWork
		}
		configuration.FillRule = provided.FillRule
		configuration.AreaCoverage = provided.AreaCoverage
		configuration.NoAntialias = provided.NoAntialias
		configuration.CoverageLevels16 = provided.CoverageLevels16
		if provided.AreaSamples != 0 {
			if !provided.AreaCoverage {
				return configuration, vectorError(ErrMalformed, "options.AreaSamples", 0,
					"explicit vertical area samples require area coverage")
			}
			configuration.AreaSamples = provided.AreaSamples
		}
	}
	if configuration.NoAntialias && (configuration.AreaCoverage || configuration.CoverageLevels16) {
		return configuration, vectorError(ErrMalformed, "options.coverage", 0,
			"pixel-center membership cannot be combined with area coverage or 16-level coverage")
	}
	if configuration.AreaCoverage && configuration.AreaSamples == 0 {
		configuration.AreaSamples = int(areaVerticalSamples)
	}
	if configuration.AreaSamples != 0 && configuration.AreaSamples != 4 && configuration.AreaSamples != 8 &&
		configuration.AreaSamples != 16 && configuration.AreaSamples != 64 {
		return configuration, vectorError(ErrUnsupported, "options.AreaSamples", 0,
			"vertical area samples must be four, eight, sixteen or sixty-four")
	}
	if configuration.Samples != 4 && configuration.Samples != 8 {
		return configuration, vectorError(ErrUnsupported, "options.Samples", 0, "sample grid must be four or eight")
	}
	if configuration.MaxDepth > 32 {
		return configuration, vectorError(ErrLimit, "options.MaxDepth", 0, "subdivision depth may not exceed 32")
	}
	return configuration, nil
}

func finitePoint(point Point) bool {
	return !math.IsNaN(point.X) && !math.IsNaN(point.Y) && !math.IsInf(point.X, 0) && !math.IsInf(point.Y, 0) &&
		math.Abs(point.X) <= 1<<40 && math.Abs(point.Y) <= 1<<40
}

func (plan *rasterPlan) flatten(a, b, c, d Point, component, depth int) error {
	if err := plan.ctx.Err(); err != nil {
		return err
	}
	tolerance := plan.options.Tolerance * plan.options.Tolerance
	if pointSegmentDistanceSquared(b, a, d) <= tolerance && pointSegmentDistanceSquared(c, a, d) <= tolerance {
		return plan.line(a, d, component)
	}
	if depth >= plan.options.MaxDepth {
		return vectorError(ErrLimit, "vectorMask.curves", plan.mask.Span.Offset, "cubic subdivision depth was exceeded")
	}
	ab, bc, cd := midpoint(a, b), midpoint(b, c), midpoint(c, d)
	abc, bcd := midpoint(ab, bc), midpoint(bc, cd)
	center := midpoint(abc, bcd)
	if err := plan.flatten(a, ab, abc, center, component, depth+1); err != nil {
		return err
	}
	return plan.flatten(center, bcd, cd, d, component, depth+1)
}

func (plan *rasterPlan) line(start, end Point, component int) error {
	if start.X == end.X && start.Y == end.Y {
		return nil
	}
	if len(plan.lines) >= plan.lineLimit {
		return vectorError(ErrLimit, "vectorMask.curves", plan.mask.Span.Offset, "flattened edges exceed resource budgets")
	}
	if len(plan.lines) == cap(plan.lines) {
		capacity := min(plan.lineLimit, max(64, cap(plan.lines)*2))
		buffer := make([]vectorLine, len(plan.lines), capacity)
		copy(buffer, plan.lines)
		plan.lines = buffer
	}
	plan.lines = append(plan.lines, vectorLine{start.X, start.Y, end.X, end.Y, component})
	return nil
}

func midpoint(left, right Point) Point {
	return Point{X: (left.X + right.X) / 2, Y: (left.Y + right.Y) / 2}
}

func pointSegmentDistanceSquared(point, start, end Point) float64 {
	dx, dy := end.X-start.X, end.Y-start.Y
	length := dx*dx + dy*dy
	amount := 0.0
	if length != 0 {
		amount = math.Max(0, math.Min(1, ((point.X-start.X)*dx+(point.Y-start.Y)*dy)/length))
	}
	dx, dy = point.X-(start.X+amount*dx), point.Y-(start.Y+amount*dy)
	return dx*dx + dy*dy
}

func (plan *rasterPlan) inside(winding []int) bool {
	filled := false
	for index, component := range plan.components {
		inside := winding[index] != 0
		if plan.rule == EvenOdd {
			inside = winding[index]%2 != 0
		}
		if index == 0 && (component.operation == 2 || component.operation == 3) {
			filled = true
		}
		switch component.operation {
		case 0:
			filled = filled != inside
		case 1:
			filled = filled || inside
		case 2:
			filled = filled && !inside
		case 3:
			filled = filled && inside
		}
	}
	return filled
}

func addSampleSpan(counts []uint8, samples, origin int, start, end float64) {
	left := math.Ceil((start-float64(origin))*float64(samples) - 0.5)
	right := math.Ceil((end-float64(origin))*float64(samples) - 0.5)
	limit := len(counts) * samples
	first, last := int(math.Max(0, math.Min(float64(limit), left))), int(math.Max(0, math.Min(float64(limit), right)))
	for first < last {
		column := first / samples
		amount := min(last-first, samples-first%samples)
		counts[column] += uint8(amount)
		first += amount
	}
}
