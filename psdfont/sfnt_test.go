package psdfont

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"slices"
	"testing"
	"unicode/utf16"
)

func fontTestU16(value uint16) []byte { return binary.BigEndian.AppendUint16(nil, value) }
func fontTestU32(value uint32) []byte { return binary.BigEndian.AppendUint32(nil, value) }

func fontTestSimple(offCurve bool) []byte {
	data := []byte{0, 1, 0, 0, 0, 0, 3, 232, 3, 232}
	data = append(data, 0, 2, 0, 0, 1)
	flag := byte(1)
	if offCurve {
		flag = 0
	}
	data = append(data, flag, 1)
	for _, delta := range []int16{0, 500, 500, 0, 1000, -1000} {
		data = binary.BigEndian.AppendUint16(data, uint16(delta))
	}
	return append(data, 0)
}

func fontTestRectangle() []byte {
	return []byte{0, 1, 0, 0, 0, 0, 0, 100, 0, 100, 0, 3, 0, 0,
		0x31, 0x33, 0x35, 0x23, 100, 100, 100, 0}
}

func fontTestCompound(firstID uint16) []byte {
	data := []byte{0xff, 0xff, 0, 10, 0, 20, 4, 236, 5, 240}
	for _, value := range []uint16{0x002b, firstID, 10, 20, 8192, 1, 2, 1, 0} {
		data = binary.BigEndian.AppendUint16(data, value)
	}
	return data
}

func fontTestCmap(twelve bool) []byte {
	codes, glyphs := []uint32{32, 65, 86, 192, 0x1f600}, []uint32{4, 1, 2, 3, 5}
	if twelve {
		data := []byte{0, 0, 0, 1, 0, 3, 0, 10, 0, 0, 0, 12}
		subtable := []byte{0, 12, 0, 0}
		subtable = append(subtable, fontTestU32(uint32(16+len(codes)*12))...)
		subtable = append(subtable, fontTestU32(0)...)
		subtable = append(subtable, fontTestU32(uint32(len(codes)))...)
		for index, code := range codes {
			subtable = append(subtable, fontTestU32(code)...)
			subtable = append(subtable, fontTestU32(code)...)
			subtable = append(subtable, fontTestU32(glyphs[index])...)
		}
		return append(data, subtable...)
	}
	codes[len(codes)-1], glyphs[len(glyphs)-1] = 0xffff, 0
	segments := len(codes)
	subtable := []byte{0, 4}
	subtable = append(subtable, fontTestU16(uint16(16+segments*8+2))...)
	for _, value := range []uint16{0, uint16(segments * 2), 8, 2, 2} {
		subtable = append(subtable, fontTestU16(value)...)
	}
	for _, code := range codes {
		subtable = append(subtable, fontTestU16(uint16(code))...)
	}
	subtable = append(subtable, 0, 0)
	for _, code := range codes {
		subtable = append(subtable, fontTestU16(uint16(code))...)
	}
	for index, code := range codes {
		delta := uint16(glyphs[index]) - uint16(code)
		if index == 1 {
			delta = 0
		}
		subtable = append(subtable, fontTestU16(delta)...)
	}
	for index := range codes {
		offset := uint16(0)
		if index == 1 {
			offset = uint16(segments*2 - index*2)
		}
		subtable = append(subtable, fontTestU16(offset)...)
	}
	subtable = append(subtable, 0, 1)
	return append([]byte{0, 0, 0, 1, 0, 3, 0, 1, 0, 0, 0, 12}, subtable...)
}

