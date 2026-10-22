package psdscene_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type precisionChannel struct {
	id   int16
	data []byte
}

type precisionLayerFixture struct {
	bounds   image.Rectangle
	opacity  uint8
	divider  uint32
	mode     string
	mask     []byte
	channels []precisionChannel
	fill     *byte
	clipping byte
	flags    byte
	clbl     *byte
	ranges   []byte
}

func precisionNumber(target *bytes.Buffer, value any) {
	_ = binary.Write(target, binary.BigEndian, value)
}

func precisionSourceFixture(depth, mode uint16, width, height int, layers []precisionLayerFixture) []byte {
	var records, channels bytes.Buffer
	precisionNumber(&records, int16(len(layers)))
	for _, layer := range layers {
		for _, coordinate := range []int{layer.bounds.Min.Y, layer.bounds.Min.X, layer.bounds.Max.Y, layer.bounds.Max.X} {
			precisionNumber(&records, int32(coordinate))
		}
		precisionNumber(&records, uint16(len(layer.channels)))
		for _, channel := range layer.channels {
			precisionNumber(&records, channel.id)
			precisionNumber(&records, uint32(len(channel.data)+2))
			precisionNumber(&channels, uint16(0))
			channels.Write(channel.data)
		}
		blend := layer.mode
		if blend == "" {
			blend = "norm"
		}
		records.WriteString("8BIM" + blend)
		records.Write([]byte{layer.opacity, layer.clipping, layer.flags, 0})
		var extra bytes.Buffer
		precisionNumber(&extra, uint32(len(layer.mask)))
		extra.Write(layer.mask)
		precisionNumber(&extra, uint32(len(layer.ranges)))
		extra.Write(layer.ranges)
		extra.Write([]byte{1, 'L', 0, 0})
		if layer.divider != 0 {
			extra.WriteString("8BIMlsct")
			precisionNumber(&extra, uint32(12))
			precisionNumber(&extra, layer.divider)
			extra.WriteString("8BIM" + blend)
		}
		if layer.fill != nil {
			extra.WriteString("8BIMiOpa")
			precisionNumber(&extra, uint32(1))
			extra.Write([]byte{*layer.fill, 0})
		}
		if layer.clbl != nil {
			extra.WriteString("8BIMclbl")
			precisionNumber(&extra, uint32(1))
			extra.Write([]byte{*layer.clbl, 0})
		}
		precisionNumber(&records, uint32(extra.Len()))
		records.Write(extra.Bytes())
	}
	records.Write(channels.Bytes())
	if records.Len()%2 != 0 {
		records.WriteByte(0)
	}
	var layerMask bytes.Buffer
	if depth == 32 {
		precisionNumber(&layerMask, uint32(0))
		precisionNumber(&layerMask, uint32(0))
		layerMask.WriteString("8BIMLr32")
		precisionNumber(&layerMask, uint32(records.Len()))
		layerMask.Write(records.Bytes())
		for layerMask.Len()%4 != 0 {
			layerMask.WriteByte(0)
		}
	} else {
		precisionNumber(&layerMask, uint32(records.Len()))
		layerMask.Write(records.Bytes())
		precisionNumber(&layerMask, uint32(0))
	}
	var resource bytes.Buffer
	resource.WriteString("8BIM")
	precisionNumber(&resource, uint16(1039))
	resource.Write([]byte{0, 0})
	precisionNumber(&resource, uint32(4))
	resource.WriteString("ICC!")
	var result bytes.Buffer
	result.WriteString("8BPS")
	precisionNumber(&result, uint16(1))
	result.Write(make([]byte, 6))
	components := uint16(3)
	if mode == 1 {
		components = 1
	}
	precisionNumber(&result, components)
	precisionNumber(&result, uint32(height))
	precisionNumber(&result, uint32(width))
	precisionNumber(&result, depth)
	precisionNumber(&result, mode)
	precisionNumber(&result, uint32(0))
	precisionNumber(&result, uint32(resource.Len()))
	result.Write(resource.Bytes())
	precisionNumber(&result, uint32(layerMask.Len()))
	result.Write(layerMask.Bytes())
	precisionNumber(&result, uint16(0))
	result.Write(make([]byte, width*height*int(components)*int(depth/8)))
	return result.Bytes()
}

