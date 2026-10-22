package psdfont

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func cffHintTestPrivate(values []byte) *Face {
	face := cffTestProgram([]byte{139, 159, 1, 139, 139, 21, 219, 139, 5, 14}, nil)
	span := Span{Offset: int64(len(face.raw)), Length: int64(len(values))}
	face.raw = append(face.raw, values...)
	face.cff.private[0].hintSource = span
	return face
}

func cffHintTestIntegers(operator int, values ...int16) []byte {
	var result []byte
	for _, value := range values {
		result = append(result, 28)
		result = binary.BigEndian.AppendUint16(result, uint16(value))
	}
	if operator > 255 {
		return append(result, 12, byte(operator-256))
	}
	return append(result, byte(operator))
}

func TestCFFHintPrivateAdobeDeltaArraysAndDefaults(t *testing.T) {
	// Adobe CFF #5176 Appendix D lists these deltas, including unordered OtherBlues zones.
	var dictionary []byte
	fields := []struct {
		operator int
		values   []int16
	}{
		{6, []int16{-14, 14, 662, 14, -226, 10, 223, 0}},
		{7, []int16{262, 8, -488, 1}},
		{8, []int16{-14, 14, 450, 10, 202, 14}},
		{9, []int16{-218, 1, 479, 8, 124, 0}},
		{10, []int16{28}}, {11, []int16{84}},
		{268, []int16{28, 12, 5}}, {269, []int16{84, 8, 6}},
		{270, []int16{1}}, {273, []int16{1}},
	}
	for _, field := range fields {
		dictionary = append(dictionary, cffHintTestIntegers(field.operator, field.values...)...)
	}
	face := cffHintTestPrivate(dictionary)
	glyph, err := face.Glyph(nil, 0, GlyphOptions{PreserveCFFHints: true})
	if err != nil || glyph.CFFHints == nil {
		t.Fatalf("Private source hints: %v", err)
	}
	private := glyph.CFFHints.Private
	for _, expected := range []struct {
		name   string
		actual []float64
		want   []float64
	}{
		{"BlueValues", private.BlueValues, []float64{-14, 0, 662, 676, 450, 460, 683, 683}},
		{"OtherBlues", private.OtherBlues, []float64{262, 270, -218, -217}},
		{"FamilyBlues", private.FamilyBlues, []float64{-14, 0, 450, 460, 662, 676}},
		{"FamilyOtherBlues", private.FamilyOtherBlues, []float64{-218, -217, 262, 270, 394, 394}},
		{"StemSnapH", private.StemSnapH, []float64{28, 40, 45}},
		{"StemSnapV", private.StemSnapV, []float64{84, 92, 98}},
	} {
		if !reflect.DeepEqual(expected.actual, expected.want) {
			t.Fatalf("%s: %v differs from independent delta example %v", expected.name, expected.actual, expected.want)
		}
	}
	if private.StdHW != 28 || private.StdVW != 84 || !private.ForceBold || private.LanguageGroup != 1 ||
		private.BlueScale != .039625 || private.BlueShift != 7 || private.BlueFuzz != 1 || private.ExpansionFactor != .06 {
		t.Fatalf("explicit or specification-default scalar differs: %+v", private)
	}
	if private.Source.BlueScale != (Span{}) || private.Source.BlueShift != (Span{}) ||
		private.Source.BlueFuzz != (Span{}) || private.Source.ExpansionFactor != (Span{}) {
		t.Fatal("missing source operators became invented authored fields")
	}
	spans := []Span{
		private.Source.BlueValues, private.Source.OtherBlues, private.Source.FamilyBlues,
		private.Source.FamilyOtherBlues, private.Source.StdHW, private.Source.StdVW,
		private.Source.StemSnapH, private.Source.StemSnapV, private.Source.ForceBold, private.Source.LanguageGroup,
	}
	for index, source := range spans {
		original, err := face.ReadSpan(nil, source)
		if err != nil || !bytes.Equal(original, cffHintTestIntegers(fields[index].operator, fields[index].values...)) {
			t.Fatalf("Private source field %d does not refer to its exact source bytes: %v", index, err)
		}
	}
	original, err := face.ReadSpan(nil, private.Source.PrivateDICT)
	if err != nil || !bytes.Equal(original, dictionary) {
		t.Fatal("whole Private DICT source changed")
	}
	private.BlueValues[0] = -123456
	private.StemSnapV[0] = -123456
	fresh, err := face.Glyph(nil, 0, GlyphOptions{PreserveCFFHints: true})
	if err != nil || fresh.CFFHints.Private.BlueValues[0] != -14 || fresh.CFFHints.Private.StemSnapV[0] != 84 {
		t.Fatal("independent requests share mutable Private hint arrays")
	}
}

