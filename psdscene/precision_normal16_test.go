package psdscene_test

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestPrecisionNormal16PartialOutsideROIUsesOneDomain(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	backdrop := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{0, 32768})}
	top := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{65535, 65535}, [2]uint16{0, 0})}
	source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{backdrop, top}))
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil || full.Pix[0] != .5 || full.Pix[3] != 0 || full.Pix[4] != .5 || full.Pix[7] != .5 {
		t.Fatalf("Normal partial/transparent bottom = %v, %v", full, err)
	}
	tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(0, 0, 1, 1))
	if err != nil || tile.Image.Pix[0] != full.Pix[0] || tile.Image.Pix[3] != full.Pix[3] {
		t.Fatalf("Normal source domain depends on ROI: %v, %v", tile, err)
	}
}

func TestPrecisionNormal16PartialBoundsUseGlobalBackground(t *testing.T) {
	bottomBounds, canvas := image.Rect(1, 0, 2, 1), image.Rect(0, 0, 2, 1)
	bottom := precisionLayerFixture{bounds: bottomBounds, opacity: 255,
		channels: []precisionChannel{{id: 0, data: make([]byte, 2)}, {id: 1, data: make([]byte, 2)},
			{id: 2, data: make([]byte, 2)}, {id: -1, data: make([]byte, 2)}}}
	for component := range 4 {
		code := uint16(32767)
		if component == 3 {
			code = 32768
		}
		binary.BigEndian.PutUint16(bottom.channels[component].data, code)
	}
	top := precisionLayerFixture{bounds: canvas, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{0, 0}, [2]uint16{0, 0})}
	source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{bottom, top}))
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil || full.Pix[0] != 1 || full.Pix[3] != 0 || full.Pix[4] != .5 || full.Pix[7] != .5 {
		t.Fatalf("partial-bounds source lost native global background: %v, %v", full, err)
	}
	tile, err := psdscene.RenderPrecisionTile(nil, source, bottomBounds)
	if err != nil || tile.Image.Pix[0] != full.Pix[4] {
		t.Fatalf("ROI covering partial bottom changed its source domain: %v, %v", tile, err)
	}
	top.mode = "mul "
	source, _ = precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{bottom, top}))
	full, err = psdscene.RenderPrecision(nil, source)
	if err != nil || full.Pix[4] != float64(32767)/65535 {
		t.Fatalf("non-Normal partial backdrop no longer remains float: %v, %v", full, err)
	}
}

func partialNormal16Fixtures() []precisionLayerFixture {
	return []precisionLayerFixture{
		{bounds: image.Rect(0, 0, 1, 1), opacity: 255, flags: 2, mode: "diss",
			channels: precisionRGBA(16, [4]float64{0, 0, 0, 65535})},
		{bounds: image.Rect(1, 0, 2, 1), opacity: 255,
			channels: precisionRGBA(16, [4]float64{32767, 32767, 32767, 0})},
		{bounds: image.Rect(4, 0, 5, 1), opacity: 255,
			channels: precisionRGBA(16, [4]float64{0, 0, 0, 0})},
		{bounds: image.Rect(5, 0, 6, 1), opacity: 0, mode: "diss",
			channels: precisionRGBA(16, [4]float64{0, 0, 0, 65535})},
	}
}

func TestPrecisionNormal16GlobalBBoxFirstOutsideROIAndSkippedReads(t *testing.T) {
	data := precisionSourceFixture(16, 3, 6, 1, partialNormal16Fixtures())
	source, reader := precisionSource(t, data)
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	for x, expected := range []float64{0, .5, 1, 1, 1, 0} {
		if full.Pix[x*4] != expected || full.Pix[x*4+3] != 0 {
			t.Fatalf("global partial bbox pixel%d = %v", x, full.Pix[x*4:x*4+4])
		}
	}
	if reader.pixelReads != 8 {
		t.Fatalf("hidden/OP0 layers or opaque qualification were decoded: reads%d, want8", reader.pixelReads)
	}
	reader.pixelReads = 0
	tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(2, 0, 5, 1))
	if err != nil {
		t.Fatal(err)
	}
	for component, value := range tile.Image.Pix {
		if value != full.Pix[8+component] {
			t.Fatalf("first-outside-ROI changed component%d", component)
		}
	}
	if reader.pixelReads != 4 {
		t.Fatalf("ROI read the missing first or skipped layers: reads%d, want4", reader.pixelReads)
	}
	fixtures := partialNormal16Fixtures()
	fixtures[2].mode = "mul "
	source, _ = precisionSource(t, precisionSourceFixture(16, 3, 6, 1, fixtures))
	tile, err = psdscene.RenderPrecisionTile(nil, source, image.Rect(2, 0, 3, 1))
	if err != nil || tile.Image.Pix[0] != 0 {
		t.Fatalf("active non-Normal outside ROI locally upgraded: %v, %v", tile, err)
	}
}

