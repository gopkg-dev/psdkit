package psdcolor

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
)

const (
	defaultMaxPixels       = 64_000_000
	defaultMaxOutputBytes  = 256 << 20
	defaultMaxProfileBytes = 4 << 20
)

// DefaultOptions returns explicit allocation limits with conservative display policies.
// ICC, palette, transparency and source float encoding overrides remain caller metadata.
func DefaultOptions() Options {
	return Options{
		MaxPixels: defaultMaxPixels, MaxOutputBytes: defaultMaxOutputBytes, MaxProfileBytes: defaultMaxProfileBytes,
	}
}

// Convert returns owned, straight-alpha sRGB display pixels from original PSD channel samples.
// At most one Options value is accepted. Metadata such as ICC, palette and saved-image alpha semantics
// must be supplied by the caller; extra channels are never silently assumed to be transparency.
func Convert(
	ctx context.Context, pixels *psdfile.CompositePixels, options ...Options,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settings, err := conversionOptions(options)
	if err != nil {
		return nil, err
	}
	components, alphaIndex, err := validatePixels(pixels, settings)
	if err != nil {
		return nil, err
	}
	profile, err := parseICC(ctx, settings.ICCProfile, settings.MaxProfileBytes, settings.Intent)
	if err != nil {
		return nil, err
	}
	if err := validateProfileSpace(profile, pixels.ColorMode); err != nil {
		return nil, err
	}
	if profile != nil && profile.lut != nil && pixels.Depth == 32 &&
		settings.FloatEncoding == FloatLinearRGB {
		return nil, fmt.Errorf("%w: linear float samples require a matrix/TRC ICC profile", ErrUnsupported)
	}
	output := image.NewNRGBA(image.Rect(0, 0, pixels.Width, pixels.Height))
	for y := 0; y < pixels.Height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < pixels.Width; x++ {
			if x&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			var channels [4]float64
			for index := range components {
				channels[index], err = sampleValue(&pixels.Channels[index], x, y, settings, false)
				if err != nil {
					return nil, sampleError(err, x, y, index)
				}
			}
			coverage := 1.0
			if alphaIndex >= 0 {
				coverage, err = sampleValue(&pixels.Channels[alphaIndex], x, y, settings, true)
				if err != nil {
					return nil, sampleError(err, x, y, alphaIndex)
				}
			}
			if pixels.ColorMode == 2 && settings.TransparencyIndex != nil &&
				uint8(math.Round(channels[0]*255)) == *settings.TransparencyIndex {
				coverage = 0
			}
			if settings.Alpha == AlphaWhiteMatte && coverage > 0 {
				for index := range components {
					white := sourceWhite(pixels.ColorMode, pixels.Depth, index)
					channels[index] = clampUnit((channels[index] - white*(1-coverage)) / coverage)
				}
			}
			var rgb [3]float64
			if coverage > 0 {
				rgb, err = convertColor(channels, pixels.ColorMode, pixels.Depth, profile, settings)
				if err != nil {
					return nil, sampleError(err, x, y, -1)
				}
			}
			position := y*output.Stride + x*4
			for index := range rgb {
				output.Pix[position+index] = unitByte(rgb[index])
			}
			output.Pix[position+3] = unitByte(coverage)
		}
	}
	return output, nil
}

func conversionOptions(options []Options) (Options, error) {
	if len(options) > 1 {
		return Options{}, fmt.Errorf("%w: at most one Options value is supported", ErrInvalid)
	}
	var settings Options
	if len(options) == 1 {
		settings = options[0]
	}
	if settings.MaxPixels < 0 || settings.MaxOutputBytes < 0 || settings.MaxProfileBytes < 0 ||
		settings.AlphaChannel < 0 || settings.Alpha > AlphaWhiteMatte ||
		settings.FloatEncoding > FloatEncodedRGB || settings.NonFinite > NonFiniteClamp ||
		settings.ToneMap > ToneMapClamp || settings.Intent > IntentAbsoluteColorimetric {
		return Options{}, fmt.Errorf("%w: invalid options", ErrInvalid)
	}
	if settings.MaxPixels == 0 {
		settings.MaxPixels = defaultMaxPixels
	}
	if settings.MaxOutputBytes == 0 {
		settings.MaxOutputBytes = defaultMaxOutputBytes
	}
	if settings.MaxProfileBytes == 0 {
		settings.MaxProfileBytes = defaultMaxProfileBytes
	}
	if int64(len(settings.ICCProfile)) > settings.MaxProfileBytes {
		return Options{}, fmt.Errorf("%w: profile exceeds MaxProfileBytes", ErrLimit)
	}
	if len(settings.Palette) != 0 && len(settings.Palette) != 768 {
		return Options{}, fmt.Errorf("%w: an indexed palette contains exactly 768 bytes", ErrInvalid)
	}
	if settings.Intent == IntentAbsoluteColorimetric {
		return Options{}, fmt.Errorf("%w: absolute colorimetric intent is not implemented", ErrUnsupported)
	}
	return settings, nil
}

