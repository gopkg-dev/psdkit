package psdcolor

import (
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"math"
	"testing"
)

type profileFixtureTag struct {
	key  string
	data []byte
}

func profileFixture(space, pcs string, version byte, tags ...profileFixtureTag) []byte {
	data := make([]byte, 132+len(tags)*12)
	data[8] = version
	copy(data[12:16], "mntr")
	copy(data[16:20], space)
	copy(data[20:24], pcs)
	copy(data[36:40], "acsp")
	for index, value := range iccWhite {
		binary.BigEndian.PutUint32(data[68+index*4:], uint32(int32(math.Round(value*65536))))
	}
	binary.BigEndian.PutUint32(data[128:132], uint32(len(tags)))
	for index, tag := range tags {
		position := 132 + index*12
		copy(data[position:position+4], tag.key)
		binary.BigEndian.PutUint32(data[position+4:position+8], uint32(len(data)))
		binary.BigEndian.PutUint32(data[position+8:position+12], uint32(len(tag.data)))
		data = append(data, tag.data...)
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}
	binary.BigEndian.PutUint32(data[:4], uint32(len(data)))
	return data
}

func profileXYZ(values [3]float64) []byte {
	data := make([]byte, 20)
	copy(data[:4], "XYZ ")
	for index, value := range values {
		binary.BigEndian.PutUint32(data[8+index*4:], uint32(int32(math.Round(value*65536))))
	}
	return data
}

func profileGamma(gamma float64) []byte {
	data := make([]byte, 14)
	copy(data[:4], "curv")
	binary.BigEndian.PutUint32(data[8:12], 1)
	binary.BigEndian.PutUint16(data[12:14], uint16(math.Round(gamma*256)))
	return data
}

func profileParametric(kind uint16, parameters ...float64) []byte {
	data := make([]byte, 12+4*len(parameters))
	copy(data[:4], "para")
	binary.BigEndian.PutUint16(data[8:10], kind)
	for index, value := range parameters {
		binary.BigEndian.PutUint32(data[12+index*4:], uint32(int32(math.Round(value*65536))))
	}
	return data
}

func profileRGB(version byte, curve []byte, columns [3][3]float64) []byte {
	tags := []profileFixtureTag{{"wtpt", profileXYZ(iccWhite)}}
	for index, prefix := range []string{"r", "g", "b"} {
		tags = append(tags, profileFixtureTag{prefix + "XYZ", profileXYZ(columns[index])})
		tags = append(tags, profileFixtureTag{prefix + "TRC", curve})
	}
	return profileFixture("RGB ", "XYZ ", version, tags...)
}

var profileSRGBColumns = [3][3]float64{
	{0.4360747, 0.2225045, 0.0139322},
	{0.3850649, 0.7168786, 0.0971045},
	{0.1430804, 0.0606169, 0.7141733},
}

