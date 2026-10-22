package psdcolor

import (
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"math"
	"testing"
)

func legacyLUTFixture(kind string, inputs, grid int, evaluate func([4]float64) [3]uint16) []byte {
	precision, start, entries := 1, 48, 256
	if kind == "mft2" {
		precision, start, entries = 2, 52, 2
	}
	count := 1
	for range inputs {
		count *= grid
	}
	data := make([]byte, start+(inputs*entries+count*3+3*entries)*precision)
	copy(data[:4], kind)
	data[8], data[9], data[10] = byte(inputs), 3, byte(grid)
	for index := range 3 {
		binary.BigEndian.PutUint32(data[12+(index*3+index)*4:], 65536)
	}
	if precision == 2 {
		binary.BigEndian.PutUint16(data[48:50], uint16(entries))
		binary.BigEndian.PutUint16(data[50:52], uint16(entries))
	}
	position := start
	writeValue := func(value uint16) {
		if precision == 1 {
			data[position] = byte(value / 257)
		} else {
			binary.BigEndian.PutUint16(data[position:], value)
		}
		position += precision
	}
	for range inputs {
		for index := 0; index < entries; index++ {
			writeValue(uint16(index * 65535 / (entries - 1)))
		}
	}
	for index := 0; index < count; index++ {
		var coordinate [4]float64
		code := index
		for dimension := inputs - 1; dimension >= 0; dimension-- {
			coordinate[dimension] = float64(code%grid) / float64(grid-1)
			code /= grid
		}
		for _, value := range evaluate(coordinate) {
			writeValue(value)
		}
	}
	for range 3 {
		for index := 0; index < entries; index++ {
			writeValue(uint16(index * 65535 / (entries - 1)))
		}
	}
	return data
}

func forwardProfile(space, pcs, key string, version byte, table []byte) []byte {
	return profileFixture(space, pcs, version,
		profileFixtureTag{"wtpt", profileXYZ(iccWhite)}, profileFixtureTag{key, table})
}

func TestICCLUTLegacyPCSAndCMYKPolarity(t *testing.T) {
	for _, testCase := range []struct {
		kind, space string
		inputs      int
		code        [3]uint16
	}{
		{"mft1", "RGB ", 3, [3]uint16{128 * 257, 128 * 257, 128 * 257}},
		{"mft2", "CMYK", 4, [3]uint16{32640, 32768, 32768}},
	} {
		table := legacyLUTFixture(testCase.kind, testCase.inputs, 2, func([4]float64) [3]uint16 {
			return testCase.code
		})
		mode := uint16(3)
		if testCase.space == "CMYK" {
			mode = 4
		}
		values := [][]float64{{128}, {64}, {192}}
		if mode == 4 {
			values = append(values, []float64{255})
		}
		pixels := colorFixture(mode, 8, 1, values...)
		image, err := Convert(nil, pixels, Options{ICCProfile: forwardProfile(testCase.space, "Lab ", "A2B0", 2, table)})
		if err != nil || image.NRGBAAt(0, 0) != (color.NRGBA{119, 119, 119, 255}) {
			t.Fatalf("legacy %s PCS encoding differs: %v, %v", testCase.kind, image, err)
		}
	}
	// A profile that maps its cyan input to lightness distinguishes PSD inverted storage from ICC ink coverage.
	table := legacyLUTFixture("mft2", 4, 2, func(input [4]float64) [3]uint16 {
		return [3]uint16{uint16(input[0] * 65280), 32768, 32768}
	})
	pixels := colorFixture(4, 8, 1, []float64{255}, []float64{255}, []float64{255}, []float64{255})
	image, err := Convert(nil, pixels, Options{ICCProfile: forwardProfile("CMYK", "Lab ", "A2B1", 2, table)})
	if err != nil || image.NRGBAAt(0, 0) != (color.NRGBA{0, 0, 0, 255}) {
		t.Fatalf("PSD inverse CMYK was not converted to ICC ink input: %v, %v", image, err)
	}
}

