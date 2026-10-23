package psdedit

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"
)

func TestEffectsDisableMasksEditSaveReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				editor := effectEditorFixture(t, version, "lfx2", false)
				set, _ := effectSourceView(t, editor.source)
				set.Items[0].Color, set.Items[0].Opacity = [3]float64{0, 0, 1}, .5
				if err := editor.SetEffects(t.Context(), 1, *set); err != nil {
					t.Fatal(err)
				}
				if err := editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 1, 1), lifecycleMaskPlane(8, 0)); err != nil {
					t.Fatal(err)
				}
				mask, err := editor.PixelMask(t.Context(), 1)
				if err != nil {
					t.Fatal(err)
				}
				mask.Disabled = true
				if err := editor.SetPixelMask(t.Context(), 1, mask); err != nil {
					t.Fatal(err)
				}
				vector := lifecycleVectorMask(t)
				vector.Disabled = true
				if err := editor.AddVectorMask(t.Context(), 1, vector); err != nil {
					t.Fatal(err)
				}
				options := SaveOptions{Compression: compression}
				saved := compositeSave(t, editor, options)
				reopened := lifecycleExpectPreview(t, editor, saved, color.NRGBA{B: 255, A: 128})
				compositeExpect(t, saved, []byte{127, 127, 255, 128})
				persisted, err := reopened.PixelMask(t.Context(), 1)
				if err != nil || !persisted.Disabled || !editVectorMask(t, saved).Disabled {
					t.Fatalf("saved file lost disabled masks: %v", err)
				}
				index, _ := lifecycleMaskChannel(t, saved, 0)
				plane, err := saved.DecodeChannel(t.Context(), 0, index)
				if err != nil || !bytes.Equal(plane.Data, []byte{0}) {
					t.Fatalf("disabled mask samples were discarded: %v", err)
				}
				for _, pixelMask := range []bool{true, false} {
					if pixelMask {
						persisted.Disabled = false
						err = reopened.SetPixelMask(t.Context(), 1, persisted)
					} else {
						vector.Disabled = false
						err = reopened.SetVectorMask(t.Context(), 1, vector)
					}
					if err != nil {
						t.Fatal(err)
					}
					before, _ := reopened.Changes()
					var output bytes.Buffer
					if err := reopened.Write(t.Context(), &output, options); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
						t.Fatalf("active mask/effect combination wrote a partial result: %v", err)
					}
					after, _ := reopened.Changes()
					if before != after {
						t.Fatal("rejected automatic preview mutated the editor")
					}
					if pixelMask {
						persisted.Disabled = true
						err = reopened.SetPixelMask(t.Context(), 1, persisted)
					} else {
						vector.Disabled = true
						err = reopened.SetVectorMask(t.Context(), 1, vector)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := reopened.SetOpacity(1, 128); err != nil {
					t.Fatal(err)
				}
				savedAgain := compositeSave(t, reopened, options)
				lifecycleExpectPreview(t, reopened, savedAgain, color.NRGBA{B: 255, A: 64})
				compositeExpect(t, savedAgain, []byte{191, 191, 255, 64})
			})
		}
	}
}
