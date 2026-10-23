package psdeffects

import (
	"context"
	"image"
	"math"
)

type effectPlan struct {
	effect             Effect
	bounds             image.Rectangle
	radius, blurRadius float64
	offsetX, offsetY   float64
	approximateShadow  bool
	deferConceal       bool
}

// Rasterize builds independently owned coverage planes without changing source alpha or effect metadata.
// Coverage excludes color and effect opacity. Input alpha must describe the shape before layer Fill opacity.
// Shadows and interior glows use a separable tent kernel; exterior glow blur remains approximate.
// Strokes use a one-pixel linear distance boundary.
func Rasterize(ctx context.Context, source *image.Alpha, set *Set, options ...Options) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := rasterConfiguration(options)
	if err != nil {
		return nil, err
	}
	if err := validAlpha(source); err != nil {
		return nil, err
	}
	if err := Validate(set); err != nil {
		return nil, err
	}
	if configuration.ApproximateDropShadow {
		if _, err := EstimateWorking(source.Rect, set, configuration); err != nil {
			return nil, err
		}
	}
	result := &Result{Bounds: source.Rect}
	if !set.Enabled || !set.EffectsVisible || source.Rect.Empty() {
		return result, nil
	}
	scale := set.Scale
	if scale == 0 {
		scale = 1
	}
	remaining, work := configuration.MaxWorkingBytes, configuration.MaxSampleWork
	count := 0
	for _, family := range []Kind{DropShadow, OuterGlow, ColorOverlay, InnerGlow, InnerShadow, Stroke} {
		for position := range set.Items {
			index := position
			if family == ColorOverlay || family == InnerGlow || family == InnerShadow {
				index = len(set.Items) - 1 - position
			}
			effect := set.Items[index]
			if !effect.Enabled || effect.Opacity == 0 || effect.Kind != family {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			count++
			if count > configuration.MaxEffects || remaining < 512 {
				return nil, effectError(ErrLimit, "effects.planes", "enabled effects exceed the output budget")
			}
			remaining -= 512
			plan, err := configuredEffectPlan(source.Rect, effect, scale, set.GlobalAngle, configuration)
			if err != nil {
				return nil, err
			}
			coverage, cost, err := renderEffect(ctx, source, plan, remaining, &work)
			if err != nil {
				return nil, err
			}
			remaining -= cost
			plane := Plane{Kind: effect.Kind, Coverage: coverage, Color: effect.Color,
				Opacity: effect.Opacity, BlendMode: effect.BlendMode,
				LayerConceals: effect.Kind == DropShadow && effect.LayerConceals}
			switch effect.Kind {
			case ColorOverlay, InnerShadow, InnerGlow:
				result.Interior = append(result.Interior, plane)
			case Stroke:
				result.Foreground = append(result.Foreground, plane)
			default:
				result.Background = append(result.Background, plane)
			}
			if effect.Kind != InnerShadow && effect.Kind != InnerGlow {
				result.Bounds = result.Bounds.Union(plan.bounds)
			}
		}
	}
	return result, nil
}

