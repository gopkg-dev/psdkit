package psdgradient

import (
	"context"
	"image"
	"math"
)

// constantGradient requires validated stop values and recognizes only an entirely constant RGBA field.
// An empty opacity list is the documented opaque default; unequal values never use a tolerance.
func constantGradient(fill Fill) ([3]float64, float64, bool) {
	rgb, constantRGB := constantColorStops(fill.ColorStops)
	opacity, constantAlpha := constantOpacityStops(fill.OpacityStops)
	return rgb, opacity, constantRGB && constantAlpha
}

func constantColorStops(stops []ColorStop) ([3]float64, bool) {
	if len(stops) == 0 {
		return [3]float64{}, false
	}
	rgb := stops[0].RGB
	for _, stop := range stops[1:] {
		if stop.RGB != rgb {
			return [3]float64{}, false
		}
	}
	return rgb, true
}

func constantOpacityStops(stops []OpacityStop) (float64, bool) {
	opacity := 1.0
	if len(stops) != 0 {
		opacity = stops[0].Opacity
		for _, stop := range stops[1:] {
			if stop.Opacity != opacity {
				return 0, false
			}
		}
	}
	return opacity, true
}

func renderConstantGradient(
	ctx context.Context, bounds image.Rectangle, rgb [3]float64, opacity float64,
) (*image.NRGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pixel := [4]byte{byte(math.Round(rgb[0] * 255)), byte(math.Round(rgb[1] * 255)),
		byte(math.Round(rgb[2] * 255)), byte(math.Round(opacity * 255))}
	result := image.NewNRGBA(bounds)
	for row := range bounds.Dy() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range bounds.Dx() {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			offset := row*result.Stride + column*4
			copy(result.Pix[offset:offset+4], pixel[:])
		}
	}
	return result, nil
}
