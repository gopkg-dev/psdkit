package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type openTypeSceneReader struct {
	reader       io.ReaderAt
	forbidden    []psdfile.Span
	forbiddenHit int
	reads        int
	hook         func()
}

func (reader *openTypeSceneReader) ReadAt(data []byte, at int64) (int, error) {
	reader.reads++
	if reader.hook != nil {
		reader.hook()
	}
	for _, span := range reader.forbidden {
		if len(data) != 0 && at < span.Offset+span.Length && at+int64(len(data)) > span.Offset {
			reader.forbiddenHit++
			return 0, errors.New("OpenType scene read saved artwork")
		}
	}
	return reader.reader.ReadAt(data, at)
}

func openTypeSceneSource(t *testing.T, fixture []byte, denyLayers []bool) (*psdfile.Document, *openTypeSceneReader) {
	t.Helper()
	reader := &openTypeSceneReader{reader: bytes.NewReader(fixture)}
	source, err := psdfile.Read(reader, int64(len(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	for index, layer := range source.Layers {
		if denyLayers != nil && !denyLayers[index] {
			continue
		}
		for _, channel := range layer.Channels {
			if channel.ID >= -1 {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
	}
	reader.reads = 0
	return source, reader
}

func openTypeSceneWrongSavedBounds(t *testing.T, fixture []byte) []byte {
	t.Helper()
	source, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	owned := bytes.Clone(fixture[record.Span.Offset : record.Span.Offset+record.Span.Length])
	for index, value := range []uint32{47, 99, 49, 101} {
		binary.BigEndian.PutUint32(owned[index*4:], value)
	}
	for index := range record.Channels {
		binary.BigEndian.PutUint32(owned[20+index*6:], 6)
	}
	layerInfo := binary.BigEndian.AppendUint16(nil, 1)
	layerInfo = append(layerInfo, owned...)
	for _, value := range []byte{200, 30, 220, 255} {
		layerInfo = append(layerInfo, 0, 0, value, value, value, value)
	}
	if len(layerInfo)%2 != 0 {
		layerInfo = append(layerInfo, 0)
	}
	layerMask := binary.BigEndian.AppendUint32(nil, uint32(len(layerInfo)))
	layerMask = append(layerMask, layerInfo...)
	layerMask = append(layerMask,
		fixture[source.GlobalMask.Offset:source.LayerMaskSection.Offset+source.LayerMaskSection.Length]...)
	result := bytes.Clone(fixture[:source.LayerMaskSection.Offset])
	result = binary.BigEndian.AppendUint32(result, uint32(len(layerMask)))
	result = append(result, layerMask...)
	result = append(result, fixture[source.Composite.Span.Offset:]...)
	return result
}

func openTypeSceneEqualWindow(t *testing.T, actual, expected *image.NRGBA, window image.Rectangle) {
	t.Helper()
	if actual == nil || actual.Rect != window {
		t.Fatalf("unexpected output rectangle: %v expected %v", actual, window)
	}
	for y := window.Min.Y; y < window.Max.Y; y++ {
		for x := window.Min.X; x < window.Max.X; x++ {
			if actual.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
				t.Fatalf("regional pixel (%d,%d) actual %v expected %v", x, y,
					actual.NRGBAAt(x, y), expected.NRGBAAt(x, y))
			}
		}
	}
}

func TestOpenTypeSceneIndependentFullAndROIWithStaleSavedBounds(t *testing.T) {
	canvas := image.Rect(0, 0, 128, 96)
	for _, name := range []string{"layout_oracle.json", "layout_geometry_oracle.json"} {
		data, err := os.ReadFile(testfixture.Path(t, filepath.Join("psdfont", "testdata", "opentype", name)))
		if err != nil {
			t.Fatal(err)
		}
		var oracle textSourceOracle
		if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		faces := make(map[string]*psdfont.Face)
		for _, font := range oracle.Fonts {
			face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", font.File))
			if err != nil {
				t.Fatal(err)
			}
			faces[font.File] = face
		}
		for _, row := range oracle.Cases {
			if row.Config.ID != "13.75-zero" {
				continue
			}
			t.Run(name+"/"+row.ID, func(t *testing.T) {
				face := faces[row.Font]
				fixture := openTypeSceneWrongSavedBounds(t,
					openTypeTestPSD(t, row.Text, face.PostScriptName, row.Config.Size, true))
				before := sha256.Sum256(fixture)
				source, reader := openTypeSceneSource(t, fixture, nil)
				resolver := psdfont.ResolverFunc(func(_ context.Context, fontName string) (*psdfont.Face, error) {
					if fontName != face.PostScriptName {
						return nil, psdfont.ErrMissingFont
					}
					return face, nil
				})
				mode := OpenTypeTextNone
				for _, feature := range row.Features {
					if feature.Tag == "kern" && feature.Enabled {
						mode = OpenTypeTextMetrics
					}
				}
				settings := OpenTypeTextRenderOptions{Kerning: mode,
					Shape: psdfont.ShapeOptions{Script: row.Script, Language: row.Language, Features: row.Features}}
				actual, err := Render(nil, source, Options{ApproximateOpenTypeText: &settings, FontResolver: resolver,
					CachedSpecialLayers: true})
				if row.ExpectedError != "" {
					kind := map[string]error{"unsupported": psdfont.ErrUnsupported,
						"missing_glyph": psdfont.ErrMissingGlyph}[row.ExpectedError]
					if actual != nil || !errors.Is(err, kind) || reader.forbiddenHit != 0 {
						t.Fatalf("scene failed closed incorrectly: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				expectedRun := &psdfont.Run{Scale: 1, Approximate: true}
				ops := map[string]psdfont.PathOp{"Move": psdfont.Move, "Line": psdfont.Line,
					"Quad": psdfont.Quad, "Close": psdfont.Close}
				for _, item := range row.Glyphs {
					glyph := &psdfont.Glyph{}
					for _, point := range item.Path {
						command := psdfont.Command{Op: ops[point.Op]}
						if command.Op != psdfont.Close {
							command.X, command.Y = point.X+20, -(point.Y + 50)
						}
						if command.Op == psdfont.Quad {
							command.ControlX, command.ControlY = point.CX+20, -(point.CY + 50)
						}
						glyph.Path = append(glyph.Path, command)
					}
					expectedRun.Glyphs = append(expectedRun.Glyphs, psdfont.PositionedGlyph{Glyph: glyph})
				}
				coverage, err := psdfont.RasterizeRun(nil, expectedRun, canvas)
				if err != nil {
					t.Fatal(err)
				}
				artwork := image.NewNRGBA(canvas)
				for index, alpha := range coverage.Pix {
					at := index * 4
					artwork.Pix[at], artwork.Pix[at+1], artwork.Pix[at+2], artwork.Pix[at+3] = 102, 51, 204, alpha
				}
				expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: 128, Height: 96,
					Layers: []*psdmodel.Layer{{Bounds: canvas, Pixels: artwork, BlendMode: "normal"}}})
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, actual, expected, canvas)
				tileSettings := TileOptions{ApproximateOpenTypeText: &settings, FontResolver: resolver,
					CachedSpecialLayers: true}
				for _, window := range []image.Rectangle{image.Rect(13, 34, 63, 57), image.Rect(31, 42, 91, 69)} {
					region, err := RenderTile(nil, source, window, tileSettings)
					if err != nil {
						t.Fatal(err)
					}
					openTypeSceneEqualWindow(t, region, expected, window)
				}
				if reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
					t.Fatal("source artwork was read or source bytes changed")
				}
			})
		}
	}
}

func TestOpenTypeSceneCallbackOptionsVisitorAndBudget(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := openTypeTestPSD(t, "ffi AVfi", face.PostScriptName, 13.75, true)
	canvas := image.Rect(0, 0, 128, 96)
	baselineSource, _ := openTypeSceneSource(t, fixture, nil)
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	settings := OpenTypeTextRenderOptions{Kerning: OpenTypeTextMetrics,
		Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
	expected, err := Render(nil, baselineSource, Options{ApproximateOpenTypeText: &settings, FontResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"scene", "tile", "visitor"} {
		t.Run(operation, func(t *testing.T) {
			source, reader := openTypeSceneSource(t, fixture, nil)
			bounds := canvas
			policy := OpenTypeTextRenderOptions{Bounds: &bounds, Kerning: OpenTypeTextMetrics,
				Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
			mutate := func() {
				policy.Shape.Features[0].Enabled = false
				bounds.Max = image.Pt(1, 1)
			}
			reader.hook = mutate
			ownedResolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
				mutate()
				return face, nil
			})
			tileOptions := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: ownedResolver}
			switch operation {
			case "scene":
				actual, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: ownedResolver})
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, actual, expected, canvas)
			case "tile":
				window := image.Rect(9, 32, 73, 69)
				actual, err := RenderTile(nil, source, window, tileOptions)
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, actual, expected, window)
			case "visitor":
				visited := 0
				err := VisitTiles(nil, source, canvas, image.Pt(37, 29), func(pixels *image.NRGBA) error {
					mutate()
					openTypeSceneEqualWindow(t, pixels, expected, pixels.Rect)
					visited++
					return nil
				}, tileOptions)
				if err != nil || visited != 16 {
					t.Fatalf("visitor: count=%d err=%v", visited, err)
				}
			}
			if reader.reads == 0 || reader.forbiddenHit != 0 {
				t.Fatal("callback ownership control did not exercise metadata reads or read saved artwork")
			}
		})
	}
	for _, budget := range []int64{1, 16384, 60000, 500000} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			source, reader := openTypeSceneSource(t, fixture, nil)
			calls := 0
			limited := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
				calls++
				return face, nil
			})
			result, err := Render(nil, source, Options{MaxWorkingBytes: budget,
				ApproximateOpenTypeText: &settings, FontResolver: limited, CachedSpecialLayers: true})
			if result != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || reader.forbiddenHit != 0 {
				t.Fatalf("budget was not checked before glyph resolution: calls=%d err=%v", calls, err)
			}
		})
	}
	source, reader := openTypeSceneSource(t, fixture, nil)
	priorReads := reader.reads
	if result, err := Render(nil, source, Options{ApproximateText: &TextRenderOptions{},
		ApproximateOpenTypeText: &settings, FontResolver: resolver}); result != nil ||
		!errors.Is(err, psdfile.ErrMalformed) || reader.reads != priorReads {
		t.Fatalf("mutually exclusive text policies reached source callbacks: %v", err)
	}
	missing, err := Render(nil, source, Options{ApproximateOpenTypeText: &settings, CachedSpecialLayers: true})
	if missing != nil || !errors.Is(err, psdfont.ErrMissingFont) || reader.forbiddenHit != 0 {
		t.Fatalf("missing font fell back to saved pixels: %v", err)
	}
	counter := &textSourceCountContext{}
	result, err := RenderTile(counter, source, canvas, TileOptions{ApproximateOpenTypeText: &settings,
		FontResolver: resolver})
	if result == nil || err != nil {
		t.Fatal(err)
	}
	late := &textSourceCountContext{cancelAt: counter.calls.Load()}
	result, err = RenderTile(late, source, canvas, TileOptions{ApproximateOpenTypeText: &settings,
		FontResolver: resolver})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("last tile cancellation returned partial output")
	}
}