func TestICCParametricSRGBV4AndOriginalBits(t *testing.T) {
	curve := profileParametric(4, 2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045, 0, 0)
	profile := profileRGB(4, curve, profileSRGBColumns)
	pixels := colorFixture(3, 8, 3, []float64{0, 128, 255}, []float64{32, 64, 255}, []float64{255, 192, 255})
	converted, err := Convert(nil, pixels, Options{ICCProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	for x := range 3 {
		for channel := range 3 {
			got := int(converted.Pix[x*4+channel])
			want := int(pixels.Channels[channel].Data[x])
			if math.Abs(float64(got-want)) > 1 {
				t.Fatalf("sRGB identity differs at %d/%d: got %d, want %d", x, channel, got, want)
			}
		}
	}
	linear := colorFixture(3, 32, 1, []float64{0.5}, []float64{0.5}, []float64{0.5})
	converted, err = Convert(nil, linear, Options{ICCProfile: profile})
	if err != nil || converted.NRGBAAt(0, 0) != (color.NRGBA{188, 188, 188, 255}) {
		t.Fatalf("linear float ICC double-applied a transfer curve: %v, %v", converted, err)
	}
}

func TestICCAdobeRGBMatrixV2Colorimetry(t *testing.T) {
	columns := [3][3]float64{
		{0.6097559, 0.3111242, 0.0194811},
		{0.2052401, 0.6256560, 0.0608902},
		{0.1492240, 0.0632197, 0.7448387},
	}
	profile := profileRGB(2, profileGamma(563.0/256), columns)
	pixels := colorFixture(3, 8, 1, []float64{128}, []float64{64}, []float64{192})
	converted, err := Convert(nil, pixels, Options{ICCProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	// Independent LittleCMS using the system AdobeRGB1998 ICC gives RGB (146,62,197).
	expected := [3]uint8{146, 62, 197}
	for channel, value := range expected {
		if math.Abs(float64(converted.Pix[channel])-float64(value)) > 1 {
			t.Fatalf("Adobe RGB primary conversion differs: got %v, want %v", converted.Pix, expected)
		}
	}
}

func TestICCGrayCurvesAndPCSModels(t *testing.T) {
	for _, pcs := range []string{"XYZ ", "Lab "} {
		profile := profileFixture("GRAY", pcs, 2,
			profileFixtureTag{"wtpt", profileXYZ(iccWhite)}, profileFixtureTag{"kTRC", profileGamma(1)})
		pixels := colorFixture(1, 16, 1, []float64{32768})
		converted, err := Convert(nil, pixels, Options{ICCProfile: profile})
		if err != nil {
			t.Fatal(err)
		}
		expected := uint8(188)
		if pcs == "Lab " {
			expected = 119
		}
		if converted.NRGBAAt(0, 0) != (color.NRGBA{expected, expected, expected, 255}) {
			t.Fatalf("gray PCS %s differs: %v", pcs, converted.Pix)
		}
	}
}

func TestICCSampledAndAllParametricCurveFunctions(t *testing.T) {
	table := make([]byte, 18)
	copy(table[:4], "curv")
	binary.BigEndian.PutUint32(table[8:12], 3)
	binary.BigEndian.PutUint16(table[12:14], 0)
	binary.BigEndian.PutUint16(table[14:16], 16384)
	binary.BigEndian.PutUint16(table[16:18], 65535)
	curve, err := parseCurve(context.Background(), table, 32)
	if err != nil || math.Abs(curve.evaluate(0.25)-0.125) > 0.00001 ||
		math.Abs(curve.evaluate(0.75)-0.625) > 0.00001 {
		t.Fatalf("sampled curve interpolation differs: %v", err)
	}
	for _, testCase := range []struct {
		kind       uint16
		parameters []float64
		value      float64
		expected   float64
	}{
		{0, []float64{2}, 0.5, 0.25},
		{1, []float64{2, 1, -0.25}, 0.5, 0.0625},
		{2, []float64{2, 1, -0.25, 0.125}, 0.5, 0.1875},
		{3, []float64{2, 1, 0, 0.25, 0.25}, 0.125, 0.03125},
		{4, []float64{2, 1, 0, 0.25, 0.25, 0.125, 0.0625}, 0.125, 0.09375},
	} {
		curve, err := parseCurve(context.Background(), profileParametric(testCase.kind, testCase.parameters...), 256)
		if err != nil || math.Abs(curve.evaluate(testCase.value)-testCase.expected) > 0.00001 {
			t.Fatalf("parametric type %d differs: %v", testCase.kind, err)
		}
	}
}

func TestICCUnsupportedLUTDeviceLinkAndMismatch(t *testing.T) {
	base := profileRGB(4, profileGamma(1), profileSRGBColumns)
	pixels := colorFixture(3, 8, 1, []float64{128}, []float64{64}, []float64{192})
	deviceLink := append([]byte(nil), base...)
	copy(deviceLink[12:16], "link")
	if _, err := Convert(nil, pixels, Options{ICCProfile: deviceLink}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("device-link silently ignored: %v", err)
	}
	lut := profileFixture("RGB ", "XYZ ", 4,
		profileFixtureTag{"wtpt", profileXYZ(iccWhite)}, profileFixtureTag{"A2B0", make([]byte, 12)})
	if _, err := Convert(nil, pixels, Options{ICCProfile: lut}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ICC LUT silently ignored: %v", err)
	}
	gray := profileFixture("GRAY", "XYZ ", 2,
		profileFixtureTag{"wtpt", profileXYZ(iccWhite)}, profileFixtureTag{"kTRC", profileGamma(1)})
	if _, err := Convert(nil, pixels, Options{ICCProfile: gray}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("mismatched profile accepted: %v", err)
	}
	cmyk := colorFixture(4, 8, 1, []float64{255}, []float64{255}, []float64{255}, []float64{255})
	if _, err := Convert(nil, cmyk, Options{ICCProfile: lut, AllowUnmanagedCMYK: true}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unmanaged CMYK fallback discarded supplied ICC: %v", err)
	}
}

func TestICCMalformedAndStorageLimits(t *testing.T) {
	base := profileRGB(4, profileGamma(1), profileSRGBColumns)
	for _, change := range []func([]byte){
		func(data []byte) { binary.BigEndian.PutUint32(data[:4], 1) },
		func(data []byte) { copy(data[36:40], "oops") },
		func(data []byte) { binary.BigEndian.PutUint32(data[128:132], math.MaxUint32) },
		func(data []byte) { binary.BigEndian.PutUint32(data[136:140], 1) },
		func(data []byte) { copy(data[144:148], data[132:136]) },
	} {
		data := append([]byte(nil), base...)
		change(data)
		if _, err := parseICC(context.Background(), data, int64(len(data))); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed ICC accepted: %v", err)
		}
	}
	if _, err := parseICC(context.Background(), base, int64(len(base))-1); !errors.Is(err, ErrLimit) {
		t.Fatalf("profile budget ignored: %v", err)
	}
	table := make([]byte, 18)
	copy(table[:4], "curv")
	binary.BigEndian.PutUint32(table[8:12], 3)
	if _, err := parseCurve(context.Background(), table, 5); !errors.Is(err, ErrLimit) {
		t.Fatalf("curve storage budget ignored: %v", err)
	}
}
