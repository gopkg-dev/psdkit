package psdwarp

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// PresetGeometry owns certified preset coordinates with explicitly selected mathematical pixel sampling.
// The supported preset is horizontal Arc Lower with no perspective distortion or quilt slices.
// Mathematical sampling does not claim Photoshop-native pixel equivalence.
type PresetGeometry struct{ patch *Patch }

// CompilePresetGeometry validates and copies native horizontal Arc Lower placement geometry.
// Bounds and source coordinates are preserved; unsupported schemas are never replaced by a flat quad.
// Placement must be an affine quad because projective preset composition is unverified.
func CompilePresetGeometry(
	ctx context.Context, placed *psdmodel.Placed, options ...Options,
) (*PresetGeometry, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := warpOptions(options)
	if err != nil {
		return nil, err
	}
	if placed == nil || placed.Warp == nil || placed.Width <= 0 || placed.Height <= 0 {
		return nil, ErrInvalid
	}
	if len(placed.Unsupported) != 0 {
		return nil, ErrUnsupported
	}
	if configuration.MaxBytes <= 16 {
		return nil, ErrLimit
	}
	controls, err := arcLowerControls(placed)
	if err != nil {
		return nil, err
	}
	if err := presetAffinePlacement(placed); err != nil {
		return nil, err
	}
	geometry := *placed
	warp := *placed.Warp
	warp.Style, warp.Rotate = "custom", "horizontal"
	warp.Value, warp.Perspective, warp.PerspectiveOther = 0, 0, 0
	warp.UOrder, warp.VOrder, warp.HasEnvelope = 4, 4, true
	warp.MeshPoints = controls[:]
	geometry.Warp = &warp
	configuration.MaxBytes -= 16
	patch, err := Compile(ctx, &geometry, configuration)
	if err != nil {
		return nil, err
	}
	return &PresetGeometry{patch: patch}, nil
}

// presetAffinePlacement excludes unverified projective composition of preset controls.
func presetAffinePlacement(placed *psdmodel.Placed) error {
	values := placed.NonAffineTransform
	if values == nil {
		values = placed.Transform
	}
	if _, err := quadMatrix(values); err != nil {
		return err
	}
	scale, precision := 0.0, 0.0
	for index, value := range values {
		scale = math.Max(scale, math.Abs(value-values[index%2]))
		magnitude := math.Abs(value)
		precision = math.Max(precision, math.Nextafter(magnitude, math.Inf(1))-magnitude)
	}
	// Local edge extent bounds relative error; ULPs account for large coordinate translations.
	tolerance := scale*1e-10 + 8*precision
	dx := (values[4] - values[2]) - (values[6] - values[0])
	dy := (values[5] - values[3]) - (values[7] - values[1])
	if math.Abs(dx) > tolerance || math.Abs(dy) > tolerance {
		return ErrUnsupported
	}
	return nil
}

// WorkingBytes reports retained immutable geometry including this wrapper.
func (g *PresetGeometry) WorkingBytes() int64 {
	if g == nil || g.patch == nil {
		return 0
	}
	return 16 + g.patch.WorkingBytes()
}

// Forward maps a source unit-square point to its warped document location.
func (g *PresetGeometry) Forward(u, v float64) (psdmodel.Point, error) {
	if g == nil {
		return psdmodel.Point{}, ErrInvalid
	}
	return g.patch.Forward(u, v)
}

// Inverse recovers a source unit-square point from a warped document location.
func (g *PresetGeometry) Inverse(
	ctx context.Context, point psdmodel.Point,
) (float64, float64, bool, error) {
	if g == nil || g.patch == nil {
		return 0, 0, false, ErrInvalid
	}
	return g.patch.Inverse(ctx, point)
}

// Bounds returns a conservative curved document rectangle, optionally clipped to a canvas.
func (g *PresetGeometry) Bounds(canvas image.Rectangle) (image.Rectangle, error) {
	if g == nil {
		return image.Rectangle{}, ErrInvalid
	}
	return g.patch.Bounds(canvas)
}