func validatePixels(pixels *psdfile.CompositePixels, settings Options) (int, int, error) {
	if pixels == nil || pixels.Width < 0 || pixels.Height < 0 {
		return 0, -1, fmt.Errorf("%w: missing pixels or negative dimensions", ErrInvalid)
	}
	width, height := uint64(pixels.Width), uint64(pixels.Height)
	if height != 0 && width > uint64(settings.MaxPixels)/height {
		return 0, -1, fmt.Errorf("%w: dimensions exceed MaxPixels", ErrLimit)
	}
	pixelCount := width * height
	platformLimit := uint64(^uint(0) >> 1)
	if width > platformLimit/4 || pixelCount > platformLimit/4 ||
		pixelCount > uint64(settings.MaxOutputBytes)/4 {
		return 0, -1, fmt.Errorf("%w: display buffer exceeds MaxOutputBytes or integer range", ErrLimit)
	}
	components, err := colorComponents(pixels.ColorMode, pixels.Depth, settings)
	if err != nil {
		return 0, -1, err
	}
	if len(pixels.Channels) < components {
		return 0, -1, fmt.Errorf("%w: missing color channels", ErrInvalid)
	}
	rowBytes := width/8 + (width%8+7)/8
	if pixels.Depth != 1 {
		rowBytes = width * uint64(pixels.Depth/8)
	}
	if rowBytes > platformLimit || height != 0 && rowBytes > platformLimit/height {
		return 0, -1, fmt.Errorf("%w: source plane size exceeds integer range", ErrLimit)
	}
	for index := range pixels.Channels {
		plane := &pixels.Channels[index]
		if plane.Width != pixels.Width || plane.Height != pixels.Height || plane.Depth != pixels.Depth ||
			plane.RowBytes != int(rowBytes) || uint64(len(plane.Data)) != rowBytes*height {
			return 0, -1, fmt.Errorf("%w: channel %d has inconsistent shape or data length", ErrInvalid, index)
		}
	}
	alphaIndex := -1
	if settings.Alpha != AlphaNone {
		alphaIndex = settings.AlphaChannel
		if alphaIndex == 0 {
			alphaIndex = components
		}
		if alphaIndex < components || alphaIndex >= len(pixels.Channels) {
			return 0, -1, fmt.Errorf("%w: transparency plane is absent or overlaps color channels", ErrInvalid)
		}
	}
	if settings.Alpha == AlphaWhiteMatte && pixels.ColorMode == 2 {
		return 0, -1, fmt.Errorf("%w: white-matted indexed samples cannot be unmatted as palette indices", ErrUnsupported)
	}
	if settings.TransparencyIndex != nil && pixels.ColorMode != 2 {
		return 0, -1, fmt.Errorf("%w: TransparencyIndex applies only to indexed color", ErrInvalid)
	}
	return components, alphaIndex, nil
}

func colorComponents(mode, depth uint16, settings Options) (int, error) {
	if depth != 1 && depth != 8 && depth != 16 && depth != 32 {
		return 0, fmt.Errorf("%w: unsupported sample depth %d", ErrUnsupported, depth)
	}
	switch mode {
	case 0:
		if depth != 1 {
			return 0, fmt.Errorf("%w: bitmap samples must be one bit", ErrUnsupported)
		}
		return 1, nil
	case 1:
		return 1, nil
	case 2:
		if depth != 8 || len(settings.Palette) != 768 {
			return 0, fmt.Errorf("%w: indexed color requires 8-bit samples and a palette", ErrInvalid)
		}
		return 1, nil
	case 3:
		return 3, nil
	case 4:
		if depth != 8 && depth != 16 {
			return 0, fmt.Errorf("%w: only 8/16-bit CMYK is supported", ErrUnsupported)
		}
		if len(settings.ICCProfile) == 0 && !settings.AllowUnmanagedCMYK {
			return 0, fmt.Errorf("%w: CMYK without ICC requires AllowUnmanagedCMYK", ErrUnsupported)
		}
		return 4, nil
	case 9:
		if depth != 8 && depth != 16 {
			return 0, fmt.Errorf("%w: only 8/16-bit Lab is supported", ErrUnsupported)
		}
		return 3, nil
	}
	return 0, fmt.Errorf("%w: color mode %d", ErrUnsupported, mode)
}

func sampleValue(plane *psdfile.Plane, x, y int, settings Options, coverage bool) (float64, error) {
	row := y * plane.RowBytes
	switch plane.Depth {
	case 1:
		return float64((plane.Data[row+x/8] >> (7 - x%8)) & 1), nil
	case 8:
		return float64(plane.Data[row+x]) / 255, nil
	case 16:
		return float64(binary.BigEndian.Uint16(plane.Data[row+x*2:])) / 65535, nil
	case 32:
		value := float64(math.Float32frombits(binary.BigEndian.Uint32(plane.Data[row+x*4:])))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			switch settings.NonFinite {
			case NonFiniteReject:
				return 0, ErrNonFinite
			case NonFiniteZero:
				value = 0
			case NonFiniteClamp:
				if math.IsInf(value, 1) {
					value = 1
				} else {
					value = 0
				}
			}
		}
		if value < 0 || value > 1 {
			if coverage || settings.ToneMap != ToneMapClamp {
				return 0, ErrOutOfRange
			}
			value = clampUnit(value)
		}
		return value, nil
	}
	return 0, ErrUnsupported
}

