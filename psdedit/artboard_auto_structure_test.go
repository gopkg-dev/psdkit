package psdedit

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdscene"
)

func artboardAutoSolid(pixel color.NRGBA) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 5, 1))
	for x := range 5 {
		pixels.SetNRGBA(x, 0, pixel)
	}
	return pixels
}

func TestArtboardAutoFixtureMatchesIndependentSavedCoordinatePixels(t *testing.T) {
	for background, pixel := range map[int32]color.NRGBA{
		1: {R: 255, G: 127, B: 127, A: 255}, 2: {R: 128, A: 255},
		3: {R: 255, A: 128}, 4: {R: 133, G: 40, B: 80, A: 255},
	} {
		editor := compositeEditor(t, artboardAutoFixture(t, 2, background, 9))
		expected := artboardAutoExpected(pixel, color.NRGBA{G: 200, B: 100, A: 255})
		actual, err := psdscene.Render(t.Context(), editor.source)
		if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
			t.Fatalf("independent artboard fixture does not have declared source pixels: %v, %v", err, actual)
		}
	}
}

func TestArtboardAutoChildStructureCrossBoardMoveAndGroupPreview(t *testing.T) {
	white, red := color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBA{R: 255, A: 255}
	blue, green := color.NRGBA{B: 255, A: 255}, color.NRGBA{G: 200, B: 100, A: 255}
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			editor := compositeEditor(t, artboardAutoFixture(t, version, 1, -13))
			metadata := artboardAutoMetadata(t, editor.source)
			for stage, expected := range []*image.NRGBA{
				artboardAutoExpected(white, green), artboardAutoExpected(blue, green),
				artboardAutoExpected(red, green), artboardAutoExpected(blue, red),
				artboardAutoExpected(white, red), artboardAutoExpected(white, red),
				artboardAutoExpected(color.NRGBA{}, red),
			} {
				left, right := artboardAutoLayer(t, editor, "left-board"), artboardAutoLayer(t, editor, "right-board")
				switch stage {
				case 0:
					if err := editor.Remove(artboardAutoLayer(t, editor, "left-child")); err != nil {
						t.Fatal(err)
					}
				case 1:
					if _, err := editor.AddRaster(left, 0, "new-bottom", artboardAutoSolid(red), image.Point{}); err != nil {
						t.Fatal(err)
					}
					if _, err := editor.AddRaster(left, 1, "new-top", artboardAutoSolid(blue), image.Point{}); err != nil {
						t.Fatal(err)
					}
				case 2:
					if err := editor.Move(artboardAutoLayer(t, editor, "new-top"), left, 0); err != nil {
						t.Fatal(err)
					}
				case 3:
					if err := editor.Move(artboardAutoLayer(t, editor, "new-bottom"), right, 1); err != nil {
						t.Fatal(err)
					}
				case 4:
					if err := editor.Remove(artboardAutoLayer(t, editor, "new-top")); err != nil {
						t.Fatal(err)
					}
				case 5:
					if err := editor.Move(right, 0, 0); err != nil {
						t.Fatal(err)
					}
				case 6:
					if err := editor.SetVisible(left, false); err != nil {
						t.Fatal(err)
					}
				}
				if stage == 5 {
					for _, selected := range []struct {
						id     LayerID
						bounds image.Rectangle
						pixel  color.NRGBA
					}{{left, image.Rect(0, 0, 2, 1), white}, {right, image.Rect(3, 0, 5, 1), red}} {
						before, _ := editor.Changes()
						group, err := editor.CompositeGroup(t.Context(), selected.id)
						if err != nil || group.Rect != selected.bounds {
							t.Fatalf("isolated board preview misclassified sibling boards or lost bounds: %v", err)
						}
						for x := selected.bounds.Min.X; x < selected.bounds.Max.X; x++ {
							if group.NRGBAAt(x, 0) != selected.pixel {
								t.Fatal("isolated board preview lost its own background or clipping")
							}
						}
						if after, _ := editor.Changes(); before != after {
							t.Fatal("isolated artboard preview advanced revision")
						}
					}
				}
				reopened, saved := artboardAutoRoundtrip(t, editor, compression, expected, stage%2 != 0)
				if !reflect.DeepEqual(metadata, artboardAutoMetadata(t, saved)) {
					t.Fatal("child-only structure edits changed retained artboard metadata")
				}
				editor = reopened
			}
		}
	}
}
