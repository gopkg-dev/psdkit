package psdkit_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdcolor"
)

type displaySourceFixture struct {
	version, mode, depth uint16
	width, height        uint32
	planes               [][]byte
	palette, profile     []byte
	transparent          *uint16
	marker               string
	negativeLayers       bool
}

func displayWrite(t *testing.T, target *bytes.Buffer, value any) {
	t.Helper()
	if err := binary.Write(target, binary.BigEndian, value); err != nil {
		t.Fatal(err)
	}
}

func displayResource(t *testing.T, target *bytes.Buffer, identifier uint16, data []byte) {
	t.Helper()
	target.WriteString("8BIM")
	displayWrite(t, target, identifier)
	target.Write([]byte{0, 0})
	displayWrite(t, target, uint32(len(data)))
	target.Write(data)
	if len(data)%2 != 0 {
		target.WriteByte(0)
	}
}

func displayFixtureBytes(t *testing.T, fixture displaySourceFixture) []byte {
	t.Helper()
	if fixture.version == 0 {
		fixture.version = 1
	}
	if fixture.mode == 0 && fixture.depth == 0 {
		fixture.mode = 3
	}
	if fixture.depth == 0 {
		fixture.depth = 8
	}
	if fixture.width == 0 {
		fixture.width = 1
	}
	if fixture.height == 0 {
		fixture.height = 1
	}
	var output, resources, layerMask bytes.Buffer
	output.WriteString("8BPS")
	for _, value := range []any{fixture.version, [6]byte{}, uint16(len(fixture.planes)), fixture.height,
		fixture.width, fixture.depth, fixture.mode, uint32(len(fixture.palette))} {
		displayWrite(t, &output, value)
	}
	output.Write(fixture.palette)
	if fixture.profile != nil {
		displayResource(t, &resources, 1039, fixture.profile)
	}
	if fixture.transparent != nil {
		var data bytes.Buffer
		displayWrite(t, &data, *fixture.transparent)
		displayResource(t, &resources, 1047, data.Bytes())
	}
	displayWrite(t, &output, uint32(resources.Len()))
	output.Write(resources.Bytes())
	if fixture.negativeLayers {
		var records bytes.Buffer
		displayWrite(t, &records, int16(-1))
		for _, value := range []any{[4]int32{}, uint16(0)} {
			displayWrite(t, &records, value)
		}
		records.WriteString("8BIMnorm")
		records.Write([]byte{255, 0, 0, 0})
		displayWrite(t, &records, uint32(12))
		displayWrite(t, &records, uint32(0))
		displayWrite(t, &records, uint32(0))
		records.Write([]byte{1, 'L', 0, 0})
		if fixture.version == 2 {
			displayWrite(t, &layerMask, uint64(records.Len()))
		} else {
			displayWrite(t, &layerMask, uint32(records.Len()))
		}
		layerMask.Write(records.Bytes())
	} else if fixture.version == 2 {
		displayWrite(t, &layerMask, uint64(0))
	} else {
		displayWrite(t, &layerMask, uint32(0))
	}
	displayWrite(t, &layerMask, uint32(0))
	if fixture.marker != "" {
		layerMask.WriteString("8BIM" + fixture.marker)
		if fixture.version == 2 && (fixture.marker == "Mtrn" || fixture.marker == "Mt16" || fixture.marker == "Mt32") {
			displayWrite(t, &layerMask, uint64(0))
		} else {
			displayWrite(t, &layerMask, uint32(0))
		}
	}
	if fixture.version == 2 {
		displayWrite(t, &output, uint64(layerMask.Len()))
	} else {
		displayWrite(t, &output, uint32(layerMask.Len()))
	}
	output.Write(layerMask.Bytes())
	displayWrite(t, &output, uint16(0))
	for _, plane := range fixture.planes {
		output.Write(plane)
	}
	return output.Bytes()
}

func displaySamplePlane(depth uint16, values ...float64) []byte {
	data := make([]byte, len(values)*int(depth/8))
	for index, value := range values {
		switch depth {
		case 8:
			data[index] = byte(math.Round(value * 255))
		case 16:
			binary.BigEndian.PutUint16(data[index*2:], uint16(math.Round(value*65535)))
		case 32:
			binary.BigEndian.PutUint32(data[index*4:], math.Float32bits(float32(value)))
		}
	}
	return data
}

func displayUnsupportedICC() []byte {
	data := make([]byte, 188)
	binary.BigEndian.PutUint32(data, uint32(len(data)))
	data[8] = 4
	copy(data[12:16], "mntr")
	copy(data[16:20], "RGB ")
	copy(data[20:24], "XYZ ")
	copy(data[36:40], "acsp")
	for index, value := range []float64{0.9642, 1, 0.8249} {
		binary.BigEndian.PutUint32(data[68+index*4:], uint32(int32(math.Round(value*65536))))
	}
	binary.BigEndian.PutUint32(data[128:132], 2)
	copy(data[132:136], "wtpt")
	binary.BigEndian.PutUint32(data[136:140], 156)
	binary.BigEndian.PutUint32(data[140:144], 20)
	copy(data[144:148], "A2B0")
	binary.BigEndian.PutUint32(data[148:152], 176)
	binary.BigEndian.PutUint32(data[152:156], 12)
	copy(data[156:160], "XYZ ")
	for index, value := range []float64{0.9642, 1, 0.8249} {
		binary.BigEndian.PutUint32(data[164+index*4:], uint32(int32(math.Round(value*65536))))
	}
	copy(data[176:180], "mpet")
	return data
}

