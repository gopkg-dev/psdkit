package psdfilter

import (
	"context"
	"image"
	"math"
)

// surfaceBlur uses a finite separable tent footprint and a maximum-channel range gate.
// Accepted neighbors are weighted by their alpha and their distance from the center color.
// This declared approximation preserves center alpha and excludes hidden transparent RGB.
// Photoshop's private Surface Blur kernel and threshold transfer are not reproduced.
func surfaceBlur(ctx context.Context, source, target *image.NRGBA, filter Filter) error {
	width, height := source.Rect.Dx(), source.Rect.Dy()
	radius := int(math.Ceil(filter.Radius))
	span := filter.Radius + 1
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
			for dy := -radius; dy <= radius; dy++ {
				y := max(0, min(height-1, row+dy))
				verticalWeight := span - math.Abs(float64(dy))
				for dx := -radius; dx <= radius; dx++ {
					x := max(0, min(width-1, column+dx))
					offset := y*source.Stride + x*4
					if source.Pix[offset+3] == 0 {
						continue
					}
					var difference float64
					for component := range 3 {
						difference = max(difference, math.Abs(float64(source.Pix[offset+component])-
							float64(source.Pix[sourceOffset+component])))
					}
					if difference > filter.Threshold {
						continue
					}
					weight := verticalWeight * (span - math.Abs(float64(dx))) *
						(1 - difference/(filter.Threshold+1)) * float64(source.Pix[offset+3])
					total += weight
					for component := range 3 {
						values[component] += weight * float64(source.Pix[offset+component])
					}
				}
			}
			if total == 0 {
				continue
			}
			for component := range 3 {
				original := float64(source.Pix[sourceOffset+component])
				target.Pix[targetOffset+component] = roundedByte(original + filter.Opacity*(values[component]/total-original))
			}
		}
	}
	return nil
}