func sourceWhite(mode, depth uint16, channel int) float64 {
	if mode == 0 {
		return 0
	}
	if mode == 9 && channel > 0 {
		if depth == 16 {
			return 32768.0 / 65535
		}
		return 128.0 / 255
	}
	return 1
}

func convertColor(
	channels [4]float64, mode, depth uint16, profile *iccProfile, settings Options,
) ([3]float64, error) {
	if profile != nil && profile.lut != nil {
		input := channels
		if mode == 0 {
			input[0] = 1 - input[0]
		}
		if mode == 2 {
			index := int(math.Round(input[0] * 255))
			for channel := range 3 {
				input[channel] = float64(settings.Palette[channel*256+index]) / 255
			}
		}
		if mode == 4 {
			for channel := range input {
				input[channel] = 1 - input[channel]
			}
		}
		return profile.lut.display(input, profile.pcs), nil
	}
	var rgb [3]float64
	switch mode {
	case 0, 1:
		value := channels[0]
		if mode == 0 {
			value = 1 - value
		}
		if profile != nil {
			if depth == 32 && settings.FloatEncoding == FloatLinearRGB {
				return xyzD50RGB([3]float64{iccWhite[0] * value, value, iccWhite[2] * value}), nil
			}
			value = profile.curves[0].evaluate(value)
			if profile.pcs == "Lab " {
				return labRGB(value*100, 0, 0), nil
			}
			return xyzD50RGB([3]float64{iccWhite[0] * value, value, iccWhite[2] * value}), nil
		}
		if depth == 32 && settings.FloatEncoding == FloatLinearRGB {
			value = encodeSRGB(value)
		}
		return [3]float64{value, value, value}, nil
	case 2:
		index := int(math.Round(channels[0] * 255))
		for channel := range rgb {
			rgb[channel] = float64(settings.Palette[channel*256+index]) / 255
		}
	case 3:
		rgb = [3]float64{channels[0], channels[1], channels[2]}
	case 4:
		return [3]float64{channels[0] * channels[3], channels[1] * channels[3], channels[2] * channels[3]}, nil
	case 9:
		componentScale := 255.0
		if depth == 16 {
			// Photoshop's native 16-bit Lab neutral a/b word is 32768, with 256 words per unit.
			componentScale = 65535.0 / 256
		}
		return labRGB(channels[0]*100, channels[1]*componentScale-128, channels[2]*componentScale-128), nil
	}
	if profile != nil {
		if depth != 32 || settings.FloatEncoding == FloatEncodedRGB {
			for channel := range rgb {
				rgb[channel] = profile.curves[channel].evaluate(rgb[channel])
			}
		}
		return xyzD50RGB(profile.matrix.multiply(rgb)), nil
	}
	if depth == 32 && settings.FloatEncoding == FloatLinearRGB {
		for channel := range rgb {
			rgb[channel] = encodeSRGB(rgb[channel])
		}
	}
	return rgb, nil
}

func validateProfileSpace(profile *iccProfile, mode uint16) error {
	if profile == nil {
		return nil
	}
	if (mode == 2 || mode == 3) && profile.space == "RGB " ||
		(mode == 0 || mode == 1) && profile.space == "GRAY" || mode == 4 && profile.space == "CMYK" {
		return nil
	}
	return fmt.Errorf("%w: ICC color space %q does not match color mode %d", ErrUnsupported, profile.space, mode)
}

func sampleError(err error, x, y, channel int) error {
	return fmt.Errorf("%w at pixel (%d,%d), channel %d", err, x, y, channel)
}

func clampUnit(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func unitByte(value float64) uint8 {
	return uint8(math.Round(clampUnit(value) * 255))
}

func encodeSRGB(value float64) float64 {
	value = clampUnit(value)
	if value <= 0.0031308 {
		return 12.92 * value
	}
	return 1.055*math.Pow(value, 1/2.4) - 0.055
}

func labRGB(lightness, componentA, componentB float64) [3]float64 {
	intermediateY := (lightness + 16) / 116
	intermediateX := intermediateY + componentA/500
	intermediateZ := intermediateY - componentB/200
	inverse := func(value float64) float64 {
		if value > 6.0/29 {
			return value * value * value
		}
		return 3 * (6.0 / 29) * (6.0 / 29) * (value - 4.0/29)
	}
	return xyzD50RGB([3]float64{
		iccWhite[0] * inverse(intermediateX), inverse(intermediateY), iccWhite[2] * inverse(intermediateZ),
	})
}
