// Package psdwarp evaluates certified single-patch custom PSD warps with affine placement.
// An explicit mathematical entry point retains source-space cubic plus homography calculations.
package psdwarp

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

var (
	// ErrInvalid identifies malformed or degenerate geometry or image data.
	ErrInvalid = errors.New("psdwarp: invalid geometry")
	// ErrUnsupported identifies an unsupported or uncertifiable warp.
	ErrUnsupported = errors.New("psdwarp: unsupported warp")
	// ErrLimit identifies an exceeded pixel, output or numerical-work budget.
	ErrLimit = errors.New("psdwarp: limit exceeded")
	// ErrConvergence identifies an interior point whose inverse could not be recovered.
	ErrConvergence = errors.New("psdwarp: inverse did not converge")
)

// Options bounds output and per-sample numerical work.
// Defaults are 20 million pixels, 256 MiB and 24 Newton iterations.
// MaxBytes covers output, retained compiled geometry and raster scratch, excluding caller-owned input.
type Options struct {
	MaxPixels     int64
	MaxBytes      int64
	MaxIterations int
}

type matrix [9]float64
type segment struct{ first, second psdmodel.Point }

// Patch owns normalized controls and an immutable perspective transform.
// Only custom, horizontal, 4x4 unsplit envelopes with zero preset distortions are accepted.
// Injectivity is certified conservatively; an uncertifiable patch is rejected rather than approximated.
type Patch struct {
	controls  [16]psdmodel.Point
	forward   matrix
	inverse   matrix
	reference [6]float64
	hull      [4]float64
	content   image.Point
	edges     []segment
	options   Options
}

func warpContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func warpOptions(options []Options) (Options, error) {
	if len(options) > 1 {
		return Options{}, ErrInvalid
	}
	var value Options
	if len(options) == 1 {
		value = options[0]
	}
	if value.MaxPixels < 0 || value.MaxBytes < 0 || value.MaxIterations < 0 {
		return Options{}, ErrLimit
	}
	if value.MaxPixels == 0 {
		value.MaxPixels = 20_000_000
	}
	if value.MaxBytes == 0 {
		value.MaxBytes = 256 << 20
	}
	if value.MaxIterations == 0 {
		value.MaxIterations = 24
	}
	if value.MaxIterations > 128 {
		return Options{}, ErrLimit
	}
	return value, nil
}

// Compile validates a custom tensor-product cubic patch and copies its geometry.
// The control-point hull is mapped through NonAffineTransform when present,
// otherwise Transform. Photoshop's warped quad describes that hull; Warp.Bounds
// identifies the original content rectangle rather than a second expansion to apply.
// Selected placement must be affine because native projective Warp composition is unverified.
func Compile(ctx context.Context, placed *psdmodel.Placed, options ...Options) (*Patch, error) {
	return compilePatch(ctx, placed, false, options...)
}

// CompileMathematical computes a source-space cubic followed by a placement homography.
// It explicitly permits projective placement without claiming Photoshop-native composition or pixels.
// Callers may use Forward, Inverse, Bounds and Project for this mathematical model only.
// Consumers must select this model explicitly rather than rely on default projection.
func CompileMathematical(ctx context.Context, placed *psdmodel.Placed, options ...Options) (*Patch, error) {
	return compilePatch(ctx, placed, true, options...)
}