func fontTestTables(twelve, longLoca bool, metrics int) map[string][]byte {
	head := make([]byte, 54)
	copy(head, fontTestU32(0x00010000))
	copy(head[12:], fontTestU32(0x5f0f3cf5))
	copy(head[18:], fontTestU16(1000))
	if longLoca {
		copy(head[50:], fontTestU16(1))
	}
	maxp := make([]byte, 32)
	copy(maxp, fontTestU32(0x00010000))
	copy(maxp[4:], fontTestU16(6))
	hhea := make([]byte, 36)
	copy(hhea, fontTestU32(0x00010000))
	copy(hhea[4:], []byte{3, 32, 0xff, 0x38, 0, 100})
	copy(hhea[34:], fontTestU16(uint16(metrics)))
	advances := []uint16{600, 600, 650, 700, 250, 400}
	var hmtx []byte
	for index, advance := range advances {
		if index < metrics {
			hmtx = append(hmtx, fontTestU16(advance)...)
		}
		hmtx = append(hmtx, fontTestU16(uint16(index))...)
	}
	var glyf, loca []byte
	for _, glyph := range [][]byte{nil, fontTestSimple(false), fontTestSimple(true), fontTestCompound(1), nil,
		fontTestRectangle()} {
		if longLoca {
			loca = append(loca, fontTestU32(uint32(len(glyf)))...)
		} else {
			loca = append(loca, fontTestU16(uint16(len(glyf)/2))...)
		}
		glyf = append(glyf, glyph...)
	}
	if longLoca {
		loca = append(loca, fontTestU32(uint32(len(glyf)))...)
	} else {
		loca = append(loca, fontTestU16(uint16(len(glyf)/2))...)
	}
	fontName := "FixtureFont-Regular"
	var nameValue []byte
	for _, unit := range utf16.Encode([]rune(fontName)) {
		nameValue = append(nameValue, fontTestU16(unit)...)
	}
	name := []byte{0, 0, 0, 1, 0, 18, 0, 3, 0, 1, 4, 9, 0, 6}
	name = append(name, fontTestU16(uint16(len(nameValue)))...)
	name = append(name, 0, 0)
	name = append(name, nameValue...)
	kern := []byte{0, 0, 0, 1, 0, 0, 0, 20, 0, 1, 0, 1, 0, 6, 0, 0, 0, 0,
		0, 1, 0, 2, 0xff, 0xb0}
	return map[string][]byte{"head": head, "maxp": maxp, "hhea": hhea, "hmtx": hmtx,
		"loca": loca, "glyf": glyf, "cmap": fontTestCmap(twelve), "name": name, "kern": kern}
}

