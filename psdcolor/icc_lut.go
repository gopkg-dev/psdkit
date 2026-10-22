package psdcolor

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
)

type iccLUT struct {
	inputs           int
	grid, stride     [4]int
	data             []uint16
	inputTables      [4][]uint16
	outputTables     [3][]uint16
	aCurves          [4]toneCurve
	mCurves          [3]toneCurve
	bCurves          [3]toneCurve
	useA, useM, useB bool
	matrix           matrix3
	offset           [3]float64
	useMatrix        bool
	encoding         string
}

func parseLUT(
	ctx context.Context, data []byte, inputs int, pcs string, byteLimit int64,
) (*iccLUT, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("%w: truncated ICC LUT tag", ErrInvalid)
	}
	if binary.BigEndian.Uint32(data[4:8]) != 0 {
		return nil, fmt.Errorf("%w: ICC LUT reserved field is not zero", ErrInvalid)
	}
	switch string(data[:4]) {
	case "mft1", "mft2":
		if string(data[:4]) == "mft1" && pcs == "XYZ " {
			return nil, fmt.Errorf("%w: lut8Type PCSXYZ has no standard encoding", ErrUnsupported)
		}
		return parseLegacyLUT(ctx, data, inputs, byteLimit)
	case "mAB ":
		return parseAToBLUT(ctx, data, inputs, byteLimit)
	default:
		return nil, fmt.Errorf("%w: ICC forward tag type %q", ErrUnsupported, data[:4])
	}
}

func parseLegacyLUT(ctx context.Context, data []byte, inputs int, byteLimit int64) (*iccLUT, error) {
	precision, start := 1, 48
	inputEntries, outputEntries := 256, 256
	if string(data[:4]) == "mft2" {
		precision, start = 2, 52
	}
	if len(data) < start {
		return nil, fmt.Errorf("%w: truncated legacy ICC LUT header", ErrInvalid)
	}
	if int(data[8]) != inputs || inputs < 1 || inputs > 4 || data[9] != 3 {
		return nil, fmt.Errorf("%w: ICC forward LUT channel count does not match color space", ErrUnsupported)
	}
	if data[10] < 2 || data[11] != 0 {
		return nil, fmt.Errorf("%w: ICC LUT grid or padding is invalid", ErrInvalid)
	}
	for row := range 3 {
		for column := range 3 {
			expected := 0.0
			if row == column {
				expected = 1
			}
			position := 12 + 4*(row*3+column)
			if signedFixed(data[position:position+4]) != expected {
				return nil, fmt.Errorf("%w: device-space legacy LUT matrix must be identity", ErrUnsupported)
			}
		}
	}
	if precision == 2 {
		inputEntries = int(binary.BigEndian.Uint16(data[48:50]))
		outputEntries = int(binary.BigEndian.Uint16(data[50:52]))
		if inputEntries < 2 || inputEntries > 4096 || outputEntries < 2 || outputEntries > 4096 {
			return nil, fmt.Errorf("%w: legacy LUT shaper length is outside [2,4096]", ErrInvalid)
		}
	}
	lut := &iccLUT{inputs: inputs, encoding: string(data[:4])}
	for index := range inputs {
		lut.grid[index] = int(data[10])
	}
	clutEntries, err := lut.entryCount(byteLimit)
	if err != nil {
		return nil, err
	}
	totalEntries := int64(inputs)*int64(inputEntries) + int64(clutEntries) + 3*int64(outputEntries)
	if totalEntries > byteLimit/2 {
		return nil, fmt.Errorf("%w: ICC LUT storage exceeds MaxProfileBytes", ErrLimit)
	}
	if totalEntries > int64(len(data)-start)/int64(precision) {
		return nil, fmt.Errorf("%w: legacy ICC LUT tables are truncated", ErrInvalid)
	}
	position := start
	readTable := func(count int) ([]uint16, error) {
		table, err := lutValues(ctx, data[position:position+count*precision], count, precision)
		position += count * precision
		return table, err
	}
	for channel := range inputs {
		lut.inputTables[channel], err = readTable(inputEntries)
		if err != nil {
			return nil, err
		}
	}
	lut.data, err = readTable(clutEntries)
	if err != nil {
		return nil, err
	}
	for channel := range 3 {
		lut.outputTables[channel], err = readTable(outputEntries)
		if err != nil {
			return nil, err
		}
	}
	return lut, nil
}

func (lut *iccLUT) entryCount(byteLimit int64) (int, error) {
	entries := int64(3)
	for dimension := lut.inputs - 1; dimension >= 0; dimension-- {
		count := lut.grid[dimension]
		if count < 2 {
			return 0, fmt.Errorf("%w: ICC CLUT dimension must contain at least two nodes", ErrInvalid)
		}
		if entries > byteLimit/2/int64(count) {
			return 0, fmt.Errorf("%w: ICC CLUT dimensions exceed storage budget", ErrLimit)
		}
		lut.stride[dimension] = int(entries)
		entries *= int64(count)
	}
	return int(entries), nil
}

