package psdwarp

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// ProjectSupersampled explicitly selects the report's fixed uniform-grid sampling experiment.
// It uses one whole-source extended lattice, approximately 2.5 document pixels per cell,
// 4x4 document-space subpixels, RGB Keys a=0 and transparent-border alpha Keys a=-0.75.
// Signed channels are averaged over owned subpixels before clipping. This mathematical
// profile does not claim Photoshop parity. The default Project method remains unchanged.
// With no options, the compiled grid's limits apply; supplied options replace those limits.
func (g *UniformGrid) ProjectSupersampled(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle, options ...Options,
) (*image.NRGBA, image.Point, error) {
	return g.projectSupersampled(ctx, source, canvas, false, options...)
}

func (g *UniformGrid) projectSupersampled(
	ctx context.Context, source *image.NRGBA, canvas image.Rectangle, premultiplied bool, options ...Options,
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
		source.Rect.Dy()-1 > (len(source.Pix)-source.Rect.Dx()*4)/source.Stride ||
		canvas.Max.X < canvas.Min.X || canvas.Max.Y < canvas.Min.Y {
		return nil, image.Point{}, ErrInvalid
	}
	operation := *g
	if len(options) != 0 {
		configuration, err := warpOptions(options)
		if err != nil {
			return nil, image.Point{}, err
		}
		operation.options = configuration
	}
	if operation.WorkingBytes() > operation.options.MaxBytes {
		return nil, image.Point{}, ErrLimit
	}
	reference, err := operation.qualityReference(ctx, source.Rect.Size())
	if err != nil {
		return nil, image.Point{}, err
	}
	operation.reference = reference
	bounds, nx, ny, err := operation.qualityGeometry(ctx, source.Rect.Size(), canvas)
	if err != nil {
		return nil, image.Point{}, err
	}
	if bounds.Empty() {
		if operation.WorkingBytes()+4 > operation.options.MaxBytes {
			return nil, canvas.Min, ErrLimit
		}
		if err := ctx.Err(); err != nil {
			return nil, canvas.Min, err
		}
		return image.NewNRGBA(image.Rect(0, 0, 1, 1)), canvas.Min, nil
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	available := operation.options.MaxBytes - operation.WorkingBytes()
	if width > operation.options.MaxPixels/height || width > int64(^uint(0)>>1)/4/height ||
		available < 0 || width > available/4/height {
		return nil, bounds.Min, ErrLimit
	}
	plan, err := operation.qualityRaster(ctx, source.Rect.Size(), bounds, nx, ny)
	if err != nil {
		return nil, bounds.Min, err
	}
	result := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for row := range int(height) {
		if err := ctx.Err(); err != nil {
			return nil, bounds.Min, err
		}
		cells := plan.indices[plan.offsets[row]:plan.offsets[row+1]]
		for column := range int(width) {
			if column&31 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, bounds.Min, err
				}
			}
			var accumulator gridSampleAccumulator
			for subY := range 4 {
				for subX := range 4 {
					point := psdmodel.Point{
						X: float64(bounds.Min.X+column) + (float64(subX)+0.5)/4,
						Y: float64(bounds.Min.Y+row) + (float64(subY)+0.5)/4,
					}
					for _, cellIndex := range cells {
						cell := &plan.cells[cellIndex]
						if point.X < float64(cell.box.Min.X) || point.X >= float64(cell.box.Max.X) {
							continue
						}
						u, v, covered := cell.inverse(point)
						if covered {
							accumulator.add(gridRGBKeys0(source, u, v, premultiplied))
							break
						}
					}
				}
			}
			pixel := accumulator.pixel(premultiplied)
			offset := row*result.Stride + column*4
			for component := range pixel {
				result.Pix[offset+component] = byte(math.RoundToEven(pixel[component]))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, bounds.Min, err
	}
	return result, bounds.Min, nil
}

func (g *UniformGrid) forwardExtended(u, v float64) (psdmodel.Point, error) {
	if !isFinite(u) || !isFinite(v) {
		return psdmodel.Point{}, ErrInvalid
	}
	column, localU := gridAxisParameter(&g.knots[0], g.count, u)
	row, localV := gridAxisParameter(&g.knots[1], g.count, v)
	piece := &g.pieces[row*g.count+column]
	point, _, _ := piece.surface(localU, localV)
	result, valid := applyMatrix(piece.forward, point)
	if !valid || math.Abs(result.X) > 1e12 || math.Abs(result.Y) > 1e12 {
		return psdmodel.Point{}, ErrInvalid
	}
	return result, nil
}

func (g *UniformGrid) qualityGeometry(
	ctx context.Context, sourceSize image.Point, canvas image.Rectangle,
) (image.Rectangle, int, int, error) {
	marginU, marginV := 3.5/float64(sourceSize.X), 3.5/float64(sourceSize.Y)
	reference := g.reference
	hull := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	work := 0
	for row := range g.count {
		for column := range g.count {
			if err := ctx.Err(); err != nil {
				return image.Rectangle{}, 0, 0, err
			}
			piece := g.pieces[row*g.count+column]
			piece.controls = g.qualityExtendedControls(row, column, sourceSize)
			if err := certifyGridPiece(ctx, &piece, reference, g.count, &work); err != nil {
				return image.Rectangle{}, 0, 0, err
			}
			for _, control := range piece.controls {
				point, valid := applyMatrix(piece.forward, control)
				if !valid || math.Abs(point.X) > 1e12 || math.Abs(point.Y) > 1e12 {
					return image.Rectangle{}, 0, 0, ErrInvalid
				}
				hull[0], hull[1] = math.Min(hull[0], point.X), math.Min(hull[1], point.Y)
				hull[2], hull[3] = math.Max(hull[2], point.X), math.Max(hull[3], point.Y)
			}
		}
	}
	intBound := float64(int64(^uint(0) >> 1))
	for _, value := range hull {
		if value <= -intBound+2 || value >= intBound-2 {
			return image.Rectangle{}, 0, 0, ErrLimit
		}
	}
	if hull[2]-hull[0] > intBound-4 || hull[3]-hull[1] > intBound-4 {
		return image.Rectangle{}, 0, 0, ErrLimit
	}
	// This conservative control hull bounds the entire extrapolated surface, not a sampled approximation.
	bounds := image.Rect(int(math.Floor(hull[0]))-1, int(math.Floor(hull[1]))-1,
		int(math.Ceil(hull[2]))+1, int(math.Ceil(hull[3]))+1)
	if !canvas.Empty() {
		bounds = bounds.Intersect(canvas)
	}
	// Only lattice resolution follows the report's fixed 97x97 sampled extent estimate.
	sampled := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for row := range 97 {
		if err := ctx.Err(); err != nil {
			return image.Rectangle{}, 0, 0, err
		}
		v := -marginV + (1+2*marginV)*float64(row)/96
		for column := range 97 {
			u := -marginU + (1+2*marginU)*float64(column)/96
			point, err := g.forwardExtended(u, v)
			if err != nil {
				return image.Rectangle{}, 0, 0, err
			}
			sampled[0], sampled[1] = math.Min(sampled[0], point.X), math.Min(sampled[1], point.Y)
			sampled[2], sampled[3] = math.Max(sampled[2], point.X), math.Max(sampled[3], point.Y)
		}
	}
	nx := int(max(8, min(4096, math.Ceil(math.Max(sampled[2]-sampled[0], 1)/2.5))))
	ny := int(max(8, min(4096, math.Ceil(math.Max(sampled[3]-sampled[1], 1)/2.5))))
	return bounds, nx, ny, nil
}

func (g *UniformGrid) qualityReference(ctx context.Context, sourceSize image.Point) ([4]float64, error) {
	order := g.count*3 + 1
	var controls [169]psdmodel.Point
	for row := range g.count {
		for column := range g.count {
			if err := ctx.Err(); err != nil {
				return [4]float64{}, err
			}
			extended := g.qualityExtendedControls(row, column, sourceSize)
			for y := range 4 {
				for x := range 4 {
					controls[(row*3+y)*order+column*3+x] = extended[y*4+x]
				}
			}
		}
	}
	// The same bounded source-only proposal used at compile time now sees the complete expanded domain.
	// It changes only the certificate reference; qualityGeometry and every raster cell still require proofs.
	return gridDirectionReference(ctx, controls[:order*order], order)
}

func (g *UniformGrid) qualityExtendedControls(row, column int, sourceSize image.Point) [16]psdmodel.Point {
	marginU, marginV := 3.5/float64(sourceSize.X), 3.5/float64(sourceSize.Y)
	lowU, highU, lowV, highV := 0.0, 1.0, 0.0, 1.0
	if column == 0 {
		_, lowU = gridAxisParameter(&g.knots[0], g.count, -marginU)
	}
	if column == g.count-1 {
		_, highU = gridAxisParameter(&g.knots[0], g.count, 1+marginU)
	}
	if row == 0 {
		_, lowV = gridAxisParameter(&g.knots[1], g.count, -marginV)
	}
	if row == g.count-1 {
		_, highV = gridAxisParameter(&g.knots[1], g.count, 1+marginV)
	}
	return gridReparameterize(g.pieces[row*g.count+column].controls, lowU, highU, lowV, highV)
}

func gridReparameterize(controls [16]psdmodel.Point, lowU, highU, lowV, highV float64) [16]psdmodel.Point {
	var alongU, result [16]psdmodel.Point
	for row := range 4 {
		var curve [4]psdmodel.Point
		for column := range 4 {
			curve[column] = controls[row*4+column]
		}
		curve = gridReparameterizeCurve(curve, lowU, highU)
		for column := range 4 {
			alongU[row*4+column] = curve[column]
		}
	}
	for column := range 4 {
		var curve [4]psdmodel.Point
		for row := range 4 {
			curve[row] = alongU[row*4+column]
		}
		curve = gridReparameterizeCurve(curve, lowV, highV)
		for row := range 4 {
			result[row*4+column] = curve[row]
		}
	}
	return result
}

func gridReparameterizeCurve(curve [4]psdmodel.Point, low, high float64) [4]psdmodel.Point {
	evaluate := func(value float64) (psdmodel.Point, psdmodel.Point) {
		weights, derivatives := basis(value)
		var point, derivative psdmodel.Point
		for index, control := range curve {
			point.X, point.Y = point.X+control.X*weights[index], point.Y+control.Y*weights[index]
			derivative.X += control.X * derivatives[index]
			derivative.Y += control.Y * derivatives[index]
		}
		return point, derivative
	}
	first, startDerivative := evaluate(low)
	last, endDerivative := evaluate(high)
	amount := (high - low) / 3
	return [4]psdmodel.Point{first,
		{X: first.X + amount*startDerivative.X, Y: first.Y + amount*startDerivative.Y},
		{X: last.X - amount*endDerivative.X, Y: last.Y - amount*endDerivative.Y}, last}
}

func (g *UniformGrid) qualityRaster(
	ctx context.Context, sourceSize image.Point, bounds image.Rectangle, nx, ny int,
) (*rasterPlan, error) {
	if nx < g.count || ny < g.count || nx > 4096 || ny > 4096 || bounds.Empty() {
		return nil, ErrInvalid
	}
	cellCount := int64(nx) * int64(ny)
	remaining := g.options.MaxBytes - g.WorkingBytes()
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if remaining < 0 || width <= 0 || height <= 0 || width > remaining/4/height {
		return nil, ErrLimit
	}
	remaining -= width * height * 4
	if cellCount > math.MaxUint32 || cellCount > int64(^uint(0)>>1) || cellCount > remaining/160 {
		return nil, ErrLimit
	}
	remaining -= cellCount * 160
	if height >= remaining/16 {
		return nil, ErrLimit
	}
	remaining -= (height + 1) * 16
	if remaining < 256 {
		return nil, ErrLimit
	}
	remaining -= 256
	plan := &rasterPlan{cells: make([]rasterCell, 0, int(cellCount)), bounds: bounds,
		offsets: make([]int, bounds.Dy()+1)}
	marginU, marginV := 3.5/float64(sourceSize.X), 3.5/float64(sourceSize.Y)
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
			u0 := qualityAxis(&g.knots[0], g.count, column, nx, marginU)
			v0 := qualityAxis(&g.knots[1], g.count, row, ny, marginV)
			u1 := qualityAxis(&g.knots[0], g.count, column+1, nx, marginU)
			v1 := qualityAxis(&g.knots[1], g.count, row+1, ny, marginV)
			cell := rasterCell{u: u0, v: v0, du: u1 - u0, dv: v1 - v0, right: 1, bottom: 1}
			parameters := [4][2]float64{{cell.u, cell.v}, {cell.u + cell.du, cell.v},
				{cell.u + cell.du, cell.v + cell.dv}, {cell.u, cell.v + cell.dv}}
			lowestX, lowestY, highestX, highestY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for index, parameter := range parameters {
				point, err := g.forwardExtended(parameter[0], parameter[1])
				if err != nil {
					return nil, err
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
			if err := g.qualityCellCertificate(&cell); err != nil {
				return nil, err
			}
			cell.box = image.Rect(int(math.Floor(lowestX)), int(math.Floor(lowestY)),
				int(math.Ceil(highestX)), int(math.Ceil(highestY))).Intersect(bounds)
			if cell.box.Empty() {
				continue
			}
			maximumTouches := min(int64(^uint(0)>>1), remaining/4)
			if int64(cell.box.Dy()) > maximumTouches-touches {
				return nil, ErrLimit
			}
			touches += int64(cell.box.Dy())
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

// qualityAxis retains the requested cell count while placing every C0 join on a cell edge.
// Integer partitions avoid axis buffers; outside source margins extend only the first and last pieces.
func qualityAxis(knots *[5]float64, count, index, steps int, margin float64) float64 {
	piece := 0
	for piece+1 < count && index > (piece+1)*steps/count {
		piece++
	}
	startIndex, endIndex := piece*steps/count, (piece+1)*steps/count
	start, end := knots[piece], knots[piece+1]
	if piece == 0 {
		start = -margin
	}
	if piece == count-1 {
		end = 1 + margin
	}
	if index == startIndex {
		return start
	}
	if index == endIndex {
		return end
	}
	return start + (end-start)*float64(index-startIndex)/float64(endIndex-startIndex)
}

func (g *UniformGrid) qualityCellCertificate(cell *rasterCell) error {
	var points [4]psdmodel.Point
	for index, point := range cell.points {
		normalized, valid := applyMatrix(g.pieces[0].inverse, point)
		if !valid {
			return ErrInvalid
		}
		points[index] = normalized
	}
	transform := func(first, second psdmodel.Point, extent float64) psdmodel.Point {
		dx, dy := (second.X-first.X)/extent, (second.Y-first.Y)/extent
		return psdmodel.Point{X: g.reference[0]*dx + g.reference[1]*dy,
			Y: g.reference[2]*dx + g.reference[3]*dy}
	}
	du := [2]psdmodel.Point{transform(points[0], points[1], cell.du), transform(points[3], points[2], cell.du)}
	dv := [2]psdmodel.Point{transform(points[0], points[3], cell.dv), transform(points[1], points[2], cell.dv)}
	// The bilinear map's diagonal Jacobian entries and determinant are affine in either parameter.
	// Their four corner values therefore certify one common P-matrix reference over the entire lattice.
	for _, alongU := range du {
		for _, alongV := range dv {
			determinant := crossPoint(alongU, alongV)
			if !isFinite(alongU.X) || !isFinite(alongV.Y) || !isFinite(determinant) {
				return ErrInvalid
			}
			if alongU.X <= 1e-10 || alongV.Y <= 1e-10 || determinant <= 1e-10 {
				return ErrUnsupported
			}
		}
	}
	return nil
}
