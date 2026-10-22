package psdpattern

import (
	"context"
	"image"
)

// renderScaledPattern evaluates a periodic resized tile lazily, retaining no intermediate tile allocation.
// Straight RGB and alpha are filtered independently, including the source's preserved hidden RGB values.
func renderScaledPattern(
	ctx context.Context, output, tile *image.NRGBA, fill Fill,
) (*image.NRGBA, error) {
	phaseX, phaseY := int64(fill.Phase.X), int64(fill.Phase.Y)
	for row := range output.Rect.Dy() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		y := int64(output.Rect.Min.Y) + int64(row) - phaseY
		for column := range output.Rect.Dx() {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			x := int64(output.Rect.Min.X) + int64(column) - phaseX
			pixel := scaledPatternPixel(tile, fill.Scale, x, y)
			offset := row*output.Stride + column*4
			copy(output.Pix[offset:offset+4], pixel[:])
		}
	}
	return output, nil
}

// scaledPatternPixel requires a validated tile, scale 50 or 200 and bounded integer coordinates.
func scaledPatternPixel(tile *image.NRGBA, scale float64, x, y int64) [4]byte {
	width, height := int64(tile.Rect.Dx()), int64(tile.Rect.Dy())
	var firstX, firstY, secondX, secondY int64
	if scale == 50 {
		// A reduced output pixel covers two source pixels on each axis; edge coverage wraps.
		firstX, firstY = modulo(x*2, width), modulo(y*2, height)
		secondX, secondY = (firstX+1)%width, (firstY+1)%height
	} else {
		// Doubled output keeps original source anchors; odd positions interpolate halfway.
		column, row := modulo(x, width*2), modulo(y, height*2)
		firstX, firstY = column/2, row/2
		secondX, secondY = firstX, firstY
		if column%2 != 0 {
			secondX = (firstX + 1) % width
		}
		if row%2 != 0 {
			secondY = (firstY + 1) % height
		}
	}
	offsets := [4]int{
		int(firstY)*tile.Stride + int(firstX)*4,
		int(firstY)*tile.Stride + int(secondX)*4,
		int(secondY)*tile.Stride + int(firstX)*4,
		int(secondY)*tile.Stride + int(secondX)*4,
	}
	var result [4]byte
	for component := range result {
		var total uint16
		for _, offset := range offsets {
			total += uint16(tile.Pix[offset+component])
		}
		result[component] = byte((total + 2) / 4)
	}
	return result
}
