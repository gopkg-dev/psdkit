package psdedit

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestEffectsCurrentBlendingRefusesUnsupportedEditsAtomically(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*testing.T, *Editor) (func() error, error)
	}{
		{"blend-mode", func(_ *testing.T, editor *Editor) (func() error, error) {
			return func() error { return editor.SetBlendMode(1, "norm") }, editor.SetBlendMode(1, "mul ")
		}},
		{"transparency-shapes", func(t *testing.T, editor *Editor) (func() error, error) {
			return func() error {
				return editor.SetAdvancedBlending(t.Context(), 1, AdvancedBlendingOptions{TransparencyShapes: new(true)})
			}, editor.SetAdvancedBlending(t.Context(), 1, AdvancedBlendingOptions{TransparencyShapes: new(false)})
		}},
		{"knockout", func(t *testing.T, editor *Editor) (func() error, error) {
			return func() error {
				return editor.SetAdvancedBlending(t.Context(), 1, AdvancedBlendingOptions{Knockout: new(psdmodel.KnockoutNone)})
			}, editor.SetAdvancedBlending(t.Context(), 1, AdvancedBlendingOptions{Knockout: new(psdmodel.KnockoutShallow)})
		}},
		{"blend-if", func(_ *testing.T, editor *Editor) (func() error, error) {
			return func() error { return editor.SetBlendIf(1, nil) },
				editor.SetBlendIf(1, []psdmodel.BlendRange{{Source: psdmodel.BlendSlider{BlackHigh: 128,
					WhiteLow: 255, WhiteHigh: 255}, Destination: psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}}})
		}},
		{"clipping", func(_ *testing.T, editor *Editor) (func() error, error) {
			return func() error { return editor.SetClipping(1, false) }, editor.SetClipping(1, true)
		}},
		{"channel-restrictions", func(t *testing.T, editor *Editor) (func() error, error) {
			return func() error {
				return editor.SetAdvancedBlending(t.Context(), 1, AdvancedBlendingOptions{ChannelRestrictions: []uint32{}})
			}, editor.SetAdvancedBlending(t.Context(), 1, AdvancedBlendingOptions{ChannelRestrictions: []uint32{0}})
		}},
		{"new-clipped-sibling", func(_ *testing.T, editor *Editor) (func() error, error) {
			pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			pixels.Pix[3] = 255
			id, err := editor.AddRaster(0, 1, "new clipped sibling", pixels, image.Point{})
			if err != nil {
				return nil, err
			}
			return func() error { return editor.SetClipping(id, false) }, editor.SetClipping(id, true)
		}},
	} {
		for _, existing := range []bool{false, true} {
			name := change.name + map[bool]string{false: "/first-effect-change", true: "/existing-effect-noop"}[existing]
			t.Run(name, func(t *testing.T) {
				editor := compositeEditor(t, colorOverlayVectorEditFixture(t, 1, true, true, false))
				set, _ := effectSourceView(t, editor.source)
				set.Items[0].Color = [3]float64{0, 0, 1}
				if existing {
					if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
						t.Fatal(err)
					}
				}
				restore, err := change.apply(t, editor)
				if err != nil {
					t.Fatal(err)
				}
				node := editor.roots[0]
				before, _ := editor.Changes()
				ownedBefore, effectsBefore := editor.ownedBytesLocked(nil), node.effects
				additional := make(map[int64][]byte, len(node.additional))
				for offset, data := range node.additional {
					additional[offset] = bytes.Clone(data)
				}
				if err := editor.SetEffects(t.Context(), 1, *set); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("current unsupported blending reached effect commit: %v", err)
				}
				after, _ := editor.Changes()
				if after != before || editor.ownedBytesLocked(nil) != ownedBefore || node.effects != effectsBefore ||
					len(node.additional) != len(additional) {
					t.Fatal("rejected current combination changed state or retained effect overlays")
				}
				for offset, data := range additional {
					if !bytes.Equal(data, node.additional[offset]) {
						t.Fatal("rejected current combination changed existing encoded metadata")
					}
				}
				if err := restore(); err != nil {
					t.Fatal(err)
				}
				if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
					t.Fatalf("restored supported blending did not recover: %v", err)
				}
				if _, err := editor.Composite(t.Context()); err != nil {
					t.Fatalf("restored effect combination cannot render: %v", err)
				}
			})
		}
	}
}
