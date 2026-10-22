package psdcolor

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

// ICC.1:2022 specifies the D50 PCS and matrix/TRC and grayTRC models in Annex F.
// https://www.color.org/specification/ICC.1-2022-05.pdf.
var iccWhite = [3]float64{0.9642, 1, 0.8249}

type matrix3 [3][3]float64

func (matrix matrix3) multiply(value [3]float64) [3]float64 {
	var result [3]float64
	for row := range result {
		for column := range value {
			result[row] += matrix[row][column] * value[column]
		}
	}
	return result
}

var d50ToD65 = adaptationMatrix(iccWhite, [3]float64{0.95047, 1, 1.08883})

func adaptationMatrix(sourceWhite, targetWhite [3]float64) matrix3 {
	bradford := matrix3{
		{0.8951, 0.2664, -0.1614}, {-0.7502, 1.7135, 0.0367}, {0.0389, -0.0685, 1.0296},
	}
	inverse := matrix3{
		{0.9869929, -0.1470543, 0.1599627},
		{0.4323053, 0.5183603, 0.0492912},
		{-0.0085287, 0.0400428, 0.9684867},
	}
	sourceCone, targetCone := bradford.multiply(sourceWhite), bradford.multiply(targetWhite)
	var result matrix3
	for row := range result {
		for column := range result[row] {
			for component := range 3 {
				result[row][column] += inverse[row][component] *
					(targetCone[component] / sourceCone[component]) * bradford[component][column]
			}
		}
	}
	return result
}

func xyzD50RGB(value [3]float64) [3]float64 {
	d65 := d50ToD65.multiply(value)
	xyzToSRGB := matrix3{
		{3.2404542, -1.5371385, -0.4985314},
		{-0.9692660, 1.8760108, 0.0415560},
		{0.0556434, -0.2040259, 1.0572252},
	}
	rgb := xyzToSRGB.multiply(d65)
	for channel := range rgb {
		rgb[channel] = encodeSRGB(rgb[channel])
	}
	return rgb
}

type iccProfile struct {
	space, pcs string
	matrix     matrix3
	curves     [3]toneCurve
	lut        *iccLUT
}

type iccTagSpan struct {
	start, size uint32
}

type toneCurve struct {
	kind       uint16
	parameters [7]float64
	table      []uint16
}

func (curve toneCurve) evaluate(value float64) float64 {
	value = clampUnit(value)
	if len(curve.table) > 1 {
		coordinate := value * float64(len(curve.table)-1)
		index := int(coordinate)
		if index >= len(curve.table)-1 {
			return float64(curve.table[len(curve.table)-1]) / 65535
		}
		fraction := coordinate - float64(index)
		return ((1-fraction)*float64(curve.table[index]) + fraction*float64(curve.table[index+1])) / 65535
	}
	gamma := curve.parameters[0]
	if curve.kind == 0 {
		return clampUnit(math.Pow(value, gamma))
	}
	slope, offset := curve.parameters[1], curve.parameters[2]
	constant, threshold := curve.parameters[3], curve.parameters[4]
	var result float64
	switch curve.kind {
	case 1:
		if value >= -offset/slope {
			result = math.Pow(math.Max(0, slope*value+offset), gamma)
		}
	case 2:
		result = constant
		if value >= -offset/slope {
			result += math.Pow(math.Max(0, slope*value+offset), gamma)
		}
	case 3:
		result = constant * value
		if value >= threshold {
			result = math.Pow(math.Max(0, slope*value+offset), gamma)
		}
	case 4:
		result = constant*value + curve.parameters[6]
		if value >= threshold {
			result = math.Pow(math.Max(0, slope*value+offset), gamma) + curve.parameters[5]
		}
	}
	return clampUnit(result)
}

