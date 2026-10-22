package psdfont

import (
	"context"
	"errors"
	"image"
	"math"
	"sync"
	"testing"
)

func fontTestResolver(t *testing.T) (*Face, FontResolver) {
	t.Helper()
	face, err := Decode(nil, fontTestBuild(fontTestTables(false, false, 6)))
	if err != nil {
		t.Fatal(err)
	}
	resolver := ResolverFunc(func(ctx context.Context, name string) (*Face, error) {
		if name != "FixtureFont-Regular" {
			return nil, ErrMissingFont
		}
		return face, ctx.Err()
	})
	return face, resolver
}

func TestLayoutLatinUTF16BreaksLegacyKerningAndOwnedCache(t *testing.T) {
	face, resolver := fontTestResolver(t)
	run, err := Layout(nil, "AV\r\nA", Style{FontName: "FixtureFont-Regular", Size: 100,
		Leading: 120, Tracking: 10, Kerning: true}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Approximate || run.UTF16Units != 5 || len(run.Glyphs) != 4 || run.AdvanceWidth != 119 ||
		run.Glyphs[0].Advance != 61 || run.Glyphs[1].X != 53 || run.Glyphs[1].Advance != 66 ||
		!run.Glyphs[2].LineBreak || run.Glyphs[2].ClusterStart != 2 || run.Glyphs[2].ClusterLength != 2 ||
		run.Glyphs[3].ClusterStart != 4 || run.Glyphs[3].X != 0 || run.Glyphs[3].Y != 120 ||
		run.Bounds != image.Rect(0, -100, 153, 120) {
		t.Fatal("Latin metrics, kerning, newline cluster positions or unhinted status differ")
	}
	if run.Glyphs[0].Glyph != run.Glyphs[3].Glyph {
		t.Fatal("repeated glyphs did not reuse the run-owned bounded outline cache")
	}
	run.Glyphs[0].Glyph.Contours[0][0].X = 999
	again, err := face.Glyph(nil, 1)
	if err != nil || again.Contours[0][0].X != 0 {
		t.Fatal("run-owned glyph cache mutated the retained face")
	}
}

func TestLayoutMissingFontsGlyphsUnsupportedScriptsAndLimits(t *testing.T) {
	_, resolver := fontTestResolver(t)
	style := Style{FontName: "FixtureFont-Regular", Size: 24}
	if _, err := Layout(nil, "A", style, nil); !errors.Is(err, ErrMissingFont) {
		t.Fatal("missing explicit font resolver was substituted")
	}
	if _, err := Layout(nil, "Z", style, resolver); !errors.Is(err, ErrMissingGlyph) {
		t.Fatal("missing character was replaced with .notdef")
	}
	for _, text := range []string{"中文", "العربية", "A\u0301", "A\tV", "A\u00ad", "😀"} {
		if _, err := Layout(nil, text, style, resolver); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("complex or unsupported text was silently approximated: %v", err)
		}
	}
	for _, limits := range []LayoutOptions{{MaxWorkingBytes: 1}, {MaxGlyphs: 1}, {OriginX: 1 << 41}} {
		if _, err := Layout(nil, "AA", style, resolver, limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("layout ignored limits %+v: %v", limits, err)
		}
	}
	style.Leading = 1 << 41
	if _, err := Layout(nil, "\n", style, resolver); !errors.Is(err, ErrLimit) {
		t.Fatal("newline-only layout bypassed the coordinate limit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Layout(ctx, "A", style, resolver); !errors.Is(err, context.Canceled) {
		t.Fatal("layout ignored pre-cancellation")
	}
	if _, err := Open(ctx, "nonexistent-explicit-font.ttf"); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled explicit Open performed filesystem work")
	}
}

