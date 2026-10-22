package psdwarp

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type rasterCell struct {
	points       [4]psdmodel.Point
	box          image.Rectangle
	u, v, du, dv float64
	left, right  float64
	top, bottom  float64
}

type rasterPlan struct {
	cells   []rasterCell
	offsets []int
	indices []uint32
	bounds  image.Rectangle
}

func (p *Patch) raster(ctx context.Context, source *image.NRGBA, bounds image.Rectangle) (*rasterPlan, error) {
	// The native lattice resolves the smaller of source and complete document-space extents.
	// Analytic Forward and Inverse remain separate from this raster approximation.
	whole, err := p.Bounds(image.Rectangle{})
	if err != nil {
		return nil, err
	}
	w, h := min(p.content.X, whole.Dx()), min(p.content.Y, whole.Dy())
	nx, ny := max(1, (w-1)/4+1), max(1, (h-1)/4+1)
	maximumCells := min(int64(math.MaxUint32), int64(^uint(0)>>1), p.options.MaxBytes/160)
	if int64(nx) > maximumCells/int64(ny) {
		return nil, ErrLimit
	}
	count := int64(nx) * int64(ny)
	baseBytes := p.WorkingBytes() + int64(bounds.Dx())*int64(bounds.Dy())*4 +
		count*160 + int64(bounds.Dy()+1)*16
	if count > math.MaxUint32 || baseBytes > p.options.MaxBytes {
		return nil, ErrLimit
	}
	plan := &rasterPlan{cells: make([]rasterCell, 0, int(count)), bounds: bounds,
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
			cell := rasterCell{u: float64(column) / float64(nx), v: float64(row) / float64(ny),
				du: 1 / float64(nx), dv: 1 / float64(ny), right: 1, bottom: 1}
			marginU, marginV := 0.5*float64(nx)/float64(source.Rect.Dx()),
				0.5*float64(ny)/float64(source.Rect.Dy())
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
				point, err := p.Forward(min(1, parameter[0]), min(1, parameter[1]))
				if err != nil {
					return nil, err
				}
				cell.points[index] = point
				lowestX, lowestY = math.Min(lowestX, point.X), math.Min(lowestY, point.Y)
				highestX, highestY = math.Max(highestX, point.X), math.Max(highestY, point.Y)
			}
			sign := 0.0
			for index, a := range cell.points {
				b, c := cell.points[(index+1)%4], cell.points[(index+2)%4]
				cross := (b.X-a.X)*(c.Y-b.Y) - (b.Y-a.Y)*(c.X-b.X)
				if math.Abs(cross) < 1e-12 || index > 0 && math.Signbit(cross) != math.Signbit(sign) {
					return nil, ErrUnsupported
				}
				sign = cross
			}
			padding := 0.0
			for index, a := range cell.points {
				b := cell.points[(index+1)%4]
				padding = math.Max(padding, math.Hypot(b.X-a.X, b.Y-a.Y)*math.Max(marginU, marginV)*2)
			}
			cell.box = image.Rect(int(math.Floor(lowestX-padding)), int(math.Floor(lowestY-padding)),
				int(math.Ceil(highestX+padding)), int(math.Ceil(highestY+padding))).Intersect(bounds)
			if cell.box.Empty() {
				continue
			}
			touches += int64(cell.box.Dy())
			if touches > min(int64(^uint(0)>>1), (p.options.MaxBytes-baseBytes)/4) {
				return nil, ErrLimit
			}
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

func crossPoint(a, b psdmodel.Point) float64 { return a.X*b.Y - a.Y*b.X }

func (cell *rasterCell) inverse(point psdmodel.Point) (float64, float64, bool) {
	a, b, c, d := cell.points[0], cell.points[1], cell.points[2], cell.points[3]
	e := psdmodel.Point{X: b.X - a.X, Y: b.Y - a.Y}
	f := psdmodel.Point{X: d.X - a.X, Y: d.Y - a.Y}
	g := psdmodel.Point{X: a.X - b.X + c.X - d.X, Y: a.Y - b.Y + c.Y - d.Y}
	q := psdmodel.Point{X: point.X - a.X, Y: point.Y - a.Y}
	qa, qb, qc := -crossPoint(e, g), crossPoint(q, g)-crossPoint(e, f), crossPoint(q, f)
	var roots [2]float64
	rootCount := 1
	if math.Abs(qa) < 1e-12 {
		if math.Abs(qb) < 1e-12 {
			return 0, 0, false
		}
		roots[0] = -qc / qb
	} else {
		discriminant := qb*qb - 4*qa*qc
		if discriminant < 0 {
			return 0, 0, false
		}
		stable := -0.5 * (qb + math.Copysign(math.Sqrt(discriminant), qb))
		roots[0] = stable / qa
		if stable != 0 {
			roots[1], rootCount = qc/stable, 2
		}
	}
	for _, s := range roots[:rootCount] {
		if s < cell.left-1e-9 || s > cell.right+1e-9 {
			continue
		}
		denX, denY := f.X+g.X*s, f.Y+g.Y*s
		var t float64
		if math.Abs(denX) > math.Abs(denY) {
			t = (q.X - e.X*s) / denX
		} else {
			t = (q.Y - e.Y*s) / denY
		}
		if !isFinite(t) || t < cell.top-1e-9 || t > cell.bottom+1e-9 {
			continue
		}
		return cell.u + s*cell.du, cell.v + t*cell.dv, true
	}
	return 0, 0, false
}

func edgeSupport(value float64, extent int) float64 {
	coordinate := value*float64(extent) - 0.5
	return max(0, min(1, coordinate+1, float64(extent)-coordinate))
}

func lineCoverage(point, a, b, interior psdmodel.Point) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	distance := (dx*(point.Y-a.Y) - dy*(point.X-a.X)) / math.Max(math.Abs(dx), math.Abs(dy))
	if dx*(interior.Y-a.Y)-dy*(interior.X-a.X) < 0 {
		distance = -distance
	}
	return max(0, min(1, distance+0.5))
}

