package psdscene_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func geometry16Fixtures() []precisionLayerFixture {
	var fixtures []precisionLayerFixture
	for index, bounds := range []image.Rectangle{image.Rect(-1, 0, 2, 2), image.Rect(4, 0, 6, 1),
		image.Rect(1, 2, 3, 3), image.Rect(5, 2, 7, 3)} {
		channels := []precisionChannel{{id: 0}, {id: 1}, {id: 2}, {id: -1}}
		for pixel := range bounds.Dx() * bounds.Dy() {
			for component := range 4 {
				value := uint16((pixel*971 + component*13277 + index*19483) & 65535)
				if component == 3 && (pixel+index)%2 == 0 {
					value = 0
				}
				channels[component].data = binary.BigEndian.AppendUint16(channels[component].data, value)
			}
		}
		mode := "norm"
		if index == 1 || index == 3 {
			mode = "diff"
		}
		fixtures = append(fixtures, precisionLayerFixture{bounds: bounds, mode: mode, channels: channels,
			opacity: []uint8{255, 161, 113, 231}[index]})
	}
	return fixtures
}

func geometry16Scene(fixtures []precisionLayerFixture) *psdprecision.Scene {
	scene := &psdprecision.Scene{Width: 6, Height: 3}
	for _, item := range fixtures {
		pixels := &psdprecision.FloatImage{Width: item.bounds.Dx(), Height: item.bounds.Dy(), SourceDepth: 16,
			Pix: make([]float64, item.bounds.Dx()*item.bounds.Dy()*4)}
		for component, channel := range item.channels {
			for pixel := range pixels.Width * pixels.Height {
				pixels.Pix[pixel*4+component] = float64(binary.BigEndian.Uint16(channel.data[pixel*2:])) / 65535
			}
		}
		opacity := float64(item.opacity) / 255
		scene.Layers = append(scene.Layers, &psdprecision.Layer{Bounds: item.bounds, BlendMode: item.mode,
			Pixels: pixels, Opacity: &opacity})
	}
	return scene
}

func geometry16Compare(t *testing.T, result *psdprecision.FloatImage,
	rectangle image.Rectangle, expected *psdprecision.FloatImage) {
	t.Helper()
	if result == nil || result.Width != rectangle.Dx() || result.Height != rectangle.Dy() {
		t.Fatal("source geometry differs")
	}
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
			for component := range 4 {
				got := result.Pix[((y-rectangle.Min.Y)*result.Width+x-rectangle.Min.X)*4+component]
				if got != expected.Pix[(y*expected.Width+x)*4+component] {
					t.Fatalf("source/window domain differs at(%d,%d) component%d", x, y, component)
				}
			}
		}
	}
}

