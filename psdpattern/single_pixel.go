package psdpattern

import (
	"context"
	"image"
)

// singlePixelTile checks geometry only; validation must not inspect caller-owned pixel samples.
func singlePixelTile(tile *image.NRGBA) bool {
	return tile.Rect.Dx() == 1 && tile.Rect.Dy() == 1
}

// renderSinglePixelPattern never converts the inactive geometry controls to integer coordinates.
func renderSinglePixelPattern(ctx context.Context, output, tile *image.NRGBA) (*image.NRGBA, error) {
	for row := range output.Rect.Dy() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range output.Rect.Dx() {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			offset := row*output.Stride + column*4
			copy(output.Pix[offset:offset+4], tile.Pix[:4])
		}
	}
	return output, nil
}
