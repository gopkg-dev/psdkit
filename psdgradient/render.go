package psdgradient

import (
	"context"
	"image"
	"math"
	"slices"
	"unsafe"
)

// Render returns owned straight NRGBA pixels for one classic encoded-RGB gradient fill.
// Document coordinates determine the ramp; optional cropping never rescales its geometry.
// Caller-owned stop slices remain immutable for the call and are never changed or retained.
// Constant RGB and opacity stops do not depend on classic interpolation or gradient geometry.
// An independently constant component ignores its own duplicate locations and endpoint midpoints.
// If the other component varies, ordinary geometry, smoothness and interpolation restrictions still apply.
func Render(ctx context.Context, fill Fill, documentBounds image.Rectangle, options ...Options) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := renderOptions(options)
	if err != nil {
		return nil, err
	}
	if err := validBounds(documentBounds); err != nil {
		return nil, err
	}
	outputBounds := documentBounds
	if configuration.Crop != nil {
		if err := validBounds(*configuration.Crop); err != nil {
			return nil, err
		}
		outputBounds = outputBounds.Intersect(*configuration.Crop)
		if outputBounds.Empty() {
			return nil, gradientError(ErrInvalid, "crop", "crop does not overlap the document")
		}
	}
	width, height := int64(outputBounds.Dx()), int64(outputBounds.Dy())
	if len(fill.ColorStops) > 4096 || len(fill.OpacityStops) > 4096 {
		return nil, gradientError(ErrLimit, "stops", "stop count exceeds the supported metadata limit")
	}
	stopCount := int64(len(fill.ColorStops)) + int64(len(fill.OpacityStops))
	stopBytes := int64(len(fill.ColorStops))*int64(unsafe.Sizeof(ColorStop{})) +
		int64(len(fill.OpacityStops))*int64(unsafe.Sizeof(OpacityStop{})) + 1024
	if stopCount > int64(configuration.MaxStops) || width > configuration.MaxPixels/height ||
		stopBytes > configuration.MaxWorkingBytes || width > (configuration.MaxWorkingBytes-stopBytes)/4/height ||
		width > int64(^uint(0)>>1)/4/height {
		return nil, gradientError(ErrLimit, "render", "output pixels and stop copies exceed resource budgets")
	}
	if err := Validate(fill); err != nil {
		return nil, err
	}
	alignment := documentBounds
	if fill.Align && configuration.AlignBounds != nil {
		if err := validBounds(*configuration.AlignBounds); err != nil {
			return nil, err
		}
		alignment = *configuration.AlignBounds
	}
	constantRGB, colorConstant := constantColorStops(fill.ColorStops)
	constantAlpha, opacityConstant := constantOpacityStops(fill.OpacityStops)
	if colorConstant && opacityConstant {
		return renderConstantGradient(ctx, outputBounds, constantRGB, constantAlpha)
	}
	fill.ColorStops, fill.OpacityStops = slices.Clone(fill.ColorStops), slices.Clone(fill.OpacityStops)
	slices.SortStableFunc(fill.ColorStops, func(first, second ColorStop) int {
		return int(first.Location) - int(second.Location)
	})
	slices.SortStableFunc(fill.OpacityStops, func(first, second OpacityStop) int {
		return int(first.Location) - int(second.Location)
	})
	for index := 1; !colorConstant && index < len(fill.ColorStops); index++ {
		if fill.ColorStops[index].Location == fill.ColorStops[index-1].Location {
			return nil, gradientError(ErrUnsupported, "colors.location", "co-located color stops require native validation")
		}
		if fill.ColorStops[index].Midpoint == 0 || fill.ColorStops[index].Midpoint == 100 {
			return nil, gradientError(ErrUnsupported, "colors.midpoint", "endpoint midpoints require native validation")
		}
	}
	for index := 1; !opacityConstant && index < len(fill.OpacityStops); index++ {
		if fill.OpacityStops[index].Location == fill.OpacityStops[index-1].Location {
			return nil, gradientError(ErrUnsupported, "opacities.location", "co-located opacity stops require native validation")
		}
		if fill.OpacityStops[index].Midpoint == 0 || fill.OpacityStops[index].Midpoint == 100 {
			return nil, gradientError(ErrUnsupported, "opacities.midpoint", "endpoint midpoints require native validation")
		}
	}
	plan, err := prepareGeometry(fill, alignment)
	if err != nil {
		return nil, err
	}
	result := image.NewNRGBA(outputBounds)
	for row := 0; row < int(height); row++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := 0; column < int(width); column++ {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			position := plan.position(float64(outputBounds.Min.X+column), float64(outputBounds.Min.Y+row))
			if fill.Reverse {
				position = 1 - position
			}
			rgb, alpha := constantRGB, constantAlpha
			if !colorConstant {
				rgb = rampColor(fill.ColorStops, position, fill.Smoothness)
			}
			if !opacityConstant {
				alpha = rampOpacity(fill.OpacityStops, position, fill.Smoothness)
			}
			pixel := row*result.Stride + column*4
			for component := range 3 {
				result.Pix[pixel+component] = byte(math.Round(clamp(rgb[component]) * 255))
			}
			result.Pix[pixel+3] = byte(math.Round(clamp(alpha) * 255))
		}
	}
	return result, nil
}

