package psdscene_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type integer16OneShotFailureReader struct {
	data  []byte
	alpha psdfile.Span
	fired bool
}

func (reader *integer16OneShotFailureReader) ReadAt(data []byte, offset int64) (int, error) {
	if !reader.fired && reader.alpha.Length > 0 && offset < reader.alpha.Offset+reader.alpha.Length &&
		offset+int64(len(data)) > reader.alpha.Offset {
		reader.fired = true
		return 0, psdfile.ErrLimit
	}
	return bytes.NewReader(reader.data).ReadAt(data, offset)
}

func integer16TwoPixelChannels(color, alpha [2]uint16) []precisionChannel {
	channels := make([]precisionChannel, 4)
	for component := range channels {
		channels[component].id = int16(component)
		codes := color
		if component == 3 {
			channels[component].id = -1
			codes = alpha
		}
		channels[component].data = make([]byte, 4)
		for pixel, code := range codes {
			binary.BigEndian.PutUint16(channels[component].data[pixel*2:], code)
		}
	}
	return channels
}

func TestPrecisionInteger16SourceHalfAndAbsoluteWindow(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	layer := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{32767, 32769}, [2]uint16{65535, 65535})}
	source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{layer}))
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil || full.Pix[0] != .5 || full.Pix[4] != float64(16385)/32768 {
		t.Fatalf("source RGB16 internal domain = %v, %v", full, err)
	}
	window := image.Rect(1, 0, 2, 1)
	tile, err := psdscene.RenderPrecisionTile(nil, source, window)
	if err != nil || tile.Bounds != window || tile.Image.Pix[0] != full.Pix[4] {
		t.Fatalf("source RGB16 full/tile integer mismatch = %v, %v", tile, err)
	}
}

func TestPrecisionInteger16OutsideWindowPartialAlphaKeepsFullTileIdentical(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	backdrop := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{0, 0}, [2]uint16{65535, 32768})}
	top := precisionLayerFixture{bounds: bounds, opacity: 255, mode: "scrn",
		channels: integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{65535, 65535})}
	source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{backdrop, top}))
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	window := image.Rect(0, 0, 1, 1)
	tile, err := psdscene.RenderPrecisionTile(nil, source, window)
	if err != nil {
		t.Fatal(err)
	}
	for component := range 4 {
		if tile.Image.Pix[component] != full.Pix[component] {
			t.Fatalf("outside-window source alpha changed selection at component%d: full %.17g tile %.17g",
				component, full.Pix[component], tile.Image.Pix[component])
		}
	}
	if full.Pix[0] != float64(32767)/65535 {
		t.Fatalf("source with outside-window partial alpha unexpectedly entered opaque integer backend: %.17g",
			full.Pix[0])
	}
}

func TestPrecisionInteger16QualificationBudgetDoesNotDependOnWindow(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	layer := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{65535, 65535})}
	top := precisionLayerFixture{bounds: bounds, opacity: 255, mode: "scrn",
		channels: integer16TwoPixelChannels([2]uint16{0, 0}, [2]uint16{65535, 65535})}
	source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{layer, top}))
	region := psdfile.RegionOptions{MaxWorkingBytes: 7}
	full, err := psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{Region: region})
	if err != nil {
		t.Fatal(err)
	}
	tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(1, 0, 2, 1), psdscene.TileOptions{Region: region})
	if err != nil || tile.Image.Pix[0] != full.Pix[4] || full.Pix[0] != float64(32767)/65535 {
		t.Fatalf("qualification scratch budget selected different source domains: full %v tile %v error %v",
			full, tile, err)
	}
}

func TestPrecisionInteger16PartialSourceAlphaOverOpaqueKeepsFullTileIdentical(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	backdrop := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{0, 0}, [2]uint16{65535, 65535})}
	top := precisionLayerFixture{bounds: bounds, opacity: 128,
		channels: integer16TwoPixelChannels([2]uint16{65535, 65535}, [2]uint16{1, 32767})}
	source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{backdrop, top}))
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil || full.Pix[0] != float64(1)/32768 || full.Pix[4] != float64(8224)/32768 || full.Pix[7] != 1 {
		t.Fatalf("source partial alpha was not composed in the calibrated integer domain: %v, %v", full, err)
	}
	tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(1, 0, 2, 1))
	if err != nil || tile.Image.Pix[0] != full.Pix[4] || tile.Image.Pix[3] != 1 {
		t.Fatalf("source partial alpha integer full/tile mismatch: %v, %v", tile, err)
	}
}

