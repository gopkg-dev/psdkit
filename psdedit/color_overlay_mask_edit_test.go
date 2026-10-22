package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func maskedOverlayEditFixture(t *testing.T, version uint16, nested bool, global *bool) []byte {
	t.Helper()
	tags := compositeFixtureTag("lfx2", colorOverlayPayload(t))
	tags = append(tags, compositeFixtureTag("lrFX", colorOverlayLegacy())...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0x9a, 0x77})...)
	if global != nil {
		value := byte(0)
		if *global {
			value = 1
		}
		tags = append(tags, compositeFixtureTag("lmgm", []byte{value, 0, 0, 0})...)
	}
	leaf := compositeFixtureLayer{color: []byte{200, 100, 40, 127}, mask: []byte{64}, tags: tags}
	layers := []compositeFixtureLayer{leaf}
	if nested {
		layers = []compositeFixtureLayer{{divider: 3}, leaf, {divider: 1, blend: "norm"}}
	}
	return compositeFixture(version, 8, 4, layers)
}

func maskedOverlaySourceSet(t *testing.T, editor *Editor) (LayerID, *psdeffects.Set, int) {
	t.Helper()
	infos, err := editor.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Group {
			continue
		}
		for _, block := range editor.source.Layers[info.SourceIndex].AdditionalInfo {
			if block.Key == "lfx2" || block.Key == "lmfx" {
				set, err := editor.source.DecodeEffects(nil, block)
				if err != nil {
					t.Fatal(err)
				}
				return info.ID, set, info.SourceIndex
			}
		}
	}
	t.Fatal("masked source effects missing")
	return 0, nil, 0
}

func TestSetPixelMaskedOverlayNestedLeafOwnsParamsAndSavesPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, nested := range []bool{false, true} {
			for _, global := range []*bool{nil, new(false), new(true)} {
				data := maskedOverlayEditFixture(t, version, nested, global)
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				id, set, index := maskedOverlaySourceSet(t, editor)
				maskBefore, err := editor.source.ReadSpan(editor.source.Layers[index].MaskData)
				if err != nil {
					t.Fatal(err)
				}
				set.Items[0].Color = [3]float64{60.0 / 255, 180.0 / 255, 220.0 / 255}
				set.Items[0].Opacity = .5
				if err := editor.SetEffects(nil, id, *set); err != nil {
					t.Fatal(err)
				}
				set.Items[0].Color = [3]float64{}
				set.Items[0].Opacity = 0
				preview, err := editor.Composite(nil)
				if err != nil || preview.NRGBAAt(0, 0) != (color.NRGBA{R: 130, G: 140, B: 130, A: 32}) {
					t.Fatalf("masked effect color/shape/order or ownership differs: %v, %v", preview, err)
				}
				saved := compositeSave(t, editor)
				if !bytes.Equal(data, original) || saved.Header.Version != version {
					t.Fatal("source bytes/version changed")
				}
				savedMask, err := saved.ReadSpan(saved.Layers[index].MaskData)
				if err != nil || !bytes.Equal(savedMask, maskBefore) {
					t.Fatalf("pixel mask bytes changed: %v", err)
				}
				for _, block := range saved.Layers[index].AdditionalInfo {
					if block.Key == "pRsv" {
						payload, err := saved.ReadSpan(block.Data)
						if err != nil || !bytes.Equal(payload, []byte{0x9a, 0x77}) {
							t.Fatal("unrelated block changed")
						}
					}
					if block.Key == "lmgm" {
						payload, err := saved.ReadSpan(block.Data)
						if err != nil || len(payload) != 4 || (payload[0] != 0) != *global {
							t.Fatal("mask stage source flag changed")
						}
					}
				}
				reopened, err := New(saved)
				if err != nil {
					t.Fatal(err)
				}
				pixels, err := reopened.Composite(nil)
				if err != nil || !bytes.Equal(pixels.Pix, preview.Pix) {
					t.Fatalf("automatic preview/reopen mismatch: %v", err)
				}
				_ = reopened.Close()
			}
		}
	}
}