// Validate checks supported fill semantics without allocating pixels or retaining input slices.
func Validate(fill Fill) error {
	if len(fill.Unsupported) != 0 || fill.Kind != Linear && fill.Kind != Radial ||
		fill.Interpolation != "" && fill.Interpolation != "classic" ||
		fill.Form != "" && fill.Form != "custom-stops" || fill.Dither {
		return gradientError(ErrUnsupported, "fill", "gradient contains unsupported shape, interpolation or metadata")
	}
	if !finite(fill.Angle) || !finite(fill.Scale) || fill.Scale <= 0 ||
		!finite(fill.Offset.X) || !finite(fill.Offset.Y) || !finite(fill.Smoothness) ||
		fill.Smoothness < 0 || fill.Smoothness > 4096 || len(fill.ColorStops) == 0 {
		return gradientError(ErrInvalid, "fill", "invalid angle, scale, offset, smoothness or color stop count")
	}
	if len(fill.ColorStops) > 4096 || len(fill.OpacityStops) > 4096 {
		return gradientError(ErrLimit, "stops", "stop count exceeds the supported metadata limit")
	}
	for _, stop := range fill.ColorStops {
		if stop.Location > 4096 || stop.Midpoint > 100 {
			return gradientError(ErrInvalid, "colors", "invalid color stop location or midpoint")
		}
		for _, value := range stop.RGB {
			if !finite(value) || value < 0 || value > 1 {
				return gradientError(ErrInvalid, "colors", "RGB stop is outside the encoded zero-through-one range")
			}
		}
	}
	for _, stop := range fill.OpacityStops {
		if stop.Location > 4096 || stop.Midpoint > 100 || !finite(stop.Opacity) || stop.Opacity < 0 || stop.Opacity > 1 {
			return gradientError(ErrInvalid, "opacities", "invalid opacity stop location, midpoint or opacity")
		}
	}
	if _, _, constant := constantGradient(fill); fill.Smoothness != 4096 && !constant {
		return gradientError(ErrUnsupported, "fill.smoothness", "non-default classic smoothness requires native validation")
	}
	return nil
}

type gradientGeometry struct {
	kind                 Kind
	centerX, centerY     float64
	startX, startY       float64
	deltaX, deltaY, span float64
}