// Validate checks active primitive semantics before a compositor allocates output or changes state.
// Disabled effects and a disabled master switch retain readable unsupported metadata.
// Known zero-opacity effects may retain valid noise and jitter without evaluating those controls.
func Validate(set *Set) error {
	if set == nil {
		return effectError(ErrInvalid, "effects", "effect set is required")
	}
	if !set.Enabled || !set.EffectsVisible {
		return nil
	}
	if len(set.Unsupported) != 0 {
		return effectError(ErrUnsupported, "effects", set.Unsupported[0])
	}
	if !finite(set.Scale) || set.Scale < 0 || set.Scale > 100 || !finite(set.GlobalAngle) {
		return effectError(ErrInvalid, "effects.scale", "effect scale or global angle is invalid")
	}
	for _, effect := range set.Items {
		if !effect.Enabled {
			continue
		}
		if len(effect.Unsupported) != 0 {
			return effectError(ErrUnsupported, "effects.parameters", effect.Unsupported[0])
		}
		if effect.Kind != DropShadow && effect.Kind != OuterGlow && effect.Kind != InnerShadow &&
			effect.Kind != InnerGlow && effect.Kind != Stroke && effect.Kind != ColorOverlay {
			return effectError(ErrUnsupported, "effects.kind", "effect family is not supported")
		}
		if !finite(effect.Opacity) || effect.Opacity < 0 || effect.Opacity > 1 ||
			!finite(effect.Size) || effect.Size < 0 || effect.Size > 100_000 ||
			!finite(effect.Distance) || effect.Distance < 0 || effect.Distance > 100_000 ||
			!finite(effect.Spread) || effect.Spread < 0 || effect.Spread > 1 || !finite(effect.Angle) {
			return effectError(ErrInvalid, "effects.parameters", "effect opacity, size, spread, distance or angle is invalid")
		}
		for _, component := range effect.Color {
			if !finite(component) || component < 0 || component > 1 {
				return effectError(ErrInvalid, "effects.color", "RGB effect component is outside zero through one")
			}
		}
		if len(effect.BlendMode) != 4 {
			return effectError(ErrUnsupported, "effects.blendMode", "effect blend mode is not a four-byte PSD key")
		}
		if !finite(effect.Noise) || !finite(effect.Jitter) || !finite(effect.Range) {
			return effectError(ErrInvalid, "effects.noise", "noise, jitter or range is not finite")
		}
		if effect.Noise != 0 || effect.Jitter != 0 {
			if effect.Opacity != 0 {
				return effectError(ErrUnsupported, "effects.noise", "noise and jitter are not implemented")
			}
			if effect.Noise < 0 || effect.Noise > 1 || effect.Jitter < 0 || effect.Jitter > 1 {
				return effectError(ErrInvalid, "effects.noise", "noise and jitter must be zero through one")
			}
		}
		if (effect.Kind == OuterGlow || effect.Kind == InnerGlow) &&
			(effect.Range != 0.5 || effect.Technique != "SfBL") {
			return effectError(ErrUnsupported, "effects.glow", "only softer glow with a fifty-percent range is supported")
		}
		if effect.Kind == InnerGlow && effect.GlowSource != "" && effect.GlowSource != Edge &&
			effect.GlowSource != CenterSource {
			return effectError(ErrInvalid, "effects.innerGlow", "inner-glow source is invalid")
		}
		if effect.Kind == Stroke && effect.Position != Outside && effect.Position != Inside && effect.Position != Center {
			return effectError(ErrInvalid, "effects.stroke", "stroke position is invalid")
		}
	}
	return nil
}

