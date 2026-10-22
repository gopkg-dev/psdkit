package psdrender

import (
	"context"
	"errors"
	"image"
	"math"
)

const geometryEpsilon = 1e-12

// Homography is a row-major three by three perspective matrix.
type Homography [9]float64

// ProjectionPlan caches only geometry and zero-mask row bounds, never per-pixel UV maps.
type ProjectionPlan struct {
	Left, Top, Width, Height int
	Inverse                  *Homography
	MaskSpans                []int
}

// PlacedCorners validates convex, ordered smart-object corners.
func PlacedCorners(placed *Placed) ([4]Point, error) {
	var points [4]Point
	if placed == nil {
		return points, errors.New("图层不是智能对象")
	}
	values := placed.NonAffineTransform
	if values == nil {
		values = placed.Transform
	}
	if len(values) != 8 {
		return points, errors.New("智能对象缺少有效四点透视坐标")
	}
	for index := range points {
		x, y := values[index*2], values[index*2+1]
		if !finite(x) || !finite(y) {
			return points, errors.New("智能对象缺少有效四点透视坐标")
		}
		points[index] = Point{X: x, Y: y}
	}
	firstSign := 0.0
	for index, point := range points {
		next, last := points[(index+1)%4], points[(index+2)%4]
		cross := float64((next.X-point.X)*(last.Y-next.Y)) - float64((next.Y-point.Y)*(last.X-next.X))
		if math.Abs(cross) < 1e-9 || !finite(cross) || (index > 0 && math.Signbit(cross) != math.Signbit(firstSign)) {
			return points, errors.New("智能对象透视四边形退化或交叉")
		}
		if index == 0 {
			firstSign = cross
		}
	}
	return points, nil
}

func matrixProduct(left, right Homography) Homography {
	// Explicit conversions round products before addition, matching JavaScript instead of fused multiply-add.
	var result Homography
	for row := range 3 {
		for col := range 3 {
			for inner := range 3 {
				result[row*3+col] += float64(left[row*3+inner] * right[inner*3+col])
			}
		}
	}
	return result
}

func normalizePoints(points [4]Point) ([4]Point, Homography, Homography, error) {
	var center Point
	for _, point := range points {
		center.X += point.X / 4
		center.Y += point.Y / 4
	}
	distance := 0.0
	for _, point := range points {
		distance += math.Hypot(point.X-center.X, point.Y-center.Y) / 4
	}
	if !(distance > 0) || !finite(distance) {
		return points, Homography{}, Homography{}, errors.New("透视控制点重合")
	}
	scale := math.Sqrt2 / distance
	var normalized [4]Point
	for index, point := range points {
		normalized[index] = Point{X: (point.X - center.X) * scale, Y: (point.Y - center.Y) * scale}
	}
	transform := Homography{scale, 0, -center.X * scale, 0, scale, -center.Y * scale, 0, 0, 1}
	inverse := Homography{1 / scale, 0, center.X, 0, 1 / scale, center.Y, 0, 0, 1}
	return normalized, transform, inverse, nil
}