func TestCFFHintPrivateScalarSourceValidationAndBudgets(t *testing.T) {
	// This literal BCD byte sequence spells 0.05, followed by the BlueScale operator.
	dictionary := []byte{30, 0x0a, 0x05, 0xff, 12, 9}
	dictionary = append(dictionary, cffHintTestIntegers(266, 9)...)
	dictionary = append(dictionary, cffHintTestIntegers(267, 2)...)
	face := cffHintTestPrivate(dictionary)
	glyph, err := face.Glyph(nil, 0, GlyphOptions{PreserveCFFHints: true})
	if err != nil || glyph.CFFHints.Private.BlueScale != .05 ||
		glyph.CFFHints.Private.BlueShift != 9 || glyph.CFFHints.Private.BlueFuzz != 2 {
		t.Fatalf("authored scalar hint values: %v", err)
	}
	for _, delta := range []int64{-1, 0} {
		actual, err := face.Glyph(nil, 0, GlyphOptions{
			PreserveCFFHints: true, MaxWorkingBytes: glyph.WorkingBytes() + delta,
		})
		if delta < 0 && (actual != nil || !errors.Is(err, ErrLimit)) ||
			delta == 0 && (err != nil || !reflect.DeepEqual(actual, glyph)) {
			t.Fatalf("Private shared peak budget delta %d: %v", delta, err)
		}
	}
	for cancelAt := 1; cancelAt <= 20; cancelAt++ {
		base, cancel := context.WithCancel(context.Background())
		ctx := &fontCancelContext{Context: base, cancel: cancel, after: cancelAt}
		actual, err := face.Glyph(ctx, 0, GlyphOptions{PreserveCFFHints: true})
		if actual != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Private parse cancellation %d produced a partial glyph: %v", cancelAt, err)
		}
	}
	for _, control := range [][]byte{
		cffHintTestIntegers(6, 0), cffHintTestIntegers(7, 20, -1),
		cffHintTestIntegers(268, 10, -1), cffHintTestIntegers(265, 0),
		cffHintTestIntegers(270, 2), cffHintTestIntegers(273, 2),
	} {
		actual, err := cffHintTestPrivate(control).Glyph(nil, 0, GlyphOptions{PreserveCFFHints: true})
		if actual != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed opt-in Private hint field returned partial metadata: %v", err)
		}
	}
}

func TestCFFHintOptInRejectsUnsupportedComponentsAndWidths(t *testing.T) {
	for _, program := range [][]byte{
		{139, 138, 1, 139, 139, 21, 14},
		{139, 139, 204, 205, 14},
	} {
		actual, err := cffTestProgram(program, nil).Glyph(nil, 0, GlyphOptions{PreserveCFFHints: true})
		if actual != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("undefined width or composite hint program accepted: %v", err)
		}
	}
	plain, err := cffTestProgram([]byte{139, 138, 1, 139, 139, 21, 14}, nil).Glyph(nil, 0)
	if err != nil || plain == nil || plain.CFFHints != nil {
		t.Fatalf("default unhinted VM began executing or rejecting negative hint widths: %v", err)
	}
}

