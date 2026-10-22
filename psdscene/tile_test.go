package psdscene_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func tileFixtureChannels(depth uint16, bounds image.Rectangle, seed int) []precisionChannel {
	var channels []precisionChannel
	for component, id := range []int16{0, 1, 2, -1} {
		var values []byte
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				value := float64((x*23+y*37+component*61+seed)%256) / 255
				if component == 3 {
					value = .25 + value*.75
				}
				switch depth {
				case 8:
					values = append(values, byte(math.Round(value*255)))
				case 16:
					values = binary.BigEndian.AppendUint16(values, uint16(math.Round(value*65535)))
				case 32:
					if component < 3 {
						value = value*3 - .75
					}
					values = binary.BigEndian.AppendUint32(values, math.Float32bits(float32(value)))
				}
			}
		}
		channels = append(channels, precisionChannel{id: id, data: values})
	}
	return channels
}

func TestPrecisionTilesMatchFullAtAbsoluteOrigins(t *testing.T) {
	for _, depth := range []uint16{16, 32} {
		bounds := image.Rect(0, 0, 8, 6)
		base := precisionLayerFixture{bounds: bounds, opacity: 221, channels: tileFixtureChannels(depth, bounds, 7)}
		clipBounds := image.Rect(2, 1, 7, 5)
		clip := precisionLayerFixture{bounds: clipBounds, opacity: 137, clipping: 1, mode: "mul ",
			channels: tileFixtureChannels(depth, clipBounds, 43)}
		topBounds := image.Rect(1, 2, 6, 6)
		density := byte(173)
		top := precisionLayerFixture{bounds: topBounds, opacity: 173, mode: "mul ",
			channels: tileFixtureChannels(depth, topBounds, 93),
			mask:     precisionMask(image.Rect(2, 2, 5, 5), 149, 4, &density, false),
			ranges:   []byte{20, 70, 200, 250, 0, 0, 255, 255}}
		var mask []byte
		for pixel := range 9 {
			if depth == 16 {
				mask = binary.BigEndian.AppendUint16(mask, uint16(6000+pixel*7000))
			} else {
				mask = append(mask, precisionFloat(float32(pixel)/8)...)
			}
		}
		top.channels = append(top.channels, precisionChannel{id: -2, data: mask})
		end := precisionLayerFixture{opacity: 255, divider: 3}
		group := precisionLayerFixture{opacity: 155, divider: 1, mode: "norm"}
		source, _ := precisionSource(t, precisionSourceFixture(depth, 3, 8, 6,
			[]precisionLayerFixture{base, clip, end, top, group}))
		full, err := psdscene.RenderPrecision(nil, source)
		if err != nil {
			t.Fatal(err)
		}
		for _, rectangle := range []image.Rectangle{
			image.Rect(0, 0, 8, 6), image.Rect(3, 2, 7, 5), image.Rect(0, 4, 3, 6), image.Rect(6, 0, 8, 3),
		} {
			tile, err := psdscene.RenderPrecisionTile(nil, source, rectangle)
			if err != nil {
				t.Fatal(err)
			}
			if tile.Bounds != rectangle || tile.Image.SourceDepth != depth || string(tile.Image.ICCProfile) != "ICC!" {
				t.Fatal("tile lost absolute placement or source provenance")
			}
			for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
				for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
					for component := range 4 {
						actual := tile.Image.Pix[((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4+component]
						expected := full.Pix[(y*8+x)*4+component]
						if actual != expected {
							t.Fatalf("depth%d tile %v (%d,%d)/%d = %.17g, full %.17g",
								depth, rectangle, x, y, component, actual, expected)
						}
					}
				}
			}
		}
	}
}