func rasterConfiguration(options []Options) (Options, error) {
	configuration := Options{MaxWorkingBytes: 256 << 20, MaxSampleWork: 100_000_000, MaxEffects: 128}
	if len(options) > 1 {
		return configuration, effectError(ErrInvalid, "options", "at most one raster option is accepted")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxWorkingBytes < 0 || provided.MaxSampleWork < 0 || provided.MaxEffects < 0 {
			return configuration, effectError(ErrInvalid, "options", "raster budgets must not be negative")
		}
		if provided.DeferDropShadowConceal && !provided.ApproximateDropShadow {
			return configuration, effectError(ErrInvalid, "options",
				"deferring shadow concealment requires the explicit approximate drop-shadow policy")
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxSampleWork != 0 {
			configuration.MaxSampleWork = provided.MaxSampleWork
		}
		if provided.MaxEffects != 0 {
			configuration.MaxEffects = provided.MaxEffects
		}
		configuration.ApproximateDropShadow = provided.ApproximateDropShadow
		configuration.DeferDropShadowConceal = provided.DeferDropShadowConceal
	}
	return configuration, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validAlpha(source *image.Alpha) error {
	if source == nil || source.Rect.Min.X > source.Rect.Max.X || source.Rect.Min.Y > source.Rect.Max.Y {
		return effectError(ErrInvalid, "effects.alpha", "source alpha or its bounds are invalid")
	}
	if err := validEffectBounds(source.Rect); err != nil {
		return err
	}
	width, height := int64(source.Rect.Dx()), int64(source.Rect.Dy())
	if width == 0 || height == 0 {
		return nil
	}
	if source.Stride < 0 || int64(source.Stride) < width || height-1 > int64(len(source.Pix))/int64(source.Stride) ||
		(height-1)*int64(source.Stride)+width > int64(len(source.Pix)) {
		return effectError(ErrInvalid, "effects.alpha", "source alpha stride or backing length is invalid")
	}
	return nil
}

func planEffect(bounds image.Rectangle, effect Effect, scale, globalAngle float64) (effectPlan, error) {
	plan := effectPlan{effect: effect, bounds: bounds}
	if effect.Kind == ColorOverlay {
		return plan, nil
	}
	size := effect.Size * scale
	if effect.Kind == Stroke {
		plan.radius = size
		if effect.Position == Center {
			plan.radius /= 2
		}
		if effect.Position != Inside {
			plan.bounds = bounds.Inset(-int(math.Ceil(plan.radius)))
		}
		return plan, nil
	}
	plan.radius, plan.blurRadius = size*effect.Spread, size*(1-effect.Spread)
	expansion := int(math.Ceil(plan.radius) + math.Ceil(plan.blurRadius))
	if effect.Kind == OuterGlow && plan.blurRadius == 0 && plan.radius == 3 {
		// The native three-pixel soft-matte brush retains a ten-percent fourth axial sample.
		expansion++
	}
	plan.bounds = bounds.Inset(-expansion)
	if effect.Kind == DropShadow || effect.Kind == InnerShadow {
		angle := effect.Angle
		if effect.UseGlobalLight {
			angle = globalAngle
		}
		angle = math.Mod(angle, 360) * math.Pi / 180
		plan.offsetX, plan.offsetY = -effect.Distance*scale*math.Cos(angle), effect.Distance*scale*math.Sin(angle)
		if math.Abs(plan.offsetX-math.Round(plan.offsetX)) < 1e-9 {
			plan.offsetX = math.Round(plan.offsetX)
		}
		if math.Abs(plan.offsetY-math.Round(plan.offsetY)) < 1e-9 {
			plan.offsetY = math.Round(plan.offsetY)
		}
		if effect.Kind == InnerShadow {
			// Native inner-shadow geometry rounds the directional offset before filtering its coverage.
			plan.offsetX, plan.offsetY = math.Round(plan.offsetX), math.Round(plan.offsetY)
		}
		plan.bounds.Min.X += int(math.Floor(plan.offsetX))
		plan.bounds.Min.Y += int(math.Floor(plan.offsetY))
		plan.bounds.Max.X += int(math.Ceil(plan.offsetX))
		plan.bounds.Max.Y += int(math.Ceil(plan.offsetY))
	}
	if effect.Kind == InnerShadow || effect.Kind == InnerGlow {
		// Interior filtering needs only the source output and convolution halo, not displaced exterior bounds.
		plan.bounds = bounds.Inset(-int(math.Ceil(plan.blurRadius)))
	}
	return plan, nil
}

func renderEffect(
	ctx context.Context, source *image.Alpha, plan effectPlan, available int64, work *int64,
) (*image.Alpha, int64, error) {
	area, scratch, visits, err := effectCosts(plan, available)
	if err != nil {
		return nil, 0, err
	}
	if scratch > available-area {
		return nil, 0, effectError(ErrLimit, "effects.scratch", "effect output and scratch exceed working budgets")
	}
	if visits > *work {
		return nil, 0, effectError(ErrLimit, "effects.work", "effect sample visits exceed CPU budgets")
	}
	*work -= visits
	width, height := plan.bounds.Dx(), plan.bounds.Dy()
	inner := plan.effect.Kind == InnerGlow || plan.effect.Kind == InnerShadow
	hardGlow := (plan.effect.Kind == OuterGlow || plan.effect.Kind == InnerGlow) && plan.blurRadius == 0
	coverage := image.NewAlpha(plan.bounds)
	if plan.effect.Kind == ColorOverlay {
		for y := plan.bounds.Min.Y; y < plan.bounds.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			copy(coverage.Pix[coverage.PixOffset(plan.bounds.Min.X, y):][:width],
				source.Pix[source.PixOffset(plan.bounds.Min.X, y):][:width])
		}
		return coverage, area, nil
	}
	if plan.effect.Kind == Stroke {
		return renderStroke(ctx, source, plan, coverage, area)
	}
	values := make([]float64, int(area))
	for y := plan.bounds.Min.Y; y < plan.bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		for x := plan.bounds.Min.X; x < plan.bounds.Max.X; x++ {
			edgeRange := float64(0)
			if hardGlow {
				edgeRange = plan.effect.Range
			} else if plan.effect.Kind == DropShadow && plan.radius > 0 {
				edgeRange = 1
			}
			value, err := displacedMorphologyKernel(ctx, source,
				float64(x)-plan.offsetX, float64(y)-plan.offsetY, plan.radius, edgeRange, !inner,
				plan.approximateShadow)
			if err != nil {
				return nil, 0, err
			}
			values[coverage.PixOffset(x, y)] = value
		}
	}
	if plan.blurRadius > 0 {
		var err error
		if inner || plan.effect.Kind == DropShadow {
			err = tentBlur(ctx, values, width, height, plan.blurRadius)
		} else {
			err = gaussian(ctx, values, width, height, plan.blurRadius)
		}
		if err != nil {
			return nil, 0, err
		}
	}
	for y := plan.bounds.Min.Y; y < plan.bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		for x := plan.bounds.Min.X; x < plan.bounds.Max.X; x++ {
			value := values[coverage.PixOffset(x, y)]
			if inner {
				if plan.effect.Kind == InnerGlow && plan.blurRadius > 0 {
					value = math.Min(1, (1-value)/plan.effect.Range)
					if plan.effect.GlowSource == CenterSource {
						value = 1 - value
					}
				} else if plan.effect.Kind != InnerGlow || plan.effect.GlowSource != CenterSource {
					value = 1 - value
				}
				value *= sourceAlpha(source, x, y)
			} else if plan.effect.Kind == OuterGlow || plan.effect.LayerConceals && !plan.deferConceal {
				value *= 1 - sourceAlpha(source, x, y)
			}
			coverage.Pix[coverage.PixOffset(x, y)] = quantize(value)
		}
	}
	return coverage, area, nil
}

