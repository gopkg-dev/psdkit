package psdfont

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"
)

type cffOraclePath struct {
	Op     string      `json:"op"`
	Points [][]float64 `json:"points"`
}

type cffOracleGlyph struct {
	ID        uint16          `json:"id"`
	Path      []cffOraclePath `json:"path"`
	Bounds    []float64       `json:"bounds"`
	Advance   uint16          `json:"advance"`
	Bearing   int16           `json:"bearing"`
	Width     float64         `json:"charstring_width"`
	SourceSHA string          `json:"charstring_source_sha256"`
}

type cffOracleFont struct {
	File, SHA256 string
	Glyphs       []cffOracleGlyph
	Shapes       []struct {
		Text       string
		Placements []struct {
			ID       uint16
			Cluster  int64
			XAdvance int64 `json:"x_advance"`
			YAdvance int64 `json:"y_advance"`
			XOffset  int64 `json:"x_offset"`
			YOffset  int64 `json:"y_offset"`
		}
	}
}

func cffTestOracle(t *testing.T) []cffOracleFont {
	t.Helper()
	data, err := os.ReadFile("testdata/cff1/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct{ Fonts []cffOracleFont }
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Fonts) != 5 {
		t.Fatal("five portable CFF fonts required")
	}
	return oracle.Fonts
}

func cffTestFace(t *testing.T, item cffOracleFont) *Face {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/cff1/fonts", item.File))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != item.SHA256 {
		t.Fatal("portable CFF font bytes differ")
	}
	face, err := Decode(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if face.cff == nil {
		t.Fatal("CFF face was not selected")
	}
	return face
}

func TestCFFPortableFontToolsOutlineMetrics(t *testing.T) {
	if Move != 1 || Line != 2 || Quad != 3 || Close != 4 || Cubic != 5 {
		t.Fatal("preexisting PathOp values changed")
	}
	for _, font := range cffTestOracle(t) {
		t.Run(font.File, func(t *testing.T) {
			face := cffTestFace(t, font)
			for _, expected := range font.Glyphs {
				glyph, err := face.Glyph(nil, expected.ID)
				if err != nil {
					t.Fatalf("glyph=%d: %v", expected.ID, err)
				}
				if glyph.AdvanceWidth != expected.Advance || glyph.LeftSideBearing != expected.Bearing ||
					glyph.CharstringWidth != expected.Width || len(glyph.Path) != len(expected.Path) {
					t.Fatalf("glyph=%d: metrics, Type2 width or path count differ: %v", expected.ID, glyph)
				}
				original, err := face.ReadSpan(nil, glyph.Source)
				if err != nil {
					t.Fatal(err)
				}
				sourceDigest := sha256.Sum256(original)
				if hex.EncodeToString(sourceDigest[:]) != expected.SourceSHA {
					t.Fatalf("glyph=%d: Type2 original SourceSpan differs", expected.ID)
				}
				for index, command := range glyph.Path {
					want := expected.Path[index]
					op := map[PathOp]string{Move: "moveTo", Line: "lineTo", Cubic: "curveTo", Close: "closePath"}
					if op[command.Op] != want.Op {
						t.Fatalf("glyph=%d path=%d op differs", expected.ID, index)
					}
					if command.Op == Close {
						continue
					}
					points := [][]float64{{command.X, command.Y}}
					if command.Op == Cubic {
						points = [][]float64{{command.ControlX, command.ControlY}, {command.Control2X, command.Control2Y},
							{command.X, command.Y}}
					}
					if !reflect.DeepEqual(points, want.Points) {
						t.Fatalf("glyph=%d path=%d: %v differs from %v", expected.ID, index, points, want.Points)
					}
				}
				if len(expected.Bounds) != 0 {
					want := [4]int16{int16(math.Floor(expected.Bounds[0])), int16(math.Floor(expected.Bounds[1])),
						int16(math.Ceil(expected.Bounds[2])), int16(math.Ceil(expected.Bounds[3]))}
					if glyph.Bounds != want {
						t.Fatalf("glyph=%d: true curve bounds %v differ from %v", expected.ID, glyph.Bounds, want)
					}
				} else if glyph.Bounds != [4]int16{} {
					t.Fatalf("glyph=%d: empty outline has ink bounds", expected.ID)
				}
				retained := int64(unsafe.Sizeof(Glyph{})) + int64(cap(glyph.Path))*int64(unsafe.Sizeof(Command{})) +
					int64(cap(glyph.Contours))*int64(unsafe.Sizeof([]Point{}))
				for _, contour := range glyph.Contours {
					retained += int64(cap(contour)) * int64(unsafe.Sizeof(Point{}))
				}
				if glyph.WorkingBytes() < retained {
					t.Fatalf("glyph=%d: peak reservation is below retained allocation", expected.ID)
				}
				bounded, err := face.Glyph(nil, expected.ID, GlyphOptions{MaxWorkingBytes: glyph.WorkingBytes()})
				if err != nil || !reflect.DeepEqual(bounded, glyph) {
					t.Fatalf("glyph=%d: exact peak budget differs: %v", expected.ID, err)
				}
				bounded, err = face.Glyph(nil, expected.ID, GlyphOptions{MaxWorkingBytes: glyph.WorkingBytes() - 1})
				if bounded != nil || !errors.Is(err, ErrLimit) {
					t.Fatalf("glyph=%d: peak minus one must reject: %v", expected.ID, err)
				}
			}
		})
	}
}