func lutValues(ctx context.Context, data []byte, count, precision int) ([]uint16, error) {
	values := make([]uint16, count)
	for index := range values {
		if index&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if precision == 1 {
			values[index] = uint16(data[index]) * 257
		} else {
			values[index] = binary.BigEndian.Uint16(data[index*2 : index*2+2])
		}
	}
	return values, nil
}

func interpolateTable(table []uint16, value float64) float64 {
	if len(table) == 0 {
		return value
	}
	position := clampUnit(value) * float64(len(table)-1)
	index := int(position)
	if index >= len(table)-1 {
		return float64(table[len(table)-1]) / 65535
	}
	fraction := position - float64(index)
	return ((1-fraction)*float64(table[index]) + fraction*float64(table[index+1])) / 65535
}

func (lut *iccLUT) interpolate(values [4]float64) [3]float64 {
	var base [4]int
	var fraction [4]float64
	for dimension := 0; dimension < lut.inputs; dimension++ {
		coordinate := clampUnit(values[dimension]) * float64(lut.grid[dimension]-1)
		base[dimension] = int(coordinate)
		fraction[dimension] = coordinate - float64(base[dimension])
		if base[dimension] == lut.grid[dimension]-1 {
			base[dimension]--
			fraction[dimension] = 1
		}
	}
	if lut.inputs < 3 {
		var result [3]float64
		for corner := 0; corner < 1<<lut.inputs; corner++ {
			weight, position := 1.0, 0
			for dimension := 0; dimension < lut.inputs; dimension++ {
				upper := (corner >> dimension) & 1
				position += (base[dimension] + upper) * lut.stride[dimension]
				if upper == 0 {
					weight *= 1 - fraction[dimension]
				} else {
					weight *= fraction[dimension]
				}
			}
			for channel := range result {
				result[channel] += weight * float64(lut.data[position+channel]) / 65535
			}
		}
		return result
	}
	if lut.inputs == 3 {
		return lut.tetrahedron(base, fraction, 0, 0)
	}
	// For four inputs, interpolate two three-dimensional tetrahedra along the first dimension.
	lower := lut.tetrahedron(base, fraction, 1, base[0]*lut.stride[0])
	upper := lut.tetrahedron(base, fraction, 1, (base[0]+1)*lut.stride[0])
	for channel := range lower {
		lower[channel] += (upper[channel] - lower[channel]) * fraction[0]
	}
	return lower
}

func (lut *iccLUT) tetrahedron(
	base [4]int, fraction [4]float64, first, offset int,
) [3]float64 {
	dimensions := [3]int{first, first + 1, first + 2}
	for index := 1; index < 3; index++ {
		for cursor := index; cursor > 0 && fraction[dimensions[cursor]] > fraction[dimensions[cursor-1]]; cursor-- {
			dimensions[cursor], dimensions[cursor-1] = dimensions[cursor-1], dimensions[cursor]
		}
	}
	position := offset
	for dimension := first; dimension < first+3; dimension++ {
		position += base[dimension] * lut.stride[dimension]
	}
	var result [3]float64
	weight := 1 - fraction[dimensions[0]]
	for vertex := range 4 {
		for channel := range result {
			result[channel] += weight * float64(lut.data[position+channel]) / 65535
		}
		if vertex < 3 {
			position += lut.stride[dimensions[vertex]]
			weight = fraction[dimensions[vertex]]
			if vertex < 2 {
				weight -= fraction[dimensions[vertex+1]]
			}
		}
	}
	return result
}

func (lut *iccLUT) display(input [4]float64, pcs string) [3]float64 {
	for channel := 0; channel < lut.inputs; channel++ {
		input[channel] = interpolateTable(lut.inputTables[channel], input[channel])
		if lut.useA {
			input[channel] = lut.aCurves[channel].evaluate(input[channel])
		}
	}
	var output [3]float64
	if len(lut.data) > 0 {
		output = lut.interpolate(input)
	} else {
		output = [3]float64{input[0], input[1], input[2]}
	}
	for channel := range output {
		output[channel] = interpolateTable(lut.outputTables[channel], output[channel])
		if lut.useM {
			output[channel] = lut.mCurves[channel].evaluate(output[channel])
		}
	}
	if lut.useMatrix {
		output = lut.matrix.multiply(output)
		for channel := range output {
			output[channel] = clampUnit(output[channel] + lut.offset[channel])
		}
	}
	if lut.useB {
		for channel := range output {
			output[channel] = lut.bCurves[channel].evaluate(output[channel])
		}
	}
	if pcs == "XYZ " {
		for channel := range output {
			output[channel] *= 65535.0 / 32768
		}
		return xyzD50RGB(output)
	}
	if lut.encoding == "mft2" {
		return labRGB(math.Min(output[0]*65535, 65280)/65280*100,
			math.Min(output[1]*65535, 65280)/256-128, math.Min(output[2]*65535, 65280)/256-128)
	}
	return labRGB(output[0]*100, output[1]*255-128, output[2]*255-128)
}

