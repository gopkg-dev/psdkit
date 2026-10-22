package psdrender

import (
	"errors"
	"image"
	"math"
)

// CompileProjectionRegion retains complete placement geometry while clipping only its output allocation.
// It never allocates or checks the complete transformed layer against the eager canvas pixel limit.
// Empty intersections return a zero-sized plan; callers can skip source resolution for that plan.
func CompileProjectionRegion(layer *Layer, canvas, rectangle image.Rectangle) (*ProjectionPlan, error) {
	if canvas.Empty() || rectangle.Min.X < canvas.Min.X || rectangle.Min.Y < canvas.Min.Y ||
		rectangle.Max.X < rectangle.Min.X || rectangle.Max.Y < rectangle.Min.Y ||
		rectangle.Max.X > canvas.Max.X || rectangle.Max.Y > canvas.Max.Y {
		return nil, errors.New("projection region must lie inside the document canvas")
	}
	if layer == nil {
		return nil, errors.New("missing placed projection layer")
	}
	corners, err := PlacedCorners(layer.Placed)
	if err != nil {
		return nil, err
	}
	left, top, right, bottom := corners[0].X, corners[0].Y, corners[0].X, corners[0].Y
	for _, point := range corners[1:] {
		left, top = min(left, point.X), min(top, point.Y)
		right, bottom = max(right, point.X), max(bottom, point.Y)
	}
	left, top = max(float64(rectangle.Min.X), math.Floor(left)), max(float64(rectangle.Min.Y), math.Floor(top))
	right, bottom = min(float64(rectangle.Max.X), math.Ceil(right)), min(float64(rectangle.Max.Y), math.Ceil(bottom))
	if right <= left || bottom <= top {
		return &ProjectionPlan{Left: rectangle.Min.X, Top: rectangle.Min.Y}, nil
	}
	if math.Abs(left) > 1e12 || math.Abs(top) > 1e12 || right-left > MaxDimension || bottom-top > MaxDimension {
		return nil, ErrImageSize
	}
	if err := AssertImageSize(int(right-left), int(bottom-top)); err != nil {
		return nil, err
	}
	inverse, err := SolveHomography(corners, [4]Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}})
	if err != nil {
		return nil, err
	}
	return &ProjectionPlan{Left: int(left), Top: int(top), Width: int(right - left), Height: int(bottom - top),
		Inverse: &inverse}, nil
}