func precisionFloat(value float32) []byte {
	return binary.BigEndian.AppendUint32(nil, math.Float32bits(value))
}

func precisionRGBA(depth uint16, values [4]float64) []precisionChannel {
	var result []precisionChannel
	for _, component := range []int{3, 0, 1, 2} {
		id := int16(component)
		if component == 3 {
			id = -1
		}
		data := precisionFloat(float32(values[component]))
		if depth == 16 {
			data = binary.BigEndian.AppendUint16(nil, uint16(values[component]))
		}
		result = append(result, precisionChannel{id, data})
	}
	return result
}

type precisionReader struct {
	data       []byte
	pixelSpans []psdfile.Span
	pixelReads int
	forbid     bool
	cancel     context.CancelFunc
	failure    error
}

func (reader *precisionReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.pixelSpans {
		if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			reader.pixelReads++
			if reader.forbid {
				return 0, errors.New("pixel read forbidden before preflight")
			}
			if reader.failure != nil {
				return 0, reader.failure
			}
			if reader.cancel != nil {
				reader.cancel()
			}
		}
	}
	return bytes.NewReader(reader.data).ReadAt(data, offset)
}

func precisionSource(t *testing.T, data []byte) (*psdfile.Document, *precisionReader) {
	t.Helper()
	reader := &precisionReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range source.Layers {
		for _, channel := range layer.Channels {
			reader.pixelSpans = append(reader.pixelSpans, channel.Data)
		}
	}
	t.Cleanup(func() { _ = source.Close() })
	return source, reader
}

func TestPrecisionSourceOriginalDepthHDRAndProfile(t *testing.T) {
	for _, depth := range []uint16{16, 32} {
		values := [4]float64{12345, 23456, 34567, 65535}
		if depth == 32 {
			values = [4]float64{2.125, -.625, .12345679104328156, .5}
		}
		data := precisionSourceFixture(depth, 3, 1, 1, []precisionLayerFixture{
			{bounds: image.Rect(0, 0, 1, 1), opacity: 255, channels: precisionRGBA(depth, values)},
		})
		original := bytes.Clone(data)
		source, _ := precisionSource(t, data)
		result, err := psdscene.RenderPrecision(nil, source)
		if err != nil {
			t.Fatal(err)
		}
		for index, value := range values {
			if depth == 16 {
				if index == 3 {
					value = 1
				} else {
					value = float64((uint32(value)+1)/2) / 32768
				}
			}
			if result.Pix[index] != value {
				t.Fatalf("depth%d sample%d = %.17g, expected %.17g", depth, index, result.Pix[index], value)
			}
		}
		expectedEncoding := psdprecision.EncodedRGB
		if depth == 32 {
			expectedEncoding = psdprecision.LinearRGB
		}
		if result.SourceDepth != depth || string(result.ICCProfile) != "ICC!" || result.Encoding != expectedEncoding {
			t.Fatal("source precision, encoding or opaque profile provenance changed")
		}
		result.ICCProfile[0], result.Pix[0] = 0, 0
		if !bytes.Equal(data, original) {
			t.Fatal("rendered owned values mutated original source bytes")
		}
	}
}

func TestPrecisionSourceActiveAlternateGroupTree(t *testing.T) {
	bottom := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{0, 0, 1, 1})}
	groupEnd := precisionLayerFixture{opacity: 255, divider: 3}
	top := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{2, 0, 0, 1})}
	group := precisionLayerFixture{opacity: 128, divider: 1}
	source, _ := precisionSource(t, precisionSourceFixture(32, 3, 1, 1,
		[]precisionLayerFixture{bottom, groupEnd, top, group}))
	if source.ActiveLayerSet < 0 || source.LayerSets[source.ActiveLayerSet].Key != "Lr32" {
		t.Fatal("fixture did not exercise the native alternate high-depth layer set")
	}
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	coverage := 128.0 / 255
	if math.Abs(result.Pix[0]-2*coverage) > 1e-12 || math.Abs(result.Pix[2]-(1-coverage)) > 1e-12 || result.Pix[3] != 1 {
		t.Fatalf("group order or opacity changed: %v", result.Pix)
	}
}