func TestSetPixelMaskedOverlaySourceInvalidAndCancelAreAtomic(t *testing.T) {
	editor := compositeEditor(t, maskedOverlayEditFixture(t, 1, true, nil))
	id, set, index := maskedOverlaySourceSet(t, editor)
	set.Items[0].Opacity = .5
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetEffects(ctx, id, *set); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel cause changed: %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty {
		t.Fatal("cancelled edit mutated overlays")
	}
	set.Items[0].Opacity = 2
	if err := editor.SetEffects(nil, id, *set); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid desired effect accepted: %v", err)
	}
	state, _ = editor.Changes()
	if state.Dirty {
		t.Fatal("invalid edit partially committed")
	}
	set.Items[0].Opacity = .5
	maskAt := editor.source.Layers[index].Mask.Span.Offset
	data := maskedOverlayEditFixture(t, 1, true, nil)
	data[maskAt+17] |= 0x80
	invalid := compositeEditor(t, data)
	invalidID, desired, _ := maskedOverlaySourceSet(t, invalid)
	desired.Items[0].Opacity = .5
	if err := invalid.SetEffects(nil, invalidID, *desired); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown original mask flags were erased by valid desired parameters: %v", err)
	}
	state, _ = invalid.Changes()
	if state.Dirty {
		t.Fatal("unsupported source partially mutated")
	}
}

func TestSetPixelMaskedOverlayRetainsClippingAndOtherFamilyRefusals(t *testing.T) {
	editor := compositeEditor(t, maskedOverlayEditFixture(t, 1, true, nil))
	id, set, _ := maskedOverlaySourceSet(t, editor)
	set.Items[0].Kind = psdeffects.DropShadow
	if err := editor.SetEffects(nil, id, *set); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("outer family accepted: %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty {
		t.Fatal("family rejection mutated node")
	}
	limited := compositeEditor(t, maskedOverlayEditFixture(t, 1, true, nil), psdfile.LoadOptions{
		Limits: psdfile.Limits{MaxDecodeBytes: 64 << 10},
	})
	limitedID, desired, _ := maskedOverlaySourceSet(t, limited)
	desired.Items[0].Opacity = .5
	if err := limited.SetEffects(nil, limitedID, *desired); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("metadata working budget ignored: %v", err)
	}
	state, _ = limited.Changes()
	if state.Dirty {
		t.Fatal("budget refusal mutated node")
	}
}

func TestSetPixelMaskedOverlayCannotRepairInvalidOriginalStyle(t *testing.T) {
	payload := colorOverlayPayload(t)
	originalOpacity := binary.BigEndian.AppendUint64(nil, math.Float64bits(100))
	invalidOpacity := binary.BigEndian.AppendUint64(nil, math.Float64bits(200))
	if bytes.Count(payload, originalOpacity) != 1 {
		t.Fatal("fixture opacity encoding is not independently identifiable")
	}
	payload = bytes.Replace(payload, originalOpacity, invalidOpacity, 1)
	data := compositeFixture(1, 8, 4, []compositeFixtureLayer{
		{color: []byte{200, 100, 40, 127}, mask: []byte{64}, tags: compositeFixtureTag("lfx2", payload)},
	})
	editor := compositeEditor(t, data)
	id, desired, _ := maskedOverlaySourceSet(t, editor)
	if desired.Items[0].Opacity != 2 {
		t.Fatal("original invalid opacity was normalized or erased")
	}
	desired.Items[0].Opacity = .5
	if err := editor.SetEffects(nil, id, *desired); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("valid desired opacity erased an invalid original style: %v", err)
	}
	state, _ := editor.Changes()
	if state.Dirty {
		t.Fatal("invalid original style partially committed")
	}
}
