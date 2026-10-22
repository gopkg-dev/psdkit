package psdcolor

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"sort"
)

// ProfileTransformOptions bounds an RGB matrix/TRC profile conversion.
type ProfileTransformOptions struct {
	// AssumeSRGB explicitly supplies standard sRGB for either empty profile.
	AssumeSRGB bool
	// MaxPixels defaults to 64 million pixels.
	MaxPixels int64
	// MaxWorkingBytes includes the owned output and conservative ICC parsing storage.
	// Zero uses 256 MiB. The caller's input image and profile bytes remain borrowed.
	MaxWorkingBytes int64
	// MaxProfileBytes limits each source or destination profile, defaulting to 4 MiB.
	MaxProfileBytes int64
}

// TransformProfile converts straight RGB8 between explicitly supplied ICC profiles.
// It implements relative colorimetric RGB matrix/TRC transforms without black-point
// compensation. Empty profiles require AssumeSRGB. Alpha and the input rectangle
// are preserved in an owned, tightly packed result; all inputs remain unchanged.
// D50 PCS values stay float64 until the final destination RGB8 clipping and rounding.
// LUT profiles, singular matrices and noninvertible destination curves are unsupported.
func TransformProfile(
	ctx context.Context, input *image.NRGBA, sourceICC, destinationICC []byte,
	options ...ProfileTransformOptions,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settings, err := profileTransformSettings(options)
	if err != nil {
		return nil, err
	}
	width, height, outputBytes, _, err := profileTransformPreflight(ctx, input, sourceICC, destinationICC, settings)
	if err != nil {
		return nil, err
	}
	source, err := profileTransformParse(ctx, sourceICC, settings)
	if err != nil {
		return nil, fmt.Errorf("source profile: %w", err)
	}
	destination, err := profileTransformParse(ctx, destinationICC, settings)
	if err != nil {
		return nil, fmt.Errorf("destination profile: %w", err)
	}
	inverse, err := inverseProfileMatrix(destination.matrix)
	if err != nil {
		return nil, fmt.Errorf("destination profile: %w", err)
	}
	for _, curve := range destination.curves {
		if err := invertibleProfileCurve(ctx, curve); err != nil {
			return nil, fmt.Errorf("destination profile: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &image.NRGBA{Pix: make([]byte, int(outputBytes)), Stride: width * 4, Rect: input.Rect}
	if width == 0 || height == 0 {
		return result, nil
	}
	for y := range height {
		inputRow := y * input.Stride
		outputRow := y * result.Stride
		for x := range width {
			if x&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			position := inputRow + x*4
			var linear [3]float64
			for channel := range linear {
				linear[channel] = source.curves[channel].evaluate(float64(input.Pix[position+channel]) / 255)
			}
			converted := inverse.multiply(source.matrix.multiply(linear))
			for channel := range converted {
				value := inverseProfileCurve(destination.curves[channel], converted[channel])
				result.Pix[outputRow+x*4+channel] = uint8(math.Round(clampUnit(value) * 255))
			}
			result.Pix[outputRow+x*4+3] = input.Pix[position+3]
		}
	}
	return result, nil
}

// ProfileTransformWorkingBytes reports the conservative peak allocation reserved
// by TransformProfile, excluding caller-owned image and profile buffers. It performs
// the same size, image buffer, profile envelope and budget checks without allocating
// pixels or parsing curves. Successful estimation does not prove that all ICC tags
// or destination curves are supported. Zero budgets retain their documented defaults.
func ProfileTransformWorkingBytes(
	ctx context.Context, input *image.NRGBA, sourceICC, destinationICC []byte,
	options ...ProfileTransformOptions,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	settings, err := profileTransformSettings(options)
	if err != nil {
		return 0, err
	}
	_, _, _, working, err := profileTransformPreflight(ctx, input, sourceICC, destinationICC, settings)
	return working, err
}

func profileTransformPreflight(
	ctx context.Context, input *image.NRGBA, sourceICC, destinationICC []byte, settings ProfileTransformOptions,
) (int, int, int64, int64, error) {
	width, height, outputBytes, err := profileImageDimensions(input, settings.MaxPixels)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	sourceCost, err := profileTransformCost(ctx, sourceICC, settings)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("source profile: %w", err)
	}
	destinationCost, err := profileTransformCost(ctx, destinationICC, settings)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("destination profile: %w", err)
	}
	if outputBytes > settings.MaxWorkingBytes || sourceCost > settings.MaxWorkingBytes-outputBytes ||
		destinationCost > settings.MaxWorkingBytes-outputBytes-sourceCost {
		return 0, 0, 0, 0, fmt.Errorf("%w: profile conversion exceeds MaxWorkingBytes", ErrLimit)
	}
	return width, height, outputBytes, outputBytes + sourceCost + destinationCost, nil
}

func profileTransformSettings(options []ProfileTransformOptions) (ProfileTransformOptions, error) {
	settings := ProfileTransformOptions{}
	if len(options) > 1 {
		return settings, fmt.Errorf("%w: at most one profile transform option set is allowed", ErrInvalid)
	}
	if len(options) == 1 {
		settings = options[0]
	}
	if settings.MaxPixels < 0 || settings.MaxWorkingBytes < 0 || settings.MaxProfileBytes < 0 {
		return settings, fmt.Errorf("%w: negative profile transform budget", ErrInvalid)
	}
	if settings.MaxPixels == 0 {
		settings.MaxPixels = defaultMaxPixels
	}
	if settings.MaxWorkingBytes == 0 {
		settings.MaxWorkingBytes = defaultMaxOutputBytes
	}
	if settings.MaxProfileBytes == 0 {
		settings.MaxProfileBytes = defaultMaxProfileBytes
	}
	return settings, nil
}

func profileImageDimensions(input *image.NRGBA, pixelLimit int64) (int, int, int64, error) {
	if input == nil || input.Rect.Min.X > input.Rect.Max.X || input.Rect.Min.Y > input.Rect.Max.Y {
		return 0, 0, 0, fmt.Errorf("%w: invalid profile conversion image", ErrInvalid)
	}
	width, height := input.Rect.Dx(), input.Rect.Dy()
	if width < 0 || height < 0 || width > int(^uint(0)>>1)/4 ||
		width > 0 && int64(height) > math.MaxInt64/int64(width) {
		return 0, 0, 0, fmt.Errorf("%w: profile image dimensions overflow", ErrLimit)
	}
	pixels := int64(width) * int64(height)
	if pixels > pixelLimit || pixels > int64(int(^uint(0)>>1))/4 {
		return 0, 0, 0, fmt.Errorf("%w: profile image exceeds pixel budget", ErrLimit)
	}
	if input.Stride < width*4 || input.Stride < 0 {
		return 0, 0, 0, fmt.Errorf("%w: profile image stride is invalid", ErrInvalid)
	}
	if width > 0 && height > 0 {
		rowBytes := int64(width) * 4
		if int64(height-1) > (math.MaxInt64-rowBytes)/int64(input.Stride) ||
			int64(height-1)*int64(input.Stride)+rowBytes > int64(len(input.Pix)) {
			return 0, 0, 0, fmt.Errorf("%w: profile image pixel buffer is truncated", ErrInvalid)
		}
	}
	return width, height, pixels * 4, nil
}

func profileTransformCost(
	ctx context.Context, data []byte, settings ProfileTransformOptions,
) (int64, error) {
	if len(data) == 0 {
		if !settings.AssumeSRGB {
			return 0, fmt.Errorf("%w: an explicit ICC profile or AssumeSRGB is required", ErrInvalid)
		}
		return 1024, nil
	}
	if int64(len(data)) > settings.MaxProfileBytes {
		return 0, fmt.Errorf("%w: ICC profile exceeds MaxProfileBytes", ErrLimit)
	}
	if len(data) < 132 {
		return 0, fmt.Errorf("%w: truncated ICC header", ErrInvalid)
	}
	count := uint64(binary.BigEndian.Uint32(data[128:132]))
	if count > 4096 || count > (uint64(len(data))-132)/12 {
		return 0, fmt.Errorf("%w: invalid ICC tag count", ErrInvalid)
	}
	for index := range count {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		position := 132 + int(index)*12
		key := string(data[position : position+4])
		if key[:3] == "A2B" || key[:3] == "B2A" || key[:3] == "D2B" || key[:3] == "B2D" {
			return 0, fmt.Errorf("%w: profile transform LUT tag %q", ErrUnsupported, key)
		}
	}
	// Covers owned uint16 curves, tag/span maps and parsing temporaries for both profiles.
	if int64(len(data)) > (math.MaxInt64-1024-int64(count)*256)/2 {
		return 0, fmt.Errorf("%w: ICC parsing storage overflows", ErrLimit)
	}
	return int64(len(data))*2 + int64(count)*256 + 1024, nil
}

func profileTransformParse(
	ctx context.Context, data []byte, settings ProfileTransformOptions,
) (*iccProfile, error) {
	if len(data) == 0 {
		return assumedSRGBProfile(), nil
	}
	if string(data[16:20]) != "RGB " || string(data[20:24]) != "XYZ " {
		return nil, fmt.Errorf("%w: profile transform requires RGB matrix/TRC with XYZ PCS", ErrUnsupported)
	}
	// Check valid XYZ columns before the shared parser's singular-matrix validation.
	// The shared display converter retains its existing ErrInvalid error contract.
	var columns matrix3
	var seen [3]bool
	count := int(binary.BigEndian.Uint32(data[128:132]))
	for index := range count {
		position := 132 + index*12
		key := string(data[position : position+4])
		channel := -1
		for candidate, signature := range []string{"rXYZ", "gXYZ", "bXYZ"} {
			if key == signature {
				channel = candidate
			}
		}
		if channel < 0 {
			continue
		}
		start := uint64(binary.BigEndian.Uint32(data[position+4 : position+8]))
		size := uint64(binary.BigEndian.Uint32(data[position+8 : position+12]))
		if start+size > uint64(len(data)) {
			continue
		}
		values, err := parseXYZ(data[start : start+size])
		if err != nil {
			continue
		}
		seen[channel] = true
		for row := range values {
			columns[row][channel] = values[row]
		}
	}
	if seen == [3]bool{true, true, true} {
		if _, err := inverseProfileMatrix(columns); err != nil {
			return nil, err
		}
	}
	return parseICC(ctx, data, settings.MaxProfileBytes, IntentRelativeColorimetric)
}

func assumedSRGBProfile() *iccProfile {
	curve := toneCurve{kind: 4, parameters: [7]float64{2.4, 1 / 1.055, 0.055 / 1.055, 1 / 12.92, 0.04045}}
	return &iccProfile{space: "RGB ", pcs: "XYZ ", curves: [3]toneCurve{curve, curve, curve}, matrix: matrix3{
		{0.4360747, 0.3850649, 0.1430804},
		{0.2225045, 0.7168786, 0.0606169},
		{0.0139322, 0.0971045, 0.7141733},
	}}
}

func inverseProfileMatrix(matrix matrix3) (matrix3, error) {
	determinant := matrix[0][0]*(matrix[1][1]*matrix[2][2]-matrix[1][2]*matrix[2][1]) -
		matrix[0][1]*(matrix[1][0]*matrix[2][2]-matrix[1][2]*matrix[2][0]) +
		matrix[0][2]*(matrix[1][0]*matrix[2][1]-matrix[1][1]*matrix[2][0])
	if math.Abs(determinant) < 1e-12 || math.IsNaN(determinant) || math.IsInf(determinant, 0) {
		return matrix3{}, fmt.Errorf("%w: profile matrix is singular", ErrUnsupported)
	}
	var inverse matrix3
	for row := range inverse {
		for column := range inverse[row] {
			firstRow, secondRow := (column+1)%3, (column+2)%3
			firstColumn, secondColumn := (row+1)%3, (row+2)%3
			inverse[row][column] = (matrix[firstRow][firstColumn]*matrix[secondRow][secondColumn] -
				matrix[firstRow][secondColumn]*matrix[secondRow][firstColumn]) / determinant
		}
	}
	return inverse, nil
}

func invertibleProfileCurve(ctx context.Context, curve toneCurve) error {
	if len(curve.table) > 1 {
		for index := 1; index < len(curve.table); index++ {
			if index&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if curve.table[index] <= curve.table[index-1] {
				return fmt.Errorf("%w: destination TRC is not strictly increasing", ErrUnsupported)
			}
		}
		return nil
	}
	if curve.kind == 0 {
		return nil
	}
	gamma, slope, offset := curve.parameters[0], curve.parameters[1], curve.parameters[2]
	constant, threshold := curve.parameters[3], curve.parameters[4]
	highConstant, lowConstant := 0.0, 0.0
	if curve.kind == 1 || curve.kind == 2 {
		threshold = -offset / slope
		if threshold > 0 {
			return fmt.Errorf("%w: destination TRC has a constant lower branch", ErrUnsupported)
		}
		if curve.kind == 2 {
			highConstant = constant
		}
	}
	if curve.kind == 4 {
		highConstant, lowConstant = curve.parameters[5], curve.parameters[6]
	}
	high := func(value float64) float64 {
		return math.Pow(math.Max(0, slope*value+offset), gamma) + highConstant
	}
	low := func(value float64) float64 { return constant*value + lowConstant }
	start, end := high(0), high(1)
	if curve.kind >= 3 && threshold > 0 {
		if constant <= 0 {
			return fmt.Errorf("%w: destination TRC has a constant lower branch", ErrUnsupported)
		}
		start = low(0)
		if threshold > 1 {
			end = low(1)
		} else if high(threshold) < low(threshold) {
			return fmt.Errorf("%w: destination TRC decreases at its branch boundary", ErrUnsupported)
		}
	}
	if math.IsNaN(start) || math.IsNaN(end) || start < 0 || end > 1 || end <= start {
		return fmt.Errorf("%w: destination TRC has a clipped or constant interval", ErrUnsupported)
	}
	return nil
}

// ICC.1:2022 Annex F.1 defines inversion and nearest-endpoint treatment outside
// the inverse curve range. This subset rejects flat subdomains rather than guessing.
func inverseProfileCurve(curve toneCurve, value float64) float64 {
	if len(curve.table) > 1 {
		target := value * 65535
		if target <= float64(curve.table[0]) {
			return 0
		}
		last := len(curve.table) - 1
		if target >= float64(curve.table[last]) {
			return 1
		}
		upper := sort.Search(len(curve.table), func(index int) bool { return float64(curve.table[index]) >= target })
		fraction := (target - float64(curve.table[upper-1])) / float64(curve.table[upper]-curve.table[upper-1])
		return (float64(upper-1) + fraction) / float64(last)
	}
	if value <= curve.evaluate(0) {
		return 0
	}
	if value >= curve.evaluate(1) {
		return 1
	}
	gamma, slope, offset := curve.parameters[0], curve.parameters[1], curve.parameters[2]
	if curve.kind == 0 {
		return math.Pow(value, 1/gamma)
	}
	constant, threshold := curve.parameters[3], curve.parameters[4]
	highConstant, lowConstant := 0.0, 0.0
	if curve.kind == 2 {
		highConstant = constant
	}
	if curve.kind == 4 {
		highConstant, lowConstant = curve.parameters[5], curve.parameters[6]
	}
	if curve.kind >= 3 && threshold > 0 {
		lowEnd := constant*threshold + lowConstant
		highStart := math.Pow(math.Max(0, slope*threshold+offset), gamma) + highConstant
		if threshold > 1 || value < lowEnd {
			return (value - lowConstant) / constant
		}
		if value < highStart {
			return threshold
		}
	}
	return (math.Pow(math.Max(0, value-highConstant), 1/gamma) - offset) / slope
}