func TestRGB8TilesMatchFullWithGroupsClippingBlendIfAndFeather(t *testing.T) {
	bounds := image.Rect(0, 0, 64, 48)
	base := precisionLayerFixture{bounds: bounds, opacity: 221, channels: tileFixtureChannels(8, bounds, 7)}
	clipBounds := image.Rect(3, 2, 61, 43)
	clip := precisionLayerFixture{bounds: clipBounds, opacity: 137, clipping: 1, mode: "mul ",
		channels: tileFixtureChannels(8, clipBounds, 43)}
	topBounds := image.Rect(5, 7, 58, 44)
	maskBounds := image.Rect(10, 10, 56, 42)
	maskData := precisionMask(maskBounds, 0, 4, nil, false)
	maskData = maskData[:18]
	maskData[17] |= 16
	maskData = append(maskData, 3, 173)
	maskData = binary.BigEndian.AppendUint64(maskData, math.Float64bits(1.5))
	top := precisionLayerFixture{bounds: topBounds, opacity: 173, mode: "mul ",
		channels: tileFixtureChannels(8, topBounds, 93), mask: maskData,
		ranges: []byte{20, 70, 200, 250, 0, 0, 255, 255}}
	mask := make([]byte, maskBounds.Dx()*maskBounds.Dy())
	for pixel := range mask {
		mask[pixel] = byte((pixel*19 + 41) % 256)
	}
	top.channels = append(top.channels, precisionChannel{id: -2, data: mask})
	end := precisionLayerFixture{opacity: 255, divider: 3}
	group := precisionLayerFixture{opacity: 155, divider: 1, mode: "norm"}
	source, _ := precisionSource(t, precisionSourceFixture(8, 3, 64, 48,
		[]precisionLayerFixture{base, clip, end, top, group}))
	full, err := psdscene.Render(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := psdscene.VisitTiles(nil, source, bounds, image.Pt(7, 5), func(tile *image.NRGBA) error {
		for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
			for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
				if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
					t.Fatalf("RGB8 tile seam or feather halo at (%d,%d): tile=%v full=%v", x, y,
						tile.NRGBAAt(x, y), full.NRGBAAt(x, y))
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRGB8TileRealMaskChannelWithoutVectorAndLifecycle(t *testing.T) {
	layer := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: []precisionChannel{{0, []byte{200}}, {1, []byte{40}}, {2, []byte{60}}, {-1, []byte{255}},
			{-2, []byte{255}}, {-3, []byte{64}}},
		mask: precisionMask(image.Rect(0, 0, 1, 1), 0, 8, nil, true)}
	source, reader := precisionSource(t, precisionSourceFixture(8, 3, 1, 1, []precisionLayerFixture{layer}))
	view, err := source.ReadLayerMetadata(nil, 0)
	if err != nil || view.PixelMaskChannel != -3 {
		t.Fatalf("flags8 real user mask did not select original -3 channel: %+v, %v", view, err)
	}
	result, err := psdscene.RenderTile(nil, source, image.Rect(0, 0, 1, 1))
	if err != nil || !bytes.Equal(result.Pix, []byte{200, 40, 60, 191}) {
		t.Fatalf("derived raster mask replaced real source user mask: %v", err)
	}
	reader.pixelReads = 0
	reader.forbid = true
	_, err = psdscene.RenderTile(nil, source, image.Rect(0, 0, 1, 1),
		psdscene.TileOptions{MaxWorkingBytes: 1024})
	if !errors.Is(err, psdfile.ErrLimit) || reader.pixelReads != 0 {
		t.Fatalf("RGB8 tile exceeded work preflight before source samples: %v", err)
	}
	reader.forbid = false
	retained := bytes.Clone(result.Pix)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := psdscene.RenderTile(nil, source, image.Rect(0, 0, 1, 1)); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source tile silently used cached snapshot: %v", err)
	}
	if !bytes.Equal(retained, result.Pix) {
		t.Fatal("returned tile depends on source lifetime")
	}
}

func TestNativeSourceTilesMatchFullSupportedSemantics(t *testing.T) {
	if os.Getenv("PSDKIT_TILE_NATIVE") != "1" {
		t.Skip("retained Photoshop-native source controls are required")
	}
	for _, name := range []string{
		"p3-native/clipping-default", "p3-native/clipping-ungrouped-opacity",
		"p3-native/blend-if-source-gray-split", "p3-native/blend-if-both-blue-split",
		"p3-native/group-nested-mask", "p3-native/group-pass-through-mask-opacity",
		"p3-native/mask-density", "p3-native/mask-flag-invert",
		"vector-native/rectangle-integer", "vector-native/solid-fill-red", "vector-native/vector-user-both",
		"vector-feather-native/vector-feather-three", "vector-feather-native/vector-feather-cross-canvas",
		"gradient-native/linear-angle45", "gradient-native/radial-angle45",
		"pattern-native/rgb-grid-2x2", "pattern-native/alpha-grid-4x3",
		"next-native/feather-one", "next-native/feather-half", "next-native/feather-group",
		"effects-native/shadow-hard", "effects-native/glow-hard", "effects-native/stroke-outside",
		"inner-effects-native/shadow-angle0", "inner-effects-native/glow-edge-hard-size2",
	} {
		t.Run(name, func(t *testing.T) {
			source, err := psdfile.Open(filepath.Join("..", "output", name+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			full, err := psdscene.Render(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			if err := psdscene.VisitTiles(nil, source, full.Rect, image.Pt(7, 5), func(tile *image.NRGBA) error {
				for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
					for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
						if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
							t.Fatalf("native source regional semantics differ at (%d,%d): tile=%v full=%v", x, y,
								tile.NRGBAAt(x, y), full.NRGBAAt(x, y))
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativePrecisionSourceTilesMatchFullSupportedSemantics(t *testing.T) {
	if os.Getenv("PSDKIT_TILE_NATIVE") != "1" {
		t.Skip("retained Photoshop-native precision sources are required")
	}
	for _, name := range []string{
		"p6-precision-native/rgb16-group-mask", "p6-precision-native/rgb32-group-mask",
		"p6-precision-blends-native/rgb16-multiply", "p6-precision-blends-native/rgb32-multiply",
		"p6-precision-blends-native/rgb32-pass-opacity-mask",
		"p6-clipping-native/rgb16-clip-mask-multi", "p6-clipping-native/rgb32-clip-mask-multi",
	} {
		t.Run(name, func(t *testing.T) {
			source, err := psdfile.Open(filepath.Join("..", "output", name+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			full, err := psdscene.RenderPrecision(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			canvas := image.Rect(0, 0, full.Width, full.Height)
			if err := psdscene.VisitPrecisionTiles(nil, source, canvas, image.Pt(2, 1),
				func(tile *psdprecision.RegionImage) error {
					for y := tile.Bounds.Min.Y; y < tile.Bounds.Max.Y; y++ {
						for x := tile.Bounds.Min.X; x < tile.Bounds.Max.X; x++ {
							for component := range 4 {
								actual := tile.Image.Pix[((y-tile.Bounds.Min.Y)*tile.Bounds.Dx()+x-tile.Bounds.Min.X)*4+component]
								expected := full.Pix[(y*full.Width+x)*4+component]
								if actual != expected {
									t.Fatalf("native precision source tile differs at (%d,%d)/%d: %.17g vs %.17g",
										x, y, component, actual, expected)
								}
							}
						}
					}
					return nil
				}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeLivePlacedTilesMatchFullWithoutParentCache(t *testing.T) {
	if os.Getenv("PSDKIT_TILE_NATIVE") != "1" {
		t.Skip("retained Photoshop-native placed source controls are required")
	}
	for _, name := range []string{
		"linked-native/embedded-control", "raster-embedded-native/png-alpha", "raster-embedded-native/png-opaque",
		"raster-embedded-native/jpeg-444", "raster-embedded-native/jpeg-420",
		"warp-native/identity", "warp-native/bent", "warp-native/scale2",
		"smart-filter-native/opaque-r2", "smart-filter-native/alpha-r3", "smart-filter-native/opaque-chain",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "output", name+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			source, reader := precisionSource(t, data)
			full, err := psdscene.Render(nil, source, psdscene.Options{AssumeUntaggedSRGB: true,
				SmartFilters: &psdfilter.Options{ApproximateGaussian: true}})
			if err != nil {
				t.Fatal(err)
			}
			// Only placed-layer channel spans are forbidden; ordinary source layers remain live inputs.
			reader.pixelSpans = nil
			for index, record := range source.Layers {
				view, err := source.ReadLayerMetadata(nil, index)
				if err != nil {
					t.Fatal(err)
				}
				if view.Layer.Placed != nil {
					for _, channel := range record.Channels {
						if channel.ID >= -1 {
							reader.pixelSpans = append(reader.pixelSpans, channel.Data)
						}
					}
				}
			}
			if len(reader.pixelSpans) == 0 {
				t.Fatal("native fixture has no placed layer saved channels to guard")
			}
			reader.pixelReads = 0
			reader.forbid = true
			if err := psdscene.VisitTiles(nil, source, full.Rect, image.Pt(7, 5), func(tile *image.NRGBA) error {
				for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
					for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
						if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
							t.Fatalf("live placed tile differs at (%d,%d): tile=%v full=%v", x, y,
								tile.NRGBAAt(x, y), full.NRGBAAt(x, y))
						}
					}
				}
				return nil
			}, psdscene.TileOptions{AssumeUntaggedSRGB: true,
				SmartFilters: &psdfilter.Options{ApproximateGaussian: true}}); err != nil {
				t.Fatal(err)
			}
			if reader.pixelReads != 0 {
				t.Fatal("live placed ROI reused saved parent placed-layer channels")
			}
		})
	}
}

type tileSparseSegment struct {
	offset int64
	data   []byte
}

type tileSparseReader struct {
	prefix       []byte
	segments     []tileSparseSegment
	planes       []psdfile.Span
	samples      [][]byte
	footer       psdfile.Span
	length       int64
	pixelReads   int
	pixelBytes   int
	forbidPixels bool
}

func (reader *tileSparseReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 || offset >= reader.length {
		return 0, io.EOF
	}
	count := min(len(data), int(reader.length-offset))
	for index := range count {
		position := offset + int64(index)
		data[index] = 0
		if position < int64(len(reader.prefix)) {
			data[index] = reader.prefix[position]
		}
		for _, segment := range reader.segments {
			if position >= segment.offset && position < segment.offset+int64(len(segment.data)) {
				data[index] = segment.data[position-segment.offset]
			}
		}
		if position >= reader.footer.Offset && position < reader.footer.Offset+reader.footer.Length {
			return 0, errors.New("merged footer samples must never supply a source tile")
		}
		for channel, span := range reader.planes {
			if position >= span.Offset && position < span.Offset+span.Length {
				if reader.forbidPixels {
					return 0, errors.New("samples accessed before tile preflight")
				}
				data[index] = reader.samples[channel][(position-span.Offset)%int64(len(reader.samples[channel]))]
			}
		}
	}
	for _, span := range reader.planes {
		if offset < span.Offset+span.Length && offset+int64(count) > span.Offset {
			reader.pixelReads++
			reader.pixelBytes += count
		}
	}
	if count < len(data) {
		return count, io.EOF
	}
	return count, nil
}

func tileSparseSource(t *testing.T, depth uint16) (*psdfile.Document, *tileSparseReader) {
	t.Helper()
	const dimension = 300_000
	planeLength := int64(dimension) * dimension * int64(depth/8)
	var record bytes.Buffer
	precisionNumber(&record, int16(1))
	for _, coordinate := range []int32{0, 0, dimension, dimension} {
		precisionNumber(&record, coordinate)
	}
	precisionNumber(&record, uint16(3))
	for channel := range 3 {
		precisionNumber(&record, int16(channel))
		precisionNumber(&record, uint64(planeLength+2))
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	precisionNumber(&record, uint32(12))
	record.Write(make([]byte, 8))
	record.Write([]byte{1, 'L', 0, 0})
	layerLength := int64(record.Len()) + 3*(planeLength+2)
	var prefix bytes.Buffer
	prefix.WriteString("8BPS")
	precisionNumber(&prefix, uint16(2))
	prefix.Write(make([]byte, 6))
	for _, value := range []any{uint16(3), uint32(dimension), uint32(dimension), depth, uint16(3),
		uint32(0), uint32(0), uint64(8 + layerLength + 4), uint64(layerLength)} {
		precisionNumber(&prefix, value)
	}
	prefix.Write(record.Bytes())
	reader := &tileSparseReader{prefix: prefix.Bytes()}
	for channel := range 3 {
		compressionOffset := int64(prefix.Len()) + int64(channel)*(planeLength+2)
		reader.segments = append(reader.segments, tileSparseSegment{compressionOffset, []byte{0, 0}})
		reader.planes = append(reader.planes, psdfile.Span{Offset: compressionOffset + 2, Length: planeLength})
		sample := []byte{byte(71 + channel*53)}
		switch depth {
		case 16:
			sample = binary.BigEndian.AppendUint16(nil, uint16(12345+channel*12345))
		case 32:
			sample = precisionFloat([]float32{2.125, -.625, .375}[channel])
		}
		reader.samples = append(reader.samples, sample)
	}
	footerOffset := int64(prefix.Len()) + 3*(planeLength+2) + 4
	reader.footer = psdfile.Span{Offset: footerOffset + 2, Length: planeLength * 3}
	reader.length = reader.footer.Offset + reader.footer.Length
	source, err := psdfile.Read(reader, reader.length, psdfile.LoadOptions{Limits: psdfile.Limits{
		MaxFileBytes: reader.length, MaxDecodePixels: 4, MaxDecodeBytes: 256 << 10,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source, reader
}

func TestTilesReconstructSparseHugePSBWithoutMergedFooter(t *testing.T) {
	for _, depth := range []uint16{8, 16, 32} {
		source, reader := tileSparseSource(t, depth)
		if reader.pixelReads != 0 {
			t.Fatal("indexing read sparse pixel samples")
		}
		if _, err := source.DecodeChannel(nil, 0, 0); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("depth%d full giant plane did not exceed source limits: %v", depth, err)
		}
		rectangle := image.Rect(234_567, 299_999, 234_569, 300_000)
		if depth == 8 {
			result, err := psdscene.RenderTile(nil, source, rectangle)
			if err != nil || result.Rect != rectangle || !bytes.Equal(result.Pix, []byte{71, 124, 177, 255, 71, 124, 177, 255}) {
				t.Fatalf("huge RGB8 PSB source reconstruction differs: %v", err)
			}
		} else {
			result, err := psdscene.RenderPrecisionTile(nil, source, rectangle)
			if err != nil || result.Bounds != rectangle || result.Image.SourceDepth != depth {
				t.Fatalf("huge precise PSB tile metadata differs: %v", err)
			}
			for pixel := range 2 {
				for channel := range 3 {
					expected := float64(12345+channel*12345) / 65535
					if depth == 16 {
						expected = float64((12345+channel*12345+1)/2) / 32768
					}
					if depth == 32 {
						expected = []float64{2.125, -.625, .375}[channel]
					}
					if result.Image.Pix[pixel*4+channel] != expected {
						t.Fatal("huge tile changed original precision or HDR values")
					}
				}
			}
		}
		if reader.pixelReads != 3 || reader.pixelBytes != 6*int(depth/8) {
			t.Fatalf("tile read samples outside its three tiny channel windows: reads=%d bytes=%d",
				reader.pixelReads, reader.pixelBytes)
		}
	}
}

func TestTileVisitorBudgetsTraversalAndStopsOnCallbackError(t *testing.T) {
	source, reader := tileSparseSource(t, 32)
	window := image.Rect(100_000, 200_000, 100_006, 200_004)
	callbacks := 0
	err := psdscene.VisitPrecisionTiles(nil, source, window, image.Pt(2, 2),
		func(tile *psdprecision.RegionImage) error {
			callbacks++
			if tile.Bounds.Dx() != 2 || tile.Bounds.Dy() != 2 || !tile.Bounds.In(window) {
				t.Fatal("visitor lost document coordinates")
			}
			return nil
		})
	if err != nil || callbacks != 6 {
		t.Fatalf("large traversal incorrectly charged MaxPixels to complete window: %v count=%d", err, callbacks)
	}
	stop := errors.New("stop tile visitation")
	callbacks = 0
	err = psdscene.VisitPrecisionTiles(nil, source, window, image.Pt(2, 2),
		func(*psdprecision.RegionImage) error { callbacks++; return stop })
	if !errors.Is(err, stop) || callbacks != 1 {
		t.Fatal("callback cancellation did not stop region work")
	}
	reader.forbidPixels = true
	_, err = psdscene.RenderPrecisionTile(nil, source, image.Rect(0, 0, 2, 2),
		psdscene.TileOptions{MaxWorkingBytes: 1024})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("sample access happened before whole tile working preflight: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdscene.RenderPrecisionTile(ctx, source, image.Rect(0, 0, 2, 2)); !errors.Is(err, context.Canceled) {
		t.Fatal("tile ignored pre-canceled context")
	}
}

func TestConcurrentPrecisionTilesShareOnlyImmutableSource(t *testing.T) {
	bounds := image.Rect(0, 0, 16, 12)
	layer := precisionLayerFixture{bounds: bounds, opacity: 173, channels: tileFixtureChannels(32, bounds, 71)}
	data := precisionSourceFixture(32, 3, 16, 12, []precisionLayerFixture{layer})
	original := bytes.Clone(data)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	full, err := psdscene.RenderPrecision(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			rectangle := image.Rect(worker*3, worker*2, worker*3+4, worker*2+3)
			for range 3 {
				tile, err := psdscene.RenderPrecisionTile(nil, source, rectangle)
				if err != nil {
					t.Error(err)
					return
				}
				actual := tile.Image.Pix[0]
				expected := full.Pix[(rectangle.Min.Y*16+rectangle.Min.X)*4]
				if actual != expected {
					t.Errorf("concurrent tile samples changed: %.17g vs %.17g", actual, expected)
				}
				tile.Image.Pix[0], tile.Image.ICCProfile[0] = 0, 0
			}
		})
	}
	workers.Wait()
	if !bytes.Equal(data, original) {
		t.Fatal("concurrent owned tile mutation modified original PSD bytes")
	}
}

func TestNativeTileOutputProfileKeepsExplicitUntaggedPolicy(t *testing.T) {
	if os.Getenv("PSDKIT_TILE_NATIVE") != "1" {
		t.Skip("retained native RGB ICC source is required")
	}
	profileSource, err := psdfile.Open(filepath.Join("..", "output", "p6-native", "rgb8-srgb.psd"))
	if err != nil {
		t.Fatal(err)
	}
	defer profileSource.Close()
	profile, err := psdscene.ReadSourceRGBProfile(nil, profileSource, 0)
	if err != nil || len(profile) == 0 {
		t.Fatalf("native target ICC source is absent: %v", err)
	}
	layer := precisionLayerFixture{bounds: image.Rect(0, 0, 1, 1), opacity: 255,
		channels: []precisionChannel{{0, []byte{123}}, {1, []byte{45}}, {2, []byte{67}}, {-1, []byte{255}}}}
	data := precisionSourceFixture(8, 3, 1, 1, []precisionLayerFixture{layer})
	resourceLength := int(binary.BigEndian.Uint32(data[30:34]))
	data = append(data[:34], data[34+resourceLength:]...)
	binary.BigEndian.PutUint32(data[30:34], 0)
	source, _ := precisionSource(t, data)
	if _, err := psdscene.RenderTile(nil, source, image.Rect(0, 0, 1, 1),
		psdscene.TileOptions{OutputProfile: profile}); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("untagged tile output silently assumed a working profile: %v", err)
	}
	full, err := psdscene.Render(nil, source, psdscene.Options{OutputProfile: profile, AssumeUntaggedSRGB: true})
	if err != nil {
		t.Fatal(err)
	}
	tile, err := psdscene.RenderTile(nil, source, image.Rect(0, 0, 1, 1),
		psdscene.TileOptions{OutputProfile: profile, AssumeUntaggedSRGB: true})
	if err != nil || !bytes.Equal(tile.Pix, full.Pix) {
		t.Fatalf("explicit tile target profile differs from Scene policy: %v", err)
	}
}
