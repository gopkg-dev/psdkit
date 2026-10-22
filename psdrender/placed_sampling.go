package psdrender

import (
	"context"
	"errors"
	"image"
	"math"
)

// ErrUnsupportedCrop identifies transparent source borders whose transformed support is not calibrated.
var ErrUnsupportedCrop = errors.New("PSD transparent-border projection requires axis-aligned placement")

// PlacedSamplingOptions selects additional explicitly approximate prototype-canvas sampling.
type PlacedSamplingOptions struct {
	// ApproximateTransparentBorder permits non-axis-aligned transparent source borders.
	// It samples the complete original canvas with premultiplied bilinear interpolation.
	// Placement UV, output origin and mask frame remain fixed; Photoshop pixel equivalence is not guaranteed.
	ApproximateTransparentBorder bool
}

// ProjectPlacedContentsContext projects a PSD/PSB prototype canvas through its placement.
// Transparent outer borders use native ink support for calibrated axis-aligned placements.
// The complete canvas UV mapping and cache rectangle remain fixed; masks remain separate.
// Image-print fitting retains the legacy ProjectImageContext sampling contract.
func ProjectPlacedContentsContext(ctx context.Context, layer *Layer, source *image.NRGBA,
	plan *ProjectionPlan,
) (*image.NRGBA, image.Point, error) {
	return ProjectPlacedContentsWithOptions(ctx, layer, source, plan, PlacedSamplingOptions{})
}

// ProjectPlacedContentsWithOptions preserves the strict prototype sampler unless an approximation is selected.
// ApproximateTransparentBorder falls back only when transformed transparent support causes ErrUnsupportedCrop.
// The original canvas, placement metadata and projection plan are never cropped or modified.
func ProjectPlacedContentsWithOptions(
	ctx context.Context, layer *Layer, source *image.NRGBA, plan *ProjectionPlan, options PlacedSamplingOptions,
) (*image.NRGBA, image.Point, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, image.Point{}, err
	}
	if err := validateImage(source); err != nil {
		return nil, image.Point{}, err
	}
	if layer == nil || plan == nil {
		return nil, image.Point{}, errors.New("missing PSD projection plan")
	}
	ink, err := sourceInkBounds(ctx, source)
	if err != nil {
		return nil, image.Point{}, err
	}
	if ink == image.Rect(0, 0, source.Rect.Dx(), source.Rect.Dy()) {
		return projectImage(ctx, layer, source, plan)
	}
	pixels, origin, err := projectCroppedImage(ctx, layer, source, plan, ink)
	if !options.ApproximateTransparentBorder || !errors.Is(err, ErrUnsupportedCrop) {
		return pixels, origin, err
	}
	if err := AssertImageSize(plan.Width, plan.Height); err != nil {
		return nil, origin, err
	}
	if plan.MaskSpans != nil {
		for row := 0; row < plan.Height; row++ {
			start, end := plan.MaskSpans[row*2], plan.MaskSpans[row*2+1]
			if start < 0 || end < start || end > plan.Width {
				return nil, origin, ErrImageSize
			}
		}
	}
	pixels, origin, err = projectImage(ctx, layer, source, plan)
	if err != nil {
		return nil, origin, err
	}
	if err := ctx.Err(); err != nil {
		return nil, origin, err
	}
	return pixels, origin, nil
}

func sourceInkBounds(ctx context.Context, source *image.NRGBA) (image.Rectangle, error) {
	width, height := source.Rect.Dx(), source.Rect.Dy()
	left, top, right, bottom := width, height, 0, 0
	for row := range height {
		if err := ctx.Err(); err != nil {
			return image.Rectangle{}, err
		}
		for column := range width {
			if column&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return image.Rectangle{}, err
				}
			}
			if source.Pix[row*source.Stride+column*4+3] == 0 {
				continue
			}
			left, top = min(left, column), min(top, row)
			right, bottom = max(right, column+1), max(bottom, row+1)
		}
	}
	if right <= left || bottom <= top {
		return image.Rectangle{}, nil
	}
	return image.Rect(left, top, right, bottom), nil
}

