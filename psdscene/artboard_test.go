package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type artboardTestEntry struct {
	key, kind string
	data      []byte
}

func artboardTestU32(value uint32) []byte { return binary.BigEndian.AppendUint32(nil, value) }
func artboardTestDouble(value float64) []byte {
	return binary.BigEndian.AppendUint64(nil, math.Float64bits(value))
}

func artboardTestDescriptor(class string, entries ...artboardTestEntry) []byte {
	data := artboardTestU32(0)
	data = append(data, artboardTestU32(uint32(len(class)))...)
	data = append(data, class...)
	data = append(data, artboardTestU32(uint32(len(entries)))...)
	for _, entry := range entries {
		length := len(entry.key)
		if length == 4 {
			length = 0
		}
		data = append(data, artboardTestU32(uint32(length))...)
		data = append(data, entry.key...)
		data = append(data, entry.kind...)
		data = append(data, entry.data...)
	}
	return data
}

func artboardTestBlock(key string, data []byte) []byte {
	result := append([]byte("8BIM"+key), artboardTestU32(uint32(len(data)))...)
	result = append(result, data...)
	if len(data)%2 != 0 {
		result = append(result, 0)
	}
	return result
}

type artboardTestConfiguration struct {
	background   uint32
	origin       float64
	pass         bool
	duplicate    bool
	unknown      bool
	groupOpacity byte
}

func artboardTestPSD(settings artboardTestConfiguration) []byte {
	rect := artboardTestDescriptor("classFloatRect",
		artboardTestEntry{"Left", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Top ", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Rght", "doub", artboardTestDouble(2)},
		artboardTestEntry{"Btom", "doub", artboardTestDouble(1)})
	rgb := artboardTestDescriptor("RGBC",
		artboardTestEntry{"Rd  ", "doub", artboardTestDouble(10)},
		artboardTestEntry{"Grn ", "doub", artboardTestDouble(80)},
		artboardTestEntry{"Bl  ", "doub", artboardTestDouble(160)})
	entries := []artboardTestEntry{{"artboardRect", "Objc", rect},
		{"artboardBackgroundType", "long", artboardTestU32(settings.background)}, {"Clr ", "Objc", rgb},
		{"guideIndeces", "VlLs", artboardTestU32(0)}}
	if settings.unknown {
		entries = append(entries, artboardTestEntry{"futureClipRule", "bool", []byte{1}})
	}
	board := append(artboardTestU32(16), artboardTestDescriptor("artboard", entries...)...)
	point := artboardTestDescriptor("Pnt ",
		artboardTestEntry{"Hrzn", "doub", artboardTestDouble(settings.origin)},
		artboardTestEntry{"Vrtc", "doub", artboardTestDouble(0)})
	document := append(artboardTestU32(16), artboardTestDescriptor("artboard",
		artboardTestEntry{"Cnt ", "long", artboardTestU32(1)},
		artboardTestEntry{"origin", "Objc", point}, artboardTestEntry{"autoExpandOffset", "Objc", point})...)
	var records, pixels bytes.Buffer
	_ = binary.Write(&records, binary.BigEndian, int16(-4))
	for index := range 4 {
		bounds, values, opacity, mode := image.Rectangle{}, [4]byte{}, byte(255), "norm"
		switch index {
		case 1:
			bounds, values, opacity = image.Rect(0, 0, 3, 1), [4]byte{255, 0, 0, 255}, 153
		case 2:
			bounds, values, mode = image.Rect(1, 0, 2, 1), [4]byte{0, 255, 0, 128}, "mul "
		case 3:
			opacity = settings.groupOpacity
			if settings.pass {
				mode = "pass"
			}
		}
		for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
			_ = binary.Write(&records, binary.BigEndian, int32(coordinate))
		}
		channels := 0
		if index == 1 || index == 2 {
			channels = 4
		}
		_ = binary.Write(&records, binary.BigEndian, uint16(channels))
		for component := 0; component < channels; component++ {
			id := int16(component)
			if component == 3 {
				id = -1
			}
			_ = binary.Write(&records, binary.BigEndian, id)
			_ = binary.Write(&records, binary.BigEndian, uint32(bounds.Dx()*bounds.Dy()+2))
			_ = binary.Write(&pixels, binary.BigEndian, uint16(0))
			pixels.Write(bytes.Repeat([]byte{values[component]}, bounds.Dx()*bounds.Dy()))
		}
		records.WriteString("8BIM" + mode)
		records.Write([]byte{opacity, 0, 0, 0})
		extra := append(make([]byte, 8), 1, 'L', 0, 0)
		if index == 0 || index == 3 {
			divider := uint32(3)
			if index == 3 {
				divider = 1
			}
			payload := append(artboardTestU32(divider), "8BIM"+mode...)
			extra = append(extra, artboardTestBlock("lsct", payload)...)
		}
		if index == 3 {
			extra = append(extra, artboardTestBlock("artb", board)...)
			if settings.duplicate {
				extra = append(extra, artboardTestBlock("abdd", board)...)
			}
		}
		_ = binary.Write(&records, binary.BigEndian, uint32(len(extra)))
		records.Write(extra)
	}
	records.Write(pixels.Bytes())
	if records.Len()%2 != 0 {
		records.WriteByte(0)
	}
	layerMask := append(artboardTestU32(uint32(records.Len())), records.Bytes()...)
	layerMask = append(layerMask, artboardTestU32(0)...)
	layerMask = append(layerMask, artboardTestBlock("artd", document)...)
	data := []byte("8BPS")
	data = binary.BigEndian.AppendUint16(data, 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = append(data, artboardTestU32(1)...)
	data = append(data, artboardTestU32(2)...)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	data = append(data, artboardTestU32(uint32(len(layerMask)))...)
	data = append(data, layerMask...)
	// Deliberately incorrect magenta footer proves that only ordinary layer pixels participate.
	data = append(data, 0, 0, 255, 255, 0, 0, 255, 255, 255, 255)
	return data
}