func TestPrecisionNormal16PartialBoundsBudgetAndReaderCauses(t *testing.T) {
	fixture := precisionLayerFixture{bounds: image.Rect(1, 0, 2, 1), opacity: 255,
		channels: precisionRGBA(16, [4]float64{32767, 32767, 32767, 0})}
	data := precisionSourceFixture(16, 3, 6, 1, []precisionLayerFixture{fixture})
	for _, tiled := range []bool{false, true} {
		source, reader := precisionSource(t, data)
		reader.forbid = true
		var err error
		if tiled {
			_, err = psdscene.RenderPrecisionTile(nil, source, image.Rect(0, 0, 6, 1),
				psdscene.TileOptions{MaxWorkingBytes: 1100})
		} else {
			_, err = psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{MaxWorkingBytes: 1100})
		}
		if !errors.Is(err, psdfile.ErrLimit) || reader.pixelReads != 0 {
			t.Fatalf("tile%t partial bbox budget reads%d, %v", tiled, reader.pixelReads, err)
		}
		source, reader = precisionSource(t, data)
		reader.failure = psdfile.ErrLimit
		if tiled {
			_, err = psdscene.RenderPrecisionTile(nil, source, image.Rect(1, 0, 2, 1))
		} else {
			_, err = psdscene.RenderPrecision(nil, source)
		}
		var detail *psdfile.Error
		if !errors.Is(err, psdfile.ErrLimit) || !errors.As(err, &detail) || !errors.Is(detail.Cause, psdfile.ErrLimit) {
			t.Fatalf("tile%t ReaderAt cause was swallowed or fell back: %v", tiled, err)
		}
		source, reader = precisionSource(t, data)
		ctx, cancel := context.WithCancel(context.Background())
		reader.cancel = cancel
		if tiled {
			_, err = psdscene.RenderPrecisionTile(ctx, source, image.Rect(1, 0, 2, 1))
		} else {
			_, err = psdscene.RenderPrecision(ctx, source)
		}
		cancel()
		if !errors.Is(err, context.Canceled) || reader.pixelReads != 1 {
			t.Fatalf("tile%t cancellation during first source read: reads%d, %v", tiled, reader.pixelReads, err)
		}
	}
}

func TestPrecisionNormal16EmptyFirstUsesFloatInEveryROI(t *testing.T) {
	empty := precisionLayerFixture{bounds: image.Rectangle{}, opacity: 255,
		channels: []precisionChannel{{id: 0}, {id: 1}, {id: 2}, {id: -1}}}
	for _, fullCoverage := range []bool{false, true} {
		top := precisionLayerFixture{bounds: image.Rect(1, 0, 2, 1), opacity: 255,
			channels: precisionRGBA(16, [4]float64{32767, 32767, 32767, 65535})}
		if fullCoverage {
			top.bounds = image.Rect(0, 0, 2, 1)
			top.channels = integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{65535, 65535})
		}
		source, reader := precisionSource(t, precisionSourceFixture(16, 3, 2, 1,
			[]precisionLayerFixture{empty, top}))
		full, err := psdscene.RenderPrecision(nil, source)
		if err != nil || full.Pix[4] != float64(32767)/65535 || reader.pixelReads != 4 {
			t.Fatalf("cover%t empty-first source upgraded: %v, reads%d, %v",
				fullCoverage, full, reader.pixelReads, err)
		}
		reader.pixelReads = 0
		tile, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(1, 0, 2, 1))
		if err != nil || tile.Image.Pix[0] != full.Pix[4] || reader.pixelReads != 4 {
			t.Fatalf("cover%t ROI dropped global empty first: %v, reads%d, %v",
				fullCoverage, tile, reader.pixelReads, err)
		}
	}
}

func TestPrecisionNormal16BudgetPrecedesSourceReads(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	layer := precisionLayerFixture{bounds: bounds, opacity: 255,
		channels: integer16TwoPixelChannels([2]uint16{32767, 32767}, [2]uint16{0, 32768})}
	for _, tiled := range []bool{false, true} {
		source, reader := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{layer}))
		reader.forbid = true
		var err error
		if tiled {
			_, err = psdscene.RenderPrecisionTile(nil, source, bounds, psdscene.TileOptions{MaxWorkingBytes: 1100})
		} else {
			_, err = psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{MaxWorkingBytes: 1100})
		}
		if !errors.Is(err, psdfile.ErrLimit) || reader.pixelReads != 0 {
			t.Fatalf("tile%t Normal preflight read source before budget: reads%d, %v", tiled, reader.pixelReads, err)
		}
	}
}