func prepareGeometry(fill Fill, bounds image.Rectangle) (gradientGeometry, error) {
	angle := math.Mod(fill.Angle, 360) * math.Pi / 180
	sine, cosine := math.Sincos(angle)
	if math.Abs(cosine) < 1e-12 {
		cosine = 0
	}
	if math.Abs(sine) < 1e-12 {
		sine = 0
	}
	width, height := float64(bounds.Dx()), float64(bounds.Dy())
	span := math.Inf(1)
	if cosine != 0 {
		span = width / math.Abs(cosine)
	}
	if sine != 0 {
		span = math.Min(span, height/math.Abs(sine))
	}
	span *= fill.Scale / 100
	plan := gradientGeometry{kind: fill.Kind, span: span,
		centerX: float64(bounds.Min.X) + width/2 + width*fill.Offset.X/100,
		centerY: float64(bounds.Min.Y) + height/2 + height*fill.Offset.Y/100}
	if fill.Kind == Radial || fill.Scale != 100 {
		halfWidth, halfHeight := math.Floor(width*fill.Scale/200), math.Floor(height*fill.Scale/200)
		distance := math.Inf(1)
		if cosine != 0 {
			distance = halfWidth / math.Abs(cosine)
		}
		if sine != 0 {
			distance = math.Min(distance, halfHeight/math.Abs(sine))
		}
		deltaX, deltaY := math.Trunc(distance*cosine), math.Trunc(-distance*sine)
		if fill.Kind == Radial {
			plan.span = math.Hypot(deltaX, deltaY)
		} else {
			plan.startX, plan.startY = plan.centerX-deltaX, plan.centerY-deltaY
			plan.deltaX, plan.deltaY = 2*deltaX, 2*deltaY
			plan.span = plan.deltaX*plan.deltaX + plan.deltaY*plan.deltaY
		}
	} else {
		plan.startX, plan.startY = floorEndpoint(plan.centerX-span*cosine/2),
			floorEndpoint(plan.centerY+span*sine/2)
		endX, endY := floorEndpoint(plan.centerX+span*cosine/2), floorEndpoint(plan.centerY-span*sine/2)
		plan.deltaX, plan.deltaY = endX-plan.startX, endY-plan.startY
		plan.span = plan.deltaX*plan.deltaX + plan.deltaY*plan.deltaY
	}
	if !finite(plan.span) || !finite(plan.centerX) || !finite(plan.centerY) {
		return plan, gradientError(ErrInvalid, "geometry", "gradient geometry exceeds finite coordinate precision")
	}
	if plan.span <= 0 {
		return plan, gradientError(ErrUnsupported, "geometry", "subpixel-degenerate gradient axes require native validation")
	}
	return plan, nil
}

func (plan gradientGeometry) position(x, y float64) float64 {
	if plan.kind == Radial {
		return clamp(math.Hypot(x-plan.centerX, y-plan.centerY) / plan.span)
	}
	return clamp(((x-plan.startX)*plan.deltaX + (y-plan.startY)*plan.deltaY) / plan.span)
}

func floorEndpoint(value float64) float64 {
	nearest := math.Round(value)
	if math.Abs(value-nearest) < 1e-9 {
		return nearest
	}
	return math.Floor(value)
}

func rampColor(stops []ColorStop, position, smoothness float64) [3]float64 {
	location := position * 4096
	index, _ := slices.BinarySearchFunc(stops, location, func(stop ColorStop, position float64) int {
		if float64(stop.Location) < position {
			return -1
		}
		if float64(stop.Location) > position {
			return 1
		}
		return 0
	})
	if index == 0 {
		return stops[0].RGB
	}
	if index == len(stops) {
		return stops[len(stops)-1].RGB
	}
	left, right := stops[index-1], stops[index]
	position = (location - float64(left.Location)) / float64(right.Location-left.Location)
	if len(stops) > 2 {
		position = midpointPosition(position, float64(right.Midpoint)/100)
		before, after := stops[max(0, index-2)], stops[min(len(stops)-1, index+1)]
		var rgb [3]float64
		for component := range 3 {
			rgb[component] = classicComponent(before.RGB[component], left.RGB[component],
				right.RGB[component], after.RGB[component], position, smoothness)
		}
		return rgb
	}
	weight := midpointWeight(position,
		float64(right.Midpoint)/100, smoothness)
	var rgb [3]float64
	for component := range 3 {
		rgb[component] = left.RGB[component] + weight*(right.RGB[component]-left.RGB[component])
	}
	return rgb
}