func TestICCLUTPCSXYZIdentityAndTetrahedralInterpolation(t *testing.T) {
	table := legacyLUTFixture("mft2", 3, 2, func(input [4]float64) [3]uint16 {
		var xyz [3]float64
		for channel := range 3 {
			for component := range 3 {
				xyz[component] += profileSRGBColumns[channel][component] * input[channel]
			}
		}
		return [3]uint16{uint16(math.Round(xyz[0] * 32768)), uint16(math.Round(xyz[1] * 32768)),
			uint16(math.Round(xyz[2] * 32768))}
	})
	pixels := colorFixture(3, 16, 1, []float64{32768}, []float64{32768}, []float64{32768})
	image, err := Convert(nil, pixels, Options{ICCProfile: forwardProfile("RGB ", "XYZ ", "A2B1", 2, table)})
	if err != nil || image.NRGBAAt(0, 0) != (color.NRGBA{188, 188, 188, 255}) {
		t.Fatalf("PCS XYZ LUT normalization differs: %v, %v", image, err)
	}
	lut := &iccLUT{inputs: 3, grid: [4]int{2, 2, 2}}
	_, _ = lut.entryCount(1024)
	lut.data = make([]uint16, 24)
	lut.data[7*3] = 65535
	// Only the opposite cube corner is one; a tetrahedron assigns the smallest fractional coordinate to it.
	result := lut.interpolate([4]float64{0.2, 0.7, 0.5})
	if math.Abs(result[0]-0.2) > 1e-9 {
		t.Fatalf("tetrahedral interpolation was replaced by trilinear averaging: %v", result)
	}
}

func TestICCLUTIntentSelectionAndUnsupportedFloatEncoding(t *testing.T) {
	constant := func(lightness uint16) []byte {
		return legacyLUTFixture("mft2", 3, 2, func([4]float64) [3]uint16 {
			return [3]uint16{lightness, 32768, 32768}
		})
	}
	profile := profileFixture("RGB ", "Lab ", 2,
		profileFixtureTag{"wtpt", profileXYZ(iccWhite)}, profileFixtureTag{"A2B0", constant(0)},
		profileFixtureTag{"A2B1", constant(65280)}, profileFixtureTag{"A2B2", constant(32640)})
	pixels := colorFixture(3, 8, 1, []float64{128}, []float64{128}, []float64{128})
	for _, testCase := range []struct {
		intent RenderingIntent
		value  uint8
	}{{IntentRelativeColorimetric, 255}, {IntentPerceptual, 0}, {IntentSaturation, 119}} {
		image, err := Convert(nil, pixels, Options{ICCProfile: profile, Intent: testCase.intent})
		if err != nil || image.NRGBAAt(0, 0).R != testCase.value {
			t.Fatalf("intent %d selected the wrong forward table: %v", testCase.intent, err)
		}
	}
	floating := colorFixture(3, 32, 1, []float64{0.5}, []float64{0.5}, []float64{0.5})
	if _, err := Convert(nil, floating, Options{ICCProfile: profile}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("LUT transfer curves were silently applied to linear floats: %v", err)
	}
	if _, err := Convert(nil, floating, Options{ICCProfile: profile, FloatEncoding: FloatEncodedRGB}); err != nil {
		t.Fatalf("explicit encoded float LUT input rejected: %v", err)
	}
	if _, err := Convert(nil, pixels, Options{Intent: IntentAbsoluteColorimetric}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("absolute intent silently treated as relative: %v", err)
	}
}

func aToBIdentityFixture() []byte {
	data := make([]byte, 68)
	copy(data[:4], "mAB ")
	data[8], data[9] = 3, 3
	binary.BigEndian.PutUint32(data[12:16], 32)
	for offset := 32; offset < 68; offset += 12 {
		copy(data[offset:offset+4], "curv")
	}
	return data
}

func TestICCLUTAToBCurvesAndBounds(t *testing.T) {
	table := aToBIdentityFixture()
	pixels := colorFixture(3, 8, 1, []float64{128}, []float64{128}, []float64{128})
	image, err := Convert(nil, pixels, Options{ICCProfile: forwardProfile("RGB ", "Lab ", "A2B0", 4, table)})
	if err != nil || image.NRGBAAt(0, 0) != (color.NRGBA{119, 119, 119, 255}) {
		t.Fatalf("mAB identity B curves differ: %v, %v", image, err)
	}
	for _, change := range []func([]byte){
		func(data []byte) { binary.BigEndian.PutUint32(data[12:16], 1) },
		func(data []byte) { binary.BigEndian.PutUint32(data[12:16], 0) },
		func(data []byte) { binary.BigEndian.PutUint32(data[16:20], 32) },
		func(data []byte) { binary.BigEndian.PutUint32(data[28:32], 32) },
	} {
		invalid := append([]byte(nil), table...)
		change(invalid)
		if _, err := parseLUT(context.Background(), invalid, 3, "Lab ", 4096); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed mAB offsets/element pairing accepted: %v", err)
		}
	}
}

