package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func patternOptionsArtwork() *image.NRGBA {
	pixels := &image.NRGBA{Rect: image.Rect(0, 0, 4, 4), Stride: 20, Pix: make([]byte, 76)}
	for row := range 4 {
		for column := range 4 {
			index := row*pixels.Stride + column*4
			copy(pixels.Pix[index:], []byte{byte(20 + row*7), byte(40 + column*13), byte(90 + row + column),
				byte((row*4 + column) * 17)})
		}
	}
	return pixels
}

func assertPatternOptionsCache(t *testing.T, saved *psdfile.Document, pixels *image.NRGBA) {
	t.Helper()
	layer, err := saved.DecodeLayer(nil, 0)
	if err != nil || layer.Bounds != pixels.Rect {
		t.Fatalf("pattern saved layer cache geometry differs: %v", err)
	}
	seen := 0
	for _, channel := range layer.Channels {
		component := int(channel.ID)
		if channel.ID == -1 {
			component = 3
		}
		if component < 0 || component > 3 {
			continue
		}
		for row := range pixels.Rect.Dy() {
			for column := range pixels.Rect.Dx() {
				if channel.Data[row*pixels.Rect.Dx()+column] != pixels.Pix[row*pixels.Stride+column*4+component] {
					t.Fatalf("pattern cache component %d at (%d,%d) differs from supplied source artwork",
						component, column, row)
				}
			}
		}
		seen++
	}
	if seen != 4 {
		t.Fatalf("saved pattern cache has %d RGB/alpha planes, want 4", seen)
	}
}

func TestPatternOptionsSavedArtworkPSDPSBAllCompressionsWithoutLiveFallback(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, sampling := range [][2]float64{{75, 37}, {150, -90}} {
			editor := patternEditSource(t, version, patternEditDescriptor(),
				compositeFixtureTag("zzZZ", []byte{99, 7, 0, 5}), nil)
			fill := *patternEditRead(t, editor.source).Fill
			fill.Scale, fill.Angle, fill.Phase = sampling[0], sampling[1], psdpattern.Phase{X: 1, Y: -2}
			pixels := patternOptionsArtwork()
			if err := editor.SetPatternFillWithOptions(nil, 1, &fill, PatternFillEditOptions{}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("missing artwork bypassed strict sampling: %v", err)
			}
			if err := editor.SetPatternFillWithOptions(nil, 1, &fill, PatternFillEditOptions{Pixels: pixels}); err != nil {
				t.Fatal(err)
			}
			node, _, _, _ := editor.findNodeLocked(1)
			before, _ := editor.Changes()
			owned := editor.ownedBytesLocked(nil)
			if err := editor.SetPatternFillWithOptions(nil, 1, &fill, PatternFillEditOptions{Pixels: pixels}); err != nil {
				t.Fatal(err)
			}
			if after, _ := editor.Changes(); before != after || editor.ownedBytesLocked(nil) != owned {
				t.Fatal("identical pattern and artwork accumulated overlays or advanced revision")
			}
			clear(pixels.Pix)
			fill.Scale, fill.Angle, fill.Phase.X = 100, 0, 5
			if node.pattern.Scale != sampling[0] || node.pattern.Angle != sampling[1] || node.pattern.Phase.X != 1 {
				t.Fatal("edited metadata retained caller-owned state")
			}
			if result, err := editor.Composite(nil); result != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsupported live pattern used supplied cache: %v", err)
			}
			var rejected bytes.Buffer
			if err := editor.Write(nil, &rejected); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
				t.Fatalf("unsupported automatic preview wrote partial output: %v", err)
			}
			preview := image.NewNRGBA(image.Rect(0, 0, 4, 4))
			for pixel := range 16 {
				copy(preview.Pix[pixel*4:], []byte{5, 20, 40, 255})
			}
			for compression := range uint16(4) {
				saved := compositeSave(t, editor, SaveOptions{Compression: compression, CompositeImage: preview})
				assertPatternOptionsCache(t, saved, patternOptionsArtwork())
				view := patternEditRead(t, saved)
				if !reflect.DeepEqual(view.Fill, node.pattern) || saved.Header.Version != version {
					t.Fatal("saved scale/angle/phase or PSD/PSB version changed")
				}
				oldPatterns, _ := editor.source.ReadSpan(editor.source.AdditionalInfo[0].Span)
				newPatterns, _ := saved.ReadSpan(saved.AdditionalInfo[0].Span)
				if !bytes.Equal(oldPatterns, newPatterns) {
					t.Fatal("supplied artwork changed encoded local pattern tiles")
				}
				opaque := false
				for _, block := range saved.Layers[0].AdditionalInfo {
					if block.Key == "zzZZ" {
						data, err := saved.ReadSpan(block.Data)
						opaque = err == nil && bytes.Equal(data, []byte{99, 7, 0, 5})
					}
				}
				if !opaque {
					t.Fatal("unrelated opaque layer metadata changed")
				}
				composite := append(bytes.Repeat([]byte{5}, 16), bytes.Repeat([]byte{20}, 16)...)
				composite = append(composite, bytes.Repeat([]byte{40}, 16)...)
				compositeExpect(t, saved, composite)
				if live, err := psdscene.Render(nil, saved); live != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
					t.Fatalf("saved supplied cache became a live pattern result: %v", err)
				}
				reopened, err := New(saved)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				updated := *view.Fill
				updated.Scale, updated.Angle = 125, -12
				if err := reopened.SetPatternFillWithOptions(nil, 1, &updated,
					PatternFillEditOptions{Pixels: patternOptionsArtwork()}); err != nil {
					t.Fatalf("saved known sampling metadata could not be edited again: %v", err)
				}
				second := compositeSave(t, reopened, SaveOptions{CompositeImage: preview})
				assertPatternOptionsCache(t, second, patternOptionsArtwork())
				if fill := patternEditRead(t, second).Fill; fill.Scale != 125 || fill.Angle != -12 {
					t.Fatal("second metadata edit was not saved")
				}
			}
		}
	}
}