func precisionMask(bounds image.Rectangle, outside, flags byte, density *byte, real bool) []byte {
	var result bytes.Buffer
	for _, value := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		precisionNumber(&result, int32(value))
	}
	if density != nil {
		flags |= 16
	}
	result.Write([]byte{outside, flags})
	if density != nil {
		result.Write([]byte{1, *density})
	}
	if real {
		result.Write([]byte{4, 255})
		for _, value := range []int32{0, 0, 1, 1} {
			precisionNumber(&result, value)
		}
	} else if result.Len() == 18 {
		result.Write([]byte{0, 0})
	}
	return result.Bytes()
}

func TestPrecisionSourceMaskOutsideInvertDensityAndRealMask(t *testing.T) {
	density := byte(128)
	layer := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{2, -.5, .12345679104328156, 1}),
		mask:     precisionMask(image.Rect(2, 0, 3, 1), 128, 4, &density, false)}
	layer.channels = append(layer.channels, precisionChannel{-2, precisionFloat(0)})
	source, _ := precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{layer}))
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	expected := 1 - (128.0/255)*(128.0/255)
	if math.Abs(result.Pix[3]-expected) > 1e-12 || result.Pix[0] != 2 {
		t.Fatalf("outside mask inversion/density changed source HDR colors: %v", result.Pix)
	}
	layer.mask = precisionMask(image.Rect(0, 0, 1, 1), 0, 8, nil, true)
	layer.channels = append(layer.channels, precisionChannel{-3, precisionFloat(.25)})
	source, _ = precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{layer}))
	result, err = psdscene.RenderPrecision(nil, source)
	if err != nil || result.Pix[3] != .75 || result.Pix[0] != 2 {
		t.Fatalf("derived -2 mask was reapplied instead of real -3 mask: %v", err)
	}
}

func TestPrecisionSourceGrayscaleAndRelativeMask(t *testing.T) {
	layer := precisionLayerFixture{bounds: image.Rect(5, 7, 6, 8), opacity: 255,
		channels: []precisionChannel{{0, precisionFloat(-.75)}, {-2, precisionFloat(.25)}},
		mask:     precisionMask(image.Rect(5, 7, 6, 8), 255, 1, nil, false)}
	source, _ := precisionSource(t, precisionSourceFixture(32, 1, 6, 8, []precisionLayerFixture{layer}))
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	offset := (7*6 + 5) * 4
	if result.Pix[offset] != -.75 || result.Pix[offset+1] != -.75 || result.Pix[offset+2] != -.75 ||
		result.Pix[offset+3] != .25 || result.Pix[3] != 0 {
		t.Fatalf("relative grayscale mask origin or color expansion changed: %v", result.Pix)
	}
}

func TestPrecisionSourcePreflightLifecycleAndNonFinite(t *testing.T) {
	layer := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{2, -.5, .12345679104328156, 1})}
	data := precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{layer})
	source, reader := precisionSource(t, data)
	reader.forbid = true
	_, err := psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{MaxWorkingBytes: 1088})
	if !errors.Is(err, psdfile.ErrLimit) || reader.pixelReads != 0 {
		t.Fatalf("source samples read before whole-pipeline working preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdscene.RenderPrecision(ctx, source); !errors.Is(err, context.Canceled) || reader.pixelReads != 0 {
		t.Fatalf("pre-canceled source rendering accessed pixels: %v", err)
	}
	reader.forbid = false
	ctx, cancel = context.WithCancel(context.Background())
	reader.cancel = cancel
	if _, err := psdscene.RenderPrecision(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("source channel read cancellation was lost: %v", err)
	}
	cancel()
	reader.cancel = nil
	underlying := errors.New("source precision read failure")
	reader.failure = underlying
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, underlying) {
		t.Fatalf("custom ReaderAt error was lost: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source was rendered from stale normalized samples: %v", err)
	}
	layer.channels[1].data = binary.BigEndian.AppendUint32(nil, 0x7fc01234)
	data = precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{layer})
	original := bytes.Clone(data)
	source, _ = precisionSource(t, data)
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, psdprecision.ErrNonFinite) {
		t.Fatalf("source NaN silently changed: %v", err)
	}
	result, err := psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{NonFinite: psdprecision.NonFiniteZero})
	if err != nil || result.Pix[0] != 0 || !bytes.Equal(data, original) {
		t.Fatalf("explicit NaN policy modified original 32-bit payload: %v", err)
	}
}