func openTypeSceneCombine(t *testing.T, inputs ...[]byte) []byte {
	t.Helper()
	var prefix, composite []byte
	records, channels := []byte{}, []byte{}
	for index, input := range inputs {
		source, err := psdfile.Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		if len(source.Layers) != 1 {
			t.Fatal("combine fixture requires one physical leaf per input")
		}
		if index == 0 {
			prefix = bytes.Clone(input[:source.LayerMaskSection.Offset])
			composite = bytes.Clone(input[source.Composite.Span.Offset:])
		}
		record := source.Layers[0]
		records = append(records, input[record.Span.Offset:record.Span.Offset+record.Span.Length]...)
		for _, channel := range record.Channels {
			channels = append(channels, input[channel.Span.Offset:channel.Span.Offset+channel.Span.Length]...)
		}
		_ = source.Close()
	}
	layerInfo := binary.BigEndian.AppendUint16(nil, uint16(len(inputs)))
	layerInfo = append(layerInfo, records...)
	layerInfo = append(layerInfo, channels...)
	if len(layerInfo)%2 != 0 {
		layerInfo = append(layerInfo, 0)
	}
	layerMask := binary.BigEndian.AppendUint32(nil, uint32(len(layerInfo)))
	layerMask = append(layerMask, layerInfo...)
	layerMask = append(layerMask, 0, 0, 0, 0)
	result := binary.BigEndian.AppendUint32(prefix, uint32(len(layerMask)))
	result = append(result, layerMask...)
	return append(result, composite...)
}

