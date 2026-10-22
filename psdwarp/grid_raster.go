package psdwarp

import (
	"context"
	"fmt"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// ProjectUniformGrid compiles and samples an explicitly uniform cubic quilt.
// Opacity, masks, blend modes and effects remain separate from the replacement pixels.
func ProjectUniformGrid(
	ctx context.Context, source *image.NRGBA, placed *psdmodel.Placed, canvas image.Rectangle, options ...Options,
) (*image.NRGBA, image.Point, error) {
	grid, err := CompileUniformGrid(ctx, placed, options...)
	if err != nil {
		return nil, image.Point{}, err
	}
	return grid.Project(ctx, source, canvas)
}

// ProjectWithOptions samples immutable grid geometry with this operation's resource limits.
// Its budget includes all retained pieces as well as the complete raster plan and output.
func (g *UniformGrid) ProjectWithOptions(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle, options ...Options,
) (*image.NRGBA, image.Point, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if g == nil {
		return nil, image.Point{}, ErrInvalid
	}
	configuration, err := warpOptions(options)
	if err != nil {
		return nil, image.Point{}, err
	}
	operation := *g
	operation.options = configuration
	return operation.Project(ctx, source, canvas)
}

// Project samples one whole-source lattice with premultiplied bilinear filtering and returns straight-alpha pixels.
// The lattice uses approximately four source/document pixels per cell and includes all
// piece boundaries. Internal seams never receive transparent-border coverage and
// clipped projections retain the same global sampling phase as full projections.
func (g *UniformGrid) Project(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle,
) (*image.NRGBA, image.Point, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if g == nil || g.count != 3 && g.count != 4 || len(g.pieces) != g.count*g.count ||
		source == nil || source.Rect.Dx() <= 0 || source.Rect.Dy() <= 0 ||
		source.Rect.Max.X < source.Rect.Min.X || source.Rect.Max.Y < source.Rect.Min.Y ||
		source.Rect.Dx() > int(^uint(0)>>1)/4 || source.Stride < source.Rect.Dx()*4 ||
		len(source.Pix) < source.Rect.Dx()*4 ||
		source.Rect.Dy()-1 > (len(source.Pix)-source.Rect.Dx()*4)/source.Stride {
		return nil, image.Point{}, ErrInvalid
	}
	bounds, err := g.Bounds(canvas)
	if err != nil {
		return nil, image.Point{}, err
	}
	if bounds.Empty() {
		if g.WorkingBytes()+4 > g.options.MaxBytes {
			return nil, canvas.Min, ErrLimit
		}
		return image.NewNRGBA(image.Rect(0, 0, 1, 1)), canvas.Min, nil
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	available := g.options.MaxBytes - g.WorkingBytes()
	if width > g.options.MaxPixels/height || width > int64(^uint(0)>>1)/4/height ||
		available < 0 || width > available/4/height {
		return nil, bounds.Min, ErrLimit
	}
	plan, err := g.raster(ctx, source, bounds)
	if err != nil {
		return nil, bounds.Min, err
	}
	result := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for row := range int(height) {
		if err := ctx.Err(); err != nil {
			return nil, bounds.Min, err
		}
		for column := range int(width) {
			if column&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, bounds.Min, err
				}
			}
			point := psdmodel.Point{X: float64(bounds.Min.X+column) + 0.5, Y: float64(bounds.Min.Y+row) + 0.5}
			var pixel [4]float64
			for _, cellIndex := range plan.indices[plan.offsets[row]:plan.offsets[row+1]] {
				cell := &plan.cells[cellIndex]
				if point.X < float64(cell.box.Min.X) || point.X >= float64(cell.box.Max.X) {
					continue
				}
				u, v, covered := cell.inverse(point)
				if !covered {
					continue
				}
				pixel = rasterSample(source, u, v)
				pixel[3] *= cell.edgeCoverage(point, source, u, v)
				break
			}
			offset := row*result.Stride + column*4
			for component := range pixel {
				result.Pix[offset+component] = byte(math.RoundToEven(max(0, min(255, pixel[component]))))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, bounds.Min, err
	}
	return result, bounds.Min, nil
}

func (g *UniformGrid) raster(
	ctx context.Context, source *image.NRGBA, bounds image.Rectangle,
) (*rasterPlan, error) {
	whole, err := g.Bounds(image.Rectangle{})
	if err != nil {
		return nil, err
	}
	w, h := min(g.content.X, whole.Dx()), min(g.content.Y, whole.Dy())
	nx, ny := max(1, (w-1)/4+1), max(1, (h-1)/4+1)
	// A single lattice spans all pieces; rounding its count upward places every
	// C0 boundary on a cell edge. Source UV uses the normalized typed interval of
	// that piece, while the geometric subdivisions and document phase stay fixed.
	nx, ny = g.count*((nx-1)/g.count+1), g.count*((ny-1)/g.count+1)
	maximumCells := min(int64(math.MaxUint32), int64(^uint(0)>>1), g.options.MaxBytes/160)
	if int64(nx) > maximumCells/int64(ny) {
		return nil, ErrLimit
	}
	cellCount := int64(nx) * int64(ny)
	remaining := g.options.MaxBytes - g.WorkingBytes()
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if remaining < 0 || height <= 0 || width <= 0 || width > remaining/4/height {
		return nil, ErrLimit
	}
	remaining -= width * height * 4
	if cellCount > math.MaxUint32 || cellCount > remaining/160 {
		return nil, ErrLimit
	}
	remaining -= cellCount * 160
	// Reserve both row-index arrays using subtraction, so individually valid
	// output and scratch costs cannot overflow a summed MaxInt64 budget.
	if height == math.MaxInt64 || height+1 > remaining/16 {
		return nil, ErrLimit
	}
	remaining -= (height + 1) * 16
	maximumTouches := min(int64(^uint(0)>>1), remaining/4)
	plan := &rasterPlan{cells: make([]rasterCell, 0, int(cellCount)), bounds: bounds,
		offsets: make([]int, bounds.Dy()+1)}
	var touches int64
	for row := range ny {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range nx {
			if column&63 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			u0 := gridAxisLattice(&g.knots[0], g.count, column, nx)
			v0 := gridAxisLattice(&g.knots[1], g.count, row, ny)
			u1 := gridAxisLattice(&g.knots[0], g.count, column+1, nx)
			v1 := gridAxisLattice(&g.knots[1], g.count, row+1, ny)
			du, dv := u1-u0, v1-v0
			if gridAxisUniform(&g.knots[0], g.count) {
				du = 1 / float64(nx)
			}
			if gridAxisUniform(&g.knots[1], g.count) {
				dv = 1 / float64(ny)
			}
			cell := rasterCell{u: u0, v: v0, du: du, dv: dv, right: 1, bottom: 1}
			marginU, marginV := 0.5/(float64(source.Rect.Dx())*cell.du),
				0.5/(float64(source.Rect.Dy())*cell.dv)
			if column == 0 {
				cell.left = -marginU
			}
			if column == nx-1 {
				cell.right += marginU
			}
			if row == 0 {
				cell.top = -marginV
			}
			if row == ny-1 {
				cell.bottom += marginV
			}
			parameters := [4][2]float64{{cell.u, cell.v}, {cell.u + cell.du, cell.v},
				{cell.u + cell.du, cell.v + cell.dv}, {cell.u, cell.v + cell.dv}}
			lowestX, lowestY, highestX, highestY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for index, parameter := range parameters {
				point, err := g.Forward(min(1, parameter[0]), min(1, parameter[1]))
				if err != nil {
					return nil, fmt.Errorf("grid raster corner %g,%g: %w", parameter[0], parameter[1], err)
				}
				cell.points[index] = point
				lowestX, lowestY = math.Min(lowestX, point.X), math.Min(lowestY, point.Y)
				highestX, highestY = math.Max(highestX, point.X), math.Max(highestY, point.Y)
			}
			sign := 0.0
			for index, first := range cell.points {
				second, third := cell.points[(index+1)%4], cell.points[(index+2)%4]
				cross := (second.X-first.X)*(third.Y-second.Y) - (second.Y-first.Y)*(third.X-second.X)
				if math.Abs(cross) < 1e-12 || index > 0 && math.Signbit(cross) != math.Signbit(sign) {
					return nil, ErrUnsupported
				}
				sign = cross
			}
			padding := 0.0
			for index, first := range cell.points {
				second := cell.points[(index+1)%4]
				padding = math.Max(padding, math.Hypot(second.X-first.X, second.Y-first.Y)*math.Max(marginU, marginV)*2)
			}
			cell.box = image.Rect(int(math.Floor(lowestX-padding)), int(math.Floor(lowestY-padding)),
				int(math.Ceil(highestX+padding)), int(math.Ceil(highestY+padding))).Intersect(bounds)
			if cell.box.Empty() {
				continue
			}
			span := int64(cell.box.Dy())
			if span > maximumTouches-touches {
				return nil, ErrLimit
			}
			touches += span
			for y := cell.box.Min.Y; y < cell.box.Max.Y; y++ {
				plan.offsets[y-bounds.Min.Y+1]++
			}
			plan.cells = append(plan.cells, cell)
		}
	}
	for index := 1; index < len(plan.offsets); index++ {
		plan.offsets[index] += plan.offsets[index-1]
	}
	plan.indices = make([]uint32, int(touches))
	positions := append([]int(nil), plan.offsets...)
	for index, cell := range plan.cells {
		if index&63 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		for y := cell.box.Min.Y; y < cell.box.Max.Y; y++ {
			row := y - bounds.Min.Y
			plan.indices[positions[row]] = uint32(index)
			positions[row]++
		}
	}
	return plan, nil
}
