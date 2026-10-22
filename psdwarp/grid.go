package psdwarp

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// UniformGrid owns a certified uniform 10x10 or 13x13 custom control net.
// Each adjacent group of four controls defines one cubic piece, sharing its last
// row and column with its neighbor. Source UV coordinates span the whole net.
// Placement is affine; native projective composition is deliberately unsupported.
type UniformGrid struct {
	pieces    []Patch
	count     int
	content   image.Point
	knots     [2][5]float64
	hull      [4]float64
	reference [4]float64
	options   Options
}

// CompileUniformGrid copies and certifies an explicitly uniform quilt envelope.
// Internal source slices must divide Warp.Bounds equally. Outer slice guards may
// be the bounds themselves or Photoshop's paired 0.6-pixel guards. The complete
// typed slice window is normalized to the source domain, moving guarded interior
// knots slightly away from i/count. All controls share one control-hull mapping.
// A common P-matrix Jacobian certificate excludes overlaps between pieces,
// including across C0 seams where adjacent derivatives need not agree.
func CompileUniformGrid(
	ctx context.Context, placed *psdmodel.Placed, options ...Options,
) (*UniformGrid, error) {
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
	order := warp.DeformColumns
	if !warp.HasDeformColumns || !warp.HasDeformRows || warp.DeformRows != order ||
		order != 10 && order != 13 || warp.UOrder != 4 || warp.VOrder != 4 || !warp.Split ||
		warp.Style != "custom" || !warp.HasEnvelope || len(warp.MeshPoints) != order*order ||
		warp.Rotate != "" && warp.Rotate != "horizontal" || len(warp.Unsupported) != 0 ||
		len(placed.Unsupported) != 0 || warp.ClassID != "" && warp.ClassID != "quiltWarp" ||
		warp.MeshClass != "" && warp.MeshClass != "rationalPoint" ||
		warp.MeshUnits != [2]string{} && warp.MeshUnits != [2]string{"#Pxl", "#Pxl"} {
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
	if err := uniformGridSlices(warp, placed.Width, placed.Height, (order-1)/3); err != nil {
		return nil, fmt.Errorf("grid source slices: %w", err)
	}
	if err := presetAffinePlacement(placed); err != nil {
		return nil, fmt.Errorf("grid placement: %w", err)
	}
	values := placed.NonAffineTransform
	if values == nil {
		values = placed.Transform
	}
	forward, err := quadMatrix(values)
	if err != nil {
		return nil, err
	}
	inverse, err := inverseMatrix(forward)
	if err != nil {
		return nil, err
	}
	hull := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, point := range warp.MeshPoints {
		if !isFinite(point.X) || !isFinite(point.Y) || math.Abs(point.X) > 1e12 || math.Abs(point.Y) > 1e12 {
			return nil, ErrInvalid
		}
		hull[0], hull[1] = math.Min(hull[0], point.X), math.Min(hull[1], point.Y)
		hull[2], hull[3] = math.Max(hull[2], point.X), math.Max(hull[3], point.Y)
	}
	width, height := hull[2]-hull[0], hull[3]-hull[1]
	if width <= 0 || height <= 0 {
		return nil, ErrInvalid
	}
	count := (order - 1) / 3
	// Reserve every piece header before flattening any edges.
	if int64(1024+count*count*1024) > configuration.MaxBytes {
		return nil, ErrLimit
	}
	grid := &UniformGrid{pieces: make([]Patch, count*count), count: count,
		content: image.Pt(placed.Width, placed.Height), options: configuration,
		knots: gridSourceKnots(warp, count),
		hull:  [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}}
	var controls [169]psdmodel.Point
	for index, point := range warp.MeshPoints {
		controls[index] = psdmodel.Point{X: (point.X - hull[0]) / width, Y: (point.Y - hull[1]) / height}
	}
	reference, err := gridDirectionReference(ctx, controls[:order*order], order)
	if err != nil {
		return nil, err
	}
	grid.reference = reference
	work := 0
	for row := range count {
		for column := range count {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			piece := &grid.pieces[row*count+column]
			piece.forward, piece.inverse = forward, inverse
			piece.options = configuration
			piece.content = grid.content
			for y := range 4 {
				for x := range 4 {
					piece.controls[y*4+x] = controls[(row*3+y)*order+column*3+x]
				}
			}
			local, err := gridReference(piece.controls[0], piece.controls[3], piece.controls[12])
			if err != nil {
				return nil, fmt.Errorf("grid piece %d,%d inverse reference: %w", column, row, err)
			}
			piece.reference = [6]float64{local[0], local[1], local[2], local[3],
				piece.controls[0].X, piece.controls[0].Y}
			if err := certifyGridPiece(ctx, piece, reference, count, &work); err != nil {
				return nil, fmt.Errorf("grid piece %d,%d global certification: %w", column, row, err)
			}
			piece.hull = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
			var homogeneous [16][3]float64
			for index, control := range piece.controls {
				point, valid := applyMatrix(forward, control)
				if !valid || math.Abs(point.X) > 1e12 || math.Abs(point.Y) > 1e12 {
					return nil, ErrInvalid
				}
				homogeneous[index] = [3]float64{point.X, point.Y, 1}
				piece.hull[0], piece.hull[1] = math.Min(piece.hull[0], point.X), math.Min(piece.hull[1], point.Y)
				piece.hull[2], piece.hull[3] = math.Max(piece.hull[2], point.X), math.Max(piece.hull[3], point.Y)
			}
			for _, indices := range [4][4]int{{0, 1, 2, 3}, {3, 7, 11, 15}, {15, 14, 13, 12}, {12, 8, 4, 0}} {
				var curve [4][3]float64
				for index, controlIndex := range indices {
					curve[index] = homogeneous[controlIndex]
				}
				// flattenEdge accounts for its own temporary growth. Other pieces remain retained.
				piece.options.MaxBytes = configuration.MaxBytes - grid.WorkingBytes() + piece.WorkingBytes()
				if err := piece.flattenEdge(ctx, curve, 0); err != nil {
					return nil, err
				}
			}
			piece.options = configuration
			grid.hull[0], grid.hull[1] = math.Min(grid.hull[0], piece.hull[0]), math.Min(grid.hull[1], piece.hull[1])
			grid.hull[2], grid.hull[3] = math.Max(grid.hull[2], piece.hull[2]), math.Max(grid.hull[3], piece.hull[3])
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if grid.WorkingBytes() > configuration.MaxBytes {
		return nil, ErrLimit
	}
	return grid, nil
}

func uniformGridSlices(warp *psdmodel.Warp, width, height, count int) error {
	bounds := warp.Bounds
	if bounds == nil || !bounds.Pixels {
		return ErrUnsupported
	}
	w, h := bounds.Right-bounds.Left, bounds.Bottom-bounds.Top
	if !isFinite(bounds.Left) || !isFinite(bounds.Top) || !isFinite(w) || !isFinite(h) || w <= 0 || h <= 0 ||
		math.Abs(bounds.Left) > 1e12 || math.Abs(bounds.Top) > 1e12 {
		return ErrInvalid
	}
	if math.Abs(w-float64(width)) > 1e-8*math.Max(1, w) ||
		math.Abs(h-float64(height)) > 1e-8*math.Max(1, h) || len(warp.QuiltSlices) != 2 {
		return ErrUnsupported
	}
	var seen [2]bool
	for _, slice := range warp.QuiltSlices {
		axis := 0
		if slice.Axis == "quiltSliceY" {
			axis = 1
		} else if slice.Axis != "quiltSliceX" {
			return ErrUnsupported
		}
		if seen[axis] || slice.Location != "envelope" || slice.ClassID != "UntF" ||
			slice.Prefix != uint32(count+1) || slice.Field != slice.Axis || slice.Kind != "UnFl" ||
			slice.Unit != "#Pxl" || len(slice.Values) != count+1 {
			return ErrUnsupported
		}
		seen[axis] = true
		start, extent := bounds.Left, w
		if axis == 1 {
			start, extent = bounds.Top, h
		}
		tolerance := math.Max(1e-8, extent*1e-10)
		for index, value := range slice.Values {
			if !isFinite(value) {
				return ErrInvalid
			}
			if index > 0 && index < count && math.Abs(value-start-extent*float64(index)/float64(count)) > tolerance {
				return ErrUnsupported
			}
			if index > 0 && value <= slice.Values[index-1] {
				return ErrUnsupported
			}
		}
		first, last := start-slice.Values[0], slice.Values[count]-start-extent
		if math.Abs(first) <= tolerance && math.Abs(last) <= tolerance {
			continue
		}
		if math.Abs(first-0.6) > tolerance || math.Abs(last-0.6) > tolerance {
			return ErrUnsupported
		}
	}
	return nil
}

// WorkingBytes reports all retained grid and piece geometry, excluding caller-owned inputs.
func (g *UniformGrid) WorkingBytes() int64 {
	if g == nil {
		return 0
	}
	result := int64(1024)
	for index := range g.pieces {
		result += g.pieces[index].WorkingBytes()
	}
	return result
}

// Bounds returns a conservative whole-grid rectangle, optionally clipped in document coordinates.
func (g *UniformGrid) Bounds(canvas image.Rectangle) (image.Rectangle, error) {
	if g == nil {
		return image.Rectangle{}, ErrInvalid
	}
	return (&Patch{hull: g.hull}).Bounds(canvas)
}

// Forward maps whole-source unit-square coordinates into document coordinates.
func (g *UniformGrid) Forward(u, v float64) (psdmodel.Point, error) {
	if g == nil || g.count != 3 && g.count != 4 || len(g.pieces) != g.count*g.count ||
		!isFinite(u) || !isFinite(v) || u < 0 || u > 1 || v < 0 || v > 1 {
		return psdmodel.Point{}, ErrInvalid
	}
	column, localU := gridAxisParameter(&g.knots[0], g.count, u)
	row, localV := gridAxisParameter(&g.knots[1], g.count, v)
	// Fused multiply/subtract may retain a tiny signed seam residual after the
	// integer selection rounded the same scaled coordinate onto the seam.
	localU, localV = max(0, min(1, localU)), max(0, min(1, localV))
	return g.pieces[row*g.count+column].Forward(localU, localV)
}

// Inverse recovers whole-source UV coordinates. Exterior points return inside=false.
// Numerical failure inside any certified piece returns ErrConvergence.
func (g *UniformGrid) Inverse(
	ctx context.Context, point psdmodel.Point,
) (float64, float64, bool, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return 0, 0, false, err
	}
	if g == nil || g.count != 3 && g.count != 4 || len(g.pieces) != g.count*g.count ||
		!isFinite(point.X) || !isFinite(point.Y) {
		return 0, 0, false, ErrInvalid
	}
	var failure error
	for index := range g.pieces {
		piece := &g.pieces[index]
		if point.X < piece.hull[0]-1e-8 || point.X > piece.hull[2]+1e-8 ||
			point.Y < piece.hull[1]-1e-8 || point.Y > piece.hull[3]+1e-8 {
			continue
		}
		u, v, inside, err := piece.Inverse(ctx, point)
		if err != nil {
			if !errors.Is(err, ErrConvergence) {
				return 0, 0, false, err
			}
			failure = err
			continue
		}
		if inside {
			return gridAxisCoordinate(&g.knots[0], g.count, index%g.count, u),
				gridAxisCoordinate(&g.knots[1], g.count, index/g.count, v), true, nil
		}
	}
	return 0, 0, false, failure
}
