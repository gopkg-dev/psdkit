// Package canvaspix preserves the frontend's eight-bit Canvas export quantization.
package canvaspix

import (
	"image"
	"math"
)

// ForPNG matches Canvas PNG export's premultiply and normalized float32 readback.
// It borrows opaque inputs and returns an owned copy for translucent images.
// Callers validate their own image dimension budgets before calling this function.
func ForPNG(source *image.NRGBA) *image.NRGBA {
	if source.Opaque() {
		return source
	}
	result := image.NewNRGBA(image.Rect(0, 0, source.Rect.Dx(), source.Rect.Dy()))
	readback(result, source)
	return result
}

// ReadbackInPlace applies Canvas getImageData quantization to an owned buffer.
// Use this before JavaScript-equivalent edits of the straight RGB/alpha bytes.
func ReadbackInPlace(pixels *image.NRGBA) {
	readback(pixels, pixels)
}

func readback(result, source *image.NRGBA) {
	const normalizedByte = float32(1.0 / 255)
	for y := 0; y < source.Rect.Dy(); y++ {
		for x := 0; x < source.Rect.Dx(); x++ {
			si := source.PixOffset(source.Rect.Min.X+x, source.Rect.Min.Y+y)
			di := result.PixOffset(result.Rect.Min.X+x, result.Rect.Min.Y+y)
			alpha := int(source.Pix[si+3])
			result.Pix[di+3] = uint8(alpha)
			if alpha == 0 {
				clear(result.Pix[di : di+3])
				continue
			}
			if alpha == 255 {
				copy(result.Pix[di:di+3], source.Pix[si:si+3])
				continue
			}
			inverseAlpha := float32(1 / (float32(alpha) * normalizedByte))
			for component := range 3 {
				premultiplied := (int(source.Pix[si+component])*alpha + 127) / 255
				color := float32(premultiplied) * normalizedByte
				unpremultiplied := float32(color * inverseAlpha)
				value := math.RoundToEven(float64(float32(unpremultiplied * 255)))
				result.Pix[di+component] = uint8(max(0, min(255, value)))
			}
		}
	}
}