func rampOpacity(stops []OpacityStop, position, smoothness float64) float64 {
	if len(stops) == 0 {
		return 1
	}
	location := position * 4096
	index, _ := slices.BinarySearchFunc(stops, location, func(stop OpacityStop, position float64) int {
		if float64(stop.Location) < position {
			return -1
		}
		if float64(stop.Location) > position {
			return 1
		}
		return 0
	})
	if index == 0 {
		return stops[0].Opacity
	}
	if index == len(stops) {
		return stops[len(stops)-1].Opacity
	}
	left, right := stops[index-1], stops[index]
	position = (location - float64(left.Location)) / float64(right.Location-left.Location)
	if len(stops) > 2 {
		position = midpointPosition(position, float64(right.Midpoint)/100)
		before, after := stops[max(0, index-2)], stops[min(len(stops)-1, index+1)]
		return classicComponent(before.Opacity, left.Opacity, right.Opacity, after.Opacity, position, smoothness)
	}
	weight := midpointWeight(position,
		float64(right.Midpoint)/100, smoothness)
	return left.Opacity + weight*(right.Opacity-left.Opacity)
}

func midpointWeight(position, midpoint, smoothness float64) float64 {
	position = midpointPosition(position, midpoint)
	cubic := .5*position + 1.5*position*position - position*position*position
	return position + smoothness/4096*(cubic-position)
}

func midpointPosition(position, midpoint float64) float64 {
	if position <= 0 {
		return 0
	}
	if position >= 1 {
		return 1
	}
	if midpoint == 0 {
		return 1
	}
	if midpoint == 1 {
		return 0
	}
	if position < midpoint {
		position = .5 * position / midpoint
	} else {
		position = .5 + .5*(position-midpoint)/(1-midpoint)
	}
	return position
}

func classicComponent(before, left, right, after, position, smoothness float64) float64 {
	squared, cubed := position*position, position*position*position
	leftSlope, rightSlope := .5*(right-before), .5*(after-left)
	cubic := (2*cubed-3*squared+1)*left + (cubed-2*squared+position)*leftSlope +
		(-2*cubed+3*squared)*right + (cubed-squared)*rightSlope
	linear := left + position*(right-left)
	return clamp(linear + smoothness/4096*(cubic-linear))
}

func renderOptions(options []Options) (Options, error) {
	result := Options{MaxPixels: 20_000_000, MaxWorkingBytes: 256 << 20, MaxStops: 4096}
	if len(options) > 1 {
		return result, gradientError(ErrInvalid, "options", "provide at most one render options value")
	}
	if len(options) == 0 {
		return result, nil
	}
	provided := options[0]
	if provided.MaxPixels < 0 || provided.MaxWorkingBytes < 0 || provided.MaxStops < 0 {
		return result, gradientError(ErrInvalid, "options", "resource budgets must not be negative")
	}
	if provided.MaxPixels != 0 {
		result.MaxPixels = provided.MaxPixels
	}
	if provided.MaxWorkingBytes != 0 {
		result.MaxWorkingBytes = provided.MaxWorkingBytes
	}
	if provided.MaxStops != 0 {
		result.MaxStops = provided.MaxStops
	}
	result.AlignBounds, result.Crop = provided.AlignBounds, provided.Crop
	return result, nil
}

func validBounds(bounds image.Rectangle) error {
	const coordinateLimit = 1 << 40
	if bounds.Empty() || int64(bounds.Min.X) < -coordinateLimit || int64(bounds.Min.Y) < -coordinateLimit ||
		int64(bounds.Max.X) > coordinateLimit || int64(bounds.Max.Y) > coordinateLimit ||
		int64(bounds.Max.X)-int64(bounds.Min.X) > int64(^uint(0)>>1) ||
		int64(bounds.Max.Y)-int64(bounds.Min.Y) > int64(^uint(0)>>1) {
		return gradientError(ErrInvalid, "bounds", "bounds are empty, reversed or exceed finite pixel precision")
	}
	return nil
}

func finite(value float64) bool { return !math.IsInf(value, 0) && !math.IsNaN(value) }

func clamp(value float64) float64 { return math.Min(1, math.Max(0, value)) }