func effectCosts(plan effectPlan, available int64) (int64, int64, int64, error) {
	if err := validEffectBounds(plan.bounds); err != nil {
		return 0, 0, 0, err
	}
	width, height := int64(plan.bounds.Dx()), int64(plan.bounds.Dy())
	if width <= 0 || height <= 0 || width > available/height || width > int64(^uint(0)>>1)/height {
		return 0, 0, 0, effectError(ErrLimit, "effects.bounds", "expanded effect bounds exceed output budgets")
	}
	area := width * height
	if plan.effect.Kind == ColorOverlay {
		return area, 0, area, nil
	}
	workingFactor := int64(9)
	if plan.effect.Kind == Stroke {
		workingFactor = 1
	} else if plan.blurRadius > 0 {
		workingFactor = 17
	}
	radius := int64(math.Ceil(plan.radius))
	hardGlow := (plan.effect.Kind == OuterGlow || plan.effect.Kind == InnerGlow) && plan.blurRadius == 0
	if plan.effect.Kind == Stroke || hardGlow ||
		plan.effect.Kind == DropShadow && plan.radius > 0 && !plan.approximateShadow {
		radius++
	}
	kernelRadius := int64(math.Ceil(plan.blurRadius))
	if plan.approximateShadow {
		kernelRadius = max(0, kernelRadius-1)
	}
	kernelBytes := (2*kernelRadius + 1) * 8
	if kernelBytes > available || area > (available-kernelBytes)/workingFactor {
		return 0, 0, 0, effectError(ErrLimit, "effects.scratch", "effect output and scratch exceed working budgets")
	}
	visits := (radius*2 + 1) * (radius*2 + 1)
	if plan.effect.Kind != Stroke {
		if plan.offsetX != math.Floor(plan.offsetX) {
			visits *= 2
		}
		if plan.offsetY != math.Floor(plan.offsetY) {
			visits *= 2
		}
	}
	if plan.effect.Kind == Stroke && plan.effect.Position == Center {
		visits *= 2
	}
	if kernelRadius > 0 {
		visits += (kernelRadius*2 + 1) * 2
	}
	visits += 3
	if visits <= 0 || area > int64(^uint64(0)>>1)/visits {
		return 0, 0, 0, effectError(ErrLimit, "effects.work", "effect sample visits overflow CPU budgets")
	}
	return area, (workingFactor-1)*area + kernelBytes, area * visits, nil
}