func projectCroppedImage(ctx context.Context, layer *Layer, source *image.NRGBA, plan *ProjectionPlan,
	ink image.Rectangle,
) (*image.NRGBA, image.Point, error) {
	origin := image.Pt(plan.Left, plan.Top)
	if plan.MaskSpans != nil && len(plan.MaskSpans) < plan.Height*2 {
		return nil, origin, ErrImageSize
	}
	if ink.Empty() || plan.Inverse == nil {
		pixels, err := EmptyImage(plan.Width, plan.Height)
		return pixels, origin, err
	}
	corners, err := PlacedCorners(layer.Placed)
	if err != nil {
		return nil, origin, err
	}
	coordinatesEqual := func(a, b float64) bool { return math.Abs(a-b) <= 1e-8 }
	horizontal := coordinatesEqual(corners[0].Y, corners[1].Y) && coordinatesEqual(corners[2].Y, corners[3].Y) &&
		coordinatesEqual(corners[0].X, corners[3].X) && coordinatesEqual(corners[1].X, corners[2].X)
	vertical := coordinatesEqual(corners[0].X, corners[1].X) && coordinatesEqual(corners[2].X, corners[3].X) &&
		coordinatesEqual(corners[0].Y, corners[3].Y) && coordinatesEqual(corners[1].Y, corners[2].Y)
	if !horizontal && !vertical {
		return nil, origin, ErrUnsupportedCrop
	}
	forward, err := SolveHomography([4]Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}}, corners)
	if err != nil {
		return nil, origin, err
	}
	lowestX, lowestY, highestX, highestY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, point := range [4]image.Point{ink.Min, {X: ink.Max.X, Y: ink.Min.Y}, ink.Max,
		{X: ink.Min.X, Y: ink.Max.Y}} {
		u, v := float64(point.X)/float64(source.Rect.Dx()), float64(point.Y)/float64(source.Rect.Dy())
		denominator := forward[6]*u + forward[7]*v + forward[8]
		x, y := (forward[0]*u+forward[1]*v+forward[2])/denominator,
			(forward[3]*u+forward[4]*v+forward[5])/denominator
		if !finite(x) || !finite(y) {
			return nil, origin, ErrImageSize
		}
		lowestX, lowestY, highestX, highestY = min(lowestX, x), min(lowestY, y), max(highestX, x), max(highestY, y)
	}
	paint := image.Rect(int(math.Floor(lowestX+1e-9)), int(math.Floor(lowestY+1e-9)),
		int(math.Ceil(highestX-1e-9)), int(math.Ceil(highestY-1e-9)))
	paint = paint.Intersect(image.Rectangle{Min: origin, Max: origin.Add(image.Pt(plan.Width, plan.Height))})
	result, err := EmptyImage(plan.Width, plan.Height)
	if err != nil {
		return nil, origin, err
	}
	h := *plan.Inverse
	for row := paint.Min.Y; row < paint.Max.Y; row++ {
		if err := ctx.Err(); err != nil {
			return nil, origin, err
		}
		start, end := paint.Min.X, paint.Max.X
		if plan.MaskSpans != nil {
			start = max(start, origin.X+plan.MaskSpans[(row-origin.Y)*2])
			end = min(end, origin.X+plan.MaskSpans[(row-origin.Y)*2+1])
		}
		for column := start; column < end; column++ {
			if plan.MaskSpans != nil && LayerMaskAlpha(layer, column, row) == 0 {
				continue
			}
			x, y := float64(column)+0.5, float64(row)+0.5
			denominator := h[6]*x + h[7]*y + h[8]
			u, v := (h[0]*x+h[1]*y+h[2])/denominator, (h[3]*x+h[4]*y+h[5])/denominator
			if !finite(u) || !finite(v) || u < 0 || u > 1 || v < 0 || v > 1 {
				continue
			}
			sampleInk(source, ink, u*float64(source.Rect.Dx())-0.5, v*float64(source.Rect.Dy())-0.5,
				result.Pix, (row-origin.Y)*result.Stride+(column-origin.X)*4)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, origin, err
	}
	return result, origin, nil
}

func sampleInk(source *image.NRGBA, ink image.Rectangle, x, y float64, target []byte, offset int) {
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(ix), y-float64(iy)
	var alpha, red, green, blue float64
	for row := range 2 {
		for column := range 2 {
			sx, sy := max(ink.Min.X, min(ink.Max.X-1, ix+column)), max(ink.Min.Y, min(ink.Max.Y-1, iy+row))
			index := sy*source.Stride + sx*4
			wx, wy := 1-fx, 1-fy
			if column != 0 {
				wx = fx
			}
			if row != 0 {
				wy = fy
			}
			weight := wx * wy * float64(source.Pix[index+3]) / 255
			alpha += weight
			red += float64(source.Pix[index]) * weight
			green += float64(source.Pix[index+1]) * weight
			blue += float64(source.Pix[index+2]) * weight
		}
	}
	if alpha > 0 {
		target[offset], target[offset+1] = ClampByte(red/alpha), ClampByte(green/alpha)
		target[offset+2] = ClampByte(blue / alpha)
	}
	target[offset+3] = ClampByte(alpha * 255)
}
