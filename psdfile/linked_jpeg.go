package psdfile

import (
	"context"
	"image"
	"math"
)

// renderLinkedJPEG uses the reconstruction calibrated against controlled native RGB8 JPEG sources.
// Default 4:2:0 preserves each chroma cell's mean with horizontal monotone slopes and vertical repetition.
// Explicit centered reconstruction uses a separable triangle filter; other default ratios remain nearest.
func renderLinkedJPEG(ctx context.Context, destination *image.NRGBA, source *image.YCbCr, centered bool) error {
	if !centered {
		if source.SubsampleRatio == image.YCbCrSubsampleRatio420 {
			return renderLinkedJPEGMonotone(ctx, destination, source)
		}
		return renderLinkedJPEGNearest(ctx, destination, source)
	}
	horizontal, vertical := 1, 1
	switch source.SubsampleRatio {
	case image.YCbCrSubsampleRatio422:
		horizontal = 2
	case image.YCbCrSubsampleRatio420:
		horizontal, vertical = 2, 2
	case image.YCbCrSubsampleRatio440:
		vertical = 2
	case image.YCbCrSubsampleRatio444:
	default:
		return renderLinkedJPEGNearest(ctx, destination, source)
	}
	width, height := source.Rect.Dx(), source.Rect.Dy()
	chromaWidth, chromaHeight := (width+horizontal-1)/horizontal, (height+vertical-1)/vertical
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, adjacentRow, rowWeight, rowDenominator := jpegChromaCoordinate(y, vertical, chromaHeight)
		for x := range width {
			column, adjacentColumn, columnWeight, columnDenominator :=
				jpegChromaCoordinate(x, horizontal, chromaWidth)
			denominator := rowDenominator * columnDenominator
			bias := denominator / 2
			if horizontal == 2 && x%2 == 0 {
				bias--
			}
			chroma := func(plane []byte) uint8 {
				upper := int(plane[row*source.CStride+column])*columnWeight +
					int(plane[row*source.CStride+adjacentColumn])*(columnDenominator-columnWeight)
				lower := int(plane[adjacentRow*source.CStride+column])*columnWeight +
					int(plane[adjacentRow*source.CStride+adjacentColumn])*(columnDenominator-columnWeight)
				return uint8((upper*rowWeight + lower*(rowDenominator-rowWeight) + bias) / denominator)
			}
			red, green, blue := jpegFloatRGB(source.Y[y*source.YStride+x], chroma(source.Cb), chroma(source.Cr))
			position := y*destination.Stride + x*4
			destination.Pix[position], destination.Pix[position+1] = red, green
			destination.Pix[position+2], destination.Pix[position+3] = blue, 255
		}
	}
	return nil
}

func renderLinkedJPEGMonotone(ctx context.Context, destination *image.NRGBA, source *image.YCbCr) error {
	chromaWidth := (source.Rect.Max.X+1)/2 - source.Rect.Min.X/2
	for y := 0; y < destination.Rect.Dy(); y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		pointY := source.Rect.Min.Y + y
		row := pointY/2 - source.Rect.Min.Y/2
		for x := 0; x < destination.Rect.Dx(); x++ {
			if x&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			pointX := source.Rect.Min.X + x
			column := pointX/2 - source.Rect.Min.X/2
			cb := jpegMonotoneChroma(source.Cb, source.CStride, row, column, chromaWidth, pointX%2 == 0)
			cr := jpegMonotoneChroma(source.Cr, source.CStride, row, column, chromaWidth, pointX%2 == 0)
			red, green, blue := jpegFloatRGB(source.Y[source.YOffset(pointX, pointY)], cb, cr)
			position := y*destination.Stride + x*4
			destination.Pix[position], destination.Pix[position+1] = red, green
			destination.Pix[position+2], destination.Pix[position+3] = blue, 255
		}
	}
	return nil
}

func jpegMonotoneChroma(plane []byte, stride, row, column, width int, leftPixel bool) uint8 {
	base := row * stride
	current := int(plane[base+column])
	left := int(plane[base+max(0, column-1)])
	right := int(plane[base+min(width-1, column+1)])
	a, b := current-left, right-current
	delta := 0
	if a*b > 0 {
		absolute := func(value int) int {
			if value < 0 {
				return -value
			}
			return value
		}
		first, second := absolute(a), absolute(b)
		delta = min(max(first, second), 2*min(first, second))
		if a < 0 {
			delta = -delta
		}
	}
	// Quarter-code arithmetic preserves opposing half-code ties without floating-point ambiguity.
	if leftPixel {
		return uint8((current*4 - delta + 1) / 4)
	}
	return uint8((current*4 + delta + 2) / 4)
}

func renderLinkedJPEGNearest(ctx context.Context, destination *image.NRGBA, source *image.YCbCr) error {
	for y := 0; y < destination.Rect.Dy(); y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := 0; x < destination.Rect.Dx(); x++ {
			pointX, pointY := source.Rect.Min.X+x, source.Rect.Min.Y+y
			chromaOffset := source.COffset(pointX, pointY)
			red, green, blue := jpegFloatRGB(source.Y[source.YOffset(pointX, pointY)],
				source.Cb[chromaOffset], source.Cr[chromaOffset])
			position := y*destination.Stride + x*4
			destination.Pix[position], destination.Pix[position+1] = red, green
			destination.Pix[position+2], destination.Pix[position+3] = blue, 255
		}
	}
	return nil
}

func jpegFloatRGB(y, cb, cr uint8) (uint8, uint8, uint8) {
	luminance, blueDifference, redDifference := float64(y), float64(cb)-128, float64(cr)-128
	code := func(value float64) uint8 { return uint8(max(0, min(255, math.Floor(value+0.5)))) }
	return code(luminance + 1.402*redDifference),
		code(luminance - 0.344136*blueDifference - 0.714136*redDifference),
		code(luminance + 1.772*blueDifference)
}

func jpegChromaCoordinate(position, ratio, extent int) (int, int, int, int) {
	if ratio == 1 {
		return position, position, 1, 1
	}
	current := position / 2
	adjacent := current - 1
	if position%2 != 0 {
		adjacent = current + 1
	}
	adjacent = max(0, min(extent-1, adjacent))
	return current, adjacent, 3, 4
}