func TestPrecisionInteger16QualificationDoesNotSwallowReaderLimitCause(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	layer := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{65535, 65535})}
	data := precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{layer})
	for _, tiled := range []bool{false, true} {
		reader := &integer16OneShotFailureReader{data: data}
		source, err := psdfile.Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		for _, channel := range source.Layers[0].Channels {
			if channel.ID == -1 {
				reader.alpha = channel.Data
			}
		}
		if tiled {
			_, err = psdscene.RenderPrecisionTile(nil, source, image.Rect(0, 0, 1, 1))
		} else {
			_, err = psdscene.RenderPrecision(nil, source)
		}
		var detail *psdfile.Error
		if !errors.Is(err, psdfile.ErrLimit) || !errors.As(err, &detail) ||
			!errors.Is(detail.Kind, psdfile.ErrMalformed) || detail.Cause == nil {
			t.Fatalf("tile%t ReaderAt ErrLimit cause was swallowed or reclassified: %v", tiled, err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrecisionInteger16WholePipelineBudgetPrecedesQualificationReads(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	layer := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{65535, 65535})}
	for _, tiled := range []bool{false, true} {
		source, reader := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{layer}))
		reader.forbid = true
		var err error
		if tiled {
			_, err = psdscene.RenderPrecisionTile(nil, source, bounds,
				psdscene.TileOptions{MaxWorkingBytes: 1100})
		} else {
			_, err = psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{MaxWorkingBytes: 1100})
		}
		if !errors.Is(err, psdfile.ErrLimit) || reader.pixelReads != 0 {
			t.Fatalf("tile%t pixel samples read before complete pipeline budget: reads=%d error=%v",
				tiled, reader.pixelReads, err)
		}
	}
}

func TestPrecisionInteger16QualificationChargesRepeatedRLETables(t *testing.T) {
	const height = 20_000
	bounds := image.Rect(0, 0, 1, height)
	channels := make([]precisionChannel, 4)
	for component := range channels {
		channels[component].id = int16(component)
		channels[component].data = make([]byte, height*2)
		for row := range height {
			binary.BigEndian.PutUint16(channels[component].data[row*2:], 32767)
		}
	}
	channels[3].id = -1
	channels[3].data = make([]byte, height*4)
	for row := range height {
		binary.BigEndian.PutUint16(channels[3].data[row*2:], 2)
		channels[3].data[height*2+row*2], channels[3].data[height*2+row*2+1] = 255, 255
	}
	layer := precisionLayerFixture{bounds: bounds, opacity: 255, channels: channels}
	topChannels := make([]precisionChannel, 4)
	for component := range topChannels {
		topChannels[component].id = int16(component)
		topChannels[component].data = make([]byte, height*2)
	}
	topChannels[3].id = -1
	for row := range height {
		binary.BigEndian.PutUint16(topChannels[3].data[row*2:], 65535)
	}
	top := precisionLayerFixture{bounds: bounds, opacity: 255, mode: "scrn", channels: topChannels}
	data := precisionSourceFixture(16, 3, 1, height, []precisionLayerFixture{layer, top})
	indexChannels := append([]precisionChannel(nil), channels...)
	indexChannels[3].data = make([]byte, height*2)
	indexLayer := precisionLayerFixture{bounds: bounds, opacity: 255, channels: indexChannels}
	indexData := precisionSourceFixture(16, 3, 1, height, []precisionLayerFixture{indexLayer, top})
	indexed, err := psdfile.Parse(indexData)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range indexed.Layers[0].Channels {
		if channel.ID == -1 {
			binary.BigEndian.PutUint16(data[channel.Span.Offset:], 1)
		}
	}
	if err := indexed.Close(); err != nil {
		t.Fatal(err)
	}
	source, _ := precisionSource(t, data)
	region := psdfile.RegionOptions{MaxRLEWorkBytes: 100_000}
	full, err := psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{Region: region})
	if err != nil {
		t.Fatal(err)
	}
	tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(0, 0, 1, 1),
		psdscene.TileOptions{Region: region})
	if err != nil || full.Pix[0] != float64(32767)/65535 || tile.Image.Pix[0] != full.Pix[0] {
		t.Fatalf("repeated RLE table work bypassed shared qualification budget: full %v tile %v error %v",
			full.Pix[:4], tile, err)
	}
}
