package psdeffects

import "image"

// Estimate describes retained coverage and the largest simultaneous scratch allocation.
// Source alpha belongs to the caller and is excluded. SampleWork bounds morphology/convolution visits.
type Estimate struct {
	OutputBytes, ScratchBytes, SampleWork int64
	Planes                                int
	Bounds                                image.Rectangle
}

// EstimateWorking predicts the same bounded primitive work as Rasterize without allocating pixel buffers.
func EstimateWorking(bounds image.Rectangle, set *Set, options ...Options) (Estimate, error) {
	configuration, err := rasterConfiguration(options)
	if err != nil {
		return Estimate{}, err
	}
	if err := Validate(set); err != nil {
		return Estimate{}, err
	}
	if err := validEffectBounds(bounds); err != nil {
		return Estimate{}, err
	}
	estimate := Estimate{Bounds: bounds}
	if !set.Enabled || !set.EffectsVisible || bounds.Empty() {
		return estimate, nil
	}
	scale := set.Scale
	if scale == 0 {
		scale = 1
	}
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		plan, err := configuredEffectPlan(bounds, effect, scale, set.GlobalAngle, configuration)
		if err != nil {
			return Estimate{}, err
		}
		area, scratch, visits, err := effectCosts(plan, configuration.MaxWorkingBytes)
		if err != nil {
			return Estimate{}, err
		}
		if area > configuration.MaxWorkingBytes-estimate.OutputBytes-512 ||
			visits > configuration.MaxSampleWork-estimate.SampleWork {
			return Estimate{}, effectError(ErrLimit, "effects.estimate", "effect cache or work exceeds configured budgets")
		}
		estimate.OutputBytes += area + 512
		estimate.ScratchBytes = max(estimate.ScratchBytes, scratch)
		estimate.SampleWork += visits
		estimate.Planes++
		if effect.Kind != InnerShadow && effect.Kind != InnerGlow {
			estimate.Bounds = estimate.Bounds.Union(plan.bounds)
		}
		if estimate.Planes > configuration.MaxEffects ||
			estimate.ScratchBytes > configuration.MaxWorkingBytes-estimate.OutputBytes {
			return Estimate{}, effectError(ErrLimit, "effects.estimate", "retained planes and scratch exceed configured budgets")
		}
	}
	return estimate, nil
}

func validEffectBounds(bounds image.Rectangle) error {
	if bounds.Min.X > bounds.Max.X || bounds.Min.Y > bounds.Max.Y {
		return effectError(ErrInvalid, "effects.bounds", "source bounds have reversed edges")
	}
	const coordinateLimit = int64(1 << 40)
	for _, coordinate := range []int{bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y} {
		if int64(coordinate) < -coordinateLimit || int64(coordinate) > coordinateLimit {
			return effectError(ErrLimit, "effects.bounds", "coordinates exceed precise raster limits")
		}
	}
	return nil
}
