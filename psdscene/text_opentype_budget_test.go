package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func TestOpenTypeTextSourceSmallBudgetFullAndTiles(t *testing.T) {
	for _, name := range []string{"base.ttf", "geometry-y-position.ttf"} {
		face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
		for _, modern := range []bool{false, true} {
			for _, size := range []float64{13.75, 96} {
				for _, samples := range []int{4, 8} {
					data := openTypeTestPSD(t, "AVfiAffl", face.PostScriptName, size, modern)
					before := sha256.Sum256(data)
					reader := &textRasterForbiddenReader{reader: bytes.NewReader(data)}
					source, err := psdfile.Read(reader, int64(len(data)))
					if err != nil {
						t.Fatal(err)
					}
					reader.forbidden = append(reader.forbidden, source.Composite.Data)
					for _, layer := range source.Layers {
						for _, channel := range layer.Channels {
							reader.forbidden = append(reader.forbidden, channel.Data)
						}
					}
					records, err := source.TextLayers(nil)
					if err != nil {
						t.Fatal(err)
					}
					settings := OpenTypeTextRenderOptions{MaxWorkingBytes: 64 << 20, Samples: samples,
						Kerning: OpenTypeTextMetrics,
						Shape:   psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
					full, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
					if err != nil {
						t.Fatal(err)
					}
					settings.MaxWorkingBytes = 1 << 20
					bounded, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
					if err != nil || bounded == nil || !bytes.Equal(full.Pix, bounded.Pix) {
						t.Fatalf("font=%s modern=%t size=%g samples=%d low-budget difference: %v",
							name, modern, size, samples, err)
					}
					for _, rectangle := range []image.Rectangle{
						image.Rect(20, 44, 28, 52), image.Rect(0, 0, 64, 96), image.Rect(64, 0, 128, 96),
						image.Rect(17, 19, 106, 75), image.Rect(127, 95, 128, 96),
					} {
						settings.Bounds = &rectangle
						tile, tileErr := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
						if tileErr != nil || tile == nil || tile.Rect != rectangle {
							t.Fatalf("small tile rejected: %v", tileErr)
						}
						for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
							for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
								if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
									t.Fatalf("tile differs from full at %d,%d", x, y)
								}
							}
						}
					}
					if sha256.Sum256(data) != before {
						t.Fatal("source bytes changed")
					}
					if err := source.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

func TestOpenTypeTextSourceSmallBudgetLimitsAndCauses(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	source, err := psdfile.Parse(openTypeTestPSD(t, "A", face.PostScriptName, 13.75, true))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := OpenTypeTextRenderOptions{MaxWorkingBytes: 1 << 20, Kerning: OpenTypeTextNone, Samples: 8}
	count := &textSourceCountContext{}
	baseline, err := RenderOpenTypeTextLayer(count, source, records[0], resolver, settings)
	if err != nil || baseline == nil {
		t.Fatalf("one-glyph 1MiB render failed: %v", err)
	}
	late := &textSourceCountContext{cancelAt: count.calls.Load()}
	partial, err := RenderOpenTypeTextLayer(late, source, records[0], resolver, settings)
	if partial != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation returned output: %v", err)
	}
	for _, canceledAt := range []int64{1, 50, 100, 500} {
		partial, err = RenderOpenTypeTextLayer(&textSourceCountContext{cancelAt: canceledAt},
			source, records[0], resolver, settings)
		if partial != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation at %d did not preserve cause: %v", canceledAt, err)
		}
	}
	cause := errors.New("owned resolver cause")
	failing := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return nil, cause })
	partial, err = RenderOpenTypeTextLayer(nil, source, records[0], failing, settings)
	if partial != nil || !errors.Is(err, cause) {
		t.Fatalf("resolver cause was lost: %v", err)
	}
	settings.Shape.MaxWorkingBytes = 1
	partial, err = RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if partial != nil || !errors.Is(err, psdfont.ErrLimit) {
		t.Fatalf("explicit shape limit bypassed: %v", err)
	}
	settings.Shape.MaxWorkingBytes = 0
	low, high := int64(1), int64(1<<20)
	for low < high {
		middle := low + (high-low)/2
		settings.MaxWorkingBytes = middle
		pixels, renderErr := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
		if renderErr == nil && pixels != nil {
			high = middle
			continue
		}
		if pixels != nil || (!errors.Is(renderErr, psdfile.ErrLimit) && !errors.Is(renderErr, psdfont.ErrLimit)) {
			t.Fatalf("unexpected minimum-budget error: %v", renderErr)
		}
		low = middle + 1
	}
	settings.MaxWorkingBytes = low
	exact, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if err != nil || exact == nil || !bytes.Equal(exact.Pix, baseline.Pix) {
		t.Fatalf("source metadata boundary failed: %v", err)
	}
	settings.MaxWorkingBytes--
	partial, err = RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if partial != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("metadata boundary minus one succeeded: %v", err)
	}
}

