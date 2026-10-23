package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"
)

func hueFixture() []byte {
	data := make([]byte, 100)
	binary.BigEndian.PutUint16(data, 2)
	data[3] = 0x71
	putHueValues(data[4:], HueSaturationValues{26, 25, 0})
	putHueValues(data[10:], HueSaturationValues{-35, 40, -20})
	for index := range 6 {
		bounds := [4]int16{int16((315 + index*60) % 360), int16((345 + index*60) % 360),
			int16((15 + index*60) % 360), int16((45 + index*60) % 360)}
		for bound, value := range bounds {
			binary.BigEndian.PutUint16(data[16+index*14+bound*2:], uint16(value))
		}
		putHueValues(data[24+index*14:], HueSaturationValues{int16(index * 5), -20, 10})
	}
	return append(data, 0x91, 0x72, 0x00, 0x44, 0x32)
}

func TestHueSaturationMetadataOwnedSignedOrderedAndExactRoundTrip(t *testing.T) {
	for _, key := range []string{"hue2", "hue "} {
		data := hueFixture()
		original := bytes.Clone(data)
		adjustment, err := Decode(nil, key, data, DecodeOptions{SourceOffset: 51})
		if err != nil || adjustment.Kind != HueSaturation || adjustment.HueSaturation == nil ||
			adjustment.Version != 2 || adjustment.Span != (Span{Offset: 51, Length: int64(len(data))}) {
			t.Fatalf("typed hue/saturation header or source span differs: %v", err)
		}
		parameters := adjustment.HueSaturation
		if parameters.Master != (HueSaturationValues{-35, 40, -20}) || parameters.Padding != 0x71 ||
			parameters.Ranges[0].Bounds != ([4]int16{315, 345, 15, 45}) ||
			parameters.Ranges[5].Values != (HueSaturationValues{25, -20, 10}) {
			t.Fatal("signed master, ignored padding or ordered range values differ")
		}
		clear(data)
		if !bytes.Equal(adjustment.Raw, original) || !bytes.Equal(parameters.Unknown, original[100:]) {
			t.Fatal("hue/saturation borrowed caller source storage")
		}
		encoded, err := Encode(nil, *adjustment)
		if err != nil || !bytes.Equal(encoded, original) {
			t.Fatalf("untouched hue/saturation source bytes changed: %v", err)
		}
		parameters.Master.Hue = 55
		encoded, err = Encode(nil, *adjustment)
		if err != nil || encoded[3] != 0x71 || !bytes.Equal(encoded[100:], original[100:]) {
			t.Fatalf("edited hue/saturation did not retain ignored source bytes: %v", err)
		}
		parameters.Unknown[0]++
		if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ignored source tail was editable: %v", err)
		}
	}
}

func TestHueSaturationNativeMasterColorizeAndAlphaSamples(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 5, 1))
	copy(source.Pix, []byte{166, 166, 89, 255, 66, 66, 36, 128, 102, 102, 0, 1,
		255, 255, 153, 0, 90, 90, 90, 255})
	cases := []struct {
		parameters HueSaturationParams
		colors     [5][3]byte
	}{
		{HueSaturationParams{Master: HueSaturationValues{Saturation: 40}},
			[5][3]byte{{191, 191, 64}, {76, 76, 26}, {102, 102, 0}, {255, 255, 153}, {90, 90, 90}}},
		{HueSaturationParams{Colorize: true, Colorization: HueSaturationValues{-150, 35, 10}},
			[5][3]byte{{100, 140, 180}, {47, 71, 96}, {47, 71, 96}, {193, 209, 225}, {69, 107, 144}}},
	}
	for _, fixture := range cases {
		adjustment := Adjustment{Kind: HueSaturation, HueSaturation: &fixture.parameters}
		result, err := Apply(nil, source, adjustment, Options{ApproximateHueSaturation: true})
		if err != nil {
			t.Fatal(err)
		}
		for pixel, expected := range fixture.colors {
			for component, value := range expected {
				actual := int(result.Pix[pixel*4+component])
				if actual < int(value)-1 || actual > int(value)+1 {
					t.Fatalf("native control pixel %d component %d differs: %d != %d", pixel, component, actual, value)
				}
			}
			if result.Pix[pixel*4+3] != source.Pix[pixel*4+3] {
				t.Fatal("hue/saturation changed exact source alpha")
			}
		}
	}
}