func openTypeSceneEffectsFixture(t *testing.T, input []byte, size float64) []byte {
	t.Helper()
	unit := func(name string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: name, Width: 64, Number: value, Bits: math.Float64bits(value)}
	}
	color := &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
		{Key: "Rd  ", Type: "doub", Value: float64(20)}, {Key: "Grn ", Type: "doub", Value: float64(90)},
		{Key: "Bl  ", Type: "doub", Value: float64(220)}}}
	shadow := &psdfile.Descriptor{ClassID: "DrSh", Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true}, {Key: "Clr ", Type: "Objc", Value: color},
		{Key: "Opct", Type: "UntF", Value: unit("#Prc", 70)}, {Key: "uglg", Type: "bool", Value: false},
		{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 7)}, {Key: "lagl", Type: "UntF", Value: unit("#Ang", 90)},
		{Key: "blur", Type: "UntF", Value: unit("#Pxl", size)}}}
	descriptor := &psdfile.Descriptor{Version: 16, ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true}, {Key: "DrSh", Type: "Objc", Value: shadow}}}
	payload, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = append([]byte{0, 0, 0, 0}, payload...)
	tag := binary.BigEndian.AppendUint32([]byte("8BIMlfx2"), uint32(len(payload)))
	tag = append(tag, payload...)
	if len(payload)%2 != 0 {
		tag = append(tag, 0)
	}
	source, err := psdfile.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	recordBytes := bytes.Clone(input[record.Span.Offset : record.Span.Offset+record.Span.Length])
	lengthAt := record.Extra.Offset - record.Span.Offset - 4
	binary.BigEndian.PutUint32(recordBytes[lengthAt:], uint32(record.Extra.Length)+uint32(len(tag)))
	recordBytes = append(recordBytes, tag...)
	layerInfo := binary.BigEndian.AppendUint16(nil, 1)
	layerInfo = append(layerInfo, recordBytes...)
	for _, channel := range record.Channels {
		layerInfo = append(layerInfo, input[channel.Span.Offset:channel.Span.Offset+channel.Span.Length]...)
	}
	if len(layerInfo)%2 != 0 {
		layerInfo = append(layerInfo, 0)
	}
	layerMask := binary.BigEndian.AppendUint32(nil, uint32(len(layerInfo)))
	layerMask = append(layerMask, layerInfo...)
	layerMask = append(layerMask,
		input[source.GlobalMask.Offset:source.LayerMaskSection.Offset+source.LayerMaskSection.Length]...)
	result := bytes.Clone(input[:source.LayerMaskSection.Offset])
	result = binary.BigEndian.AppendUint32(result, uint32(len(layerMask)))
	result = append(result, layerMask...)
	return append(result, input[source.Composite.Span.Offset:]...)
}

