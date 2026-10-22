package psdfont

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"
)

type seacOracleCase struct {
	ID       uint16
	Glyph    string
	Commands [][7]float64
	Bounds   [4]int16
	Hmtx     [2]int
	RawWidth float64 `json:"raw_width"`
	BaseID   uint16  `json:"base_id"`
	AccentID uint16  `json:"accent_id"`
}

type seacOracleFont struct {
	File, SHA256 string
	FontName     string `json:"font_name"`
	Cases        []seacOracleCase
	HB           []struct {
		ID       uint16
		Cluster  int64
		Advance  int64
		YAdvance int64 `json:"y_advance"`
		XOffset  int64 `json:"x_offset"`
		YOffset  int64 `json:"y_offset"`
	} `json:"HB_ABC"`
}

func seacOracle(t *testing.T) []seacOracleFont {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cff1seac", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct{ Fonts []seacOracleFont }
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Fonts) != 4 {
		t.Fatalf("portable fourfont oracle: %v", err)
	}
	return oracle.Fonts
}

func seacFace(t *testing.T, row seacOracleFont) *Face {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cff1seac", row.File))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != row.SHA256 {
		t.Fatal("portable font bytes changed")
	}
	face, err := Decode(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	return face
}

func seacCompareGlyph(t *testing.T, face *Face, row seacOracleCase) {
	t.Helper()
	glyph, err := face.Glyph(nil, row.ID)
	if err != nil || glyph == nil {
		t.Fatalf("glyph%s: %v", row.Glyph, err)
	}
	if glyph.AdvanceWidth != uint16(row.Hmtx[0]) || glyph.LeftSideBearing != int16(row.Hmtx[1]) ||
		glyph.CharstringWidth != row.RawWidth || glyph.Bounds != row.Bounds || len(glyph.Path) != len(row.Commands) {
		t.Fatalf("glyph%s changed composite metrics/width/bounds/path count", row.Glyph)
	}
	for index, expected := range row.Commands {
		command := glyph.Path[index]
		actual := [7]float64{float64(command.Op), command.X, command.Y, command.ControlX, command.ControlY,
			command.Control2X, command.Control2Y}
		for field := range actual {
			if math.Float64bits(actual[field]) != math.Float64bits(expected[field]) {
				t.Fatalf("glyph%s command%d field%d exact mismatch %.17g/%.17g", row.Glyph, index, field,
					actual[field], expected[field])
			}
		}
	}
	if glyph.Source != face.cff.charstrings[row.ID] || len(glyph.ComponentSources) != 2 || len(glyph.Instructions) != 0 ||
		glyph.HasComponentMetrics {
		t.Fatal("composite Source/provenance/hint/metrics domain differs")
	}
	if glyph.ComponentSources[0] != face.cff.charstrings[row.BaseID] ||
		glyph.ComponentSources[1] != face.cff.charstrings[row.AccentID] {
		t.Fatal("component provenance differs from independent FontTools base/accent glyphIDs")
	}
	for _, span := range append([]Span{glyph.Source}, glyph.ComponentSources...) {
		data, err := face.ReadSpan(nil, span)
		if err != nil || len(data) == 0 || !bytes.Equal(data, face.raw[span.Offset:span.Offset+span.Length]) {
			t.Fatal("provenance does not identify original owned charstring bytes")
		}
	}
	retained := int64(unsafe.Sizeof(Glyph{})) + int64(cap(glyph.Path))*int64(unsafe.Sizeof(Command{})) +
		int64(cap(glyph.Contours))*int64(unsafe.Sizeof([]Point{})) +
		int64(cap(glyph.ComponentSources))*int64(unsafe.Sizeof(Span{}))
	for _, contour := range glyph.Contours {
		retained += int64(cap(contour)) * int64(unsafe.Sizeof(Point{}))
	}
	if glyph.WorkingBytes() < retained {
		t.Fatal("recorded glyph workspace misses owned ComponentSources/outline capacity")
	}
}

func TestCFFSeacCompleteStandardEncodingAndRawDomains(t *testing.T) {
	count := 0
	for _, font := range seacOracle(t) {
		t.Run(font.File, func(t *testing.T) {
			face := seacFace(t, font)
			for _, row := range font.Cases {
				seacCompareGlyph(t, face, row)
				count++
			}
			run, err := Shape(nil, face, "ABC")
			if err != nil || run == nil || len(run.Glyphs) != len(font.HB) {
				t.Fatalf("HB typography: %v", err)
			}
			for index, expected := range font.HB {
				actual := run.Glyphs[index]
				if actual.GlyphID != expected.ID || actual.ClusterStart != expected.Cluster ||
					actual.XAdvance != expected.Advance || actual.YAdvance != expected.YAdvance ||
					actual.XOffset != expected.XOffset || actual.YOffset != expected.YOffset {
					t.Fatal("composite hmtx/OpenType typography differs from HarfBuzz")
				}
			}
		})
	}
	if count != 268 {
		t.Fatalf("actual complete glyph cases=%d want268", count)
	}
	t.Logf("actual268glyph cases, full256code pair permutation includes107nonempty.notdef slots")
}