func parseICC(
	ctx context.Context, data []byte, byteLimit int64, intents ...RenderingIntent,
) (*iccProfile, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if int64(len(data)) > byteLimit {
		return nil, fmt.Errorf("%w: ICC profile exceeds MaxProfileBytes", ErrLimit)
	}
	if len(data) < 132 || string(data[36:40]) != "acsp" {
		return nil, fmt.Errorf("%w: ICC header is missing or malformed", ErrInvalid)
	}
	size := uint64(binary.BigEndian.Uint32(data[0:4]))
	if size < 132 || size > uint64(len(data)) {
		return nil, fmt.Errorf("%w: ICC declared size is invalid", ErrInvalid)
	}
	for _, padding := range data[size:] {
		if padding != 0 {
			return nil, fmt.Errorf("%w: ICC contains data after its declared size", ErrInvalid)
		}
	}
	data = data[:size]
	if data[8] != 2 && data[8] != 4 {
		return nil, fmt.Errorf("%w: ICC profile version %d", ErrUnsupported, data[8])
	}
	for index, white := range iccWhite {
		illuminant := signedFixed(data[68+index*4 : 72+index*4])
		if math.Abs(illuminant-white) > 0.002 {
			return nil, fmt.Errorf("%w: ICC PCS illuminant is not D50", ErrUnsupported)
		}
	}
	profileClass := string(data[12:16])
	switch profileClass {
	case "scnr", "mntr", "prtr", "spac":
	case "link":
		return nil, fmt.Errorf("%w: ICC device-link profiles require a LUT transform", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: ICC profile class %q", ErrUnsupported, profileClass)
	}
	profile := &iccProfile{space: string(data[16:20]), pcs: string(data[20:24])}
	if profile.space != "RGB " && profile.space != "GRAY" && profile.space != "CMYK" {
		return nil, fmt.Errorf("%w: ICC color space %q", ErrUnsupported, profile.space)
	}
	if profile.pcs != "XYZ " && profile.pcs != "Lab " {
		return nil, fmt.Errorf("%w: ICC profile connection space %q", ErrUnsupported, profile.pcs)
	}
	count := uint64(binary.BigEndian.Uint32(data[128:132]))
	if count > (uint64(len(data))-132)/12 {
		return nil, fmt.Errorf("%w: ICC tag table is truncated", ErrInvalid)
	}
	if count > 4096 {
		return nil, fmt.Errorf("%w: ICC tag count exceeds supported parsing budget", ErrLimit)
	}
	tableEnd := uint64(132) + count*12
	tags := make(map[string]iccTagSpan, int(count))
	spans := make([]iccTagSpan, 0, int(count))
	for index := range count {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		position := 132 + int(index)*12
		signature := string(data[position : position+4])
		span := iccTagSpan{
			start: binary.BigEndian.Uint32(data[position+4 : position+8]),
			size:  binary.BigEndian.Uint32(data[position+8 : position+12]),
		}
		if span.size < 8 || span.start%4 != 0 || uint64(span.start) < tableEnd ||
			uint64(span.start)+uint64(span.size) > uint64(len(data)) {
			return nil, fmt.Errorf("%w: ICC tag %q has invalid bounds", ErrInvalid, signature)
		}
		if _, exists := tags[signature]; exists {
			return nil, fmt.Errorf("%w: duplicate ICC tag %q", ErrInvalid, signature)
		}
		if slices.Contains([]string{
			"D2B0", "D2B1", "D2B2", "D2B3",
		}, signature) {
			return nil, fmt.Errorf("%w: ICC LUT tag %q is not implemented", ErrUnsupported, signature)
		}
		tags[signature] = span
		spans = append(spans, span)
	}
	slices.SortFunc(spans, func(left, right iccTagSpan) int {
		if left.start < right.start {
			return -1
		}
		if left.start > right.start {
			return 1
		}
		return int(left.size) - int(right.size)
	})
	for index := 1; index < len(spans); index++ {
		previous, current := spans[index-1], spans[index]
		if uint64(previous.start)+uint64(previous.size) > uint64(current.start) && previous != current {
			return nil, fmt.Errorf("%w: ICC tag data partially overlaps", ErrInvalid)
		}
	}
	readTag := func(signature string) ([]byte, error) {
		span, exists := tags[signature]
		if !exists {
			return nil, fmt.Errorf("%w: ICC tag %q is missing", ErrInvalid, signature)
		}
		return data[span.start : uint64(span.start)+uint64(span.size)], nil
	}
	whiteTag, err := readTag("wtpt")
	if err != nil {
		return nil, err
	}
	mediaWhite, err := parseXYZ(whiteTag)
	if err != nil {
		return nil, err
	}
	if mediaWhite[0] <= 0 || mediaWhite[1] <= 0 || mediaWhite[2] <= 0 {
		return nil, fmt.Errorf("%w: ICC media white point is not positive", ErrInvalid)
	}
	intent := IntentRelativeColorimetric
	if len(intents) > 0 {
		intent = intents[0]
	}
	signature := "A2B1"
	if intent == IntentPerceptual {
		signature = "A2B0"
	}
	if intent == IntentSaturation {
		signature = "A2B2"
	}
	span, hasTransform := tags[signature]
	if !hasTransform {
		span, hasTransform = tags["A2B0"]
	}
	if hasTransform {
		inputs := 3
		if profile.space == "GRAY" {
			inputs = 1
		}
		if profile.space == "CMYK" {
			inputs = 4
		}
		profile.lut, err = parseLUT(ctx, data[span.start:uint64(span.start)+uint64(span.size)],
			inputs, profile.pcs, byteLimit)
		if err != nil {
			return nil, err
		}
		return profile, nil
	}
	if profile.space == "CMYK" || profile.space == "RGB " && profile.pcs != "XYZ " {
		return nil, fmt.Errorf("%w: source profile requires an implemented forward ICC transform", ErrUnsupported)
	}
	var curveStorage int64
	parsedCurves := make(map[iccTagSpan]toneCurve)
	readCurve := func(signature string) (toneCurve, error) {
		span, exists := tags[signature]
		if !exists {
			return toneCurve{}, fmt.Errorf("%w: ICC tag %q is missing", ErrInvalid, signature)
		}
		if curve, found := parsedCurves[span]; found {
			return curve, nil
		}
		curve, err := parseCurve(ctx, data[span.start:uint64(span.start)+uint64(span.size)], byteLimit-curveStorage)
		if err != nil {
			return toneCurve{}, err
		}
		curveStorage += int64(len(curve.table)) * 2
		parsedCurves[span] = curve
		return curve, nil
	}
	if profile.space == "GRAY" {
		profile.curves[0], err = readCurve("kTRC")
		if err != nil {
			return nil, err
		}
		return profile, nil
	}
	for index, prefix := range []string{"r", "g", "b"} {
		matrixTag, err := readTag(prefix + "XYZ")
		if err != nil {
			return nil, err
		}
		column, err := parseXYZ(matrixTag)
		if err != nil {
			return nil, err
		}
		for row := range profile.matrix {
			profile.matrix[row][index] = column[row]
		}
		profile.curves[index], err = readCurve(prefix + "TRC")
		if err != nil {
			return nil, err
		}
	}
	columns := profile.matrix
	determinant := columns[0][0]*(columns[1][1]*columns[2][2]-columns[1][2]*columns[2][1]) -
		columns[0][1]*(columns[1][0]*columns[2][2]-columns[1][2]*columns[2][0]) +
		columns[0][2]*(columns[1][0]*columns[2][1]-columns[1][1]*columns[2][0])
	if math.Abs(determinant) < 1e-12 {
		return nil, fmt.Errorf("%w: ICC color matrix is singular", ErrInvalid)
	}
	return profile, nil
}

func parseXYZ(data []byte) ([3]float64, error) {
	if len(data) != 20 || string(data[:4]) != "XYZ " || binary.BigEndian.Uint32(data[4:8]) != 0 {
		return [3]float64{}, fmt.Errorf("%w: malformed ICC XYZ tag", ErrInvalid)
	}
	var value [3]float64
	for index := range value {
		value[index] = signedFixed(data[8+index*4 : 12+index*4])
	}
	return value, nil
}

func parseCurve(ctx context.Context, data []byte, byteLimit int64) (toneCurve, error) {
	return parseCurveSamples(ctx, data, byteLimit, true)
}

func parseCurveSamples(ctx context.Context, data []byte, byteLimit int64, monotonic bool) (toneCurve, error) {
	if byteLimit < 0 {
		return toneCurve{}, fmt.Errorf("%w: ICC curves exceed storage budget", ErrLimit)
	}
	if len(data) < 12 {
		return toneCurve{}, fmt.Errorf("%w: truncated ICC curve", ErrInvalid)
	}
	if binary.BigEndian.Uint32(data[4:8]) != 0 {
		return toneCurve{}, fmt.Errorf("%w: ICC curve reserved field is not zero", ErrInvalid)
	}
	switch string(data[:4]) {
	case "curv":
		count := uint64(binary.BigEndian.Uint32(data[8:12]))
		if count > (uint64(len(data))-12)/2 {
			return toneCurve{}, fmt.Errorf("%w: ICC curve table is truncated", ErrInvalid)
		}
		curve := toneCurve{parameters: [7]float64{1}}
		if count == 1 {
			curve.parameters[0] = float64(binary.BigEndian.Uint16(data[12:14])) / 256
			if curve.parameters[0] <= 0 {
				return toneCurve{}, fmt.Errorf("%w: ICC gamma must be positive", ErrInvalid)
			}
		}
		if count > 1 {
			if count > uint64(byteLimit)/2 {
				return toneCurve{}, fmt.Errorf("%w: ICC curves exceed storage budget", ErrLimit)
			}
			curve.table = make([]uint16, int(count))
			for index := range curve.table {
				if index&4095 == 0 {
					if err := ctx.Err(); err != nil {
						return toneCurve{}, err
					}
				}
				curve.table[index] = binary.BigEndian.Uint16(data[12+index*2 : 14+index*2])
				if monotonic && index > 0 && curve.table[index] < curve.table[index-1] {
					return toneCurve{}, fmt.Errorf("%w: ICC curve is not monotonic", ErrInvalid)
				}
			}
		}
		return curve, nil
	case "para":
		if binary.BigEndian.Uint16(data[10:12]) != 0 {
			return toneCurve{}, fmt.Errorf("%w: ICC parametric curve reserved field is not zero", ErrInvalid)
		}
		kind := binary.BigEndian.Uint16(data[8:10])
		if kind > 4 {
			return toneCurve{}, fmt.Errorf("%w: ICC parametric curve type %d", ErrUnsupported, kind)
		}
		count := []int{1, 3, 4, 5, 7}[kind]
		if len(data) < 12+count*4 {
			return toneCurve{}, fmt.Errorf("%w: ICC parametric curve is truncated", ErrInvalid)
		}
		curve := toneCurve{kind: kind}
		for index := range count {
			curve.parameters[index] = signedFixed(data[12+index*4 : 16+index*4])
		}
		if curve.parameters[0] <= 0 || kind > 0 && curve.parameters[1] <= 0 {
			return toneCurve{}, fmt.Errorf("%w: ICC parametric gamma and slope must be positive", ErrInvalid)
		}
		if kind >= 3 && (curve.parameters[3] < 0 ||
			curve.parameters[4] < -curve.parameters[2]/curve.parameters[1]) {
			return toneCurve{}, fmt.Errorf("%w: ICC parametric curve has an invalid lower branch", ErrInvalid)
		}
		return curve, nil
	default:
		return toneCurve{}, fmt.Errorf("%w: ICC curve tag type %q", ErrUnsupported, data[:4])
	}
}

func signedFixed(data []byte) float64 {
	return float64(int32(binary.BigEndian.Uint32(data))) / 65536
}