func TestCFFHintLayoutCacheRetainedBudgetAndDefaultPixels(t *testing.T) {
	face := cffTestFace(t, cffTestOracle(t)[0])
	resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
	style := Style{FontName: face.PostScriptName, Size: 18}
	opts := LayoutOptions{GlyphLimits: GlyphOptions{PreserveCFFHints: true}}
	run, err := LayoutOpenType(nil, "AAAA", style, resolver, ShapeOptions{}, opts)
	if err != nil || run == nil || len(run.Glyphs) != 4 || run.Glyphs[0].Glyph.CFFHints == nil {
		t.Fatalf("layout did not retain requested CFF1 source hints: %v", err)
	}
	for _, placed := range run.Glyphs[1:] {
		if placed.Glyph != run.Glyphs[0].Glyph {
			t.Fatal("repeated glyphs no longer share the run's read-only owned outline and metadata")
		}
	}
	for _, delta := range []int64{-1, 0} {
		limits := opts
		limits.MaxWorkingBytes = run.WorkingBytes() + delta
		actual, err := LayoutOpenType(nil, "AAAA", style, resolver, ShapeOptions{}, limits)
		if delta < 0 && (actual != nil || !errors.Is(err, ErrLimit)) ||
			delta == 0 && (err != nil || !reflect.DeepEqual(actual, run)) {
			t.Fatalf("layout CFF metadata peak delta %d: %v", delta, err)
		}
	}
	plain, err := LayoutOpenType(nil, "AAAA", style, resolver, ShapeOptions{})
	if err != nil || plain.Glyphs[0].Glyph.CFFHints != nil || plain.WorkingBytes() >= run.WorkingBytes() {
		t.Fatalf("default layout began owning optional CFF1 hint storage: %v", err)
	}
	region := image.Rect(-1, -25, 60, 5)
	first, err := RasterizeRun(nil, plain, region)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RasterizeRun(nil, run, region)
	if err != nil || !bytes.Equal(first.Pix, second.Pix) || first.Rect != second.Rect {
		t.Fatalf("hint metadata preservation changed default unhinted layout coverage: %v", err)
	}
}

func TestCFFHintSourceHanActualFonts(t *testing.T) {
	directory := os.Getenv("PSDKIT_CFF_HINT_FONT_DIR")
	if directory == "" {
		t.Skip("set PSDKIT_CFF_HINT_FONT_DIR to caller-supplied Source Han Sans CN 2.005R fonts")
	}
	for _, weight := range []string{"Medium", "Bold", "Heavy"} {
		t.Run(weight, func(t *testing.T) {
			face, err := Open(nil, filepath.Join(directory, "SourceHanSansCN-"+weight+".otf"))
			if err != nil {
				t.Fatal(err)
			}
			for _, character := range "AESOadeos" {
				id, err := face.LookupRune(nil, character)
				if err != nil {
					t.Fatal(err)
				}
				glyph, err := face.Glyph(nil, id, GlyphOptions{PreserveCFFHints: true})
				if err != nil || glyph == nil || glyph.CFFHints == nil || len(glyph.CFFHints.Stems) == 0 {
					t.Fatalf("%c did not preserve actual font stem hints: %v", character, err)
				}
				private := glyph.CFFHints.Private
				if len(private.BlueValues) == 0 || private.StdHW <= 0 || private.StdVW <= 0 ||
					private.Source.PrivateDICT.Length == 0 || private.Source.BlueValues.Length == 0 {
					t.Fatalf("%c has no actual FD Private alignment and standard stems", character)
				}
				plain, err := face.Glyph(nil, id)
				if err != nil || !reflect.DeepEqual(plain.Path, glyph.Path) ||
					!reflect.DeepEqual(plain.Contours, glyph.Contours) || plain.AdvanceWidth != glyph.AdvanceWidth {
					t.Fatalf("%c source outlines or hmtx changed while collecting metadata: %v", character, err)
				}
			}
		})
	}
}
