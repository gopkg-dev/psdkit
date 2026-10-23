package psdedit

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func noisyShadowEditor(t *testing.T, version uint16) *Editor {
	t.Helper()
	template := effectEditorFixture(t, version, "lfx2", true)
	_, block := effectSourceView(t, template.source)
	descriptor, err := template.source.DecodeDescriptor(psdfile.Span{
		Offset: block.Data.Offset + 4, Length: block.Data.Length - 4,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	shadow := descriptor.Entries[1].Value.(*psdfile.Descriptor)
	shadow.Entries = append(shadow.Entries,
		psdfile.DescriptorEntry{Key: "Nose", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Width: 64, Number: float64(25)}},
		psdfile.DescriptorEntry{Key: "ShdN", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Prc", Width: 64, Number: float64(50)}})
	encoded, err := template.source.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(make([]byte, 4), encoded...)
	payload = append(payload, make([]byte, (4-len(payload)%4)%4)...)
	tags := compositeFixtureTag("lfx2", payload)
	tags = append(tags, compositeFixtureTag("lrFX", effectLegacyFixture(true))...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{19, 27, 35, 41})...)
	return compositeEditor(t, compositeFixture(version, 8, 3,
		[]compositeFixtureLayer{{color: []byte{255, 0, 0, 255}, tags: tags}}))
}

func TestZeroOpacityNoiseEffectsEditSaveReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("version%d/compression%d", version, compression), func(t *testing.T) {
				editor := noisyShadowEditor(t, version)
				if output, err := editor.Composite(t.Context()); output != nil || !errors.Is(err, ErrUnsupported) {
					t.Fatalf("active noisy shadow was rendered without an implementation: %v", err)
				}
				set, _ := effectSourceView(t, editor.source)
				set.Items[0].Opacity = 0
				if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
					t.Fatal(err)
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: compression})
				reopened := lifecycleExpectPreview(t, editor, saved, color.NRGBA{R: 255, A: 255})
				persisted, _ := effectSourceView(t, saved)
				if persisted.Items[0].Opacity != 0 || persisted.Items[0].Noise != .25 || persisted.Items[0].Jitter != .5 {
					t.Fatal("saving zero opacity discarded retained noise controls")
				}
				live, err := psdscene.Render(t.Context(), saved)
				if err != nil || live.NRGBAAt(0, 0) != (color.NRGBA{R: 255, A: 255}) {
					t.Fatalf("reopened zero-opacity effect did not rerender: %v", err)
				}
				before, _ := reopened.Changes()
				persisted.Items[0].Opacity = .5
				if err := reopened.SetEffects(t.Context(), 1, *persisted); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("reenabling noise bypassed its unsupported algorithm: %v", err)
				}
				after, _ := reopened.Changes()
				if before != after {
					t.Fatal("rejected effect activation changed editor state")
				}
				persisted.Items[0].Opacity, persisted.Items[0].Color = 0, [3]float64{0, 0, 1}
				if err := reopened.SetEffects(t.Context(), 1, *persisted); err != nil {
					t.Fatal(err)
				}
				if err := reopened.SetOpacity(1, 128); err != nil {
					t.Fatal(err)
				}
				savedAgain := compositeSave(t, reopened, SaveOptions{Compression: compression})
				lifecycleExpectPreview(t, reopened, savedAgain, color.NRGBA{R: 255, A: 128})
				compositeExpect(t, savedAgain, []byte{255, 127, 127, 128})
				preserved := false
				for _, block := range savedAgain.Layers[0].AdditionalInfo {
					if block.Key == "pRsv" {
						data, err := savedAgain.ReadSpan(block.Data)
						if err != nil || !bytes.Equal(data, []byte{19, 27, 35, 41}) {
							t.Fatalf("unrelated source metadata changed: %v", err)
						}
						preserved = true
					}
				}
				if !preserved {
					t.Fatal("unrelated source metadata was removed")
				}
			})
		}
	}
}