func TestRasterizeRunOwnsCoverageAndPreservesQuadraticGeometry(t *testing.T) {
	_, resolver := fontTestResolver(t)
	run, err := Layout(nil, "A", Style{FontName: "FixtureFont-Regular", Size: 10}, resolver,
		LayoutOptions{OriginY: 10})
	if err != nil {
		t.Fatal(err)
	}
	bounds := image.Rect(-1, -1, 12, 12)
	alpha, err := RasterizeRun(nil, run, bounds)
	if err != nil || alpha.AlphaAt(5, 5).A != 255 || alpha.AlphaAt(0, 0).A != 0 {
		t.Fatalf("unhinted triangle coverage was incorrect: %v", err)
	}
	clear(alpha.Pix)
	again, err := RasterizeRun(nil, run, bounds)
	if err != nil || again.AlphaAt(5, 5).A != 255 || run.Glyphs[0].Glyph.Contours[0][1].X != 500 {
		t.Fatal("owned coverage mutation changed the run's geometry")
	}
	quad, err := Layout(nil, "V", Style{FontName: "FixtureFont-Regular", Size: 20}, resolver,
		LayoutOptions{OriginY: 20})
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := RasterizeRun(nil, quad, image.Rect(0, 0, 22, 22))
	if err != nil || coverage.AlphaAt(10, 15).A < 240 || coverage.AlphaAt(10, 5).A != 0 {
		t.Fatalf("quadratic-to-cubic conversion altered curve shape: %v", err)
	}
}

func TestRasterizeRunNonZeroHolesInvalidGeometryLimitsAndCancellation(t *testing.T) {
	path := []Command{
		{Op: Move}, {Op: Line, X: 10}, {Op: Line, X: 10, Y: 10}, {Op: Line, Y: 10}, {Op: Close},
		{Op: Move, X: 3, Y: 3}, {Op: Line, X: 3, Y: 7}, {Op: Line, X: 7, Y: 7},
		{Op: Line, X: 7, Y: 3}, {Op: Close},
	}
	run := &Run{Scale: 1, Approximate: true, Glyphs: []PositionedGlyph{{Y: 10, Glyph: &Glyph{Path: path}}}}
	coverage, err := RasterizeRun(nil, run, image.Rect(0, 0, 10, 10))
	if err != nil || coverage.AlphaAt(5, 5).A != 0 || coverage.AlphaAt(1, 1).A != 255 {
		t.Fatalf("glyph contour winding lost its hole: %v", err)
	}
	for _, limits := range []RasterOptions{{MaxWorkingBytes: 1}, {MaxWorkingBytes: 1024}, {MaxSegments: 1}} {
		if _, err := RasterizeRun(nil, run, image.Rect(0, 0, 10, 10), limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("coverage ignored limits %+v: %v", limits, err)
		}
	}
	_, err = RasterizeRun(nil, run, image.Rect(0, 0, 10, 10), RasterOptions{Samples: 3})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("unsupported sampling grid was silently changed")
	}
	extremeBounds := image.Rect(-int(^uint(0)>>1), 0, int(^uint(0)>>1), 0)
	if _, err := RasterizeRun(nil, run, extremeBounds); !errors.Is(err, ErrLimit) {
		t.Fatalf("empty rectangle with overflowing width was accepted: %v", err)
	}
	path[1].X = math.NaN()
	if _, err := RasterizeRun(nil, run, image.Rect(0, 0, 10, 10)); !errors.Is(err, ErrMalformed) {
		t.Fatal("NaN path geometry was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RasterizeRun(ctx, run, image.Rect(0, 0, 10, 10)); !errors.Is(err, context.Canceled) {
		t.Fatal("coverage ignored cancellation")
	}
}

func TestFontConcurrentOwnedOutlinesAndCoverage(t *testing.T) {
	face, resolver := fontTestResolver(t)
	var group sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			glyph, err := face.Glyph(nil, 3)
			if err == nil {
				glyph.Contours[0][0].X = 123
			}
			if err != nil {
				errorsFound <- err
				return
			}
			run, err := Layout(nil, "AVA", Style{FontName: "FixtureFont-Regular", Size: 10}, resolver,
				LayoutOptions{OriginY: 10})
			if err == nil {
				_, err = RasterizeRun(nil, run, image.Rect(0, 0, 30, 12))
			}
			errorsFound <- err
		})
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
}