func aToBMatrixCLUTFixture() []byte {
	data := make([]byte, 256)
	copy(data[:4], "mAB ")
	data[8], data[9] = 3, 3
	for index, offset := range []uint32{32, 104, 68, 188, 152} {
		binary.BigEndian.PutUint32(data[12+index*4:], offset)
	}
	for _, start := range []int{32, 68, 152} {
		for offset := start; offset < start+36; offset += 12 {
			copy(data[offset:offset+4], "curv")
		}
	}
	for row := range 3 {
		for column := range 3 {
			value := profileSRGBColumns[column][row] * 32768 / 65535
			binary.BigEndian.PutUint32(data[104+(row*3+column)*4:], uint32(int32(math.Round(value*65536))))
		}
	}
	data[188], data[189], data[190], data[204] = 2, 2, 2, 2
	position := 208
	for corner := range 8 {
		for channel := range 3 {
			if corner&(1<<(2-channel)) != 0 {
				binary.BigEndian.PutUint16(data[position:], 65535)
			}
			position += 2
		}
	}
	return data
}

func TestICCLUTAToBFullProcessingOrder(t *testing.T) {
	table := aToBMatrixCLUTFixture()
	profile := forwardProfile("RGB ", "XYZ ", "A2B1", 4, table)
	pixels := colorFixture(3, 16, 1, []float64{32768}, []float64{32768}, []float64{32768})
	image, err := Convert(nil, pixels, Options{ICCProfile: profile})
	if err != nil || image.NRGBAAt(0, 0) != (color.NRGBA{188, 188, 188, 255}) {
		t.Fatalf("A/CLUT/M/matrix/B processing order differs: %v, %v", image, err)
	}
	for _, change := range []func([]byte){
		func(data []byte) { data[188] = 0 },
		func(data []byte) { data[192] = 1 },
		func(data []byte) { data[204] = 3 },
		func(data []byte) { data[205] = 1 },
		func(data []byte) { binary.BigEndian.PutUint32(data[24:28], uint32(len(data)-4)) },
	} {
		invalid := append([]byte(nil), table...)
		change(invalid)
		if _, err := parseLUT(context.Background(), invalid, 3, "XYZ ", 4096); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid mAB CLUT header accepted: %v", err)
		}
	}
	if _, err := parseLUT(context.Background(), table, 3, "XYZ ", 20); !errors.Is(err, ErrLimit) {
		t.Fatalf("mAB CLUT storage budget ignored: %v", err)
	}
}

func TestICCLUTStorageTruncationAndCancellation(t *testing.T) {
	table := legacyLUTFixture("mft2", 4, 2, func([4]float64) [3]uint16 {
		return [3]uint16{32640, 32768, 32768}
	})
	if _, err := parseLUT(context.Background(), table, 4, "Lab ", 20); !errors.Is(err, ErrLimit) {
		t.Fatalf("CLUT memory budget ignored: %v", err)
	}
	if _, err := parseLUT(context.Background(), table[:len(table)-1], 4, "Lab ", 4096); !errors.Is(err, ErrInvalid) {
		t.Fatalf("truncated LUT accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseLUT(ctx, table, 4, "Lab ", 4096); !errors.Is(err, context.Canceled) {
		t.Fatalf("LUT parsing ignored cancellation: %v", err)
	}
	mft1 := legacyLUTFixture("mft1", 3, 2, func([4]float64) [3]uint16 { return [3]uint16{} })
	if _, err := parseLUT(context.Background(), mft1, 3, "XYZ ", 4096); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("undefined LUT8 PCS XYZ encoding silently assumed: %v", err)
	}
}

func TestICCLUTSampledCurvesAndSharedStorageBudget(t *testing.T) {
	table := make([]byte, 140)
	copy(table[:4], "mAB ")
	table[8], table[9] = 3, 3
	binary.BigEndian.PutUint32(table[12:16], 32)
	for offset := 32; offset < 92; offset += 20 {
		copy(table[offset:offset+4], "curv")
		binary.BigEndian.PutUint32(table[offset+8:offset+12], 3)
		binary.BigEndian.PutUint16(table[offset+12:offset+14], 65535)
		binary.BigEndian.PutUint16(table[offset+16:offset+18], 32768)
	}
	lut, err := parseLUT(context.Background(), table, 3, "Lab ", 64)
	if err != nil || lut.bCurves[0].evaluate(0.5) != 0 || lut.bCurves[0].evaluate(0) != 1 {
		t.Fatalf("sampled LUT curves incorrectly required a monotonic TRC: %v", err)
	}
	// The same curve sequence can be shared by B and M; owned copies must still fit the storage budget.
	binary.BigEndian.PutUint32(table[16:20], 92)
	binary.BigEndian.PutUint32(table[20:24], 32)
	if _, err := parseLUT(context.Background(), table, 3, "Lab ", 30); !errors.Is(err, ErrLimit) {
		t.Fatalf("shared curve offsets bypassed the owned storage budget: %v", err)
	}
	if _, err := parseCurveSamples(context.Background(), table[32:50], -1, false); !errors.Is(err, ErrLimit) {
		t.Fatalf("negative remaining curve budget was converted to an unsigned allowance: %v", err)
	}
}