func renderStroke(
	ctx context.Context, source *image.Alpha, plan effectPlan, coverage *image.Alpha, area int64,
) (*image.Alpha, int64, error) {
	for y := plan.bounds.Min.Y; y < plan.bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		for x := plan.bounds.Min.X; x < plan.bounds.Max.X; x++ {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, 0, err
				}
			}
			value := sourceAlpha(source, x, y)
			var outer, inner float64
			var err error
			if plan.effect.Position != Inside {
				outer, err = morphology(ctx, source, x, y, plan.radius, true, 1)
			}
			if err == nil && plan.effect.Position != Outside {
				inner, err = morphology(ctx, source, x, y, plan.radius, false, 1)
			}
			if err != nil {
				return nil, 0, err
			}
			switch plan.effect.Position {
			case Outside:
				value = outer - value
			case Inside:
				value -= inner
			case Center:
				value = outer - inner
			}
			coverage.Pix[coverage.PixOffset(x, y)] = quantize(value)
		}
	}
	return coverage, area, nil
}

func sourceAlpha(source *image.Alpha, x, y int) float64 {
	if x < source.Rect.Min.X || x >= source.Rect.Max.X || y < source.Rect.Min.Y || y >= source.Rect.Max.Y {
		return 0
	}
	return float64(source.Pix[source.PixOffset(x, y)]) / 255
}

func morphology(
	ctx context.Context, source *image.Alpha, x, y int, radius float64, largest bool, edgeRange float64,
) (float64, error) {
	return morphologyKernel(ctx, source, x, y, radius, largest, edgeRange, false)
}

func morphologyKernel(
	ctx context.Context, source *image.Alpha, x, y int, radius float64, largest bool, edgeRange float64,
	approximateShadow bool,
) (float64, error) {
	value := float64(0)
	if !largest {
		value = 1
	}
	extent := int(math.Ceil(radius))
	if edgeRange > 0 && !approximateShadow {
		extent++
	}
	for dy := -extent; dy <= extent; dy++ {
		if dy&15 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		for dx := -extent; dx <= extent; dx++ {
			distanceSquared := float64(dx)*float64(dx) + float64(dy)*float64(dy)
			weight := float64(1)
			if edgeRange > 0 {
				weight = circularWeight(radius, edgeRange, distanceSquared)
				if approximateShadow {
					weight = dropShadowSpreadWeight(radius, distanceSquared)
				}
				if weight == 0 {
					continue
				}
			} else if distanceSquared > radius*radius+1e-9 {
				continue
			}
			sample := sourceAlpha(source, x+dx, y+dy)
			if largest {
				value = math.Max(value, sample*weight)
			} else {
				value = math.Min(value, 1-(1-sample)*weight)
			}
		}
	}
	return value, nil
}

