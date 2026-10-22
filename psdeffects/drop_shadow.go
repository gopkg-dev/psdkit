package psdeffects

import (
	"image"
	"math"
)

// DropShadowSourceBounds returns the source-alpha window needed to compute a shadow in output.
// It includes the local alpha needed for LayerConceals and never clips to a document canvas.
// Empty output is returned unchanged; disabled or zero-opacity shadows need no source samples.
// It describes the primitive's sampling support, not a claim of Photoshop pixel equivalence.
func DropShadowSourceBounds(
	output image.Rectangle, effect Effect, scale, globalAngle float64, options ...Options,
) (image.Rectangle, error) {
	configuration, err := rasterConfiguration(options)
	if err != nil {
		return image.Rectangle{}, err
	}
	if effect.Kind != DropShadow {
		return image.Rectangle{}, effectError(ErrUnsupported, "effects.kind", "source support requires a drop shadow")
	}
	set := &Set{Enabled: true, EffectsVisible: true, Scale: scale, GlobalAngle: globalAngle, Items: []Effect{effect}}
	if err := Validate(set); err != nil {
		return image.Rectangle{}, err
	}
	if err := validEffectBounds(output); err != nil {
		return image.Rectangle{}, err
	}
	if output.Empty() {
		return output, nil
	}
	if !effect.Enabled || effect.Opacity == 0 {
		return image.Rectangle{Min: output.Min, Max: output.Min}, nil
	}
	if scale == 0 {
		scale = 1
	}
	plan, err := configuredEffectPlan(output, effect, scale, globalAngle, configuration)
	if err != nil {
		return image.Rectangle{}, err
	}
	support := dropShadowSampleSupport(plan)
	needed := image.Rectangle{
		Min: image.Pt(int(math.Floor(float64(output.Min.X)-plan.offsetX))-support,
			int(math.Floor(float64(output.Min.Y)-plan.offsetY))-support),
		Max: image.Pt(int(math.Ceil(float64(output.Max.X)-plan.offsetX))+support,
			int(math.Ceil(float64(output.Max.Y)-plan.offsetY))+support),
	}
	if effect.LayerConceals {
		needed = needed.Union(output)
	}
	if err := validEffectBounds(needed); err != nil {
		return image.Rectangle{}, err
	}
	return needed, nil
}

func configuredEffectPlan(
	bounds image.Rectangle, effect Effect, scale, globalAngle float64, configuration Options,
) (effectPlan, error) {
	plan, err := planEffect(bounds, effect, scale, globalAngle)
	if err != nil {
		return effectPlan{}, err
	}
	if effect.Kind != DropShadow || !configuration.ApproximateDropShadow {
		return plan, nil
	}
	plan.approximateShadow = true
	plan.deferConceal = configuration.DeferDropShadowConceal
	support := dropShadowSampleSupport(plan)
	plan.bounds = image.Rectangle{
		Min: image.Pt(bounds.Min.X-support+int(math.Floor(plan.offsetX)),
			bounds.Min.Y-support+int(math.Floor(plan.offsetY))),
		Max: image.Pt(bounds.Max.X+support+int(math.Ceil(plan.offsetX)),
			bounds.Max.Y+support+int(math.Ceil(plan.offsetY))),
	}
	if err := validEffectBounds(plan.bounds); err != nil {
		return effectPlan{}, err
	}
	return plan, nil
}

// dropShadowSampleSupport excludes zero-weight endpoints of the circular matte and tent kernel.
func dropShadowSampleSupport(plan effectPlan) int {
	morphologyExtent := int(math.Ceil(plan.radius))
	blurExtent := max(0, int(math.Ceil(plan.blurRadius))-1)
	return morphologyExtent + blurExtent
}

// dropShadowSpreadWeight defines the generic matte without the legacy size-three native brush samples.
// Together with bilinear displacement, normalized separable tent blur and final round-to-byte,
// it is the ApproximateDropShadow policy; color and opacity remain outside coverage.
func dropShadowSpreadWeight(radius, distanceSquared float64) float64 {
	return math.Max(0, math.Min(1, radius+1-math.Sqrt(distanceSquared)))
}