func TestPatternOptionsStrictMetadataGeometryAndSourceGuards(t *testing.T) {
	editor := patternEditSource(t, 1, patternEditDescriptor(), nil, nil)
	if err := editor.SetPatternFillWithOptions(nil, 1, nil,
		PatternFillEditOptions{Pixels: patternOptionsArtwork()}); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("nil pattern parameters accepted: %v", err)
	}
	assertPatternOptionsUnchanged(t, editor)
	for _, test := range []struct {
		name   string
		change func(*psdpattern.Fill, *image.NRGBA)
		want   error
	}{
		{"zero scale", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Scale = 0 }, ErrInvalidEdit},
		{"negative scale", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Scale = -10 }, ErrInvalidEdit},
		{"NaN scale", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Scale = math.NaN() }, ErrInvalidEdit},
		{"infinite angle", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Angle = math.Inf(1) }, ErrInvalidEdit},
		{"NaN phase", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Phase.X = math.NaN() }, ErrInvalidEdit},
		{"fractional phase", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Phase.Y = .5 }, ErrUnsupported},
		{"huge phase", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Phase.X = 1<<40 + 1 }, ErrInvalidEdit},
		{"missing local", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.ID = "absent" }, ErrUnsupported},
		{"wrong name", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Name = "different" }, ErrInvalidEdit},
		{"unknown semantics", func(fill *psdpattern.Fill, _ *image.NRGBA) { fill.Unsupported = []string{"future"} },
			ErrUnsupported},
		{"different size", func(_ *psdpattern.Fill, pixels *image.NRGBA) { pixels.Rect.Max.X++ }, ErrInvalidEdit},
		{"different position", func(_ *psdpattern.Fill, pixels *image.NRGBA) {
			pixels.Rect = pixels.Rect.Add(image.Pt(1, 0))
		}, ErrInvalidEdit},
		{"bad stride", func(_ *psdpattern.Fill, pixels *image.NRGBA) { pixels.Stride = 15 }, ErrInvalidEdit},
		{"short samples", func(_ *psdpattern.Fill, pixels *image.NRGBA) { pixels.Pix = pixels.Pix[:20] }, ErrInvalidEdit},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := patternEditSource(t, 1, patternEditDescriptor(), nil, nil)
			fill := *patternEditRead(t, editor.source).Fill
			fill.Scale, fill.Angle = 75, 37
			pixels := patternOptionsArtwork()
			test.change(&fill, pixels)
			if err := editor.SetPatternFillWithOptions(nil, 1, &fill, PatternFillEditOptions{Pixels: pixels}); !errors.Is(err, test.want) {
				t.Fatalf("unsafe pattern edit accepted: %v, want %v", err, test.want)
			}
			assertPatternOptionsUnchanged(t, editor)
		})
	}
	for _, kind := range []string{"unknown field", "duplicate reference", "duplicate fill", "text family"} {
		t.Run(kind, func(t *testing.T) {
			descriptor := patternEditDescriptor()
			var extra, patterns []byte
			switch kind {
			case "unknown field":
				descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
					Key: "future", Type: "bool", Value: true})
			case "duplicate reference":
				patterns = patternEditFixture("first", "Original pattern", [3][4]byte{})
				patterns = append(patterns, patterns...)
			case "duplicate fill":
				extra = compositeFixtureTag("PtFl", []byte{0})
			case "text family":
				extra = compositeFixtureTag("TySh", []byte{0})
			}
			editor := patternEditSource(t, 1, descriptor, extra, patterns)
			fill := psdpattern.Fill{ID: "first", Name: "Original pattern", Scale: 75, Angle: 37, Link: true}
			if err := editor.SetPatternFillWithOptions(nil, 1, &fill,
				PatternFillEditOptions{Pixels: patternOptionsArtwork()}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unknown or ambiguous source accepted: %v", err)
			}
			assertPatternOptionsUnchanged(t, editor)
		})
	}
}