func TestOpenTypeSceneMultipleSourcesGlyphDomainAndHalo(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	canvas := image.Rect(0, 0, 128, 96)
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, MaxGlyphs: 1}
	first := openTypeTestPSD(t, "A", face.PostScriptName, 13.75, false)
	second := openTypeTestPSD(t, "V", face.PostScriptName, 13.75, false)
	fixture := openTypeSceneCombine(t, first, second)
	source, reader := openTypeSceneSource(t, fixture, nil)
	actual, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	records, err := source.TextLayers(nil)
	if err != nil || len(records) != 2 {
		t.Fatal(err)
	}
	expectedLayers := []*psdmodel.Layer{}
	for _, record := range records {
		artwork, err := RenderOpenTypeTextLayer(nil, source, record, resolver, policy)
		if err != nil {
			t.Fatal(err)
		}
		expectedLayers = append(expectedLayers, &psdmodel.Layer{Bounds: canvas, Pixels: artwork, BlendMode: "normal"})
	}
	expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: 128, Height: 96, Layers: expectedLayers})
	if err != nil {
		t.Fatal(err)
	}
	openTypeSceneEqualWindow(t, actual, expected, canvas)
	if reader.forbiddenHit != 0 {
		t.Fatal("multiple text sources used saved pixels")
	}
	var cause = errors.New("second source resolver failed")
	calls := 0
	failingResolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		calls++
		if calls == 2 {
			return nil, cause
		}
		return face, nil
	})
	partial, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: failingResolver,
		CachedSpecialLayers: true})
	if partial != nil || !errors.Is(err, cause) || calls != 2 || reader.forbiddenHit != 0 {
		t.Fatal("second-source failure returned partial pixels or selected a cache")
	}
	ordinary := cachedTestSource(nil)
	fixture = openTypeSceneCombine(t, first, ordinary)
	source, reader = openTypeSceneSource(t, fixture, []bool{true, false})
	glyphBounds := image.Rect(18, 41, 31, 51)
	policy.Bounds = &glyphBounds
	actual, err = Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if actual.Rect != canvas || actual.Pix[0] != 201 || actual.Pix[1] != 21 || actual.Pix[2] != 39 ||
		actual.Pix[3] != 255 || reader.forbiddenHit != 0 {
		t.Fatal("glyph-source Bounds cropped or changed ordinary artwork")
	}
	policy.Bounds = nil
	fixture = openTypeSceneEffectsFixture(t, openTypeSceneWrongSavedBounds(t, first), 3)
	source, reader = openTypeSceneSource(t, fixture, nil)
	full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
	if full != nil || !errors.Is(err, psdfile.ErrUnsupported) || reader.forbiddenHit != 0 {
		t.Fatal("uncalibrated effects on partially antialiased source coverage were accepted")
	}
	// Integer rectangular outlines at this owned font size have binary coverage under the supported sampler.
	fixture = openTypeSceneEffectsFixture(t, openTypeSceneWrongSavedBounds(t,
		openTypeTestPSD(t, "A", face.PostScriptName, 100, false)), 3)
	source, reader = openTypeSceneSource(t, fixture, nil)
	full, err = Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []image.Rectangle{image.Rect(17, 44, 26, 57), image.Rect(20, 52, 40, 66),
		image.Rect(12, 35, 44, 64)} {
		region, err := RenderTile(nil, source, window, TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver})
		if err != nil {
			t.Fatal(err)
		}
		openTypeSceneEqualWindow(t, region, full, window)
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("text effect halo used saved artwork")
	}
	fixture = openTypeSceneEffectsFixture(t, first, 4000)
	source, reader = openTypeSceneSource(t, fixture, nil)
	calls = 0
	boundedResolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		calls++
		return face, nil
	})
	limited, err := RenderTile(nil, source, image.Rect(20, 40, 25, 45),
		TileOptions{ApproximateOpenTypeText: &policy, FontResolver: boundedResolver})
	if limited != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || reader.forbiddenHit != 0 {
		t.Fatal("effect halo limit was not checked before glyph resolution")
	}
}