// SolveHomography uses the frontend's normalized Gauss-Jordan solve and operation ordering.
func SolveHomography(from, to [4]Point) (Homography, error) {
	for _, point := range append(from[:], to[:]...) {
		if !finite(point.X) || !finite(point.Y) {
			return Homography{}, errors.New("透视控制点必须为有限数值")
		}
	}
	source, sourceTransform, _, err := normalizePoints(from)
	if err != nil {
		return Homography{}, err
	}
	target, _, targetInverse, err := normalizePoints(to)
	if err != nil {
		return Homography{}, err
	}
	var rows [8][9]float64
	for index := range 4 {
		x, y, tx, ty := source[index].X, source[index].Y, target[index].X, target[index].Y
		rows[index*2] = [9]float64{x, y, 1, 0, 0, 0, -tx * x, -tx * y, tx}
		rows[index*2+1] = [9]float64{0, 0, 0, x, y, 1, -ty * x, -ty * y, ty}
	}
	for col := range 8 {
		pivot := col
		for row := col + 1; row < 8; row++ {
			if math.Abs(rows[row][col]) > math.Abs(rows[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(rows[pivot][col]) <= geometryEpsilon {
			return Homography{}, errors.New("透视控制点退化或共线")
		}
		rows[col], rows[pivot] = rows[pivot], rows[col]
		divisor := rows[col][col]
		for item := col; item <= 8; item++ {
			rows[col][item] /= divisor
		}
		for row := range 8 {
			if row == col {
				continue
			}
			factor := rows[row][col]
			for item := col; item <= 8; item++ {
				rows[row][item] -= float64(factor * rows[col][item])
			}
		}
	}
	normalized := Homography{
		rows[0][8], rows[1][8], rows[2][8], rows[3][8], rows[4][8], rows[5][8], rows[6][8], rows[7][8], 1,
	}
	matrix := matrixProduct(targetInverse, matrixProduct(normalized, sourceTransform))
	scale := matrix[8]
	if math.Abs(scale) <= geometryEpsilon {
		scale = 0
		for _, value := range matrix {
			scale = math.Max(scale, math.Abs(value))
		}
	}
	for index := range matrix {
		matrix[index] /= scale
		if !finite(matrix[index]) {
			return Homography{}, errors.New("单应矩阵计算结果无效")
		}
	}
	cofactor0 := float64(normalized[4]*normalized[8]) - float64(normalized[5]*normalized[7])
	cofactor3 := float64(normalized[5]*normalized[6]) - float64(normalized[3]*normalized[8])
	cofactor6 := float64(normalized[3]*normalized[7]) - float64(normalized[4]*normalized[6])
	determinant := float64(normalized[0]*cofactor0) + float64(normalized[1]*cofactor3) +
		float64(normalized[2]*cofactor6)
	normalizedScale := 0.0
	for _, value := range normalized {
		normalizedScale = math.Max(normalizedScale, math.Abs(value))
	}
	if math.Abs(determinant) <= geometryEpsilon*normalizedScale*normalizedScale*normalizedScale {
		return Homography{}, errors.New("单应矩阵退化，无法进行逆向映射")
	}
	return matrix, nil
}

// CompileProjection clips transformed bounds to a document when width and height are positive.
func CompileProjection(layer *Layer, width, height int, forCompositing bool) (*ProjectionPlan, error) {
	if layer == nil {
		return nil, errors.New("图层不是智能对象")
	}
	corners, err := PlacedCorners(layer.Placed)
	if err != nil {
		return nil, err
	}
	left, top, right, bottom := corners[0].X, corners[0].Y, corners[0].X, corners[0].Y
	for _, point := range corners[1:] {
		left, top = math.Min(left, point.X), math.Min(top, point.Y)
		right, bottom = math.Max(right, point.X), math.Max(bottom, point.Y)
	}
	left, top, right, bottom = math.Floor(left), math.Floor(top), math.Ceil(right), math.Ceil(bottom)
	if width > 0 && height > 0 {
		left, top = math.Max(0, left), math.Max(0, top)
		right, bottom = math.Min(float64(width), right), math.Min(float64(height), bottom)
	}
	if right <= left || bottom <= top {
		return &ProjectionPlan{Width: 1, Height: 1}, nil
	}
	if right-left > MaxDimension || bottom-top > MaxDimension || math.Abs(left) > 1e12 || math.Abs(top) > 1e12 {
		return nil, ErrImageSize
	}
	if err := AssertImageSize(int(right-left), int(bottom-top)); err != nil {
		return nil, err
	}
	inverse, err := SolveHomography(corners, [4]Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}})
	if err != nil {
		return nil, err
	}
	plan := &ProjectionPlan{
		Left: int(left), Top: int(top), Width: int(right - left), Height: int(bottom - top), Inverse: &inverse,
	}
	if forCompositing && layer.Mask != nil && !layer.Mask.Disabled &&
		(layer.Mask.Feather == nil || *layer.Mask.Feather == 0) {
		plan.MaskSpans = make([]int, plan.Height*2)
		for y := 0; y < plan.Height; y++ {
			start, end := 0, plan.Width
			for start < end && LayerMaskAlpha(layer, plan.Left+start, plan.Top+y) == 0 {
				start++
			}
			for end > start && LayerMaskAlpha(layer, plan.Left+end-1, plan.Top+y) == 0 {
				end--
			}
			plan.MaskSpans[y*2], plan.MaskSpans[y*2+1] = start, end
		}
	}
	return plan, nil
}

// ProjectImage projects one fitted object and returns its document-space origin.
func ProjectImage(layer *Layer, source *image.NRGBA, plan *ProjectionPlan) (*image.NRGBA, image.Point, error) {
	return projectImage(context.Background(), layer, source, plan)
}

// ProjectImageContext projects one fitted object with cancellation during pixel sampling.
func ProjectImageContext(
	ctx context.Context, layer *Layer, source *image.NRGBA, plan *ProjectionPlan,
) (*image.NRGBA, image.Point, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return projectImage(ctx, layer, source, plan)
}

func projectImage(
	ctx context.Context, layer *Layer, source *image.NRGBA, plan *ProjectionPlan,
) (*image.NRGBA, image.Point, error) {
	if err := validateImage(source); err != nil {
		return nil, image.Point{}, err
	}
	if layer == nil || plan == nil {
		return nil, image.Point{}, errors.New("missing PSD projection plan")
	}
	result, err := EmptyImage(plan.Width, plan.Height)
	if err != nil {
		return nil, image.Point{}, err
	}
	origin := image.Pt(plan.Left, plan.Top)
	if plan.Inverse == nil {
		return result, origin, nil
	}
	h := *plan.Inverse
	for y := 0; y < plan.Height; y++ {
		if err := rowCancelled(ctx, y); err != nil {
			return nil, origin, err
		}
		start, end := 0, plan.Width
		if plan.MaskSpans != nil {
			start, end = plan.MaskSpans[y*2], plan.MaskSpans[y*2+1]
		}
		for x := start; x < end; x++ {
			if plan.MaskSpans != nil && LayerMaskAlpha(layer, plan.Left+x, plan.Top+y) == 0 {
				continue
			}
			dx, dy := float64(x+plan.Left)+.5, float64(y+plan.Top)+.5
			denominator := float64(h[6]*dx) + float64(h[7]*dy) + h[8]
			u := (float64(h[0]*dx) + float64(h[1]*dy) + h[2]) / denominator
			v := (float64(h[3]*dx) + float64(h[4]*dy) + h[5]) / denominator
			if !finite(u) || !finite(v) || u < 0 || u > 1 || v < 0 || v > 1 {
				continue
			}
			sample(source, float64(u*float64(source.Rect.Dx()))-.5,
				float64(v*float64(source.Rect.Dy()))-.5, result.Pix, y*result.Stride+x*4)
		}
	}
	return result, origin, nil
}

// RequiresWarpRendering recognizes exactly the unchanged-grid subset accepted by the editor.
func RequiresWarpRendering(placed *Placed) bool {
	if placed == nil || placed.Warp == nil {
		return false
	}
	warp := placed.Warp
	if len(warp.Unsupported) != 0 {
		return true
	}
	for _, value := range append([]float64{warp.Value, warp.Perspective, warp.PerspectiveOther}, warp.Values...) {
		if !finite(value) || value != 0 {
			return true
		}
	}
	if warp.Split || (warp.Style != "" && warp.Style != "none" && warp.Style != "custom") {
		return true
	}
	if !warp.HasEnvelope {
		return warp.Style == "custom"
	}
	if warp.UOrder != 4 || warp.VOrder != 4 || len(warp.MeshPoints) != 16 {
		return true
	}
	if warp.Rotate != "" && warp.Rotate != "horizontal" {
		return true
	}
	bounds := warp.Bounds
	if bounds == nil || !bounds.Pixels {
		return true
	}
	for _, value := range []float64{bounds.Left, bounds.Top, bounds.Right, bounds.Bottom} {
		if !finite(value) {
			return true
		}
	}
	width, height := bounds.Right-bounds.Left, bounds.Bottom-bounds.Top
	if width <= 0 || height <= 0 {
		return true
	}
	scale := math.Max(1, math.Max(math.Abs(bounds.Left), math.Max(math.Abs(bounds.Top),
		math.Max(math.Abs(bounds.Right), math.Abs(bounds.Bottom)))))
	epsilon := math.Min(1e-8, math.Max(1e-10, scale*(math.Nextafter(1, 2)-1)*8))
	if math.Abs(width-float64(placed.Width)) > epsilon || math.Abs(height-float64(placed.Height)) > epsilon {
		return true
	}
	for index, point := range warp.MeshPoints {
		if !finite(point.X) || !finite(point.Y) ||
			math.Abs((point.X-bounds.Left)-float64(index%4)*width/3) > epsilon ||
			math.Abs((point.Y-bounds.Top)-float64(index/4)*height/3) > epsilon {
			return true
		}
	}
	return false
}
