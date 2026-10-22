package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func fauxAdvanceControlRun() *psdfont.Run {
	space := &psdfont.Glyph{ID: 1, AdvanceWidth: 250}
	letterA := &psdfont.Glyph{ID: 2, AdvanceWidth: 600}
	letterV := &psdfont.Glyph{ID: 3, AdvanceWidth: 600}
	return &psdfont.Run{Scale: .02, AdvanceWidth: 34, UTF16Units: 4, Glyphs: []psdfont.PositionedGlyph{
		{Rune: ' ', X: 0, Advance: 5, ClusterStart: 0, ClusterLength: 1, Glyph: space},
		{Rune: 'A', X: 5, Advance: 12, ClusterStart: 1, ClusterLength: 1, Glyph: letterA},
		{Rune: 'V', X: 17, Advance: 12, ClusterStart: 2, ClusterLength: 1, Glyph: letterV},
		{Rune: ' ', X: 29, Advance: 5, ClusterStart: 3, ClusterLength: 1, Glyph: space},
	}}
}

func TestOpenTypeFauxAdvanceCountsSourceSpacesAndFinalGlyph(t *testing.T) {
	for _, control := range []struct {
		name    string
		align   int64
		origins [4]float64
	}{
		{name: "left", origins: [4]float64{0, 46.0 / 9, 155.0 / 9, 88.0 / 3}},
		{name: "center", align: 2, origins: [4]float64{-157.0 / 9, -111.0 / 9, -2.0 / 9, 107.0 / 9}},
	} {
		t.Run(control.name, func(t *testing.T) {
			run := fauxAdvanceControlRun()
			glyphs := [3]*psdfont.Glyph{run.Glyphs[0].Glyph, run.Glyphs[1].Glyph, run.Glyphs[2].Glyph}
			// Four 5/9px synthetic advances and three -4/9px tracking gaps turn 34px into 314/9px.
			err := positionOpenTypePointLineWithAdvance(nil, run,
				openTypePointLine{justification: control.align, start: 9}, -200.0/9, 20, 5.0/9)
			if err != nil || math.Abs(run.AdvanceWidth-314.0/9) > 1e-12 {
				t.Fatalf("synthetic width %.17g: %v", run.AdvanceWidth, err)
			}
			advances := [4]float64{46.0 / 9, 109.0 / 9, 109.0 / 9, 50.0 / 9}
			for index, glyph := range run.Glyphs {
				if math.Abs(glyph.X-control.origins[index]) > 1e-12 ||
					math.Abs(glyph.Advance-advances[index]) > 1e-12 || glyph.ClusterStart != int64(index+9) {
					t.Fatalf("glyph %d: %+v; want origin %.17g", index, glyph, control.origins[index])
				}
			}
			if glyphs[0].AdvanceWidth != 250 || glyphs[1].AdvanceWidth != 600 || glyphs[2].AdvanceWidth != 600 ||
				run.Glyphs[0].Glyph != run.Glyphs[3].Glyph {
				t.Fatal("synthetic placement modified or replaced shared font hmtx metrics")
			}
		})
	}
}

func TestOpenTypeFauxAdvanceZeroRetainsExistingPointLayout(t *testing.T) {
	for _, alignment := range []int64{0, 2} {
		legacy := fauxAdvanceControlRun()
		candidate := fauxAdvanceControlRun()
		line := openTypePointLine{start: 5, justification: alignment}
		if err := positionOpenTypePointLine(nil, legacy, line, -25, 20); err != nil {
			t.Fatal(err)
		}
		if err := positionOpenTypePointLineWithAdvance(nil, candidate, line, -25, 20, 0); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(legacy, candidate) {
			t.Fatal("zero synthetic advance changed existing point layout")
		}
	}
}