func TestCFFPortableHarfBuzzShapingAndRaster(t *testing.T) {
	features := ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}, {Tag: "kern", Enabled: true}}}
	for _, font := range cffTestOracle(t) {
		face := cffTestFace(t, font)
		for _, item := range font.Shapes {
			shaped, err := Shape(nil, face, item.Text, features)
			if err != nil || shaped == nil || len(shaped.Glyphs) != len(item.Placements) {
				t.Fatalf("font=%s text=%s: %v", font.File, item.Text, err)
			}
			for index, actual := range shaped.Glyphs {
				want := item.Placements[index]
				if actual.GlyphID != want.ID || actual.ClusterStart != want.Cluster || actual.XAdvance != want.XAdvance ||
					actual.YAdvance != want.YAdvance || actual.XOffset != want.XOffset || actual.YOffset != want.YOffset {
					t.Fatalf("font=%s placement=%d: %v differs from %v", font.File, index, actual, want)
				}
			}
			resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
			run, err := LayoutOpenType(nil, item.Text, Style{FontName: face.PostScriptName, Size: 400}, resolver, features)
			if err != nil {
				t.Fatal(err)
			}
			mask, err := RasterizeRun(nil, run, image.Rect(-20, -160, 1600, 40), RasterOptions{Samples: 4})
			if err != nil || mask == nil {
				t.Fatalf("CFF run did not reach real vector coverage: %v", err)
			}
			visible := false
			for _, coverage := range mask.Pix {
				visible = visible || coverage != 0
			}
			if !visible {
				t.Fatal("CFF paths rasterized to empty output")
			}
		}
	}
}

func TestCFFBudgetsCancellationOwnership(t *testing.T) {
	face := cffTestFace(t, cffTestOracle(t)[0])
	for _, limits := range []GlyphOptions{
		{MaxOperations: 1}, {MaxPoints: 1}, {MaxContours: 1, MaxPoints: 2},
		{MaxWorkingBytes: 1}, {MaxComponents: 1}, {MaxDepth: 1},
	} {
		glyph, err := face.Glyph(nil, 25, limits)
		if glyph != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("bounded subroutine glyph %v: glyph=%v err=%v", limits, glyph, err)
		}
	}
	for cancelAt := 1; cancelAt <= 25; cancelAt++ {
		base, cancel := context.WithCancel(context.Background())
		ctx := &fontCancelContext{Context: base, cancel: cancel, after: cancelAt}
		glyph, err := face.Glyph(ctx, 25)
		if glyph != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel check %d returned partial outline: %v", cancelAt, err)
		}
	}
	glyph, err := face.Glyph(nil, 12)
	if err != nil {
		t.Fatal(err)
	}
	glyph.Path[1].Control2X = 123456
	glyph.Contours[0][1].X = 123456
	again, err := face.Glyph(nil, 12)
	if err != nil || again.Path[1].Control2X == 123456 || again.Contours[0][1].X == 123456 {
		t.Fatal("CFF glyph result shares mutable storage")
	}
	data := append([]byte(nil), face.raw...)
	owned, err := Decode(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	for index := range data {
		data[index] = 0
	}
	if glyph, err := owned.Glyph(nil, 12); err != nil || len(glyph.Path) == 0 {
		t.Fatal("CFF face borrowed input font bytes")
	}
	if result, err := Decode(nil, face.raw, DecodeOptions{MaxCFFOperations: 1}); result != nil ||
		!errors.Is(err, ErrLimit) {
		t.Fatal("CFF parser operation budget did not reject")
	}
	for _, font := range cffTestOracle(t) {
		face := cffTestFace(t, font)
		if decoded, err := Decode(nil, face.raw, DecodeOptions{MaxBytes: face.reservation}); err != nil || decoded == nil {
			t.Fatalf("CFF decode exact peak rejected: %v", err)
		}
		if decoded, err := Decode(nil, face.raw, DecodeOptions{MaxBytes: face.reservation - 1}); decoded != nil ||
			!errors.Is(err, ErrLimit) {
			t.Fatalf("CFF decode peak minus one did not reject: %v", err)
		}
	}
}

