package psdscene

import (
	"context"
	"errors"
	"image"
	"math"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func TestOpenTypeRecursionMapReservationArithmeticAndBoundaries(t *testing.T) {
	capacity := int64(1) << 57
	wrapped := int64(256) + capacity*128
	if wrapped != 256 || capacity <= (math.MaxInt64-256)/128 {
		t.Fatal("arithmetic-only control missed overflow")
	}
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
	maximum := int(^uint(0) >> 1)
	if int64(maximum) < capacity {
		t.Skip("overflow controls require a 64-bit platform")
	}
	for _, limit := range []int{maximum, int(capacity)} {
		engine := rgbTileRenderer{source: &psdfile.Document{}, settings: TileOptions{
			ApproximateOpenTypeText: &policy, MaxDepth: limit, MaxDocuments: limit, MaxWorkingBytes: math.MaxInt64}}
		release, err := engine.enterOpenTypeDocument()
		if release != nil || !errors.Is(err, psdfile.ErrLimit) || engine.settings.openTypeTraversal != nil ||
			engine.working != 0 {
			t.Fatal("unbounded map reservation reached allocation")
		}
	}
	for _, existing := range []int64{0, 73} {
		for _, difference := range []int64{-1, 0, 1} {
			engine := rgbTileRenderer{source: &psdfile.Document{}, working: existing, settings: TileOptions{
				ApproximateOpenTypeText: &policy, MaxDepth: 2, MaxDocuments: 3, MaxWorkingBytes: existing + 512 + difference}}
			release, err := engine.enterOpenTypeDocument()
			if difference < 0 {
				if release != nil || !errors.Is(err, psdfile.ErrLimit) || engine.settings.openTypeTraversal != nil ||
					engine.working != existing {
					t.Fatal("map byte-boundary minus one passed or changed storage")
				}
			} else {
				if release == nil || err != nil || engine.working != existing+512 ||
					engine.settings.openTypeTraversal.documents != 1 {
					t.Fatalf("exact bounded map allocation failed: %v", err)
				}
				release()
				if len(engine.settings.openTypeTraversal.active) != 0 {
					t.Fatal("active entry retained after release")
				}
			}
		}
	}
}

func TestOpenTypeRecursionPublicHugeLimitsBeforeCallbacks(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := openTypeTestPSD(t, "A", face.PostScriptName, 20, true)
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
	maximum := int(^uint(0) >> 1)
	limits := []int{maximum}
	huge := int64(1) << 57
	if int64(maximum) >= huge {
		limits = append(limits, int(huge))
	}
	for _, limit := range limits {
		source, reader := openTypeSceneSource(t, fixture, nil)
		calls := 0
		resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { calls++; return face, nil })
		settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
			MaxDepth: limit, MaxDocuments: limit}
		pixels, err := RenderTile(nil, source, image.Rect(0, 0, 1, 1), settings)
		if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 || reader.reads != 0 || reader.forbiddenHit != 0 {
			t.Fatalf("public huge recursion limits reached callbacks: reads=%d fonts=%d err=%v", reader.reads, calls, err)
		}
	}
}