func TestCFFSeacCollectionSpansAndOwnedComponentMetadata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "cff1seac", "SeacCollection.ttc"))
	if err != nil {
		t.Fatal(err)
	}
	for index, font := range seacOracle(t)[1:3] {
		face, err := Decode(nil, data, DecodeOptions{FaceIndex: index})
		if err != nil || face == nil {
			t.Fatal(err)
		}
		for _, row := range font.Cases {
			seacCompareGlyph(t, face, row)
		}
		glyph, err := face.Glyph(nil, font.Cases[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		glyph.ComponentSources[0] = Span{}
		fresh, err := face.Glyph(nil, font.Cases[0].ID)
		if err != nil || len(fresh.ComponentSources) != 2 || fresh.ComponentSources[0].Length == 0 {
			t.Fatal("caller metadata mutation changed face or next owned glyph")
		}
		before := append([]Command(nil), fresh.Path...)
		for position := range data {
			data[position] ^= 255
		}
		again, err := face.Glyph(nil, font.Cases[0].ID)
		if err != nil || !reflect.DeepEqual(before, again.Path) {
			t.Fatal("collection face borrowed caller bytes")
		}
		for position := range data {
			data[position] ^= 255
		}
	}
}

func TestCFFSeacSharedGlyphLimitsAndLayoutCapacity(t *testing.T) {
	for _, font := range seacOracle(t) {
		face := seacFace(t, font)
		id := font.Cases[0].ID
		glyph, err := face.Glyph(nil, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, delta := range []int64{-1, 0, 1} {
			actual, err := face.Glyph(nil, id, GlyphOptions{MaxWorkingBytes: glyph.WorkingBytes() + delta})
			if delta < 0 {
				if actual != nil || !errors.Is(err, ErrLimit) {
					t.Fatal("glyph exactbytes minus1 not nil/ErrLimit")
				}
			} else if actual == nil || err != nil {
				t.Fatalf("glyph exactbytes/+1: %v", err)
			}
		}
		points := 0
		for _, contour := range glyph.Contours {
			points += len(contour)
		}
		for _, option := range []GlyphOptions{{MaxPoints: points - 1}, {MaxContours: len(glyph.Contours) - 1},
			{MaxComponents: 2}} {
			actual, err := face.Glyph(nil, id, option)
			if actual != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("shared component capacity failed: %v", err)
			}
		}
		resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
		style := Style{FontName: face.PostScriptName, Size: 40, Leading: 50}
		for _, shaped := range []bool{false, true} {
			var run *Run
			if shaped {
				run, err = LayoutOpenType(nil, "ABCABC", style, resolver, ShapeOptions{})
			} else {
				run, err = Layout(nil, "ABCABC", style, resolver)
			}
			if run == nil || err != nil {
				t.Fatal(err)
			}
			for _, delta := range []int64{-1, 0, 1} {
				limits := LayoutOptions{MaxWorkingBytes: run.WorkingBytes() + delta}
				var actual *Run
				if shaped {
					actual, err = LayoutOpenType(nil, "ABCABC", style, resolver, ShapeOptions{}, limits)
				} else {
					actual, err = Layout(nil, "ABCABC", style, resolver, limits)
				}
				if delta < 0 {
					if actual != nil || !errors.Is(err, ErrLimit) {
						t.Fatal("layout recorded peak minus1 not nil/ErrLimit")
					}
				} else if actual == nil || err != nil {
					t.Fatalf("layout exactpeak/+1: %v", err)
				}
			}
		}
	}
}

func TestCFFSeacOptionalSourceJSONDoesNotAddLegacyNullKeys(t *testing.T) {
	for _, filename := range []string{
		filepath.Join("testdata", "opentype", "fonts", "base.ttf"),
		filepath.Join("testdata", "cff1", "fonts", "psdkit-CFF1-Name.otf"),
	} {
		face, err := Open(nil, filename)
		if err != nil {
			t.Fatal(err)
		}
		glyph, err := face.Glyph(nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(glyph, json.Deterministic(true))
		if err != nil || bytes.Contains(data, []byte("ComponentSources")) || len(glyph.ComponentSources) != 0 {
			t.Fatal("ordinary glyph JSON gained a new null metadata field")
		}
	}
	font := seacOracle(t)[0]
	face := seacFace(t, font)
	glyph, err := face.Glyph(nil, font.Cases[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(glyph, json.Deterministic(true))
	if err != nil || !bytes.Contains(data, []byte("ComponentSources")) {
		t.Fatal("compound glyph JSON omitted its owned nonempty provenance")
	}
}