func (cell *rasterCell) edgeCoverage(point psdmodel.Point, source *image.NRGBA, u, v float64) float64 {
	// Interior cells have no patch edge; rasterSample already supplies texture-border support.
	if cell.left == 0 && cell.right == 1 && cell.top == 0 && cell.bottom == 1 {
		return 1
	}
	coverageU, coverageV := 1.0, 1.0
	center := psdmodel.Point{}
	for _, corner := range cell.points {
		center.X += corner.X / 4
		center.Y += corner.Y / 4
	}
	if cell.left < 0 {
		coverageU = min(coverageU, lineCoverage(point, cell.points[3], cell.points[0], center))
	}
	if cell.right > 1 {
		coverageU = min(coverageU, lineCoverage(point, cell.points[1], cell.points[2], center))
	}
	if cell.top < 0 {
		coverageV = min(coverageV, lineCoverage(point, cell.points[0], cell.points[1], center))
	}
	if cell.bottom > 1 {
		coverageV = min(coverageV, lineCoverage(point, cell.points[2], cell.points[3], center))
	}
	supportU, supportV := edgeSupport(u, source.Rect.Dx()), edgeSupport(v, source.Rect.Dy())
	if supportU == 0 || supportV == 0 {
		return 0
	}
	// The texture's transparent border already supplies support at source-pixel resolution.
	// Native curve coverage is capped independently at one document pixel after magnification.
	return min(supportU, coverageU) / supportU * min(supportV, coverageV) / supportV
}

func rasterSample(source *image.NRGBA, u, v float64) [4]float64 {
	x, y := u*float64(source.Rect.Dx())-0.5, v*float64(source.Rect.Dy())-0.5
	left, top := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(left), y-float64(top)
	var result [4]float64
	for row := range 2 {
		for column := range 2 {
			sx, sy := left+column, top+row
			if sx < 0 || sy < 0 || sx >= source.Rect.Dx() || sy >= source.Rect.Dy() {
				continue
			}
			wx, wy := 1-fx, 1-fy
			if column != 0 {
				wx = fx
			}
			if row != 0 {
				wy = fy
			}
			offset := sy*source.Stride + sx*4
			weight := wx * wy * float64(source.Pix[offset+3])
			result[3] += weight
			for component := range 3 {
				result[component] += float64(source.Pix[offset+component]) * weight
			}
		}
	}
	if result[3] > 0 {
		for component := range 3 {
			result[component] /= result[3]
		}
	}
	return result
}