func parseAToBLUT(ctx context.Context, data []byte, inputs int, byteLimit int64) (*iccLUT, error) {
	if len(data) < 32 || int(data[8]) != inputs || data[9] != 3 || binary.BigEndian.Uint16(data[10:12]) != 0 {
		return nil, fmt.Errorf("%w: malformed ICC A-to-B header", ErrInvalid)
	}
	var offsets [5]int
	for index := range offsets {
		value := binary.BigEndian.Uint32(data[12+index*4 : 16+index*4])
		if value != 0 && (value < 32 || value%4 != 0 || uint64(value) >= uint64(len(data))) {
			return nil, fmt.Errorf("%w: ICC A-to-B element offset is invalid", ErrInvalid)
		}
		offsets[index] = int(value)
	}
	if offsets[0] == 0 || (offsets[1] == 0) != (offsets[2] == 0) ||
		(offsets[3] == 0) != (offsets[4] == 0) || inputs != 3 && offsets[3] == 0 {
		return nil, fmt.Errorf("%w: unsupported ICC A-to-B element combination", ErrInvalid)
	}
	lut := &iccLUT{inputs: inputs, encoding: "mAB "}
	var stored int64
	readCurves := func(start, count int) ([]toneCurve, error) {
		curves := make([]toneCurve, count)
		position := start
		for index := range curves {
			length, err := embeddedCurveLength(data[position:])
			if err != nil {
				return nil, err
			}
			curves[index], err = parseCurveSamples(ctx, data[position:position+length], byteLimit-stored, false)
			if err != nil {
				return nil, err
			}
			stored += int64(len(curves[index].table)) * 2
			position += (length + 3) &^ 3
			if position > len(data) {
				return nil, fmt.Errorf("%w: ICC embedded curve padding is truncated", ErrInvalid)
			}
		}
		return curves, nil
	}
	bCurves, err := readCurves(offsets[0], 3)
	if err != nil {
		return nil, err
	}
	lut.useB = true
	copy(lut.bCurves[:], bCurves)
	if offsets[1] != 0 {
		start := offsets[1]
		if len(data)-start < 48 {
			return nil, fmt.Errorf("%w: ICC A-to-B matrix is truncated", ErrInvalid)
		}
		for row := range 3 {
			for column := range 3 {
				position := start + (row*3+column)*4
				lut.matrix[row][column] = signedFixed(data[position : position+4])
			}
			lut.offset[row] = signedFixed(data[start+36+row*4 : start+40+row*4])
		}
		mCurves, err := readCurves(offsets[2], 3)
		if err != nil {
			return nil, err
		}
		copy(lut.mCurves[:], mCurves)
		lut.useM, lut.useMatrix = true, true
	}
	if offsets[3] != 0 {
		aCurves, err := readCurves(offsets[4], inputs)
		if err != nil {
			return nil, err
		}
		copy(lut.aCurves[:], aCurves)
		lut.useA = true
		start := offsets[3]
		if len(data)-start < 20 {
			return nil, fmt.Errorf("%w: ICC A-to-B CLUT is truncated", ErrInvalid)
		}
		for dimension := range 16 {
			if dimension < inputs {
				lut.grid[dimension] = int(data[start+dimension])
			} else if data[start+dimension] != 0 {
				return nil, fmt.Errorf("%w: ICC CLUT unused dimensions are not zero", ErrInvalid)
			}
		}
		precision := int(data[start+16])
		if precision != 1 && precision != 2 || data[start+17] != 0 || data[start+18] != 0 || data[start+19] != 0 {
			return nil, fmt.Errorf("%w: ICC CLUT precision or padding is invalid", ErrInvalid)
		}
		entries, err := lut.entryCount(byteLimit - stored)
		if err != nil {
			return nil, err
		}
		if entries > (len(data)-start-20)/precision {
			return nil, fmt.Errorf("%w: ICC CLUT sample table is truncated", ErrInvalid)
		}
		lut.data, err = lutValues(ctx, data[start+20:start+20+entries*precision], entries, precision)
		if err != nil {
			return nil, err
		}
	}
	return lut, nil
}

func embeddedCurveLength(data []byte) (int, error) {
	if len(data) < 12 {
		return 0, fmt.Errorf("%w: ICC embedded curve is truncated", ErrInvalid)
	}
	switch string(data[:4]) {
	case "curv":
		count := uint64(binary.BigEndian.Uint32(data[8:12]))
		if count > (uint64(len(data))-12)/2 {
			return 0, fmt.Errorf("%w: ICC embedded curve samples are truncated", ErrInvalid)
		}
		return 12 + int(count)*2, nil
	case "para":
		kind := binary.BigEndian.Uint16(data[8:10])
		if kind > 4 {
			return 0, fmt.Errorf("%w: ICC embedded parametric curve type", ErrUnsupported)
		}
		length := 12 + []int{1, 3, 4, 5, 7}[kind]*4
		if len(data) < length {
			return 0, fmt.Errorf("%w: ICC embedded parametric curve parameters are truncated", ErrInvalid)
		}
		return length, nil
	default:
		return 0, fmt.Errorf("%w: ICC embedded curve type %q", ErrUnsupported, data[:4])
	}
}