func TestOpenTypeTextSourceLargeRunStillUsesActualBudget(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts",
		"geometry-y-position.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	source, err := psdfile.Parse(openTypeTestPSD(t, strings.Repeat("AVfiAffl", 16), face.PostScriptName, 96, true))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	rectangle := image.Rect(20, 44, 28, 52)
	settings := OpenTypeTextRenderOptions{MaxWorkingBytes: 64 << 20, Kerning: OpenTypeTextMetrics,
		Samples: 8, Bounds: &rectangle}
	baseline, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxWorkingBytes = 4 << 20
	bounded, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if err != nil || bounded == nil || !bytes.Equal(baseline.Pix, bounded.Pix) {
		t.Fatalf("large curved run regressed: %v", err)
	}
	settings.MaxWorkingBytes = 1 << 20
	partial, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if partial != nil || (!errors.Is(err, psdfont.ErrLimit) && !errors.Is(err, psdfile.ErrLimit)) {
		t.Fatalf("oversized retained work bypassed low total budget: %v", err)
	}
}

func TestOpenTypeSceneSmallBudgetFullRegionAndVisitor(t *testing.T) {
	canvas := image.Rect(0, 0, 128, 96)
	for _, name := range []string{"base.ttf", "geometry-y-position.ttf"} {
		face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
		for _, samples := range []int{4, 8} {
			fixture := openTypeSceneWrongSavedBounds(t, openTypeTestPSD(t, "A", face.PostScriptName, 96, true))
			before := sha256.Sum256(fixture)
			source, reader := openTypeSceneSource(t, fixture, nil)
			policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, Samples: samples}
			baseline, err := Render(nil, source, Options{MaxWorkingBytes: 64 << 20,
				ApproximateOpenTypeText: &policy, FontResolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			bounded, err := Render(nil, source, Options{MaxWorkingBytes: 1 << 20,
				ApproximateOpenTypeText: &policy, FontResolver: resolver})
			if err != nil {
				t.Fatalf("1MiB full Scene rejected: %v", err)
			}
			openTypeSceneEqualWindow(t, bounded, baseline, canvas)
			settings := TileOptions{MaxWorkingBytes: 1 << 20, ApproximateOpenTypeText: &policy, FontResolver: resolver}
			for _, rectangle := range []image.Rectangle{
				image.Rect(20, 44, 28, 52), image.Rect(0, 0, 64, 96), image.Rect(64, 0, 128, 96),
			} {
				region, err := RenderTile(nil, source, rectangle, settings)
				if err != nil {
					t.Fatalf("1MiB Scene region rejected: %v", err)
				}
				openTypeSceneEqualWindow(t, region, baseline, rectangle)
			}
			visited := 0
			err = VisitTiles(nil, source, canvas, image.Pt(32, 16), func(tile *image.NRGBA) error {
				visited++
				openTypeSceneEqualWindow(t, tile, baseline, tile.Rect)
				return nil
			}, settings)
			if err != nil || visited != 24 {
				t.Fatalf("1MiB Scene visitor rejected or incomplete: %v tiles=%d", err, visited)
			}
			if reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
				t.Fatal("Scene read saved artwork or mutated source bytes")
			}
		}
	}
}
