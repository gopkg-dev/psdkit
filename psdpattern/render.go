package psdpattern

import (
	"context"
	"image"
	"math"
)

// Render repeats a caller-immutable straight NRGBA tile into independently owned document pixels.
// Axis sampling supports exact repeats at 100 percent, periodic box reduction at 50 percent,
// and origin-anchored straight-RGBA interpolation at 200 percent, all with integer document-pixel phase.
// Any exactly represented whole-turn angle is equivalent to zero.
// A single-pixel tile is constant for every finite positive scale and finite angle and phase.
// No source pixel, descriptor parameter or ICC profile is changed or retained.
func Render(
	ctx context.Context, tile *image.NRGBA, fill Fill, documentBounds image.Rectangle, options ...Options,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration := Options{MaxPixels: 20_000_000, MaxWorkingBytes: 256 << 20}
	if len(options) > 1 {
		return nil, patternError(ErrInvalid, "options", "provide at most one render options value")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxPixels < 0 || provided.MaxWorkingBytes < 0 {
			return nil, patternError(ErrInvalid, "options", "resource budgets must not be negative")
		}
		configuration.Crop = provided.Crop
		if provided.MaxPixels != 0 {
			configuration.MaxPixels = provided.MaxPixels
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = provided.MaxWorkingBytes
		}
	}
	if err := Validate(tile, fill); err != nil {
		return nil, err
	}
	if !validBounds(documentBounds) {
		return nil, patternError(ErrInvalid, "bounds", "document bounds or phase exceed pixel coordinate precision")
	}
	output := documentBounds
	if configuration.Crop != nil {
		if !validBounds(*configuration.Crop) {
			return nil, patternError(ErrInvalid, "crop", "crop bounds are empty, reversed or overflowing")
		}
		output = output.Intersect(*configuration.Crop)
		if output.Empty() {
			return nil, patternError(ErrInvalid, "crop", "crop does not overlap the document")
		}
	}
	width, height := int64(output.Dx()), int64(output.Dy())
	if width > configuration.MaxPixels/height || width > configuration.MaxWorkingBytes/4/height ||
		width > int64(^uint(0)>>1)/4/height {
		return nil, patternError(ErrLimit, "output", "owned output exceeds pixel or working budgets")
	}
	result := image.NewNRGBA(output)
	if singlePixelTile(tile) {
		return renderSinglePixelPattern(ctx, result, tile)
	}
	if fill.Scale != 100 {
		return renderScaledPattern(ctx, result, tile, fill)
	}
	phaseX, phaseY := int64(fill.Phase.X), int64(fill.Phase.Y)
	for row := 0; row < int(height); row++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sourceRow := modulo(int64(output.Min.Y)+int64(row)-phaseY, int64(tile.Rect.Dy()))
		for column := 0; column < int(width); column++ {
			if column&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			sourceColumn := modulo(int64(output.Min.X)+int64(column)-phaseX, int64(tile.Rect.Dx()))
			sourceOffset, outputOffset := int(sourceRow)*tile.Stride+int(sourceColumn)*4, row*result.Stride+column*4
			copy(result.Pix[outputOffset:outputOffset+4], tile.Pix[sourceOffset:sourceOffset+4])
		}
	}
	return result, nil
}

// Validate checks tile ownership bounds and the axis or single-pixel subset without allocating output pixels.
// It does not read or alter caller-owned samples and never prints the opaque fill ID.
func Validate(tile *image.NRGBA, fill Fill) error {
	if err := validateTile(tile); err != nil {
		return err
	}
	if !finite(fill.Scale) || fill.Scale <= 0 || !finite(fill.Angle) || !finite(fill.Phase.X) || !finite(fill.Phase.Y) {
		return patternError(ErrInvalid, "fill", "scale, angle and phase must be finite")
	}
	if len(fill.Unsupported) != 0 {
		return patternError(ErrUnsupported, "fill", "pattern contains unknown sampling semantics")
	}
	if singlePixelTile(tile) {
		return nil
	}
	scaleSupported := fill.Scale == 50 || fill.Scale == 100 || fill.Scale == 200
	angleSupported := math.Mod(fill.Angle, 360) == 0
	if !scaleSupported || !angleSupported ||
		fill.Phase.X != math.Trunc(fill.Phase.X) || fill.Phase.Y != math.Trunc(fill.Phase.Y) {
		return patternError(ErrUnsupported, "fill", "pattern sampling semantics are outside the verified axis subset")
	}
	const coordinateLimit = float64(1 << 40)
	if math.Abs(fill.Phase.X) > coordinateLimit || math.Abs(fill.Phase.Y) > coordinateLimit {
		return patternError(ErrInvalid, "phase", "phase exceeds exact pixel coordinate precision")
	}
	return nil
}

func validateTile(tile *image.NRGBA) error {
	if tile == nil || !validBounds(tile.Rect) || tile.Rect.Dx() > int(^uint(0)>>1)/4 ||
		tile.Stride < tile.Rect.Dx()*4 {
		return patternError(ErrInvalid, "tile", "tile rectangle or stride is invalid")
	}
	row := tile.Rect.Dx() * 4
	if len(tile.Pix) < row || tile.Rect.Dy() > 1 && (tile.Rect.Dy()-1) > (len(tile.Pix)-row)/tile.Stride {
		return patternError(ErrInvalid, "tile", "tile stride or buffer length overflows its owned pixels")
	}
	return nil
}

func validBounds(bounds image.Rectangle) bool {
	const coordinateLimit = int64(1 << 40)
	return !bounds.Empty() && int64(bounds.Min.X) >= -coordinateLimit && int64(bounds.Min.Y) >= -coordinateLimit &&
		int64(bounds.Max.X) <= coordinateLimit && int64(bounds.Max.Y) <= coordinateLimit &&
		int64(bounds.Max.X)-int64(bounds.Min.X) <= int64(^uint(0)>>1) &&
		int64(bounds.Max.Y)-int64(bounds.Min.Y) <= int64(^uint(0)>>1)
}

func modulo(value, period int64) int64 {
	result := value % period
	if result < 0 {
		result += period
	}
	return result
}

func finite(value float64) bool { return !math.IsInf(value, 0) && !math.IsNaN(value) }
