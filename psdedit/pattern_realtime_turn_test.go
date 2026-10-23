package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func patternRealtimeTurnExpected(scale float64, phase psdpattern.Phase) *image.NRGBA {
	reds := [][]byte{{25}}
	switch scale {
	case 100:
		reds = [][]byte{{10, 20}, {30, 40}}
	case 200:
		reds = [][]byte{{10, 15, 20, 15}, {20, 25, 30, 25}, {30, 35, 40, 35}, {20, 25, 30, 25}}
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	period := len(reds)
	for y := range 4 {
		for x := range 4 {
			column := ((x-int(phase.X))%period + period) % period
			row := ((y-int(phase.Y))%period + period) % period
			red := reds[row][column]
			pixels.SetNRGBA(x, y, color.NRGBA{R: red, G: red + 40, B: red + 80, A: 255})
		}
	}
	return pixels
}

func assertPatternRealtimeTurnSaved(
	t *testing.T, saved *psdfile.Document, fill psdpattern.Fill, expected *image.NRGBA,
) {
	t.Helper()
	view := patternEditRead(t, saved)
	if !reflect.DeepEqual(*view.Fill, fill) {
		t.Fatalf("saved pattern controls were normalized or changed: got %+v, want %+v", *view.Fill, fill)
	}
	var planes []byte
	for component := range 3 {
		for pixel := range 16 {
			planes = append(planes, expected.Pix[pixel*4+component])
		}
	}
	compositeExpect(t, saved, planes)
	live, err := psdscene.Render(nil, saved)
	if err != nil || live.Rect != expected.Rect || !bytes.Equal(live.Pix, expected.Pix) {
		t.Fatalf("reopened live pattern differs from independent axis samples: %v", err)
	}
}

func TestPatternRealtimeWholeTurnEditAutoSaveReopenAllCompressions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, scale := range []float64{50, 100, 200} {
			for _, angle := range []float64{-720, -360, 360, 720} {
				editor := patternEditSource(t, version, patternEditDescriptor(), nil, nil)
				original, err := editor.source.ReadSpan(editor.source.Source)
				if err != nil {
					t.Fatal(err)
				}
				fill := *patternEditRead(t, editor.source).Fill
				fill.Scale, fill.Angle, fill.Phase = scale, angle, psdpattern.Phase{X: 1, Y: -1}
				if err := editor.SetPatternFill(nil, 1, fill); err != nil {
					t.Fatalf("strict edit rejected scale %g, equivalent angle %g: %v", scale, angle, err)
				}
				expected := patternRealtimeTurnExpected(scale, fill.Phase)
				live, err := editor.Composite(nil)
				if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
					t.Fatalf("edited whole-turn pattern did not regenerate exact axis samples: %v", err)
				}
				before, _ := editor.Changes()
				owned := editor.ownedBytesLocked(nil)
				if err := editor.SetPatternFill(nil, 1, fill); err != nil {
					t.Fatal(err)
				}
				if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned {
					t.Fatal("identical whole-turn edit accumulated state")
				}
				for compression := range uint16(4) {
					saved := compositeSave(t, editor, SaveOptions{Compression: compression})
					if saved.Header.Version != version {
						t.Fatal("automatic pattern save changed PSD/PSB format")
					}
					assertPatternRealtimeTurnSaved(t, saved, fill, expected)
					reopened, err := New(saved)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reopened.Close() })
					changed := fill
					changed.Angle, changed.Phase = -angle, psdpattern.Phase{X: -2, Y: 3}
					if err := reopened.SetPatternFill(nil, 1, changed); err != nil {
						t.Fatalf("reopened whole-turn descriptor was not editable: %v", err)
					}
					second := compositeSave(t, reopened, SaveOptions{Compression: compression})
					assertPatternRealtimeTurnSaved(t, second, changed,
						patternRealtimeTurnExpected(scale, changed.Phase))
				}
				unchanged, err := editor.source.ReadSpan(editor.source.Source)
				if err != nil || !bytes.Equal(unchanged, original) {
					t.Fatalf("pattern edit or automatic save changed the immutable source: %v", err)
				}
			}
		}
	}
}