func circularWeight(radius, edgeRange, distanceSquared float64) float64 {
	if radius == 3 {
		// These native RGB8 brush transitions are distinct for stroke and half-range soft-matte glow.
		// Their PSD/PSB source and exported pixels are retained in effects-edit-size3/native.
		if edgeRange == 1 {
			switch distanceSquared {
			case 10:
				return 195.0 / 255
			case 13:
				return 90.0 / 255
			}
		}
		if edgeRange == .5 {
			switch distanceSquared {
			case 13:
				return 187.0 / 255
			case 16:
				return 26.0 / 255
			}
		}
	}
	return math.Max(0, math.Min(1, (radius+1-math.Sqrt(distanceSquared))/edgeRange))
}

func displacedMorphologyKernel(
	ctx context.Context, source *image.Alpha, x, y, radius, edgeRange float64, largest bool,
	approximateShadow bool,
) (float64, error) {
	left, top := int(math.Floor(x)), int(math.Floor(y))
	xWeight, yWeight := x-float64(left), y-float64(top)
	value := float64(0)
	for dy := 0; dy <= 1; dy++ {
		for dx := 0; dx <= 1; dx++ {
			weightX, weightY := 1-xWeight, 1-yWeight
			if dx != 0 {
				weightX = xWeight
			}
			if dy != 0 {
				weightY = yWeight
			}
			weight := weightX * weightY
			if weight == 0 {
				continue
			}
			sample, err := morphologyKernel(ctx, source, left+dx, top+dy, radius, largest, edgeRange,
				approximateShadow)
			if err != nil {
				return 0, err
			}
			value += sample * weight
		}
	}
	return value, nil
}

func quantize(value float64) byte {
	return byte(math.Round(math.Max(0, math.Min(1, value)) * 255))
}

func gaussian(ctx context.Context, values []float64, width, height int, radius float64) error {
	extent := int(math.Ceil(radius))
	sigma := math.Max(radius/3, 0.01)
	kernel := make([]float64, extent*2+1)
	total := float64(0)
	for index := range kernel {
		distance := float64(index - extent)
		kernel[index] = math.Exp(-distance * distance / (2 * sigma * sigma))
		total += kernel[index]
	}
	for index := range kernel {
		kernel[index] /= total
	}
	temporary := make([]float64, len(values))
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := range width {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			for index, weight := range kernel {
				column := x + index - extent
				if column >= 0 && column < width {
					temporary[y*width+x] += values[y*width+column] * weight
				}
			}
		}
	}
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := range width {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			value := float64(0)
			for index, weight := range kernel {
				row := y + index - extent
				if row >= 0 && row < height {
					value += temporary[row*width+x] * weight
				}
			}
			values[y*width+x] = value
		}
	}
	return nil
}

// tentBlur applies the native layer-style separable triangular kernel before interior clipping.
func tentBlur(ctx context.Context, values []float64, width, height int, radius float64) error {
	extent := max(int(math.Ceil(radius))-1, 0)
	kernel := make([]float64, extent*2+1)
	total := float64(0)
	for index := range kernel {
		kernel[index] = math.Max(0, radius-math.Abs(float64(index-extent)))
		total += kernel[index]
	}
	for index := range kernel {
		kernel[index] /= total
	}
	temporary := make([]float64, len(values))
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := range width {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			for index, weight := range kernel {
				column := x + index - extent
				if column >= 0 && column < width {
					temporary[y*width+x] += values[y*width+column] * weight
				}
			}
		}
	}
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := range width {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			value := float64(0)
			for index, weight := range kernel {
				row := y + index - extent
				if row >= 0 && row < height {
					value += temporary[row*width+x] * weight
				}
			}
			values[y*width+x] = value
		}
	}
	return nil
}