func assertPatternOptionsUnchanged(t *testing.T, editor *Editor) {
	t.Helper()
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	if state.Dirty || len(node.additional) != 0 || node.pattern != nil || node.patternTile != nil || node.pixels != nil {
		t.Fatal("failed pattern edit committed partial metadata or pixels")
	}
}

func TestPatternOptionsFloat32EncodingBudgetAndCancellation(t *testing.T) {
	descriptor := patternEditDescriptor()
	for _, parameter := range []struct{ key, unit string }{{"Scl ", "#Prc"}, {"Angl", "#Ang"}} {
		descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
			Key: parameter.key, Type: "UnFl", Value: psdfile.DescriptorUnit{
				Unit: parameter.unit, Width: 32, Number: float32(100)},
		})
	}
	editor := patternEditSource(t, 1, descriptor, nil, nil)
	fill := *patternEditRead(t, editor.source).Fill
	fill.Scale, fill.Angle = 75.1, 37
	if err := editor.SetPatternFillWithOptions(nil, 1, &fill,
		PatternFillEditOptions{Pixels: patternOptionsArtwork()}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("source float32 silently rounded supplied scale: %v", err)
	}
	assertPatternOptionsUnchanged(t, editor)
	fill.Scale, fill.Angle = 75.5, 37.25
	if err := editor.SetPatternFillWithOptions(nil, 1, &fill,
		PatternFillEditOptions{Pixels: patternOptionsArtwork()}); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	before, _ := editor.Changes()
	oldPattern, oldPixels, oldTile := node.pattern, node.pixels, node.patternTile
	fill.Angle = 38
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetPatternFillWithOptions(ctx, 1, &fill,
		PatternFillEditOptions{Pixels: patternOptionsArtwork()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pattern cache replacement succeeded: %v", err)
	}
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-32)
	owned := editor.ownedBytesLocked(nil)
	if err := editor.SetPatternFillWithOptions(nil, 1, &fill,
		PatternFillEditOptions{Pixels: patternOptionsArtwork()}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("new artwork escaped peak budget: %v", err)
	}
	if after, _ := editor.Changes(); after != before || node.pattern != oldPattern || node.patternTile != oldTile ||
		node.pixels != oldPixels || editor.ownedBytesLocked(nil) != owned {
		t.Fatal("failed replacement changed previously committed artwork")
	}
}