func TestPatternRealtimeWholeTurnsRejectNearAnglesAndKeepTransactionalGuards(t *testing.T) {
	editor := patternEditSource(t, 1, patternEditDescriptor(), nil, nil)
	fill := *patternEditRead(t, editor.source).Fill
	fill.Scale, fill.Angle = 50, 360
	if err := editor.SetPatternFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	oldFill, oldTile := node.pattern, node.patternTile
	before, _ := editor.Changes()
	for _, angle := range []float64{math.Nextafter(360, 0), 90, -450} {
		invalid := fill
		invalid.Angle = angle
		if err := editor.SetPatternFill(nil, 1, invalid); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("strict whole-turn editor accepted other angles: %v", err)
		}
	}
	fill.Angle = -360
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetPatternFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("whole-turn edit ignored cancellation: %v", err)
	}
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	owned := editor.ownedBytesLocked(nil)
	if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("whole-turn edit escaped replacement peak budget: %v", err)
	}
	if after, _ := editor.Changes(); before != after || node.pattern != oldFill || node.patternTile != oldTile ||
		editor.ownedBytesLocked(nil) != owned {
		t.Fatal("failed whole-turn edit changed the previously committed state")
	}
}

func TestPatternRealtimeKnownUnsupportedSourceCanBeRepairedWithoutArtwork(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, sourceControls := range [][4]float64{{75, 37, 0, 0}, {150, -90, 0, 0},
			{75, 37, .5, -.25}, {100, 0, .5, -.5}} {
			for _, scale := range []float64{50, 100, 200} {
				descriptor := patternEditDescriptor()
				fill := psdpattern.Fill{ID: "first", Name: "Original pattern", Link: true,
					Scale: sourceControls[0], Angle: sourceControls[1],
					Phase: psdpattern.Phase{X: sourceControls[2], Y: sourceControls[3]}}
				patchPatternDescriptor(descriptor, fill)
				editor := patternEditSource(t, version, descriptor, nil, nil)
				if pixels, err := editor.Composite(nil); pixels != nil || !errors.Is(err, ErrUnsupported) {
					t.Fatalf("original unsupported sampling unexpectedly used cached artwork: %v", err)
				}
				fill.Scale, fill.Angle, fill.Phase = scale, 360, psdpattern.Phase{X: -1, Y: 2}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := editor.SetPatternFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled pattern repair committed: %v", err)
				}
				assertPatternOptionsUnchanged(t, editor)
				missing := fill
				missing.ID = "missing"
				if err := editor.SetPatternFill(nil, 1, missing); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("pattern repair accepted a missing local reference: %v", err)
				}
				assertPatternOptionsUnchanged(t, editor)
				if err := editor.SetPatternFill(nil, 1, fill); err != nil {
					t.Fatalf("known unsupported source could not be repaired to a supported target: %v", err)
				}
				node, _, _, _ := editor.findNodeLocked(1)
				if node.pixels != nil {
					t.Fatal("pattern repair fabricated a supplied artwork cache")
				}
				expected := patternRealtimeTurnExpected(scale, fill.Phase)
				live, err := editor.Composite(nil)
				if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
					t.Fatalf("repaired pattern did not regenerate independent axis samples: %v", err)
				}
				for compression := range uint16(4) {
					saved := compositeSave(t, editor, SaveOptions{Compression: compression})
					assertPatternRealtimeTurnSaved(t, saved, fill, expected)
					reopened, err := New(saved)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reopened.Close() })
					changed := fill
					changed.Angle, changed.Phase = -360, psdpattern.Phase{X: 2, Y: -2}
					if err := reopened.SetPatternFill(nil, 1, changed); err != nil {
						t.Fatalf("repaired and reopened descriptor was not editable: %v", err)
					}
					assertPatternRealtimeTurnSaved(t, compositeSave(t, reopened), changed,
						patternRealtimeTurnExpected(scale, changed.Phase))
				}
			}
		}
	}
}
