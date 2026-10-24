package psdedit

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func smartContentsFilterAssertSource(t *testing.T, saved *psdfile.Document, contents []byte) {
	t.Helper()
	links, err := saved.LinkedFiles(t.Context())
	if err != nil || len(links) != 1 || links[0].Kind != "liFD" {
		t.Fatalf("filtered replacement lost its single embedded editable source: %v", err)
	}
	actual, err := saved.ReadSpan(links[0].Data)
	if err != nil || !bytes.Equal(actual, contents) || links[0].DataSize != uint64(len(contents)) {
		t.Fatalf("filtering baked or truncated replacement source bytes: %v", err)
	}
}

func smartContentsFilterState(t *testing.T, editor *Editor) func() {
	t.Helper()
	before, _ := editor.Changes()
	owned := editor.ownedBytesLocked(nil)
	linked := make(map[string]linkedReplacement, len(editor.linkedOverlays))
	for key, value := range editor.linkedOverlays {
		state := *value
		state.data = bytes.Clone(value.data)
		linked[key] = state
	}
	type nodeState struct {
		node       *editNode
		pixels     *image.NRGBA
		placed     *psdmodel.Placed
		bounds     image.Rectangle
		additional map[int64][]byte
	}
	states := make([]nodeState, 0, len(editor.roots))
	for _, node := range editor.roots {
		state := nodeState{node: node, pixels: node.pixels, placed: node.placed, bounds: node.bounds}
		if node.additional != nil {
			state.additional = make(map[int64][]byte, len(node.additional))
		}
		for key, value := range node.additional {
			state.additional[key] = bytes.Clone(value)
		}
		states = append(states, state)
	}
	return func() {
		t.Helper()
		if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
			len(editor.linkedOverlays) != len(linked) {
			t.Fatal("failed filtered replacement changed revisions, owned budget or shared-source membership")
		}
		for key, expected := range linked {
			if actual := editor.linkedOverlays[key]; actual == nil || !reflect.DeepEqual(*actual, expected) {
				t.Fatal("failed filtered replacement changed retained encoded source bytes or policies")
			}
		}
		for _, state := range states {
			if state.node.pixels != state.pixels || state.node.placed != state.placed ||
				state.node.bounds != state.bounds || !reflect.DeepEqual(state.node.additional, state.additional) {
				t.Fatal("failed filtered replacement committed an earlier shared instance or current filter descriptor")
			}
		}
	}
}

func TestSmartContentsFilterStrictEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				data := exactSmartFilterFixture(t, version)
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				metadata := smartFilterAutoOpaqueMetadata(t, editor.source)
				stack := smartFilterAutoExactStack(t, editor.source)
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatal(err)
				}
				for step, gray := range [][4]byte{{255, 128, 64, 0}, {0, 64, 128, 255}} {
					contents := smartEditPrototype(3-version, exactSmartFilterGrayPixels(gray))
					immutable := bytes.Clone(contents)
					if err := editor.ReplaceContents(t.Context(), 1, contents); err != nil {
						t.Fatalf("strict replacement rejected current exact live controls at step %d: %v", step, err)
					}
					contents[len(contents)-1] ^= 0xff
					values := [4]byte{255, 193, 161, 130}
					if step == 1 {
						values = [4]byte{130, 161, 193, 255}
					}
					reopened, saved := smartFilterAutoSaved(t, editor, compression,
						exactSmartFilterGrayPixels(values), psdfilter.Options{}, step == 0)
					smartContentsFilterAssertSource(t, saved, immutable)
					if !reflect.DeepEqual(exactSmartFilterStack(t, saved), stack) ||
						!reflect.DeepEqual(metadata, smartFilterAutoOpaqueMetadata(t, saved)) {
						t.Fatal("replacement reverted the current controls or discarded opaque filter metadata")
					}
					editor = reopened
				}
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = -97
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
					t.Fatalf("reopened replacement source could not continue parameter editing: %v", err)
				}
				smartFilterAutoSaved(t, editor, compression, exactSmartFilterGrayPixels([4]byte{0, 2, 4, 8}),
					psdfilter.Options{}, true)
				if !bytes.Equal(data, original) {
					t.Fatal("filtered replacement mutated original parent source bytes")
				}
			})
		}
	}
}

func TestSmartContentsFilterApproximationRequiresExplicitPolicyAfterEveryReopen(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*psdfilter.Stack)
		policy psdfilter.Options
	}{
		{"gaussian", func(stack *psdfilter.Stack) { stack.Filters[0].Radius = .7 },
			psdfilter.Options{ApproximateGaussian: true}},
		{"surface", func(stack *psdfilter.Stack) { stack.Filters[1].Radius, stack.Filters[1].Threshold = 2, 100 },
			psdfilter.Options{ApproximateSurfaceBlur: true}},
		{"sharpen", func(stack *psdfilter.Stack) { stack.Filters[2].Radius, stack.Filters[2].Amount = .7, 1.2 },
			psdfilter.Options{ApproximateSmartSharpen: true}},
		{"brightness", func(stack *psdfilter.Stack) { stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 15 },
			psdfilter.Options{ApproximateBrightnessContrast: true}},
		{"hue", func(stack *psdfilter.Stack) { stack.Filters[4].Adjustment.HueSaturation.Master.Hue = 37 },
			psdfilter.Options{ApproximateHueSaturation: true}},
	}
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, test := range cases {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, test.name), func(t *testing.T) {
					data, artwork := smartFilterAutoColorFixture(t, version)
					editor := compositeEditor(t, data)
					stack := smartFilterAutoExactStack(t, editor.source)
					test.edit(&stack)
					options := SmartContentsOptions{SmartFilters: &test.policy}
					if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{
						Render: &psdscene.Options{SmartFilters: &test.policy},
					}); err != nil {
						t.Fatal(err)
					}
					for step := range 2 {
						pixels := image.NewNRGBA(artwork.Rect)
						for index := range len(artwork.Pix) / 4 {
							value := artwork.NRGBAAt(index%2, index/2)
							if step == 0 {
								value.R, value.B = value.B, value.R
							}
							pixels.SetNRGBA(index%2, index/2, value)
						}
						contents := smartEditPrototype(version, pixels)
						unchanged := smartContentsFilterState(t, editor)
						if err := editor.ReplaceContents(t.Context(), 1, contents); !errors.Is(err, ErrUnsupported) {
							t.Fatalf("%s inherited approximate consent from an earlier edit/save: %v", test.name, err)
						}
						unchanged()
						if err := editor.ReplaceContentsWithOptions(t.Context(), 1, contents, options); err != nil {
							t.Fatalf("explicit %s replacement failed: %v", test.name, err)
						}
						expected, err := psdfilter.Apply(t.Context(), pixels, stack, test.policy)
						if err != nil || bytes.Equal(expected.Pix, pixels.Pix) {
							t.Fatalf("independent %s reference did not exercise filtering: %v", test.name, err)
						}
						reopened, saved := smartFilterAutoSaved(t, editor, compression, expected, test.policy, step == 1)
						smartContentsFilterAssertSource(t, saved, contents)
						if !reflect.DeepEqual(exactSmartFilterStack(t, saved), stack) {
							t.Fatal("approximate source replacement changed independently retained filter controls")
						}
						editor = reopened
					}
				})
			}
		}
	}
}