func TestOpenTypeFauxAdvancePreflightLeavesRunUnchanged(t *testing.T) {
	for _, control := range []struct {
		name   string
		value  float64
		change func(*psdfont.Run)
		want   error
	}{
		{name: "negative", value: -1, want: psdfile.ErrMalformed},
		{name: "nonfinite", value: math.Inf(1), want: psdfile.ErrMalformed},
		{name: "width", value: 1 << 40, want: psdfile.ErrLimit},
		{name: "lastOrigin", value: .5, change: func(run *psdfont.Run) {
			run.Glyphs[3].X = 1 << 41
		}, want: psdfile.ErrLimit},
		{name: "lastCluster", value: .5, change: func(run *psdfont.Run) {
			run.Glyphs[3].ClusterStart = math.MaxInt64
		}, want: psdfile.ErrLimit},
	} {
		t.Run(control.name, func(t *testing.T) {
			run := fauxAdvanceControlRun()
			if control.change != nil {
				control.change(run)
			}
			before := *run
			before.Glyphs = append([]psdfont.PositionedGlyph(nil), run.Glyphs...)
			err := positionOpenTypePointLineWithAdvance(nil, run,
				openTypePointLine{start: 9, justification: 2}, -25, 20, control.value)
			if !errors.Is(err, control.want) || !reflect.DeepEqual(before, *run) {
				t.Fatalf("failed preflight modified run: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := fauxAdvanceControlRun()
	before := *run
	before.Glyphs = append([]psdfont.PositionedGlyph(nil), run.Glyphs...)
	err := positionOpenTypePointLineWithAdvance(ctx, run, openTypePointLine{justification: 2}, -25, 20, .5)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(before, *run) {
		t.Fatalf("pre-cancellation modified run: %v", err)
	}
}

func TestOpenTypeFauxAdvancePolicyPreservesNonboldAndDefaultSourceRendering(t *testing.T) {
	face, resolver := typographyTestFont(t)
	for _, bold := range []bool{false, true} {
		text := " AV \rVA"
		fixture := openTypeSceneWrongSavedBounds(t, openTypeTestPSDExtra(t, text, face.PostScriptName, 20,
			true, "", typographyTestEngine(t, text, face.PostScriptName,
				typographyTestSettings{tracking: -25, leading: 24, align: 2, bold: bold})))
		physical, err := psdfile.Parse(fixture)
		if err != nil {
			t.Fatal(err)
		}
		records, err := physical.TextLayers(nil)
		if err != nil || len(records) != 1 {
			t.Fatal(err)
		}
		prefixOffset := records[0].Prefix.Offset
		if err := physical.Close(); err != nil {
			t.Fatal(err)
		}
		transform := [6]float64{1.125, .1875, -.125, .875, 61.3125, 42.6875}
		for index, value := range transform {
			at := prefixOffset + 2 + int64(index*8)
			binary.BigEndian.PutUint64(fixture[at:at+8], math.Float64bits(value))
		}
		before := sha256.Sum256(fixture)
		source, reader := openTypeSceneSource(t, fixture, nil)
		records, err = source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		legacy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
		baseline, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, legacy)
		if err != nil {
			t.Fatal(err)
		}
		explicitLegacy := legacy
		explicitLegacy.FauxBold = OpenTypeFauxBoldOutline
		unchanged, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, explicitLegacy)
		if err != nil || !bytes.Equal(baseline.Pix, unchanged.Pix) {
			t.Fatalf("explicit legacy policy changed default pixels: %v", err)
		}
		expanded := legacy
		expanded.FauxBold = OpenTypeFauxBoldExpandedAdvance
		full, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, expanded)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(baseline.Pix, full.Pix) == bold {
			t.Fatalf("expanded policy must affect only source FauxBold=%v", bold)
		}
		painted := false
		for index := 3; index < len(full.Pix); index += 4 {
			painted = painted || full.Pix[index] != 0
		}
		if !painted {
			t.Fatal("affine source text produced no live glyphs")
		}
		for _, window := range []image.Rectangle{image.Rect(25, 25, 65, 48), image.Rect(54, 36, 90, 72)} {
			regional := expanded
			regional.Bounds = &window
			pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, regional)
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, pixels, full, window)
		}
		if reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
			t.Fatal("faux-bold rendering read saved pixels or changed the original affine source")
		}
	}
}

func TestOpenTypeFauxAdvancePolicyKeepsWholeLayerLimitsAndEarlyGuards(t *testing.T) {
	face, resolver := typographyTestFont(t)
	text := " AV \rVA"
	fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 20, true, "",
		typographyTestEngine(t, text, face.PostScriptName,
			typographyTestSettings{tracking: -25, leading: 24, align: 2, bold: true}))
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		calls++
		return resolver.ResolveFont(ctx, name)
	})
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true,
		FauxBold: OpenTypeFauxBoldExpandedAdvance, MaxGlyphs: 6}
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy); pixels != nil ||
		!errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatalf("paragraph breaks and spaces must consume whole-layer quota before font resolve: %v", err)
	}
	policy.MaxGlyphs, policy.MaxWorkingBytes = 0, 16384
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy); pixels != nil ||
		!errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatalf("early allocation quota failed: %v", err)
	}
	policy.MaxWorkingBytes = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pixels, err := RenderOpenTypeTextLayer(ctx, source, records[0], counted, policy); pixels != nil ||
		!errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("pre-cancellation reached font resolve: %v", err)
	}
	for _, control := range []OpenTypeTextRenderOptions{
		{Kerning: OpenTypeTextNone, FauxBold: OpenTypeFauxBoldExpandedAdvance},
		{Kerning: OpenTypeTextNone, FauxBold: OpenTypeFauxBoldPolicy(2), ApproximateTypography: true},
		{Kerning: OpenTypeTextNone, FauxBold: OpenTypeFauxBoldExpandedAdvance, ApproximateTypography: true,
			StyleBoundaries: OpenTypeCompatibleStyleRuns},
	} {
		if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, control); pixels != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) || calls != 0 {
			t.Fatalf("invalid synthetic policy reached source/font work: %v", err)
		}
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("synthetic source guards read saved glyphs")
	}
}