func compilePatch(
	ctx context.Context, placed *psdmodel.Placed, mathematical bool, options ...Options,
) (*Patch, error) {
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
	warp := placed.Warp
	if warp.Style != "custom" || len(warp.Unsupported) != 0 || warp.Split || !warp.HasEnvelope ||
		warp.UOrder != 4 || warp.VOrder != 4 ||
		len(warp.MeshPoints) != 16 || warp.Rotate != "" && warp.Rotate != "horizontal" || len(placed.Unsupported) != 0 {
		return nil, ErrUnsupported
	}
	for _, number := range [3]float64{warp.Value, warp.Perspective, warp.PerspectiveOther} {
		if !isFinite(number) || number != 0 {
			return nil, ErrUnsupported
		}
	}
	for _, number := range warp.Values {
		if !isFinite(number) || number != 0 {
			return nil, ErrUnsupported
		}
	}
	bounds := warp.Bounds
	if bounds == nil || !bounds.Pixels {
		return nil, ErrUnsupported
	}
	width, height := bounds.Right-bounds.Left, bounds.Bottom-bounds.Top
	if !isFinite(bounds.Left) || !isFinite(bounds.Top) || !isFinite(width) || !isFinite(height) ||
		width <= 0 || height <= 0 || math.Abs(bounds.Left) > 1e12 || math.Abs(bounds.Top) > 1e12 {
		return nil, ErrInvalid
	}
	if math.Abs(width-float64(placed.Width)) > 1e-8*math.Max(1, width) ||
		math.Abs(height-float64(placed.Height)) > 1e-8*math.Max(1, height) {
		return nil, ErrUnsupported
	}
	patch := &Patch{options: configuration, content: image.Pt(placed.Width, placed.Height)}
	controlHull := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, point := range warp.MeshPoints {
		if !isFinite(point.X) || !isFinite(point.Y) {
			return nil, ErrInvalid
		}
		controlHull[0], controlHull[1] = math.Min(controlHull[0], point.X), math.Min(controlHull[1], point.Y)
		controlHull[2], controlHull[3] = math.Max(controlHull[2], point.X), math.Max(controlHull[3], point.Y)
	}
	hullWidth, hullHeight := controlHull[2]-controlHull[0], controlHull[3]-controlHull[1]
	if !isFinite(hullWidth) || !isFinite(hullHeight) || hullWidth <= 0 || hullHeight <= 0 {
		return nil, ErrInvalid
	}
	for index, point := range warp.MeshPoints {
		point.X, point.Y = (point.X-controlHull[0])/hullWidth, (point.Y-controlHull[1])/hullHeight
		if !isFinite(point.X) || !isFinite(point.Y) || math.Abs(point.X) > 1e6 || math.Abs(point.Y) > 1e6 {
			return nil, ErrInvalid
		}
		patch.controls[index] = point
	}
	certify := patch.certify
	if mathematical {
		certify = func(ctx context.Context) error { return patch.certifyWithSubdivision(ctx, true) }
	}
	if err := certify(ctx); err != nil {
		return nil, err
	}
	if !mathematical {
		if err := presetAffinePlacement(placed); err != nil {
			return nil, err
		}
	}
	values := placed.NonAffineTransform
	if values == nil {
		values = placed.Transform
	}
	patch.forward, err = quadMatrix(values)
	if err != nil {
		return nil, err
	}
	patch.inverse, err = inverseMatrix(patch.forward)
	if err != nil {
		return nil, err
	}
	patch.hull = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	var homogeneous [16][3]float64
	sign := 0.0
	for index, point := range patch.controls {
		h := patch.forward
		q := [3]float64{h[0]*point.X + h[1]*point.Y + h[2], h[3]*point.X + h[4]*point.Y + h[5],
			h[6]*point.X + h[7]*point.Y + h[8]}
		if !isFinite(q[2]) || math.Abs(q[2]) < 1e-10 || index > 0 && math.Signbit(q[2]) != math.Signbit(sign) {
			return nil, ErrUnsupported
		}
		sign = q[2]
		homogeneous[index] = q
		x, y := q[0]/q[2], q[1]/q[2]
		if !isFinite(x) || !isFinite(y) || math.Abs(x) > 1e12 || math.Abs(y) > 1e12 {
			return nil, ErrInvalid
		}
		patch.hull[0], patch.hull[1] = math.Min(patch.hull[0], x), math.Min(patch.hull[1], y)
		patch.hull[2], patch.hull[3] = math.Max(patch.hull[2], x), math.Max(patch.hull[3], y)
	}
	for _, indices := range [4][4]int{{0, 1, 2, 3}, {3, 7, 11, 15}, {15, 14, 13, 12}, {12, 8, 4, 0}} {
		var curve [4][3]float64
		for index, controlIndex := range indices {
			curve[index] = homogeneous[controlIndex]
		}
		if err := patch.flattenEdge(ctx, curve, 0); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if patch.WorkingBytes() > configuration.MaxBytes {
		return nil, ErrLimit
	}
	return patch, nil
}

// WorkingBytes reports retained compiled geometry, excluding stack-local numerical work.
func (p *Patch) WorkingBytes() int64 {
	if p == nil {
		return 0
	}
	return 1024 + int64(cap(p.edges))*32
}

// Bounds returns a conservative document-space pixel rectangle covering the complete curved patch.
// Empty canvas means no clipping; a nonempty canvas clips to its own document-space rectangle.
func (p *Patch) Bounds(canvas image.Rectangle) (image.Rectangle, error) {
	if p == nil || canvas.Max.X < canvas.Min.X || canvas.Max.Y < canvas.Min.Y {
		return image.Rectangle{}, ErrInvalid
	}
	intBound := float64(int64(^uint(0) >> 1))
	for _, value := range p.hull {
		if value <= -intBound || value >= intBound {
			return image.Rectangle{}, ErrLimit
		}
	}
	result := image.Rect(int(math.Floor(p.hull[0])), int(math.Floor(p.hull[1])),
		int(math.Ceil(p.hull[2])), int(math.Ceil(p.hull[3])))
	if !canvas.Empty() {
		result = result.Intersect(canvas)
	}
	return result, nil
}

func basis(value float64) ([4]float64, [4]float64) {
	other := 1 - value
	return [4]float64{other * other * other, 3 * value * other * other, 3 * value * value * other, value * value * value},
		[4]float64{-3 * other * other, 3*other*other - 6*value*other, 6*value*other - 3*value*value, 3 * value * value}
}

func (p *Patch) surface(u, v float64) (psdmodel.Point, psdmodel.Point, psdmodel.Point) {
	bu, du := basis(u)
	bv, dv := basis(v)
	var point, alongU, alongV psdmodel.Point
	for row := range 4 {
		for column := range 4 {
			control := p.controls[row*4+column]
			point.X += control.X * bu[column] * bv[row]
			point.Y += control.Y * bu[column] * bv[row]
			alongU.X += control.X * du[column] * bv[row]
			alongU.Y += control.Y * du[column] * bv[row]
			alongV.X += control.X * bu[column] * dv[row]
			alongV.Y += control.Y * bu[column] * dv[row]
		}
	}
	return point, alongU, alongV
}

func applyMatrix(h matrix, point psdmodel.Point) (psdmodel.Point, bool) {
	denominator := h[6]*point.X + h[7]*point.Y + h[8]
	if !isFinite(denominator) || math.Abs(denominator) < 1e-14 {
		return psdmodel.Point{}, false
	}
	result := psdmodel.Point{X: (h[0]*point.X + h[1]*point.Y + h[2]) / denominator,
		Y: (h[3]*point.X + h[4]*point.Y + h[5]) / denominator}
	return result, isFinite(result.X) && isFinite(result.Y)
}

// Forward maps a source unit-square point to its warped document location.
func (p *Patch) Forward(u, v float64) (psdmodel.Point, error) {
	if p == nil || !isFinite(u) || !isFinite(v) || u < 0 || u > 1 || v < 0 || v > 1 {
		return psdmodel.Point{}, ErrInvalid
	}
	point, _, _ := p.surface(u, v)
	result, valid := applyMatrix(p.forward, point)
	if !valid {
		return psdmodel.Point{}, ErrInvalid
	}
	return result, nil
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func quadMatrix(values []float64) (matrix, error) {
	if len(values) != 8 {
		return matrix{}, ErrInvalid
	}
	var points [4]psdmodel.Point
	for index := range points {
		points[index] = psdmodel.Point{X: values[index*2], Y: values[index*2+1]}
		if !isFinite(points[index].X) || !isFinite(points[index].Y) {
			return matrix{}, ErrInvalid
		}
	}
	origin := points[0]
	scale := 0.0
	for _, point := range points {
		scale = math.Max(scale, math.Max(math.Abs(point.X-origin.X), math.Abs(point.Y-origin.Y)))
	}
	if !(scale > 0) {
		return matrix{}, ErrInvalid
	}
	for index := range points {
		points[index].X, points[index].Y = (points[index].X-origin.X)/scale, (points[index].Y-origin.Y)/scale
	}
	sign := 0.0
	for index, point := range points {
		next, last := points[(index+1)%4], points[(index+2)%4]
		cross := (next.X-point.X)*(last.Y-next.Y) - (next.Y-point.Y)*(last.X-next.X)
		if math.Abs(cross) < 1e-12 || index != 0 && math.Signbit(cross) != math.Signbit(sign) {
			return matrix{}, ErrInvalid
		}
		sign = cross
	}
	a, b, c, d := points[0], points[1], points[2], points[3]
	dx1, dy1, dx2, dy2 := b.X-c.X, b.Y-c.Y, d.X-c.X, d.Y-c.Y
	dx3, dy3 := a.X-b.X+c.X-d.X, a.Y-b.Y+c.Y-d.Y
	denominator := dx1*dy2 - dx2*dy1
	if math.Abs(denominator) < 1e-12 {
		return matrix{}, ErrInvalid
	}
	g, h := (dx3*dy2-dx2*dy3)/denominator, (dx1*dy3-dx3*dy1)/denominator
	return matrix{scale*(b.X-a.X+g*b.X) + origin.X*g, scale*(d.X-a.X+h*d.X) + origin.X*h, origin.X,
		scale*(b.Y-a.Y+g*b.Y) + origin.Y*g, scale*(d.Y-a.Y+h*d.Y) + origin.Y*h, origin.Y, g, h, 1}, nil
}

func inverseMatrix(h matrix) (matrix, error) {
	cofactors := matrix{h[4]*h[8] - h[5]*h[7], h[2]*h[7] - h[1]*h[8], h[1]*h[5] - h[2]*h[4],
		h[5]*h[6] - h[3]*h[8], h[0]*h[8] - h[2]*h[6], h[2]*h[3] - h[0]*h[5],
		h[3]*h[7] - h[4]*h[6], h[1]*h[6] - h[0]*h[7], h[0]*h[4] - h[1]*h[3]}
	determinant := h[0]*cofactors[0] + h[1]*cofactors[3] + h[2]*cofactors[6]
	if !isFinite(determinant) || math.Abs(determinant) < 1e-14 {
		return matrix{}, ErrInvalid
	}
	for index := range cofactors {
		cofactors[index] /= determinant
	}
	return cofactors, nil
}