func TestOpenTypeSceneGlobalKnownFailuresBeforeAnyResolver(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	first := openTypeTestPSD(t, "A", face.PostScriptName, 13.75, false)
	for _, kind := range []string{"secondTextHalo", "laterOrdinaryHalo"} {
		t.Run(kind, func(t *testing.T) {
			second := first
			if kind == "laterOrdinaryHalo" {
				second = cachedTestSource(nil)
			}
			fixture := openTypeSceneCombine(t, first, openTypeSceneEffectsFixture(t, second, 4000))
			source, reader := openTypeSceneSource(t, fixture, nil)
			calls := 0
			resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
				calls++
				return face, nil
			})
			policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
			result, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
				CachedSpecialLayers: true})
			if result != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || reader.forbiddenHit != 0 {
				t.Fatalf("known %s failure followed a resolver: calls=%d err=%v", kind, calls, err)
			}
		})
	}
	t.Run("laterPlaced", func(t *testing.T) {
		placed, _, _ := tilePlacedLargeSource(t, true)
		defer placed.Close()
		record := placed.Layers[0]
		placement := record.AdditionalInfo[0]
		payload, err := placed.ReadSpan(placement.Data)
		if err != nil {
			t.Fatal(err)
		}
		descriptor, err := placed.DecodeDescriptorContext(nil, psdfile.Span{Offset: placement.Data.Offset + 8, Length: placement.Data.Length - 8}, true)
		if err != nil {
			t.Fatal(err)
		}
		for index, entry := range descriptor.Entries {
			if entry.Key != "Trnf" {
				continue
			}
			var values []psdfile.DescriptorValue
			for _, value := range []float64{20, 30, 22, 30, 22, 31, 20, 31} {
				values = append(values, psdfile.DescriptorValue{Type: "doub", Value: value})
			}
			descriptor.Entries[index].Value = values
		}
		encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		payload = append(payload[:8:8], encoded...)
		fixture := openTypeSceneCombine(t, first,
			cachedTestSource(artboardTestBlock(placement.Key, payload)))
		source, reader := openTypeSceneSource(t, fixture, nil)
		calls := 0
		resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
			calls++
			return face, nil
		})
		policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
		result, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
			CachedSpecialLayers: true})
		if result != nil || !errors.Is(err, psdfile.ErrUnsupported) || calls != 0 || reader.forbiddenHit != 0 {
			t.Fatalf("placed incompatibility followed a resolver: calls=%d err=%v", calls, err)
		}
	})
	t.Run("retainedPixels", func(t *testing.T) {
		inputs := make([][]byte, 20)
		for index := range inputs {
			inputs[index] = first
		}
		source, reader := openTypeSceneSource(t, openTypeSceneCombine(t, inputs...), nil)
		calls := 0
		resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
			calls++
			return face, nil
		})
		policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
		result, err := Render(nil, source, Options{MaxWorkingBytes: 1 << 20,
			ApproximateOpenTypeText: &policy, FontResolver: resolver})
		if result != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || reader.forbiddenHit != 0 {
			t.Fatalf("future retained output followed a resolver: calls=%d err=%v", calls, err)
		}
		var detail *psdfile.Error
		if !errors.As(err, &detail) || detail.Path != "tile.text.plan" {
			t.Fatalf("retained output control stopped before exercising the global plan: %v", err)
		}
	})
}
