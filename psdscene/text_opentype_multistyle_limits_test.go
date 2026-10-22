package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func TestOpenTypeMultiStyleRetainedBudgetBoundaryAndFinalCancel(t *testing.T) {
	cases, resolver := multiStyleOracle(t)
	for _, row := range cases {
		if !strings.HasPrefix(row.Label, "mixed-font-size") {
			continue
		}
		source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		policy := multiStyleCaseOptions(row)
		counter := &textSourceCountContext{}
		full, err := RenderOpenTypeTextLayer(counter, source, records[0], resolver, policy)
		if err != nil || full == nil {
			t.Fatal(err)
		}
		for _, distance := range []int64{0, 1, 2, 17} {
			late := &textSourceCountContext{cancelAt: counter.calls.Load() - distance}
			pixels, err := RenderOpenTypeTextLayer(late, source, records[0], resolver, policy)
			if pixels != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("late cancellation retained partial pixels: %v", err)
			}
		}
		lower, upper := int64(1), int64(8<<20)
		for lower < upper {
			middle := lower + (upper-lower)/2
			policy.MaxWorkingBytes = middle
			pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
			if err == nil {
				if pixels == nil || !bytes.Equal(pixels.Pix, full.Pix) {
					t.Fatal("budget changed rendered pixels")
				}
				upper = middle
			} else {
				if pixels != nil || !errors.Is(err, psdfile.ErrLimit) && !errors.Is(err, psdfont.ErrLimit) &&
					!errors.Is(err, psdtext.ErrLimit) {
					t.Fatalf("unexpected bounded render error: %v", err)
				}
				lower = middle + 1
			}
		}
		for _, delta := range []int64{-1, 0, 1} {
			policy.MaxWorkingBytes = lower + delta
			pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
			if delta < 0 {
				if pixels != nil || err == nil {
					t.Fatal("one-byte-short retained budget succeeded")
				}
			} else if err != nil || pixels == nil || !bytes.Equal(pixels.Pix, full.Pix) {
				t.Fatalf("tight retained buffer budget failed: %v", err)
			}
		}
		t.Logf("policy %d tight public working-byte threshold %d", row.Policy, lower)
		calls := 0
		counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
			calls++
			return resolver.ResolveFont(ctx, name)
		})
		policy.MaxWorkingBytes = 32
		reader.reads = 0
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
		if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || reader.reads != 0 {
			t.Fatal("output preflight reached source or font callback")
		}
		if reader.forbiddenHit != 0 {
			t.Fatal("budget or cancellation used saved artwork")
		}
	}
}

func TestOpenTypeMultiStyleOffROIGlobalValidationAndOwnedCallbacks(t *testing.T) {
	cases, resolver := multiStyleOracle(t)
	row := cases[0]
	row.Text = strings.Repeat("A", 256)
	row.Ends, row.Fonts, row.Sizes, row.Colors = nil, nil, nil, nil
	for index := 0; index < len(row.Text); index++ {
		end := int64(index + 1)
		if index == len(row.Text)-1 {
			end++
		}
		row.Ends = append(row.Ends, end)
		row.Fonts = append(row.Fonts, cases[0].Fonts[0])
		row.Sizes = append(row.Sizes, 20)
		row.Colors = append(row.Colors, cases[0].Colors[index%2])
	}
	source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := multiStyleCaseOptions(row)
	policy.MaxWorkingBytes = 64 << 20
	bounds := image.Rect(19, 34, 45, 54)
	policy.Bounds = &bounds
	calls := 0
	counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		calls++
		return resolver.ResolveFont(ctx, name)
	})
	full, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
	if err != nil || full == nil || calls != 256 {
		t.Fatalf("bounded ROI did not globally resolve all source fonts: calls=%d err=%v", calls, err)
	}
	// Caller mutation starts at the first metadata ReaderAt, before the first font callback.
	reader.hook = func() {
		policy.Shape.Features[0].Enabled = true
		bounds = image.Rect(0, 0, 1, 1)
	}
	owned, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
	if err != nil || owned == nil || owned.Rect != full.Rect || !bytes.Equal(owned.Pix, full.Pix) {
		t.Fatalf("callback mutation altered copied Features or Bounds: %v", err)
	}
	reader.hook = nil
	cause := errors.New("last off-ROI style font cause")
	calls = 0
	lateFont := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		calls++
		if calls == 256 {
			return nil, cause
		}
		return resolver.ResolveFont(ctx, name)
	})
	policy.Shape.Features[0].Enabled = false
	bounds = full.Rect
	missing, err := RenderOpenTypeTextLayer(nil, source, records[0], lateFont, policy)
	if missing != nil || !errors.Is(err, cause) || calls != 256 {
		t.Fatal("off-ROI source font was skipped or its cause was replaced")
	}
	policy.MaxGlyphs = 255
	calls = 0
	limited, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
	if limited != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatal("whole source glyph preflight used per-style limits")
	}
	policy.MaxGlyphs = 10000
	policy.Shape.MaxGlyphs = 255
	calls = 0
	limited, err = RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
	if limited != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatal("Shape glyph limit reset at each source style")
	}
	policy.Shape.MaxGlyphs = 0
	for _, quota := range []int{255, 256, 257} {
		policy.Shape.MaxLookupVisits = quota
		calls = 0
		result, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
		if quota < 256 {
			if result != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 {
				t.Fatal("zero lookup share restored a large per-style default")
			}
		} else if err != nil || result == nil || calls != 256 || !bytes.Equal(result.Pix, full.Pix) {
			t.Fatalf("one lookup per source style changed off-ROI output: quota=%d calls=%d err=%v", quota, calls, err)
		}
		if policy.Shape.MaxLookupVisits != quota {
			t.Fatal("renderer modified caller shaping quota")
		}
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("off-ROI stress read saved glyph pixels")
	}
}

func TestOpenTypeMultiStyleZeroSharedShapeQuotaNeverUsesDefault(t *testing.T) {
	cases, resolver := multiStyleOracle(t)
	row := cases[0]
	source, _ := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"nodes", "visits"} {
		for _, quota := range []int{1, 2, 3} {
			policy := multiStyleCaseOptions(row)
			if field == "nodes" {
				policy.Shape.MaxNodes = quota
			} else {
				policy.Shape.MaxLookupVisits = quota
			}
			calls := 0
			counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
				calls++
				return resolver.ResolveFont(ctx, name)
			})
			pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
			if quota == 1 {
				if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 {
					t.Fatal("impossible shared quota reached font callbacks")
				}
			} else if field == "nodes" {
				if pixels != nil || !errors.Is(err, psdfont.ErrLimit) || calls != 1 {
					t.Fatalf("nonzero tight node share restored defaults: calls=%d err=%v", calls, err)
				}
			} else if err != nil || pixels == nil || calls != 2 {
				t.Fatalf("nonzero lookup share lost valid no-lookup source: calls=%d err=%v", calls, err)
			}
		}
	}
}