func TestArtboardGroupBackgroundIsInsideAndCropsRastersWithoutUsingFooter(t *testing.T) {
	for _, pass := range []bool{false, true} {
		data := artboardTestPSD(artboardTestConfiguration{background: 4, groupOpacity: 255, pass: pass})
		original := sha256.Sum256(data)
		source, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		result, err := RenderArtboard(nil, source)
		if err != nil {
			t.Fatal(err)
		}
		if result.Rect != image.Rect(0, 0, 2, 1) || result.NRGBAAt(0, 0) != (color.NRGBA{157, 32, 64, 255}) ||
			result.NRGBAAt(1, 0) != (color.NRGBA{78, 32, 32, 255}) {
			t.Fatalf("pass=%t: background, multiply/alpha or saved-canvas crop differs: %v", pass, result.Pix)
		}
		if original != sha256.Sum256(data) {
			t.Fatal("single-board rendering changed source bytes")
		}
		retained := sha256.Sum256(result.Pix)
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if retained != sha256.Sum256(result.Pix) {
			t.Fatal("rendered board requires an open source")
		}
	}
}

func TestArtboardBackgroundEnumsAndNormalizedOffsetKeepOriginalLayerCoordinates(t *testing.T) {
	expected := map[uint32]color.NRGBA{1: {255, 102, 102, 255}, 2: {153, 0, 0, 255},
		3: {255, 0, 0, 153}, 4: {157, 32, 64, 255}}
	for background, color := range expected {
		for _, origin := range []float64{-8, 0, 8} {
			source, err := psdfile.Parse(artboardTestPSD(artboardTestConfiguration{
				background: background, origin: origin, groupOpacity: 255}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := RenderArtboard(nil, source)
			_ = source.Close()
			if err != nil || result.NRGBAAt(0, 0) != color {
				t.Fatalf("raw background=%d or normalized origin=%v was misinterpreted: %v, %v",
					background, origin, result, err)
			}
		}
	}
}

type artboardTestReader struct {
	data   []byte
	pixels []psdfile.Span
	reads  int
}

func (reader *artboardTestReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.pixels {
		if len(data) > 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			reader.reads++
		}
	}
	if offset < 0 || offset >= int64(len(reader.data)) && len(data) != 0 {
		return 0, io.EOF
	}
	count := copy(data, reader.data[offset:])
	if count != len(data) {
		return count, io.EOF
	}
	return count, nil
}

func TestArtboardUncalibratedMetadataAndLowBudgetRejectBeforeRasterReads(t *testing.T) {
	for _, setting := range []artboardTestConfiguration{
		{background: 4, groupOpacity: 255, duplicate: true},
		{background: 4, groupOpacity: 255, unknown: true},
		{background: 0, groupOpacity: 255},
	} {
		reader := &artboardTestReader{data: artboardTestPSD(setting)}
		source, err := psdfile.Read(reader, int64(len(reader.data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, layer := range source.Layers {
			for _, channel := range layer.Channels {
				reader.pixels = append(reader.pixels, channel.Data)
			}
		}
		reader.pixels = append(reader.pixels, source.Composite.Data)
		_, err = RenderArtboard(nil, source)
		_ = source.Close()
		if !errors.Is(err, psdfile.ErrUnsupported) || reader.reads != 0 {
			t.Fatalf("uncalibrated metadata decoded raster data: %+v, reads=%d, err=%v", setting, reader.reads, err)
		}
	}
	source, err := psdfile.Parse(artboardTestPSD(artboardTestConfiguration{background: 4, groupOpacity: 255}))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	_, err = RenderArtboard(nil, source, ArtboardRenderOptions{MaxWorkingBytes: 1})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("artboard metadata ignored the lowered working budget: %v", err)
	}
	_, err = RenderArtboard(nil, source, ArtboardRenderOptions{MaxWorkingBytes: 64 << 10})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("artboard source snapshot ignored the retained/decoder peak budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderArtboard(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artboard render: %v", err)
	}
	if _, err := RenderArtboard(nil, source, ArtboardRenderOptions{MaxWorkingBytes: 2 << 20}); err != nil {
		t.Fatal(err)
	}
}

func TestArtboardNondefaultGroupAndLifecycleRemainUnsupported(t *testing.T) {
	source, err := psdfile.Parse(artboardTestPSD(artboardTestConfiguration{background: 4, groupOpacity: 128}))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := RenderArtboard(nil, source); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("uncalibrated group opacity was silently composed: %v", err)
	}
	_, err = RenderArtboard(nil, source, ArtboardRenderOptions{MaxWorkingBytes: -1})
	if !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("invalid artboard budget: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderArtboard(nil, source); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed artboard source: %v", err)
	}
}