func TestPrecisionSourceBlendDomainAndStrict16PassGate(t *testing.T) {
	base := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: precisionRGBA(32, [4]float64{2, -.5, .25, .5})}
	top := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255, mode: "mul ",
		channels: precisionRGBA(32, [4]float64{3, 2, .8, .5})}
	source, _ := precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{base, top}))
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil || math.Abs(result.Pix[0]-11.0/3) > 1e-12 || math.Abs(result.Pix[3]-.75) > 1e-12 {
		t.Fatalf("source HDR Multiply did not preserve the alpha model: %v", err)
	}
	top.mode = "scrn"
	source, reader := precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{base, top}))
	reader.forbid = true
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, psdfile.ErrUnsupported) || reader.pixelReads != 0 {
		t.Fatalf("unavailable native32 Screen was read or approximated: %v", err)
	}
	group := precisionLayerFixture{opacity: 128, divider: 1, mode: "pass"}
	end := precisionLayerFixture{opacity: 255, divider: 3}
	top.mode = "mul "
	source, _ = precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{base, end, top, group}))
	result, err = psdscene.RenderPrecision(nil, source)
	if err != nil || result.Pix[0] <= 2 || result.Pix[3] <= .5 || result.Pix[3] >= .75 {
		t.Fatalf("calibrated source32 partial pass did not use its backdrop: %v", err)
	}
	base.channels = precisionRGBA(16, [4]float64{65535, 0, 0, 32768})
	top.channels = precisionRGBA(16, [4]float64{32768, 0, 0, 32768})
	fixture := precisionSourceFixture(16, 3, 1, 1, []precisionLayerFixture{base, end, top, group})
	source, reader = precisionSource(t, fixture)
	reader.forbid = true
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, psdfile.ErrUnsupported) || reader.pixelReads != 0 {
		t.Fatalf("uncalibrated source16 partial pass escaped the default strict gate: %v", err)
	}
}

func TestPrecisionSourceLimitedMoreBlendScopesBeforePixelReads(t *testing.T) {
	fill := byte(128)
	layer := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255, mode: "diff", fill: &fill,
		channels: precisionRGBA(16, [4]float64{32768, 16384, 49151, 65535})}
	source, reader := precisionSource(t, precisionSourceFixture(16, 3, 1, 1, []precisionLayerFixture{layer}))
	reader.forbid = true
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, psdfile.ErrUnsupported) || reader.pixelReads != 0 {
		t.Fatalf("Difference special Fill was silently reduced to opacity: %v", err)
	}
	layer.fill = nil
	layer.channels = precisionRGBA(32, [4]float64{2, -.5, .25, .5})
	for _, mode := range []string{"smud", "diff"} {
		layer.mode = mode
		source, reader = precisionSource(t, precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{layer}))
		reader.forbid = true
		_, err := psdscene.RenderPrecision(nil, source)
		if !errors.Is(err, psdfile.ErrUnsupported) || reader.pixelReads != 0 {
			t.Fatalf("unavailable or uncalibrated 32-bit mode %s was read or approximated: %v", mode, err)
		}
	}
	layer.mode = "norm"
	group := precisionLayerFixture{opacity: 255, divider: 1, mode: "dark"}
	end := precisionLayerFixture{opacity: 255, divider: 3}
	data := precisionSourceFixture(32, 3, 1, 1, []precisionLayerFixture{end, layer, group})
	source, reader = precisionSource(t, data)
	reader.forbid = true
	if _, err := psdscene.RenderPrecision(nil, source); !errors.Is(err, psdfile.ErrUnsupported) || reader.pixelReads != 0 {
		t.Fatalf("uncalibrated high-depth group Darken escaped the source scope gate: %v", err)
	}
}

var _ io.ReaderAt = (*precisionReader)(nil)