type displayCountingReader struct {
	reader           *bytes.Reader
	pixelOffset      int64
	calls, pixels    int
	forbidPixelReads bool
}

func (reader *displayCountingReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.calls++
	if reader.pixelOffset > 0 && offset+int64(len(data)) > reader.pixelOffset {
		reader.pixels++
		if reader.forbidPixelReads {
			return 0, io.ErrUnexpectedEOF
		}
	}
	return reader.reader.ReadAt(data, offset)
}

func displayCountedSource(
	t *testing.T, fixture displaySourceFixture, limits psdkit.SourceLimits,
) (*psdkit.SourceDocument, *displayCountingReader) {
	t.Helper()
	data := displayFixtureBytes(t, fixture)
	reader := &displayCountingReader{reader: bytes.NewReader(data)}
	source, err := psdkit.ReadDocument(reader, int64(len(data)), psdkit.LoadOptions{Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	reader.pixelOffset = source.Composite.Data.Offset
	reader.calls, reader.pixels = 0, 0
	return source, reader
}

func TestDisplaySourceRGBVersionsAndOwnership(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		fixture := displaySourceFixture{version: version, mode: 3, width: 2,
			planes: [][]byte{{20, 30}, {40, 50}, {60, 70}}}
		data := displayFixtureBytes(t, fixture)
		source, err := psdkit.ParseDocument(data)
		if err != nil {
			t.Fatal(err)
		}
		original := bytes.Clone(data)
		pixels, err := psdkit.DecodeDisplayComposite(nil, source)
		if err != nil || pixels.NRGBAAt(0, 0) != (color.NRGBA{20, 40, 60, 255}) {
			t.Fatalf("source display changed version %d pixels: %v", version, err)
		}
		pixels.Pix[0] = 255
		if !bytes.Equal(data, original) {
			t.Fatal("owned display aliases original file bytes")
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if pixels.NRGBAAt(0, 0).R != 255 {
			t.Fatal("closing the source invalidated the owned display image")
		}
	}
}

func TestDisplaySourceTransparencyMarkersAndOverrides(t *testing.T) {
	for _, testCase := range []struct {
		depth    uint16
		marker   string
		negative bool
		alpha    uint8
	}{
		{8, "", true, 128}, {8, "Mtrn", false, 128}, {16, "Mt16", false, 128},
		{32, "Mt32", false, 128}, {16, "Mt32", false, 255}, {8, "Mt16", false, 255},
	} {
		fixture := displaySourceFixture{mode: 3, depth: testCase.depth, marker: testCase.marker,
			negativeLayers: testCase.negative, planes: [][]byte{
				displaySamplePlane(testCase.depth, 159.0/255), displaySamplePlane(testCase.depth, 191.0/255),
				displaySamplePlane(testCase.depth, 223.0/255), displaySamplePlane(testCase.depth, 128.0/255),
			}}
		source, reader := displayCountedSource(t, fixture, psdkit.SourceLimits{})
		settings := psdkit.DisplayOptions{Color: psdkit.ColorOptions{FloatEncoding: psdcolor.FloatEncodedRGB}}
		pixels, err := psdkit.DecodeDisplayComposite(nil, source, settings)
		if err != nil || pixels.NRGBAAt(0, 0).A != testCase.alpha {
			t.Fatalf("depth %d marker %s transparency mismatch: %v", testCase.depth, testCase.marker, err)
		}
		if testCase.alpha == 128 && pixels.NRGBAAt(0, 0) != (color.NRGBA{64, 128, 191, 128}) {
			t.Fatalf("saved white matte was not removed: %v", pixels.Pix)
		}
		if reader.pixels == 0 {
			t.Fatal("successful display never read source samples")
		}
		settings.IgnoreTransparency = true
		pixels, err = psdkit.DecodeDisplayComposite(nil, source, settings)
		if err != nil || pixels.NRGBAAt(0, 0).A != 255 {
			t.Fatalf("explicit transparency override was ignored: %v", err)
		}
	}
}

func TestDisplaySourcePaletteAndTransparentIndex(t *testing.T) {
	palette := make([]byte, 768)
	palette[0], palette[256], palette[512] = 10, 20, 30
	palette[1], palette[257], palette[513] = 200, 150, 100
	transparent := uint16(1)
	fixture := displaySourceFixture{mode: 2, width: 2, palette: palette, transparent: &transparent,
		planes: [][]byte{{0, 1}}}
	source, _ := displayCountedSource(t, fixture, psdkit.SourceLimits{})
	pixels, err := psdkit.DecodeDisplayComposite(nil, source)
	if err != nil || pixels.NRGBAAt(0, 0) != (color.NRGBA{10, 20, 30, 255}) ||
		pixels.NRGBAAt(1, 0) != (color.NRGBA{}) {
		t.Fatalf("palette or resource 1047 was not propagated: %v", err)
	}
	pixels, err = psdkit.DecodeDisplayComposite(nil, source, psdkit.DisplayOptions{IgnoreTransparency: true})
	if err != nil || pixels.NRGBAAt(1, 0) != (color.NRGBA{200, 150, 100, 255}) {
		t.Fatalf("indexed transparency override ignored: %v", err)
	}
}

func TestDisplaySourceICCExplicitOverrideAndUnsupportedProfile(t *testing.T) {
	fixture := displaySourceFixture{mode: 3, profile: displayUnsupportedICC(), planes: [][]byte{{20}, {40}, {60}}}
	source, reader := displayCountedSource(t, fixture, psdkit.SourceLimits{})
	reader.forbidPixelReads = true
	if _, err := psdkit.DecodeDisplayComposite(nil, source); !errors.Is(err, psdkit.ErrColorUnsupported) {
		t.Fatalf("source ICC was ignored or samples read before rejecting it: %v", err)
	}
	if reader.pixels != 0 {
		t.Fatal("unsupported ICC caused a pixel decode before rejection")
	}
	reader.forbidPixelReads = false
	for _, settings := range []psdkit.DisplayOptions{
		{IgnoreSourceProfile: true}, {Color: psdkit.ColorOptions{ICCProfile: []byte{}}},
	} {
		pixels, err := psdkit.DecodeDisplayComposite(nil, source, settings)
		if err != nil || pixels.NRGBAAt(0, 0) != (color.NRGBA{20, 40, 60, 255}) {
			t.Fatalf("explicit profile override was not respected: %v", err)
		}
	}
}

func TestDisplaySourcePreflightBudgetsDoNotReadPixels(t *testing.T) {
	fixture := displaySourceFixture{mode: 3, depth: 16, width: 2, planes: [][]byte{
		displaySamplePlane(16, 0.2, 0.3), displaySamplePlane(16, 0.4, 0.5), displaySamplePlane(16, 0.6, 0.7),
	}}
	for _, testCase := range []struct {
		settings psdkit.DisplayOptions
		limits   psdkit.SourceLimits
	}{
		{settings: psdkit.DisplayOptions{Color: psdkit.ColorOptions{MaxPixels: 1}}},
		{settings: psdkit.DisplayOptions{Color: psdkit.ColorOptions{MaxOutputBytes: 7}}},
		{limits: psdkit.SourceLimits{MaxDecodeBytes: 17}},
	} {
		source, reader := displayCountedSource(t, fixture, testCase.limits)
		reader.forbidPixelReads = true
		if _, err := psdkit.DecodeDisplayComposite(nil, source, testCase.settings); !errors.Is(err, psdkit.ErrColorLimit) {
			t.Fatalf("source/output combined budget was not enforced before decode: %v", err)
		}
		if reader.pixels != 0 {
			t.Fatal("budget rejection accessed pixel payload")
		}
	}
	fixture.profile = displayUnsupportedICC()
	source, reader := displayCountedSource(t, fixture, psdkit.SourceLimits{})
	reader.forbidPixelReads = true
	if _, err := psdkit.DecodeDisplayComposite(nil, source, psdkit.DisplayOptions{
		Color: psdkit.ColorOptions{MaxProfileBytes: 1},
	}); !errors.Is(err, psdkit.ErrColorLimit) {
		t.Fatalf("profile budget ignored: %v", err)
	}
	if reader.calls != 0 {
		t.Fatal("ICC budget rejection accessed source bytes")
	}
}

func TestDisplaySourceCancellationCloseAndInvalidOptionsPrecedeReads(t *testing.T) {
	fixture := displaySourceFixture{mode: 3, width: 2, profile: displayUnsupportedICC(),
		planes: [][]byte{{20, 30}, {40, 50}, {60, 70}}}
	source, reader := displayCountedSource(t, fixture, psdkit.SourceLimits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdkit.DecodeDisplayComposite(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled display source request was not rejected: %v", err)
	}
	if reader.calls != 0 {
		t.Fatal("canceled request read ICC or pixels")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := psdkit.DecodeDisplayComposite(nil, source,
		psdkit.DisplayOptions{Color: psdkit.ColorOptions{MaxPixels: 1}}); !errors.Is(err, psdkit.ErrClosed) {
		t.Fatalf("closed source was processed before budget rejection: %v", err)
	}
	source, reader = displayCountedSource(t, displaySourceFixture{mode: 3, planes: [][]byte{{1}, {2}, {3}}},
		psdkit.SourceLimits{})
	reader.forbidPixelReads = true
	for _, settings := range []psdkit.ColorOptions{{MaxPixels: -1}, {NonFinite: 100}} {
		if _, err := psdkit.DecodeDisplayComposite(nil, source,
			psdkit.DisplayOptions{Color: settings}); !errors.Is(err, psdkit.ErrColorInvalid) {
			t.Fatalf("invalid conversion option was not rejected before decode: %v", err)
		}
	}
	if reader.calls != 0 {
		t.Fatal("invalid conversion options accessed source bytes")
	}
}