func TestHueSaturationCircularRangeAndOwnedTransform(t *testing.T) {
	if hueModulo(-1e-15) != 0 || hueColor(-1e-15, 1, .5) != ([3]float64{1, 0, 0}) {
		t.Fatal("negative floating point residue created an invalid sixth hue sextant")
	}
	parameters := &HueSaturationParams{}
	parameters.Ranges[0] = HueSaturationRange{Bounds: [4]int16{315, 345, 15, 45},
		Values: HueSaturationValues{Hue: 60}}
	transform, err := Compile(Adjustment{Kind: HueSaturation, HueSaturation: parameters},
		CompileOptions{ApproximateHueSaturation: true})
	if err != nil {
		t.Fatal(err)
	}
	parameters.Ranges[0].Values.Hue = -60
	for _, sample := range []struct{ input, expected [3]float64 }{
		{[3]float64{1, 0, 0}, [3]float64{1, 1, 0}},
		{[3]float64{0, 1, 0}, [3]float64{0, 1, 0}},
		{[3]float64{.5, .5, .5}, [3]float64{.5, .5, .5}},
	} {
		if actual := transform.RGB(sample.input); actual != sample.expected {
			t.Fatalf("circular color range or immutable compiled parameters differ: %v != %v", actual, sample.expected)
		}
	}
	for _, fixture := range []struct{ hue, expected float64 }{
		{315, 0}, {330, .5}, {345, 1}, {0, 1}, {15, 1}, {30, .5}, {45, 0}, {180, 0},
	} {
		if actual := hueRangeWeight(fixture.hue, parameters.Ranges[0].Bounds); actual != fixture.expected {
			t.Fatalf("hue feather %v differs: %v", fixture.hue, actual)
		}
	}
}

func TestHueSaturationMalformedBudgetsCancellationAndNewVersion(t *testing.T) {
	data := hueFixture()
	for length := range 100 {
		if _, err := Decode(nil, "hue2", data[:length]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("truncated hue block length %d accepted: %v", length, err)
		}
	}
	unknown := bytes.Clone(data)
	unknown[2] = 2
	if retained, err := Decode(nil, "hue2", unknown); !errors.Is(err, ErrUnsupported) || retained == nil ||
		!bytes.Equal(retained.Raw, unknown) {
		t.Fatalf("unknown flag lost source: %v", err)
	}
	if _, err := Decode(nil, "hue2", data, DecodeOptions{MaxBytes: 120}); !errors.Is(err, ErrLimit) {
		t.Fatalf("hue decode metadata limit ignored: %v", err)
	}
	parameters := &HueSaturationParams{Master: HueSaturationValues{Hue: 181}}
	adjustment := Adjustment{Kind: HueSaturation, HueSaturation: parameters}
	if _, err := Compile(adjustment); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid hue accepted: %v", err)
	}
	parameters.Master.Hue = 0
	parameters.Ranges[0].Bounds = [4]int16{0, 90, 45, 100}
	if _, err := Compile(adjustment); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unordered band accepted: %v", err)
	}
	parameters.Ranges[0].Bounds = [4]int16{}
	parameters.Unknown = make([]byte, 32<<10)
	if _, err := Encode(nil, adjustment, EncodeOptions{MaxBytes: 17 << 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("new unknown metadata bypassed encode budget: %v", err)
	}
	parameters.Unknown = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Encode(ctx, adjustment); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled encode accepted: %v", err)
	}
	_, err := Apply(nil, image.NewNRGBA(image.Rect(0, 0, 1, 1)), adjustment, Options{MaxWorkingBytes: 1024})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("hue transform budget ignored: %v", err)
	}
	adjustment.Version = 1
	if _, err := Encode(nil, adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unverified hue version accepted: %v", err)
	}
}

func TestHueSaturationNativePositiveFeatherAndOverlappingNegativeGain(t *testing.T) {
	parameters := &HueSaturationParams{}
	parameters.Ranges[0] = HueSaturationRange{Bounds: [4]int16{315, 345, 15, 45},
		Values: HueSaturationValues{Saturation: 60}}
	source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(source.Pix, []byte{166, 122, 89, 128})
	pixels, err := Apply(nil, source, Adjustment{Kind: HueSaturation, HueSaturation: parameters},
		Options{ApproximateHueSaturation: true})
	if err != nil {
		t.Fatal(err)
	}
	assertHueNativeColor(t, pixels.Pix, [4]byte{202, 116, 52, 128})
	parameters.Ranges[0] = HueSaturationRange{Bounds: [4]int16{300, 330, 40, 70},
		Values: HueSaturationValues{-25, -40, 10}}
	parameters.Ranges[1] = HueSaturationRange{Bounds: [4]int16{0, 20, 90, 120},
		Values: HueSaturationValues{30, -35, -15}}
	copy(source.Pix, []byte{255, 85, 0, 255})
	pixels, err = Apply(nil, source, Adjustment{Kind: HueSaturation, HueSaturation: parameters},
		Options{ApproximateHueSaturation: true})
	if err != nil {
		t.Fatal(err)
	}
	assertHueNativeColor(t, pixels.Pix, [4]byte{151, 115, 90, 255})
}