func TestCFFEveryGlyphCancellationBoundary(t *testing.T) {
	face := cffTestFace(t, cffTestOracle(t)[2])
	for _, id := range []uint16{12, 25, 29} {
		base, cancel := context.WithCancel(context.Background())
		count := &fontCancelContext{Context: base, cancel: cancel, after: int(^uint(0) >> 1)}
		if glyph, err := face.Glyph(count, id); err != nil || glyph == nil {
			t.Fatal(err)
		}
		checks := count.calls
		cancel()
		for cancelAt := 1; cancelAt <= checks; cancelAt++ {
			base, cancel := context.WithCancel(context.Background())
			ctx := &fontCancelContext{Context: base, cancel: cancel, after: cancelAt}
			glyph, err := face.Glyph(ctx, id)
			cancel()
			if glyph != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("glyph=%d cancellation=%d/%d returned partial geometry: %v", id, cancelAt, checks, err)
			}
		}
	}
}

func TestCFFFormerMatrixRejectInputsRemainOwned(t *testing.T) {
	data, err := os.ReadFile("testdata/cff1/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Rejections []struct {
			File, SHA256 string
			Path         string `json:"expected_path"`
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Rejections) != 2 {
		t.Fatal("two actual noncanonical Top/FD matrix fonts required")
	}
	for _, item := range oracle.Rejections {
		data, err := os.ReadFile(filepath.Join("testdata/cff1/fonts", item.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != item.SHA256 {
			t.Fatal("matrix fixture bytes differ")
		}
		face, err := Decode(nil, data)
		if err != nil || face == nil {
			t.Fatalf("formerly unsupported real FontMatrix must now decode: %v", err)
		}
		if face.cff.private[0].matrix != [6]float64{2, 0, 0, 1, 0, 0} {
			t.Fatal("original negative evidence was replaced instead of resolving its real matrix")
		}
	}
}

func TestCFFSelectedCollectionSourceProvenance(t *testing.T) {
	data, err := os.ReadFile("testdata/cff1/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts      []cffOracleFont
		Collection struct {
			File, SHA256 string
			Fonts        []string
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	collection, err := os.ReadFile(filepath.Join("testdata/cff1/fonts", oracle.Collection.File))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(collection)
	if hex.EncodeToString(digest[:]) != oracle.Collection.SHA256 || len(oracle.Collection.Fonts) != 2 {
		t.Fatal("owned collection identity differs")
	}
	for index, filename := range oracle.Collection.Fonts {
		var expected cffOracleFont
		for _, font := range oracle.Fonts {
			if font.File == filename {
				expected = font
			}
		}
		standalone := cffTestFace(t, expected)
		selected, err := Decode(nil, collection, DecodeOptions{FaceIndex: index})
		if err != nil || selected.FaceIndex != index || selected.PostScriptName != standalone.PostScriptName {
			t.Fatalf("selected CFF collection face=%d: %v", index, err)
		}
		for _, row := range expected.Glyphs {
			actual, err := selected.Glyph(nil, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			want, err := standalone.Glyph(nil, row.ID)
			if err != nil || !reflect.DeepEqual(actual.Path, want.Path) || !reflect.DeepEqual(actual.Contours, want.Contours) ||
				actual.Bounds != want.Bounds || actual.AdvanceWidth != want.AdvanceWidth {
				t.Fatal("selected CFF collection outline differs")
			}
			original, err := selected.ReadSpan(nil, actual.Source)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(original)
			if hex.EncodeToString(digest[:]) != row.SourceSHA {
				t.Fatal("CFF collection SourceSpan lost absolute byte provenance")
			}
		}
	}
	if selected, err := Decode(nil, collection, DecodeOptions{FaceIndex: 2}); selected != nil ||
		!errors.Is(err, ErrMalformed) {
		t.Fatal("CFF collection accepted an absent face")
	}
}