func fontTestBuild(tables map[string][]byte) []byte {
	var tags []string
	for tag := range tables {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	data := make([]byte, 12+len(tags)*16)
	copy(data, fontTestU32(0x00010000))
	copy(data[4:], fontTestU16(uint16(len(tags))))
	for index, tag := range tags {
		position := 12 + index*16
		copy(data[position:], tag)
		copy(data[position+8:], fontTestU32(uint32(len(data))))
		copy(data[position+12:], fontTestU32(uint32(len(tables[tag]))))
		data = append(data, tables[tag]...)
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
	}
	return data
}

func TestFontDecodeUnicodeMetricsAndOwnedQuadraticGlyphs(t *testing.T) {
	for _, twelve := range []bool{false, true} {
		for _, longLoca := range []bool{false, true} {
			data := fontTestBuild(fontTestTables(twelve, longLoca, 6))
			face, err := Decode(nil, data)
			if err != nil {
				t.Fatal(err)
			}
			clear(data)
			if face.PostScriptName != "FixtureFont-Regular" || face.UnitsPerEm != 1000 || face.NumGlyphs != 6 ||
				face.Ascender != 800 || face.Descender != -200 || face.LineGap != 100 {
				t.Fatal("owned font name or metric metadata was lost")
			}
			for character, expected := range map[rune]uint16{'A': 1, 'V': 2, 'À': 3, ' ': 4} {
				id, err := face.LookupRune(nil, character)
				if err != nil || id != expected {
					t.Fatalf("Unicode cmap character=%U id=%d: %v", character, id, err)
				}
			}
			if twelve {
				if id, err := face.LookupRune(nil, '😀'); err != nil || id != 5 {
					t.Fatalf("supplementary cmap did not preserve Unicode scalar: %v", err)
				}
			}
			if _, err := face.LookupRune(nil, 'Z'); !errors.Is(err, ErrMissingGlyph) {
				t.Fatalf("missing character silently used .notdef: %v", err)
			}
			glyph, err := face.Glyph(nil, 2)
			if err != nil || glyph.AdvanceWidth != 650 || glyph.LeftSideBearing != 2 || len(glyph.Contours) != 1 ||
				len(glyph.Path) != 3 || glyph.Path[1].Op != Quad || glyph.Path[1].ControlX != 500 ||
				glyph.Path[1].ControlY != 1000 || glyph.Path[1].X != 1000 {
				t.Fatalf("owned quadratic glyph decode: %v", err)
			}
			glyph.Contours[0][0].X = 9999
			glyph.Path[1].ControlX = 9999
			again, err := face.Glyph(nil, 2)
			if err != nil || again.Contours[0][0].X != 0 || again.Path[1].ControlX != 500 {
				t.Fatal("owned glyph edits changed the retained face")
			}
			compound, err := face.Glyph(nil, 3)
			if err != nil || len(compound.Contours) != 2 || compound.Contours[0][1].X != 260 ||
				compound.Contours[0][1].Y != 520 || compound.Contours[1][0].X != 260 ||
				compound.Contours[1][1].Y != 1520 {
				t.Fatalf("F2Dot14 scale or point matching was incorrect: %v", err)
			}
			if adjustment, err := face.Kerning(nil, 1, 2); err != nil || adjustment != -80 {
				t.Fatalf("legacy signed kern pair decode: %d %v", adjustment, err)
			}
			original, err := face.ReadSpan(nil, face.tables["head"])
			if err != nil {
				t.Fatal(err)
			}
			clear(original)
			original, err = face.ReadSpan(nil, face.tables["head"])
			if err != nil || u32(original, 12) != 0x5f0f3cf5 {
				t.Fatal("owned original-byte copy changed the font face")
			}
			if _, err := face.ReadSpan(nil, face.tables["head"], 1); !errors.Is(err, ErrLimit) {
				t.Fatal("font span read ignored its explicit byte budget")
			}
			space, err := face.Glyph(nil, 4)
			if err != nil || len(space.Contours) != 0 || space.AdvanceWidth != 250 {
				t.Fatal("empty glyph lost its advance width")
			}
		}
	}
}

func TestFontTailMetricsBoundsBudgetsCyclesAndCancellation(t *testing.T) {
	face, err := Decode(nil, fontTestBuild(fontTestTables(false, false, 3)))
	if err != nil {
		t.Fatal(err)
	}
	glyph, err := face.Glyph(nil, 4)
	if err != nil || glyph.AdvanceWidth != 650 || glyph.LeftSideBearing != 4 {
		t.Fatal("short hmtx did not reuse the final advance and read trailing bearings")
	}
	for _, limits := range []DecodeOptions{{MaxBytes: 1}, {MaxTables: 1}, {MaxGlyphs: 1}, {MaxCmapEntries: 1}} {
		if _, err := Decode(nil, fontTestBuild(fontTestTables(false, false, 6)), limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("font decode ignored limits %+v: %v", limits, err)
		}
	}
	for _, limits := range []GlyphOptions{
		{MaxWorkingBytes: 1}, {MaxPoints: 1}, {MaxContours: 1}, {MaxDepth: 1}, {MaxComponents: 1},
	} {
		if _, err := face.Glyph(nil, 3, limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("compound decode ignored limits %+v: %v", limits, err)
		}
	}
	tables := fontTestTables(false, false, 6)
	copy(tables["glyf"][60:], fontTestCompound(3))
	cyclic, err := Decode(nil, fontTestBuild(tables))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cyclic.Glyph(nil, 3); !errors.Is(err, ErrMalformed) {
		t.Fatalf("cyclic composite glyph was accepted: %v", err)
	}
	for _, change := range []func(map[string][]byte){
		func(tables map[string][]byte) { tables["head"][12] = 0 },
		func(tables map[string][]byte) { tables["loca"][4], tables["loca"][5] = 0xff, 0xff },
		func(tables map[string][]byte) { tables["hhea"][34], tables["hhea"][35] = 0, 7 },
		func(tables map[string][]byte) { tables["cmap"][30], tables["cmap"][31] = 0, 1 },
	} {
		tables := fontTestTables(false, false, 6)
		change(tables)
		if _, err := Decode(nil, fontTestBuild(tables)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed font table was accepted: %v", err)
		}
	}
	bad := fontTestBuild(fontTestTables(false, false, 6))
	binary.BigEndian.PutUint32(bad[20:], math.MaxUint32)
	if _, err := Decode(nil, bad); !errors.Is(err, ErrMalformed) {
		t.Fatal("out-of-bounds table offset was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, bad); !errors.Is(err, context.Canceled) {
		t.Fatal("font decode ignored cancellation")
	}
	if _, err := face.Glyph(ctx, 3); !errors.Is(err, context.Canceled) {
		t.Fatal("glyph decode ignored cancellation")
	}
}

func fontTestCollection(first, second []byte) []byte {
	data := []byte{'t', 't', 'c', 'f', 0, 1, 0, 0, 0, 0, 0, 2}
	data = append(data, fontTestU32(20)...)
	data = append(data, fontTestU32(uint32(20+len(first)))...)
	for _, face := range [][]byte{first, second} {
		clone := bytes.Clone(face)
		base := uint32(len(data))
		for index := 0; index < int(u16(clone, 4)); index++ {
			position := 12 + index*16 + 8
			binary.BigEndian.PutUint32(clone[position:], u32(clone, position)+base)
		}
		data = append(data, clone...)
	}
	return data
}

func TestFontSelectedCollectionFaceAndUnsupportedOutlines(t *testing.T) {
	first := fontTestBuild(fontTestTables(false, false, 6))
	tables := fontTestTables(true, true, 6)
	copy(tables["head"][18:], fontTestU16(2048))
	second := fontTestBuild(tables)
	data := fontTestCollection(first, second)
	face, err := Decode(nil, data, DecodeOptions{FaceIndex: 1})
	if err != nil || face.FaceIndex != 1 || face.UnitsPerEm != 2048 {
		t.Fatalf("selected TTC face did not use collection-absolute table offsets: %v", err)
	}
	if id, err := face.LookupRune(nil, '😀'); err != nil || id != 5 {
		t.Fatal("selected TTC face's cmap was replaced with face zero")
	}
	if _, err := Decode(nil, data, DecodeOptions{MaxFaces: 1}); !errors.Is(err, ErrLimit) {
		t.Fatal("collection count bypassed its limit")
	}
	for _, tag := range []string{"CFF ", "CFF2", "fvar", "gvar"} {
		tables := fontTestTables(false, false, 6)
		tables[tag] = []byte{0}
		if _, err := Decode(nil, fontTestBuild(tables)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported outline table %s was silently approximated: %v", tag, err)
		}
	}
}

func TestFontExplicitNativeFile(t *testing.T) {
	filename := os.Getenv("PSDKIT_TEST_FONT")
	if filename == "" {
		t.Skip("set PSDKIT_TEST_FONT to one explicitly selected TrueType file")
	}
	face, err := Open(nil, filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, character := range "AVég " {
		id, err := face.LookupRune(nil, character)
		if err != nil {
			t.Fatal(err)
		}
		glyph, err := face.Glyph(nil, id)
		if err != nil || glyph.AdvanceWidth == 0 {
			t.Fatalf("explicit native glyph %U: %v", character, err)
		}
	}
}