func TestHueSaturationNativeIndependentSaturationRangeBoundaries(t *testing.T) {
	// These witnesses come from independently rendered RGB8 controls with varied source chroma.
	// Each exposed a large error in the earlier parity-based color-range selector.
	fixtures := []struct {
		channel int
		input   [3]byte
		native  [3]byte
	}{
		{0, [3]byte{192, 160, 64}, [3]byte{255, 191, 1}},
		{1, [3]byte{165, 109, 90}, [3]byte{202, 91, 53}},
		{2, [3]byte{159, 191, 64}, [3]byte{159, 191, 64}},
		{3, [3]byte{112, 143, 120}, [3]byte{96, 158, 112}},
		{4, [3]byte{112, 135, 143}, [3]byte{112, 135, 143}},
		{5, [3]byte{165, 90, 108}, [3]byte{165, 90, 108}},
	}
	for _, fixture := range fixtures {
		parameters := &HueSaturationParams{}
		for index := range parameters.Ranges {
			parameters.Ranges[index].Bounds = [4]int16{int16((315 + index*60) % 360),
				int16((345 + index*60) % 360), int16((15 + index*60) % 360), int16((45 + index*60) % 360)}
		}
		parameters.Ranges[fixture.channel].Values.Saturation = 100
		source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		copy(source.Pix, []byte{fixture.input[0], fixture.input[1], fixture.input[2], 128})
		pixels, err := Apply(nil, source, Adjustment{Kind: HueSaturation, HueSaturation: parameters},
			Options{ApproximateHueSaturation: true})
		if err != nil {
			t.Fatal(err)
		}
		assertHueNativeColor(t, pixels.Pix, [4]byte{fixture.native[0], fixture.native[1], fixture.native[2], 128})
	}
}

func TestHueSaturationNativePlainMasterLightnessByteControls(t *testing.T) {
	// The gray witness comes from the full native control sweep; RGB witnesses are from a fresh alpha128 holdout.
	fixtures := []struct {
		lightness int16
		input     [4]byte
		native    [4]byte
	}{
		{-98, [4]byte{249, 249, 249, 128}, [4]byte{6, 6, 6, 128}},
		{-97, [4]byte{255, 128, 0, 128}, [4]byte{8, 4, 0, 128}},
		{-53, [4]byte{89, 166, 157, 128}, [4]byte{42, 78, 74, 128}},
		{51, [4]byte{255, 128, 0, 128}, [4]byte{255, 193, 130, 128}},
	}
	for _, fixture := range fixtures {
		parameters := &HueSaturationParams{Master: HueSaturationValues{Lightness: fixture.lightness}}
		adjustment := Adjustment{Kind: HueSaturation, HueSaturation: parameters}
		source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		copy(source.Pix, fixture.input[:])
		pixels, err := Apply(nil, source, adjustment)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(pixels.Pix, fixture.native[:]) {
			t.Fatalf("native plain lightness %d differs: %v != %v", fixture.lightness, pixels.Pix, fixture.native)
		}
		parameters.Master.Hue = 1
		if huePlainMasterLightness(parameters) {
			t.Fatal("mixed master Hue controls inherited the plain Lightness branch")
		}
		parameters.Master.Hue = 0
		parameters.Ranges[0].Values.Saturation = -1
		if huePlainMasterLightness(parameters) {
			t.Fatal("local controls inherited the plain Lightness branch")
		}
	}
}

