package psdfont

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const shapeOwnedBase = "testdata/opentype/fonts/base.ttf"

type shapeOracleGlyph struct {
	GlyphID       uint16 `json:"glyph_id"`
	ClusterStart  int64  `json:"cluster_start_utf16"`
	ClusterLength int64  `json:"cluster_length_utf16"`
	XAdvance      int64  `json:"x_advance"`
	YAdvance      int64  `json:"y_advance"`
	XOffset       int64  `json:"x_offset"`
	YOffset       int64  `json:"y_offset"`
}

type shapeOracleCase struct {
	ID            string             `json:"id"`
	Font          string             `json:"font"`
	Text          string             `json:"text"`
	Script        string             `json:"script"`
	Language      string             `json:"language"`
	Features      []ShapeFeature     `json:"features"`
	ExpectedError string             `json:"expected_error"`
	UTF16Units    int64              `json:"utf16_units"`
	Glyphs        []shapeOracleGlyph `json:"glyphs"`
}

func TestShapeOwnedHarfBuzzOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/opentype/oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"fonts"`
		Cases []shapeOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Fonts) == 0 || len(oracle.Cases) == 0 {
		t.Fatal("oracle must contain fonts and cases")
	}
	faces := make(map[string]*Face)
	sourceHashes := make(map[string][32]byte)
	for _, font := range oracle.Fonts {
		if !filepath.IsLocal(font.File) {
			t.Fatal("fixture filename must stay inside the relative testdata directory")
		}
		if faces[font.File] != nil {
			t.Fatal("duplicate fixture font")
		}
		path := filepath.Join("testdata/opentype", font.File)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(source)
		if hex.EncodeToString(sum[:]) != font.SHA256 {
			t.Fatalf("fixture font SHA mismatch: %s", font.File)
		}
		faces[font.File] = shapeTestFace(t, path)
		sourceHashes[font.File] = sum
	}
	for _, row := range oracle.Cases {
		t.Run(row.ID, func(t *testing.T) {
			face := faces[row.Font]
			if face == nil {
				t.Fatal("oracle refers to an unbound font")
			}
			run, err := Shape(context.Background(), face, row.Text, ShapeOptions{
				Script: row.Script, Language: row.Language, Features: row.Features})
			if sha256.Sum256(face.raw) != sourceHashes[row.Font] {
				t.Fatal("shaping modified the owned font")
			}
			if row.ExpectedError != "" {
				kinds := map[string]error{"unsupported": ErrUnsupported, "missing_glyph": ErrMissingGlyph,
					"malformed": ErrMalformed, "limit": ErrLimit}
				kind := kinds[row.ExpectedError]
				if kind == nil {
					t.Fatal("oracle specifies an unknown error kind")
				}
				if run != nil || !errors.Is(err, kind) {
					t.Fatalf("run=%v error=%v want %v", run, err, kind)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.UTF16Units != row.UTF16Units || len(run.Glyphs) != len(row.Glyphs) {
				t.Fatalf("UTF16Units/glyph count mismatch: actual %d/%d expected %d/%d",
					run.UTF16Units, len(run.Glyphs), row.UTF16Units, len(row.Glyphs))
			}
			for i, g := range run.Glyphs {
				actual := shapeOracleGlyph{GlyphID: g.GlyphID, ClusterStart: g.ClusterStart,
					ClusterLength: g.ClusterLength, XAdvance: g.XAdvance, YAdvance: g.YAdvance,
					XOffset: g.XOffset, YOffset: g.YOffset}
				if actual != row.Glyphs[i] {
					t.Fatalf("glyph %d actual %+v expected %+v", i, actual, row.Glyphs[i])
				}
			}
		})
	}
}

func shapeTestFace(t *testing.T, path string) *Face {
	t.Helper()
	f, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func shapeTestOptions() ShapeOptions {
	return ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}, {Tag: "kern", Enabled: true}}}
}

func copyShapeTestFace(f *Face) *Face {
	g := *f
	g.raw = append([]byte(nil), f.raw...)
	g.tables = make(map[string]Span, len(f.tables))
	maps.Copy(g.tables, f.tables)
	return &g
}

func shapeTestTable(f *Face, tag string) []byte {
	s := f.tables[tag]
	return f.raw[s.Offset : s.Offset+s.Length]
}

func shapeTestFirstLookup(table []byte) (lookup, sub int) {
	list := int(u16(table, 8))
	lookup = list + int(u16(table, list+2))
	sub = lookup + int(u16(table, lookup+6))
	return
}

func shapeTestDefaultLang(table []byte) int {
	list := int(u16(table, 4))
	for i := 0; i < int(u16(table, list)); i++ {
		pos := list + 2 + i*6
		if string(table[pos:pos+4]) == "latn" {
			script := list + int(u16(table, pos+4))
			return script + int(u16(table, script))
		}
	}
	panic("fixture has no latn/default")
}

func putShapeTest16(b []byte, at int, n uint16) { binary.BigEndian.PutUint16(b[at:], n) }

func TestShapeFailureContracts(t *testing.T) {
	base := shapeTestFace(t, shapeOwnedBase)
	tests := []struct {
		name, text string
		o          ShapeOptions
		face       *Face
		kind       error
	}{
		{"nil-face", "AV", ShapeOptions{}, nil, ErrMissingFont},
		{"invalid-UTF8", string([]byte{0xff}), ShapeOptions{}, base, ErrMalformed},
		{"unsupported-Greek-script", "Ω", ShapeOptions{}, base, ErrUnsupported},
		{"missing-Latin-glyph-zero", "Æ", ShapeOptions{}, base, ErrMissingGlyph},
		{"complex-mark", "A\u0301", ShapeOptions{}, base, ErrUnsupported},
		{"non-Latin-script-selection", "AV", ShapeOptions{Script: "arab"}, base, ErrUnsupported},
		{"explicit-absent-language", "AV", ShapeOptions{Language: "TRK "}, base, ErrUnsupported},
		{"bad-tag", "AV", ShapeOptions{Script: "lat"}, base, ErrMalformed},
		{"duplicate-features", "AV", ShapeOptions{
			Features: []ShapeFeature{{Tag: "liga"}, {Tag: "liga"}}}, base, ErrMalformed},
		{"negative-limit", "AV", ShapeOptions{MaxNodes: -1}, base, ErrLimit},
		{"bytes-before-glyph-allocation", "AV", ShapeOptions{MaxWorkingBytes: 1}, base, ErrLimit},
		{"glyph-count", "AV", ShapeOptions{MaxGlyphs: 1}, base, ErrLimit},
		{"node-count", "ffiAV", ShapeOptions{MaxNodes: 2}, base, ErrLimit},
		{"lookup-visits", "ffiAV", ShapeOptions{Features: shapeTestOptions().Features, MaxLookupVisits: 1}, base, ErrLimit},
		{"selected-flags", "fiAV", shapeTestOptions(),
			shapeTestFace(t, "testdata/opentype/fonts/selected-ignore-marks-flag.ttf"), ErrUnsupported},
		{"selected-single", "fiAV", shapeTestOptions(),
			shapeTestFace(t, "testdata/opentype/fonts/selected-single-substitution.ttf"), nil},
	}
	for _, row := range tests {
		t.Run(row.name, func(t *testing.T) {
			var before [32]byte
			if row.face != nil {
				before = sha256.Sum256(row.face.raw)
			}
			r, err := Shape(context.Background(), row.face, row.text, row.o)
			if row.kind == nil {
				if err != nil || r == nil || len(r.Glyphs) != 4 || r.UTF16Units != 4 {
					t.Fatalf("selected single substitution did not execute: %v", err)
				}
				for index, id := range []uint16{7, 8, 3, 3} {
					glyph := r.Glyphs[index]
					advance := []int64{285, 180, 600, 600}[index]
					if glyph.GlyphID != id || glyph.XAdvance != advance ||
						glyph.ClusterStart != int64(index) || glyph.ClusterLength != 1 {
						t.Fatalf("single substituted cluster%d=%+v", index, glyph)
					}
				}
				return
			}
			if r != nil || !errors.Is(err, row.kind) {
				t.Fatalf("run=%v err=%v want %v", r, err, row.kind)
			}
			if row.face != nil && sha256.Sum256(row.face.raw) != before {
				t.Fatal("error changed source")
			}
		})
	}
	for _, path := range []string{"selected-ignore-marks-flag.ttf", "selected-single-substitution.ttf"} {
		f := shapeTestFace(t, "testdata/opentype/fonts/"+path)
		if _, err := Shape(context.Background(), f, "fiAV"); err != nil {
			t.Fatalf("unselected unsupported lookup affected run: %v", err)
		}
	}
}

func TestShapeMalformedAndUnsupportedOffsets(t *testing.T) {
	base := shapeTestFace(t, shapeOwnedBase)
	tests := []struct {
		name, tag string
		mutate    func([]byte)
		kind      error
	}{
		{"coverage-bounds", "GSUB", func(b []byte) {
			_, s := shapeTestFirstLookup(b)
			putShapeTest16(b, s+2, 0xffff)
		}, ErrMalformed},
		{"lookup-offset-zero", "GSUB", func(b []byte) { l := int(u16(b, 8)); putShapeTest16(b, l+2, 0) }, ErrMalformed},
		{"required-feature-outside-list", "GSUB", func(b []byte) {
			putShapeTest16(b, shapeTestDefaultLang(b)+2, 0xfffe)
		}, ErrMalformed},
		{"reserved-lookup-flags", "GSUB", func(b []byte) {
			l, _ := shapeTestFirstLookup(b)
			putShapeTest16(b, l+2, 0x20)
		}, ErrMalformed},
		{"selected-device-format", "GPOS", func(b []byte) {
			_, s := shapeTestFirstLookup(b)
			putShapeTest16(b, s+4, 0x14)
		}, ErrUnsupported},
		{"reserved-value-format", "GPOS", func(b []byte) {
			_, s := shapeTestFirstLookup(b)
			putShapeTest16(b, s+4, 0x104)
		}, ErrMalformed},
	}
	for _, row := range tests {
		t.Run(row.name, func(t *testing.T) {
			f := copyShapeTestFace(base)
			row.mutate(shapeTestTable(f, row.tag))
			before := sha256.Sum256(f.raw)
			r, err := Shape(context.Background(), f, "ffiAV", shapeTestOptions())
			if r != nil || !errors.Is(err, row.kind) {
				t.Fatalf("run=%v err=%v want=%v", r, err, row.kind)
			}
			if sha256.Sum256(f.raw) != before {
				t.Fatal("source modified")
			}
		})
	}
	ext := shapeTestFace(t, "testdata/opentype/fonts/extension-equivalent.ttf")
	for _, tag := range []string{"GSUB", "GPOS"} {
		for _, mode := range []string{"self", "nested", "outside"} {
			t.Run(tag+"-extension-"+mode, func(t *testing.T) {
				f := copyShapeTestFace(ext)
				b := shapeTestTable(f, tag)
				l, s := shapeTestFirstLookup(b)
				switch mode {
				case "self":
					binary.BigEndian.PutUint32(b[s+4:], 0)
				case "nested":
					putShapeTest16(b, s+2, u16(b, l))
				case "outside":
					binary.BigEndian.PutUint32(b[s+4:], 0xffffffff)
				}
				r, err := Shape(context.Background(), f, "ffiAV", shapeTestOptions())
				if r != nil || !errors.Is(err, ErrMalformed) {
					t.Fatalf("run=%v err=%v", r, err)
				}
			})
		}
	}
}

func TestShapeRequiredFeatureAndOwnership(t *testing.T) {
	f := copyShapeTestFace(shapeTestFace(t, shapeOwnedBase))
	b := shapeTestTable(f, "GSUB")
	lang := shapeTestDefaultLang(b)
	fi := u16(b, lang+6)
	putShapeTest16(b, lang+2, fi)
	off, err := Shape(context.Background(), f, "fi", ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: false}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(off.Glyphs) != 1 || off.Glyphs[0].ClusterLength != 2 {
		t.Fatalf("required feature not executed: %+v", off)
	}
	first := off.Glyphs[0]
	off.Glyphs[0].GlyphID = 0
	off.Glyphs[0].ClusterLength = 99
	again, err := Shape(context.Background(), f, "fi")
	if err != nil {
		t.Fatal(err)
	}
	if again.Glyphs[0] != first {
		t.Fatal("return buffer alias")
	}

	source, err := os.ReadFile(shapeOwnedBase)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "owned.ttf")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	owned := shapeTestFace(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r, err := Shape(context.Background(), owned, "A𝼀fiV", shapeTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.UTF16Units != 6 || len(r.Glyphs) != 4 {
		t.Fatalf("supplemental or merge wrong: %+v", r)
	}
	want := [][2]int64{{0, 1}, {1, 2}, {3, 2}, {5, 1}}
	for i, g := range r.Glyphs {
		if [2]int64{g.ClusterStart, g.ClusterLength} != want[i] {
			t.Fatalf("glyph %d span %+v", i, g)
		}
	}
	if r.WorkingBytes() <= 0 {
		t.Fatal("working byte accounting absent")
	}
	closedData, err := owned.ReadSpan(context.Background(), Span{Length: int64(len(source))}, int64(len(source)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(closedData, source) {
		t.Fatal("closed source no longer owned")
	}

	decoded, err := Decode(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	source[0] ^= 0xff
	if _, err := Shape(context.Background(), decoded, "fiAV", shapeTestOptions()); err != nil {
		t.Fatal("borrowed decode source", err)
	}
}

type shapeCancelContext struct {
	calls atomic.Int64
	after int64
}

func (*shapeCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*shapeCancelContext) Done() <-chan struct{}       { return nil }
func (c *shapeCancelContext) Err() error {
	if c.calls.Add(1) >= c.after {
		return context.Canceled
	}
	return nil
}
func (*shapeCancelContext) Value(any) any { return nil }

func TestShapeCancelAndConcurrentBorrow(t *testing.T) {
	f := shapeTestFace(t, shapeOwnedBase)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Shape(ctx, f, "ffiAV", shapeTestOptions())
	if r != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel run=%v err=%v", r, err)
	}
	for _, after := range []int64{8, 64, 256} {
		c := &shapeCancelContext{after: after}
		r, err := Shape(c, f, strings.Repeat("ffiAV ", 200), shapeTestOptions())
		if r != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-cancel after%d run=%v err=%v", after, r, err)
		}
	}
	baseline, err := Shape(context.Background(), f, "ffiAV", shapeTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(f.raw)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				r, err := Shape(context.Background(), f, "ffiAV", shapeTestOptions())
				if err != nil || !reflect.DeepEqual(r.Glyphs, baseline.Glyphs) {
					t.Errorf("concurrent shape err=%v", err)
					return
				}
				r.Glyphs[0].GlyphID = 0
			}
		})
	}
	wg.Wait()
	if sha256.Sum256(f.raw) != before {
		t.Fatal("concurrent source modified")
	}
}

func TestShapeExactByteBudgetAndAbsentFeature(t *testing.T) {
	f := shapeTestFace(t, shapeOwnedBase)
	options := shapeTestOptions()
	r, err := Shape(context.Background(), f, "A𝼀ffiAV", options)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxWorkingBytes = r.WorkingBytes()
	exact, err := Shape(context.Background(), f, "A𝼀ffiAV", options)
	if err != nil || !reflect.DeepEqual(exact.Glyphs, r.Glyphs) {
		t.Fatalf("exact budget failed %v", err)
	}
	options.MaxWorkingBytes--
	short, err := Shape(context.Background(), f, "A𝼀ffiAV", options)
	if short != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("one-byte-short run=%v err=%v", short, err)
	}
	plain, err := Shape(context.Background(), f, "ffiAV")
	if err != nil {
		t.Fatal(err)
	}
	absent, err := Shape(context.Background(), f, "ffiAV", ShapeOptions{
		Features: []ShapeFeature{{Tag: "zzzz", Enabled: true}}})
	if err != nil || !reflect.DeepEqual(plain.Glyphs, absent.Glyphs) {
		t.Fatalf("absent optional feature was not no-op: %v", err)
	}
}

func TestShapeLegalZeroAndHugeLogicalMatrix(t *testing.T) {
	zero := shapeTestFace(t, "testdata/opentype/fonts/legal-ligature-glyph-zero.ttf")
	selected := ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}}}
	if r, err := Shape(context.Background(), zero, "fi", selected); r != nil || !errors.Is(err, ErrMissingGlyph) {
		t.Fatalf("legal output zero must report missing glyph when matched: run=%v err=%v", r, err)
	}
	if _, err := Shape(context.Background(), zero, "fA", selected); err != nil {
		t.Fatalf("unmatched zero output affected run: %v", err)
	}
	if _, err := Shape(context.Background(), zero, "fi"); err != nil {
		t.Fatalf("unselected zero output affected run: %v", err)
	}
	f := shapeTestFace(t, "testdata/opentype/fonts/zero-value-class-matrix.ttf")
	plain, err := Shape(context.Background(), f, "fA")
	if err != nil {
		t.Fatal(err)
	}
	selected.Features = []ShapeFeature{{Tag: "kern", Enabled: true}}
	r, err := Shape(context.Background(), f, "fA", selected)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Glyphs, plain.Glyphs) || r.Glyphs[0].XAdvance != 310 {
		t.Fatalf("zero-value first-matching class subtable failed to stop: %+v", r.Glyphs)
	}
	bad := copyShapeTestFace(f)
	b := shapeTestTable(bad, "GPOS")
	lookup, _ := shapeTestFirstLookup(b)
	sub := -1
	for i := 0; i < int(u16(b, lookup+4)); i++ {
		candidate := lookup + int(u16(b, lookup+6+2*i))
		if u16(b, candidate) == 2 && u16(b, candidate+4) == 0 && u16(b, candidate+6) == 0 {
			sub = candidate
			break
		}
	}
	if sub < 0 {
		t.Fatal("independent fixture has no zero-value class matrix")
	}
	putShapeTest16(b, sub+12, 2000)
	putShapeTest16(b, sub+14, 2000)
	if r, err := Shape(context.Background(), bad, "fA", selected); r != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("huge logical zero matrix escaped node bound: run=%v err=%v", r, err)
	}
}

func TestShapeGPOSMixedExtensionTypes(t *testing.T) {
	f := copyShapeTestFace(shapeTestFace(t, "testdata/opentype/fonts/extension-equivalent.ttf"))
	b := shapeTestTable(f, "GPOS")
	lookup, _ := shapeTestFirstLookup(b)
	if u16(b, lookup+4) < 2 {
		t.Fatal("fixture must have two extension subtables")
	}
	second := lookup + int(u16(b, lookup+8))
	putShapeTest16(b, second+2, 1)
	if r, err := Shape(context.Background(), f, "fA", shapeTestOptions()); r != nil || !errors.Is(err, ErrMalformed) {
		t.Fatalf("mixed inner lookup types: run=%v err=%v", r, err)
	}
}

func TestShapeRequiredUnsupportedAndLookupUnion(t *testing.T) {
	for _, variant := range []string{"selected-single-substitution", "selected-ignore-marks-flag"} {
		f := copyShapeTestFace(shapeTestFace(t, "testdata/opentype/fonts/"+variant+".ttf"))
		b := shapeTestTable(f, "GSUB")
		lang := shapeTestDefaultLang(b)
		putShapeTest16(b, lang+2, u16(b, lang+6))
		r, err := Shape(context.Background(), f, "fi")
		if variant == "selected-single-substitution" {
			if err != nil || r == nil || len(r.Glyphs) != 2 || r.Glyphs[0].GlyphID != 7 ||
				r.Glyphs[1].GlyphID != 8 || r.Glyphs[0].XAdvance != 310 ||
				r.Glyphs[1].XAdvance != 180 || r.UTF16Units != 2 {
				t.Fatalf("required single substitution failed: %+v %v", r, err)
			}
			continue
		}
		if r != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("required unsupported lookup silently skipped in %s: run=%v err=%v", variant, r, err)
		}
	}
	base := shapeTestFace(t, shapeOwnedBase)
	o := ShapeOptions{Features: []ShapeFeature{{Tag: "kern", Enabled: true}}}
	plain, err := Shape(context.Background(), base, "AVAV", o)
	if err != nil {
		t.Fatal(err)
	}
	f := copyShapeTestFace(base)
	b := shapeTestTable(f, "GPOS")
	lang := shapeTestDefaultLang(b)
	putShapeTest16(b, lang+2, u16(b, lang+6))
	requiredAndOptional, err := Shape(context.Background(), f, "AVAV", o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain.Glyphs, requiredAndOptional.Glyphs) {
		t.Fatal("required and optional lookup union applied pair adjustment twice")
	}
}
