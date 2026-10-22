package psdfilter

import (
	"context"
	"image"
	"math"
)

type lensSample struct {
	x, y   int
	weight float64
}

// smartSharpen uses an area-sampled lens disk, then applies a bounded straight-RGB unsharp correction.
// The noise control suppresses low-amplitude detail before Amount and Opacity are applied.
// Center alpha is retained; invisible neighbors cannot contribute hidden RGB to visible pixels.
// This explicit approximation does not clone Photoshop's private non-legacy deconvolution.
func smartSharpen(ctx context.Context, source, target *image.NRGBA, filter Filter) error {
	width, height := source.Rect.Dx(), source.Rect.Dy()
	kernel := lensKernel(filter.Radius)
	for row := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := range width {
			if column&63 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			sourceOffset := row*source.Stride + column*4
			targetOffset := row*target.Stride + column*4
			copy(target.Pix[targetOffset:targetOffset+4], source.Pix[sourceOffset:sourceOffset+4])
			if source.Pix[sourceOffset+3] == 0 {
				continue
			}
			var values [3]float64
			var total float64
			for _, sample := range kernel {
				x := max(0, min(width-1, column+sample.x))
				y := max(0, min(height-1, row+sample.y))
				offset := y*source.Stride + x*4
				weight := sample.weight * float64(source.Pix[offset+3])
				total += weight
				for component := range 3 {
					values[component] += weight * float64(source.Pix[offset+component])
				}
			}
			if total == 0 {
				continue
			}
			for component := range 3 {
				original := float64(source.Pix[sourceOffset+component])
				detail := original - values[component]/total
				magnitude := math.Abs(detail)
				if magnitude > 0 {
					detail *= magnitude / (magnitude + filter.NoiseReduction*16)
				}
				sharpened := max(0, min(255, original+filter.Amount*detail))
				target.Pix[targetOffset+component] = roundedByte(original + filter.Opacity*(sharpened-original))
			}
		}
	}
	return nil
}

func lensKernel(radius float64) []lensSample {
	// Integrating a disk with a half-pixel footprint keeps fractional radii spatially meaningful.
	diskRadius := radius + .5
	support := int(math.Ceil(radius + .5))
	diameter := support*2 + 1
	kernel := make([]lensSample, 0, diameter*diameter)
	const subpixels = 8
	for y := -support; y <= support; y++ {
		for x := -support; x <= support; x++ {
			var covered int
			for subY := range subpixels {
				for subX := range subpixels {
					xValue := float64(x) - .5 + (float64(subX)+.5)/subpixels
					yValue := float64(y) - .5 + (float64(subY)+.5)/subpixels
					if xValue*xValue+yValue*yValue <= diskRadius*diskRadius {
						covered++
					}
				}
			}
			if covered > 0 {
				kernel = append(kernel, lensSample{x: x, y: y, weight: float64(covered) / (subpixels * subpixels)})
			}
		}
	}
	if len(kernel) == 0 {
		kernel = append(kernel, lensSample{weight: 1})
	}
	return kernel
}