func TestHueSaturationNativePlainMasterHueByteControls(t *testing.T) {
	// These RGB witnesses come from the independently seeded holdout with opaque and alpha128 copies.
	fixtures := []struct {
		hue    int16
		input  [3]byte
		native [3]byte
	}{
		{2, [3]byte{255, 119, 0}, [3]byte{255, 127, 0}},
		{-2, [3]byte{255, 136, 0}, [3]byte{255, 127, 0}},
		{14, [3]byte{255, 68, 0}, [3]byte{255, 127, 0}},
		{-14, [3]byte{255, 187, 0}, [3]byte{255, 127, 0}},
		{57, [3]byte{255, 140, 0}, [3]byte{127, 255, 0}},
		{-57, [3]byte{140, 255, 0}, [3]byte{255, 127, 0}},
		{118, [3]byte{255, 136, 0}, [3]byte{0, 255, 127}},
		{-118, [3]byte{255, 119, 0}, [3]byte{127, 0, 255}},
	}
	for _, fixture := range fixtures {
		parameters := &HueSaturationParams{Master: HueSaturationValues{Hue: fixture.hue}}
		adjustment := Adjustment{Kind: HueSaturation, HueSaturation: parameters}
		for _, alpha := range []byte{128, 255} {
			source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			copy(source.Pix, []byte{fixture.input[0], fixture.input[1], fixture.input[2], alpha})
			if _, err := Apply(nil, source, adjustment); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("plain Hue changed the default non-neutral rendering policy: %v", err)
			}
			pixels, err := Apply(nil, source, adjustment, Options{ApproximateHueSaturation: true})
			if err != nil {
				t.Fatal(err)
			}
			assertHueNativeColor(t, pixels.Pix,
				[4]byte{fixture.native[0], fixture.native[1], fixture.native[2], alpha})
		}
	}
	// This native calibration witness rejects away-from-zero rounding of the negative control half tie.
	parameters := &HueSaturationParams{Master: HueSaturationValues{Hue: -90}}
	source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(source.Pix, []byte{224, 121, 19, 255})
	pixels, err := Apply(nil, source, Adjustment{Kind: HueSaturation, HueSaturation: parameters},
		Options{ApproximateHueSaturation: true})
	if err != nil || !bytes.Equal(pixels.Pix, []byte{224, 19, 223, 255}) {
		t.Fatalf("negative Hue half tie differs from the native byte witness: %v, %v", pixels, err)
	}
}

func TestHueSaturationPlainMasterHueScope(t *testing.T) {
	parameters := &HueSaturationParams{Master: HueSaturationValues{Hue: 37}}
	if !huePlainMasterHue(parameters) {
		t.Fatal("plain master Hue did not select the RGB8 branch")
	}
	for _, values := range []HueSaturationValues{{37, 1, 0}, {37, -1, 0}, {37, 80, 0}, {37, 0, 1}, {37, 0, -1}} {
		parameters.Master = values
		if huePlainMasterHue(parameters) {
			t.Fatal("mixed master controls inherited the plain Hue branch")
		}
	}
	parameters.Master = HueSaturationValues{Hue: 37}
	for index := range parameters.Ranges {
		for _, values := range []HueSaturationValues{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0},
			{0, -1, 0}, {0, 0, 1}, {0, 0, -1}} {
			parameters.Ranges[index].Values = values
			if huePlainMasterHue(parameters) {
				t.Fatal("local controls inherited the plain Hue branch")
			}
		}
		parameters.Ranges[index].Values = HueSaturationValues{}
	}
	parameters.Colorize = true
	if huePlainMasterHue(parameters) {
		t.Fatal("Colorize inherited the plain Hue branch")
	}
}

func assertHueNativeColor(t *testing.T, pixels []byte, expected [4]byte) {
	t.Helper()
	for component, value := range expected {
		actual := int(pixels[component])
		if component == 3 && actual != int(value) || actual < int(value)-1 || actual > int(value)+1 {
			t.Fatalf("native Hue/Saturation component %d differs: %d != %d", component, actual, value)
		}
	}
}

func TestHueSaturationRequiresExplicitApproximationAndMetadataRemainsWritable(t *testing.T) {
	parameters := &HueSaturationParams{}
	adjustment := Adjustment{Kind: HueSaturation, HueSaturation: parameters}
	source := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(source.Pix, []byte{128, 127, 30, 128, 100, 80, 20, 0})
	neutral, err := Apply(nil, source, adjustment)
	if err != nil || !bytes.Equal(neutral.Pix, source.Pix) {
		t.Fatalf("neutral hue no longer renders exact original RGB and alpha: %v", err)
	}
	parameters.Master.Hue = 60
	if _, err := Compile(adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("non-neutral hue compiled without an explicit approximation policy: %v", err)
	}
	if pixels, err := Apply(nil, source, adjustment); pixels != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("default hue rendering silently used an unverified model: %v", err)
	}
	encoded, err := Encode(nil, adjustment)
	if err != nil {
		t.Fatalf("pixel policy incorrectly prohibited controlled metadata encoding: %v", err)
	}
	decoded, err := Decode(nil, "hue2", encoded)
	if err != nil || decoded.HueSaturation.Master.Hue != 60 {
		t.Fatalf("non-neutral exact metadata no longer decodes: %v", err)
	}
	if _, err := Compile(adjustment, CompileOptions{}, CompileOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multiple compile policy values accepted: %v", err)
	}
	parameters.Master.Hue, parameters.Colorize = 0, true
	if _, err := Compile(adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Colorize silently bypassed the explicit approximation policy: %v", err)
	}
}
