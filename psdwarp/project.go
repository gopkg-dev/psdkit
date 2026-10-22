package psdwarp

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// Inverse recovers source UV coordinates for a document point.
// Exterior points return inside=false. Failure for a point inside the certified
// boundary returns ErrConvergence, preventing silent transparent holes.
func (p *Patch) Inverse(ctx context.Context, point psdmodel.Point) (float64, float64, bool, error) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return 0, 0, false, err
	}
	if p == nil || !isFinite(point.X) || !isFinite(point.Y) {
		return 0, 0, false, ErrInvalid
	}
	target, valid := applyMatrix(p.inverse, point)
	if !valid {
		return 0, 0, false, nil
	}
	dx, dy := target.X-p.reference[4], target.Y-p.reference[5]
	u, v := p.reference[0]*dx+p.reference[1]*dy, p.reference[2]*dx+p.reference[3]*dy
	u, v = max(0, min(1, u)), max(0, min(1, v))
	for iteration := range p.options.MaxIterations {
		if iteration&3 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, 0, false, err
			}
		}
		position, du, dv := p.surface(u, v)
		rx, ry := position.X-target.X, position.Y-target.Y
		residual := rx*rx + ry*ry
		if residual <= 1e-18 {
			inside := u >= -1e-8 && u <= 1+1e-8 && v >= -1e-8 && v <= 1+1e-8
			return max(0, min(1, u)), max(0, min(1, v)), inside, nil
		}
		determinant := du.X*dv.Y - du.Y*dv.X
		if !isFinite(determinant) || math.Abs(determinant) < 1e-12 {
			break
		}
		stepU, stepV := (rx*dv.Y-ry*dv.X)/determinant, (ry*du.X-rx*du.Y)/determinant
		accepted := false
		amount := 1.0
		for range 12 {
			nextU, nextV := u-amount*stepU, v-amount*stepV
			if nextU >= -0.25 && nextU <= 1.25 && nextV >= -0.25 && nextV <= 1.25 {
				next, _, _ := p.surface(nextU, nextV)
				if (next.X-target.X)*(next.X-target.X)+(next.Y-target.Y)*(next.Y-target.Y) < residual {
					u, v, accepted = nextU, nextV, true
					break
				}
			}
			amount *= 0.5
		}
		if !accepted {
			break
		}
	}
	if p.contains(point) {
		return 0, 0, false, ErrConvergence
	}
	return 0, 0, false, nil
}

func (p *Patch) contains(point psdmodel.Point) bool {
	winding := 0
	for _, edge := range p.edges {
		a, b := edge.first, edge.second
		cross := (b.X-a.X)*(point.Y-a.Y) - (b.Y-a.Y)*(point.X-a.X)
		if a.Y <= point.Y && point.Y < b.Y && cross > 0 {
			winding++
		} else if b.Y <= point.Y && point.Y < a.Y && cross < 0 {
			winding--
		}
	}
	return winding != 0
}

// Project compiles and projects a supported custom patch into owned straight-alpha pixels.
// It does not bake layer opacity, masks, blend modes or effects into the replacement cache.
// Default projection rejects projective Warp placement whose native composition is unverified.
func Project(ctx context.Context, source *image.NRGBA, placed *psdmodel.Placed,
	canvas image.Rectangle, options ...Options,
) (*image.NRGBA, image.Point, error) {
	patch, err := Compile(ctx, placed, options...)
	if err != nil {
		return nil, image.Point{}, err
	}
	return patch.Project(ctx, source, canvas)
}

// Project samples a four-pixel lattice at the smaller of content and document resolution.
// The analytic Forward/Inverse methods evaluate the actual cubic surface instead.
// Caller-owned source pixels remain unchanged.
func (p *Patch) Project(ctx context.Context, source *image.NRGBA, canvas image.Rectangle) (
	*image.NRGBA, image.Point, error,
) {
	ctx = warpContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if p == nil || source == nil || source.Rect.Dx() <= 0 || source.Rect.Dy() <= 0 ||
		source.Rect.Max.X < source.Rect.Min.X || source.Rect.Max.Y < source.Rect.Min.Y ||
		source.Rect.Dx() > int(^uint(0)>>1)/4 || source.Stride < source.Rect.Dx()*4 ||
		len(source.Pix) < source.Rect.Dx()*4 ||
		source.Rect.Dy()-1 > (len(source.Pix)-source.Rect.Dx()*4)/source.Stride {
		return nil, image.Point{}, ErrInvalid
	}
	bounds, err := p.Bounds(canvas)
	if err != nil {
		return nil, image.Point{}, err
	}
	if bounds.Empty() {
		if p.WorkingBytes()+4 > p.options.MaxBytes {
			return nil, canvas.Min, ErrLimit
		}
		bounds = image.Rect(canvas.Min.X, canvas.Min.Y, canvas.Min.X+1, canvas.Min.Y+1)
		return image.NewNRGBA(image.Rect(0, 0, 1, 1)), bounds.Min, nil
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	available := p.options.MaxBytes - p.WorkingBytes()
	if width > p.options.MaxPixels/height || width > int64(^uint(0)>>1)/4/height ||
		available < 0 || width > available/4/height {
		return nil, bounds.Min, ErrLimit
	}
	raster, err := p.raster(ctx, source, bounds)
	if err != nil {
		return nil, bounds.Min, err
	}
	result := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	var scratch [128]uint32
	for row := range int(height) {
		if err := ctx.Err(); err != nil {
			return nil, bounds.Min, err
		}
		indices := raster.indices[raster.offsets[row]:raster.offsets[row+1]]
		for block := 0; block < int(width); block += 32 {
			end := min(block+32, int(width))
			candidates := raster.blockIndices(indices, bounds.Min.X+block, bounds.Min.X+end, scratch[:0])
			for column := block; column < end; column++ {
				if column&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, bounds.Min, err
					}
				}
				point := psdmodel.Point{X: float64(bounds.Min.X+column) + 0.5, Y: float64(bounds.Min.Y+row) + 0.5}
				var pixel [4]float64
				for _, cellIndex := range candidates {
					cell := &raster.cells[cellIndex]
					if point.X < float64(cell.box.Min.X) || point.X >= float64(cell.box.Max.X) {
						continue
					}
					u, v, covered := cell.inverse(point)
					if covered {
						pixel = rasterSample(source, u, v)
						pixel[3] *= cell.edgeCoverage(point, source, u, v)
						break
					}
				}
				offset := row*result.Stride + column*4
				for component := range pixel {
					result.Pix[offset+component] = byte(math.RoundToEven(max(0, min(255, pixel[component]))))
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, bounds.Min, err
	}
	return result, bounds.Min, nil
}