func TestPrecisionGeometry16SourceROILazyAndVisitorOwnership(t *testing.T) {
	fixtures := geometry16Fixtures()
	data := precisionSourceFixture(16, 3, 6, 3, fixtures)
	source, reader := precisionSource(t, data)
	expected, err := psdprecision.RenderRGB16(nil, geometry16Scene(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil || string(full.ICCProfile) != "ICC!" {
		t.Fatalf("source full or ICC = %v", err)
	}
	geometry16Compare(t, full, image.Rect(0, 0, 6, 3), expected)
	for _, rectangle := range []image.Rectangle{image.Rect(4, 0, 6, 1), image.Rect(2, 1, 5, 2),
		image.Rect(1, 2, 6, 3), image.Rect(0, 0, 0, 0)} {
		reader.pixelReads = 0
		region, err := psdscene.RenderPrecisionTile(nil, source, rectangle)
		if err != nil || string(region.Image.ICCProfile) != "ICC!" {
			t.Fatalf("source region or ICC = %v", err)
		}
		geometry16Compare(t, region.Image, rectangle, expected)
		if rectangle == image.Rect(4, 0, 6, 1) && reader.pixelReads != 4 ||
			rectangle == image.Rect(2, 1, 5, 2) && reader.pixelReads != 0 ||
			rectangle.Empty() && reader.pixelReads != 0 {
			t.Fatalf("Source read outside requested intersections: %d", reader.pixelReads)
		}
	}
	visits := 0
	err = psdscene.VisitPrecisionTiles(nil, source, image.Rect(0, 0, 6, 3), image.Pt(2, 1),
		func(region *psdprecision.RegionImage) error {
			geometry16Compare(t, region.Image, region.Bounds, expected)
			visits++
			for index := range region.Image.Pix {
				region.Image.Pix[index] = -1
			}
			region.Image.ICCProfile[0] = 0
			return nil
		})
	if err != nil || visits != 9 {
		t.Fatalf("visitor failed or exposed shared output: %d %v", visits, err)
	}
	if !bytes.Equal(data, reader.data) {
		t.Fatal("source bytes changed")
	}
}

func TestPrecisionGeometry16BudgetsCausesAndWholeEligibility(t *testing.T) {
	fixtures := geometry16Fixtures()
	data := precisionSourceFixture(16, 3, 6, 3, fixtures)
	for _, tiled := range []bool{false, true} {
		render := func(source *psdfile.Document, amount int64) (*psdprecision.FloatImage, error) {
			if !tiled {
				return psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{MaxWorkingBytes: amount})
			}
			result, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(4, 0, 6, 1),
				psdscene.TileOptions{MaxWorkingBytes: amount})
			if err != nil {
				return nil, err
			}
			return result.Image, nil
		}
		source, reader := precisionSource(t, data)
		low, high := int64(1), int64(16384)
		for low < high {
			middle := (low + high) / 2
			result, err := render(source, middle)
			if errors.Is(err, psdfile.ErrLimit) && result == nil {
				low = middle + 1
			} else if err == nil && result != nil {
				high = middle
			} else {
				t.Fatalf("unexpected quota error: %v", err)
			}
		}
		for _, amount := range []int64{low - 1, low, low + 1} {
			result, err := render(source, amount)
			if amount < low && (result != nil || !errors.Is(err, psdfile.ErrLimit)) ||
				amount >= low && (result == nil || err != nil) {
				t.Fatalf("quota%d around exact%d: %v", amount, low, err)
			}
		}
		t.Logf("tiled=%v exact working-byte quota=%d", tiled, low)
		reader.pixelReads, reader.forbid = 0, true
		if result, err := render(source, 2560); result != nil || !errors.Is(err, psdfile.ErrLimit) ||
			reader.pixelReads != 0 {
			t.Fatalf("allocation preflight read pixels: %d %v", reader.pixelReads, err)
		}
	}
	source, reader := precisionSource(t, data)
	failure := errors.New("actual RGB16 source I/O failure")
	reader.failure = failure
	if result, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(4, 0, 6, 1)); result != nil ||
		!errors.Is(err, failure) {
		t.Fatalf("actual read cause swallowed: %v", err)
	}
	source, reader = precisionSource(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	reader.cancel = cancel
	if result, err := psdscene.RenderPrecisionTile(ctx, source, image.Rect(4, 0, 6, 1)); result != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation swallowed: %v", err)
	}
	if result, err := psdscene.RenderPrecisionTile(ctx, source, image.Rectangle{}); result != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("empty ROI hid cancellation: %v", err)
	}
	fixtures = append(fixtures, fixtures[3])
	source, _ = precisionSource(t, precisionSourceFixture(16, 3, 6, 3, fixtures))
	gap, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(2, 1, 5, 2))
	if err != nil || gap.Image.Pix[0] != 1 || gap.Image.Pix[3] != 0 {
		t.Fatalf("five-layer Source lost the global BBox alpha-zero background: %v", err)
	}
}

type geometry16CallbackReader struct {
	*bytes.Reader
	payloads []psdfile.Span
	callback func()
}

func (reader *geometry16CallbackReader) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.Reader.ReadAt(data, offset)
	for _, span := range reader.payloads {
		if reader.callback != nil && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			callback := reader.callback
			reader.callback = nil
			callback()
		}
	}
	return count, err
}

func TestPrecisionGeometry16BBoxSnapshotBeforeSourceCallback(t *testing.T) {
	fixtures := geometry16Fixtures()
	for index, bounds := range []image.Rectangle{image.Rect(1, 0, 2, 1), image.Rect(4, 0, 5, 1),
		image.Rect(2, 2, 3, 3), image.Rect(5, 2, 6, 3)} {
		fixtures[index].bounds = bounds
		for component := range fixtures[index].channels {
			fixtures[index].channels[component].data = fixtures[index].channels[component].data[:2]
		}
	}
	expected, err := psdprecision.RenderRGB16(nil, geometry16Scene(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	data := precisionSourceFixture(16, 3, 6, 3, fixtures)
	reader := &geometry16CallbackReader{Reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, record := range source.Layers {
		for _, channel := range record.Channels {
			reader.payloads = append(reader.payloads, channel.Data)
		}
	}
	reader.callback = func() { source.Layers[0].Bounds = image.Rect(0, 0, 6, 3) }
	result, err := psdscene.RenderPrecision(nil, source)
	if err != nil || reader.callback != nil {
		t.Fatalf("source callback control did not execute: %v", err)
	}
	geometry16Compare(t, result, image.Rect(0, 0, 6, 3), expected)
	if source.Layers[0].Bounds != image.Rect(0, 0, 6, 3) || result.Pix[0] != 0 {
		t.Fatal("background was recomputed from callback-mutated public metadata")
	}
}
