package psdscene

import (
	"context"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

// The affine control hull contains every quadratic or cubic curve, including artwork outside saved layer bounds.
// Validate all geometry before deciding that a paint is outside the requested rectangle.
func openTypePaintBounds(
	ctx context.Context, paint *openTypePaintRange, transform [6]float64,
) (image.Rectangle, error) {
	if paint == nil || paint.run == nil || !textFinite(paint.run.Scale) || paint.run.Scale <= 0 ||
		paint.begin < 0 || paint.end < paint.begin || paint.end > len(paint.run.Glyphs) {
		return image.Rectangle{}, sceneError(psdfile.ErrMalformed, "textOpenType.styleBounds", 0, "invalid paint run")
	}
	for _, value := range transform {
		if !textFinite(value) {
			return image.Rectangle{}, sceneError(psdfile.ErrMalformed, "textOpenType.styleBounds", 0,
				"nonfinite source transform")
		}
	}
	ink := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, positioned := range paint.run.Glyphs[paint.begin:paint.end] {
		if err := ctx.Err(); err != nil {
			return image.Rectangle{}, err
		}
		if positioned.Glyph == nil || !textFinite(positioned.X) || !textFinite(positioned.Y) {
			return image.Rectangle{}, sceneError(psdfile.ErrMalformed, "textOpenType.styleBounds", 0,
				"nil outline or nonfinite glyph position")
		}
		point := func(px, py float64) error {
			x, y := positioned.X+px*paint.run.Scale, positioned.Y-py*paint.run.Scale
			outputX := transform[0]*x + transform[2]*y + transform[4]
			outputY := transform[1]*x + transform[3]*y + transform[5]
			if !textFinite(outputX) || !textFinite(outputY) ||
				math.Abs(outputX) > 1<<40 || math.Abs(outputY) > 1<<40 {
				return sceneError(psdfile.ErrLimit, "textOpenType.styleBounds", 0,
					"affine outline exceeds supported coordinate precision")
			}
			ink[0], ink[1] = min(ink[0], outputX), min(ink[1], outputY)
			ink[2], ink[3] = max(ink[2], outputX), max(ink[3], outputY)
			return nil
		}
		for index, command := range positioned.Glyph.Path {
			if index%256 == 0 {
				if err := ctx.Err(); err != nil {
					return image.Rectangle{}, err
				}
			}
			if !textFinite(command.X) || !textFinite(command.Y) ||
				!textFinite(command.ControlX) || !textFinite(command.ControlY) ||
				!textFinite(command.Control2X) || !textFinite(command.Control2Y) {
				return image.Rectangle{}, sceneError(psdfile.ErrMalformed, "textOpenType.styleBounds", 0,
					"nonfinite glyph path")
			}
			switch command.Op {
			case psdfont.Move, psdfont.Line, psdfont.Quad, psdfont.Cubic:
				if err := point(command.X, command.Y); err != nil {
					return image.Rectangle{}, err
				}
				if command.Op == psdfont.Quad || command.Op == psdfont.Cubic {
					if err := point(command.ControlX, command.ControlY); err != nil {
						return image.Rectangle{}, err
					}
				}
				if command.Op == psdfont.Cubic {
					if err := point(command.Control2X, command.Control2Y); err != nil {
						return image.Rectangle{}, err
					}
				}
			case psdfont.Close:
			default:
				return image.Rectangle{}, sceneError(psdfile.ErrUnsupported, "textOpenType.styleBounds", 0,
					"unknown glyph path operation")
			}
		}
	}
	if math.IsInf(ink[0], 1) {
		return image.Rectangle{}, nil
	}
	return image.Rect(int(math.Floor(ink[0])), int(math.Floor(ink[1])),
		int(math.Ceil(ink[2])), int(math.Ceil(ink[3]))), nil
}
