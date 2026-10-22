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
	"strings"
	"sync"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
)

type layoutOraclePoint struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	OnCurve bool    `json:"on_curve"`
}

type layoutOracleCommand struct {
	Op       string  `json:"op"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	ControlX float64 `json:"control_x"`
	ControlY float64 `json:"control_y"`
}

type layoutOracleGlyph struct {
	ID            uint16                `json:"glyph_id"`
	ClusterStart  int64                 `json:"cluster_start_utf16"`
	ClusterLength int64                 `json:"cluster_length_utf16"`
	HmtxAdvance   uint16                `json:"hmtx_advance_width"`
	X             float64               `json:"baseline_x"`
	Y             float64               `json:"baseline_y"`
	Advance       float64               `json:"canvas_x_advance"`
	Contours      [][]layoutOraclePoint `json:"canvas_contours"`
	Path          []layoutOracleCommand `json:"canvas_path"`
}

type layoutOracleCase struct {
	ID            string         `json:"id"`
	Font          string         `json:"font"`
	Text          string         `json:"text"`
	Script        string         `json:"script"`
	Language      string         `json:"language"`
	Features      []ShapeFeature `json:"features"`
	ExpectedError string         `json:"expected_error"`
	UTF16Units    int64          `json:"utf16_units"`
	Scale         float64        `json:"scale"`
	Leading       float64        `json:"leading"`
	AdvanceWidth  float64        `json:"advance_width"`
	Bounds        [4]int         `json:"integer_control_hull_bounds"`
	Config        struct {
		Size    float64 `json:"size"`
		OriginX float64 `json:"origin_x"`
		OriginY float64 `json:"origin_y"`
	} `json:"config"`
	Glyphs []layoutOracleGlyph `json:"glyphs"`
}

func layoutTestNear(t *testing.T, name string, actual, expected float64) {
	t.Helper()
	if !finite(actual) || math.Abs(actual-expected) > 1e-12 {
		t.Fatalf("%s actual %.17g expected %.17g", name, actual, expected)
	}
}

func TestLayoutOpenTypeIndependentFontToolsOracle(t *testing.T) {
	for _, file := range []string{"layout_oracle.json", "layout_geometry_oracle.json"} {
		t.Run(file, func(t *testing.T) { testLayoutOpenTypeOracleFile(t, file) })
	}
}

func testLayoutOpenTypeOracleFile(t *testing.T, file string) {
	data, err := os.ReadFile(testfixture.Path(t, filepath.Join("psdfont", "testdata", "opentype", file)))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct {
			File string `json:"file"`
			SHA  string `json:"sha256"`
		} `json:"fonts"`
		Cases []layoutOracleCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Cases) == 0 || len(oracle.Fonts) == 0 {
		t.Fatal("empty layout oracle")
	}
	faces := make(map[string]*Face)
	for _, row := range oracle.Fonts {
		if !filepath.IsLocal(row.File) {
			t.Fatal("oracle font must be relative")
		}
		path := filepath.Join("testdata/opentype", row.File)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(source)
		if hex.EncodeToString(sum[:]) != row.SHA {
			t.Fatal("font SHA does not match oracle")
		}
		faces[row.File] = shapeTestFace(t, path)
	}
	for _, row := range oracle.Cases {
		t.Run(row.ID, func(t *testing.T) {
			face := faces[row.Font]
			if face == nil {
				t.Fatal("unbound font")
			}
			resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
			before := sha256.Sum256(face.raw)
			run, err := LayoutOpenType(context.Background(), row.Text,
				Style{FontName: face.PostScriptName, Size: row.Config.Size}, resolver,
				ShapeOptions{Script: row.Script, Language: row.Language, Features: row.Features},
				LayoutOptions{OriginX: row.Config.OriginX, OriginY: row.Config.OriginY})
			if sha256.Sum256(face.raw) != before {
				t.Fatal("layout changed source")
			}
			if row.ExpectedError != "" {
				kind := map[string]error{"unsupported": ErrUnsupported, "missing_glyph": ErrMissingGlyph}[row.ExpectedError]
				if kind == nil || run != nil || !errors.Is(err, kind) {
					t.Fatalf("run=%v error=%v", run, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !run.Approximate || run.UTF16Units != row.UTF16Units || len(run.Glyphs) != len(row.Glyphs) {
				t.Fatal("layout status, UTF16 units or glyph count differs")
			}
			layoutTestNear(t, "scale", run.Scale, row.Scale)
			layoutTestNear(t, "leading", run.Leading, row.Leading)
			layoutTestNear(t, "advance width", run.AdvanceWidth, row.AdvanceWidth)
			if run.Bounds != image.Rect(row.Bounds[0], row.Bounds[1], row.Bounds[2], row.Bounds[3]) {
				t.Fatalf("bounds actual %v expected %v", run.Bounds, row.Bounds)
			}
			for i, expected := range row.Glyphs {
				g := run.Glyphs[i]
				if g.LineBreak || g.Glyph.ID != expected.ID || g.Glyph.AdvanceWidth != expected.HmtxAdvance ||
					g.ClusterStart != expected.ClusterStart || g.ClusterLength != expected.ClusterLength {
					t.Fatalf("glyph %d ID/metrics/UTF16 span differs", i)
				}
				layoutTestNear(t, "baseline X", g.X, expected.X)
				layoutTestNear(t, "baseline Y", g.Y, expected.Y)
				layoutTestNear(t, "advance", g.Advance, expected.Advance)
				if len(g.Glyph.Contours) != len(expected.Contours) || len(g.Glyph.Path) != len(expected.Path) {
					t.Fatal("glyph contour/path count differs")
				}
				for j, contour := range g.Glyph.Contours {
					if len(contour) != len(expected.Contours[j]) {
						t.Fatal("contour length differs")
					}
					for k, point := range contour {
						want := expected.Contours[j][k]
						layoutTestNear(t, "canvas point X", g.X+point.X*run.Scale, want.X)
						layoutTestNear(t, "canvas point Y", g.Y-point.Y*run.Scale, want.Y)
						if point.OnCurve != want.OnCurve {
							t.Fatal("curve point kind differs")
						}
					}
				}
				for j, command := range g.Glyph.Path {
					want := expected.Path[j]
					op := map[PathOp]string{Move: "Move", Line: "Line", Quad: "Quad", Close: "Close"}[command.Op]
					if op != want.Op {
						t.Fatalf("command %d op %s expected %s", j, op, want.Op)
					}
					if command.Op == Close {
						continue
					}
					layoutTestNear(t, "canvas path X", g.X+command.X*run.Scale, want.X)
					layoutTestNear(t, "canvas path Y", g.Y-command.Y*run.Scale, want.Y)
					if command.Op == Quad {
						layoutTestNear(t, "canvas control X", g.X+command.ControlX*run.Scale, want.ControlX)
						layoutTestNear(t, "canvas control Y", g.Y-command.ControlY*run.Scale, want.ControlY)
					}
				}
			}
		})
	}
}

func layoutTestResolver(t *testing.T) (*Face, FontResolver, Style) {
	t.Helper()
	face := shapeTestFace(t, shapeOwnedBase)
	resolver := ResolverFunc(func(ctx context.Context, name string) (*Face, error) { return face, ctx.Err() })
	return face, resolver, Style{FontName: face.PostScriptName, Size: 31}
}

func TestLayoutOpenTypeContractsAndLateCacheBudget(t *testing.T) {
	face, resolver, style := layoutTestResolver(t)
	o := shapeTestOptions()
	for _, tracking := range []float64{1, -1} {
		s := style
		s.Tracking = tracking
		if r, err := LayoutOpenType(nil, "fi", s, resolver, o); r != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatal("nonzero tracking silently accepted")
		}
	}
	s := style
	s.Kerning = true
	if r, err := LayoutOpenType(nil, "AV", s, resolver, o); r != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatal("ambiguous legacy kerning silently accepted")
	}
	for _, text := range []string{"A\nV", "A\rV", "A\r\nV", "A\u0301", "中文"} {
		if r, err := LayoutOpenType(nil, text, style, resolver, o); r != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatal("unsupported line/script silently accepted")
		}
	}
	for _, limits := range []LayoutOptions{{MaxGlyphs: 1}, {MaxWorkingBytes: 1}, {OriginX: 1 << 41},
		{GlyphLimits: GlyphOptions{MaxWorkingBytes: 1}}, {GlyphLimits: GlyphOptions{MaxPoints: -1}}} {
		if r, err := LayoutOpenType(nil, "ffiAV", style, resolver, o, limits); r != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("limits %+v run=%v err=%v", limits, r, err)
		}
	}
	before := sha256.Sum256(face.raw)
	plain := ShapeOptions{}
	one, err := LayoutOpenType(nil, "AAAAAAAA", style, resolver, plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range one.Glyphs {
		if p.Glyph != one.Glyphs[0].Glyph {
			t.Fatal("repeated outline was decoded twice")
		}
	}
	for _, text := range []string{"AV", "AVfi", "AVfia"} {
		full, err := LayoutOpenType(nil, text, style, resolver, plain)
		if err != nil {
			t.Fatal(err)
		}
		exact := LayoutOptions{MaxWorkingBytes: full.reservation}
		if r, err := LayoutOpenType(nil, text, style, resolver, plain, exact); err != nil || r == nil {
			t.Fatalf("exact phase-peak budget failed: text=%s err=%v", text, err)
		}
		exact.MaxWorkingBytes--
		r, err := LayoutOpenType(nil, text, style, resolver, plain, exact)
		if r != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("one byte short not rejected: text=%s err=%v", text, err)
		}
		var field *Error
		if !errors.As(err, &field) || field.Path != "glyph.memory" {
			t.Fatalf("late multi-unique budget did not fail before outline allocation: text=%s err=%v", text, err)
		}
	}
	if sha256.Sum256(face.raw) != before {
		t.Fatal("budget failures modified face")
	}
}

func TestLayoutOpenTypeStyleResolverAndPrecisionGuards(t *testing.T) {
	face, resolver, style := layoutTestResolver(t)
	for _, bad := range []Style{
		{FontName: style.FontName, Size: math.NaN()}, {FontName: style.FontName, Size: math.Inf(1)},
		{FontName: style.FontName, Size: -1}, {FontName: style.FontName, Size: 31, Leading: -1},
		{FontName: style.FontName, Size: 31, Tracking: math.Inf(1)},
		{FontName: style.FontName, Size: math.SmallestNonzeroFloat64},
	} {
		if r, err := LayoutOpenType(nil, "AV", bad, resolver, ShapeOptions{}); r != nil ||
			!errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid style accepted: %+v err=%v", bad, err)
		}
	}
	for _, limits := range []LayoutOptions{{OriginX: math.NaN()}, {OriginY: math.Inf(1)}} {
		if r, err := LayoutOpenType(nil, "", style, resolver, ShapeOptions{}, limits); r != nil ||
			!errors.Is(err, ErrMalformed) {
			t.Fatal("nonfinite origin accepted")
		}
	}
	bad := style
	bad.Leading = 1 << 41
	if r, err := LayoutOpenType(nil, "", bad, resolver, ShapeOptions{}); r != nil || !errors.Is(err, ErrLimit) {
		t.Fatal("empty run bypassed leading precision limit")
	}
	bad = style
	bad.Size = 1e308
	bad.Leading = 1
	if r, err := LayoutOpenType(nil, "A", bad, resolver, ShapeOptions{}); r != nil || !errors.Is(err, ErrLimit) {
		t.Fatal("transformed outline/advance overflow accepted")
	}
	for _, r := range []FontResolver{nil, ResolverFunc(func(context.Context, string) (*Face, error) { return nil, nil })} {
		if run, err := LayoutOpenType(nil, "A", style, r, ShapeOptions{}); run != nil || !errors.Is(err, ErrMissingFont) {
			t.Fatal("missing exact font was substituted")
		}
	}
	bad = style
	bad.FontName = "another-font"
	if r, err := LayoutOpenType(nil, "A", bad, resolver, ShapeOptions{}); r != nil || !errors.Is(err, ErrMissingFont) {
		t.Fatal("wrong PostScript name accepted")
	}
	copyFace := *face
	copyFace.UnitsPerEm = 0
	badResolver := ResolverFunc(func(context.Context, string) (*Face, error) { return &copyFace, nil })
	if r, err := LayoutOpenType(nil, "A", style, badResolver, ShapeOptions{}); r != nil || !errors.Is(err, ErrMalformed) {
		t.Fatal("bad design scale accepted")
	}
	r, err := LayoutOpenType(nil, "A", style, resolver, ShapeOptions{}, LayoutOptions{}, LayoutOptions{})
	if r != nil || !errors.Is(err, ErrMalformed) {
		t.Fatal("extra options accepted")
	}
}

func TestLayoutOpenTypeNeverFallsBackToLegacyKerning(t *testing.T) {
	_, resolver := fontTestResolver(t)
	style := Style{FontName: "FixtureFont-Regular", Size: 100}
	run, err := LayoutOpenType(nil, "AV", style, resolver, ShapeOptions{
		Features: []ShapeFeature{{Tag: "kern", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.AdvanceWidth != 125 || run.Glyphs[0].Advance != 60 || run.Glyphs[1].X != 60 {
		t.Fatal("missing OpenType kern feature fell back to legacy kern")
	}
	legacy, err := Layout(nil, "AV", Style{FontName: style.FontName, Size: 100, Kerning: true}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.AdvanceWidth != 117 || legacy.Glyphs[1].X != 52 {
		t.Fatal("old legacy Layout behavior changed")
	}
}

func TestLayoutOpenTypeOwnershipCancelAndRasterCompatibility(t *testing.T) {
	face, resolver, style := layoutTestResolver(t)
	source, err := os.ReadFile(shapeOwnedBase)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "font.ttf")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	owned := shapeTestFace(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	closedResolver := ResolverFunc(func(context.Context, string) (*Face, error) { return owned, nil })
	run, err := LayoutOpenType(nil, "A𝼀fiV", style, closedResolver, shapeTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if run.UTF16Units != 6 || len(run.Glyphs) != 4 || run.Glyphs[2].ClusterLength != 2 {
		t.Fatal("supplemental/ligature spans changed")
	}
	run.Glyphs[0].Glyph.Contours[0][0].X = 987
	again, err := LayoutOpenType(nil, "A𝼀fiV", style, closedResolver, shapeTestOptions())
	if err != nil || again.Glyphs[0].Glyph.Contours[0][0].X == 987 {
		t.Fatal("returned outline aliased source/next run")
	}
	if _, err := RasterizeRun(nil, again, again.Bounds); err != nil {
		t.Fatal("existing approximate raster cannot consume Run", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := LayoutOpenType(ctx, "fiAV", style, resolver, shapeTestOptions()); r != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancel ignored")
	}
	for _, after := range []int64{16, 128, 1024} {
		ctx := &shapeCancelContext{after: after}
		if r, err := LayoutOpenType(ctx, strings.Repeat("ffiAV ", 300), style, resolver, shapeTestOptions()); r != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatal("mid-pipeline cancel ignored")
		}
	}
	base, err := LayoutOpenType(nil, "fiAV", style, resolver, shapeTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(face.raw)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r, err := LayoutOpenType(nil, "fiAV", style, resolver, shapeTestOptions())
			if err != nil || !reflect.DeepEqual(r, base) {
				t.Errorf("concurrent run differs: %v", err)
				return
			}
			r.Glyphs[0].Glyph.Contours[0][0].X = 777
		})
	}
	wg.Wait()
	if sha256.Sum256(face.raw) != before {
		t.Fatal("concurrent calls modified source")
	}
}
