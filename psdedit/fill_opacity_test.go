package psdedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func fillOpacityBlocks(t *testing.T, source *psdfile.Document) []psdfile.AdditionalBlock {
	t.Helper()
	var result []psdfile.AdditionalBlock
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "iOpa" {
			result = append(result, block)
		}
	}
	return result
}

func TestSetFillOpacityCanonicalInsertDuplicatesAndRGB8PSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, duplicates := range []bool{false, true} {
			t.Run(fmt.Sprintf("version_%d/duplicates_%t", version, duplicates), func(t *testing.T) {
				tags := compositeFixtureTag("zzZZ", []byte{7, 9, 11})
				if duplicates {
					tags = append(tags, compositeFixtureTag("iOpa", []byte{192, 0, 0, 0})...)
					tags = append(tags, compositeFixtureTag("zzAA", []byte{23, 27})...)
					tags = append(tags, compositeFixtureTag("iOpa", []byte{64, 0, 0, 0})...)
				}
				data := compositeFixture(version, 8, 3, []compositeFixtureLayer{{
					color: []byte{255, 0, 0, 255}, tags: tags,
				}})
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				info, err := editor.Layer(1)
				initial := uint8(255)
				if duplicates {
					initial = 64
				}
				if err != nil || info.FillOpacity != initial || info.Opacity != 255 {
					t.Fatalf("last-wins current fill opacity differs: %+v %v", info, err)
				}
				preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				editor.nativeTextPreview = preview
				if err := editor.SetFillOpacity(1, initial); err != nil || editor.revision != 0 ||
					editor.nativeTextPreview != preview {
					t.Fatalf("same-value edit changed accurate preview or revision: %v", err)
				}
				if err := editor.SetFillOpacity(1, 128); err != nil {
					t.Fatal(err)
				}
				if editor.revision != 1 || editor.nativeTextPreview != nil || !editor.visualChanged ||
					editor.structureChanged || editor.roots[0].opacity != 255 {
					t.Fatal("fill edit changed layer opacity, structure or revision semantics")
				}
				for _, query := range []func() ([]LayerInfo, error){editor.Layers, editor.Tree} {
					values, err := query()
					if err != nil || len(values) != 1 || values[0].FillOpacity != 128 {
						t.Fatalf("owned metadata query lost current fill: %v", err)
					}
				}
				pixels, err := editor.Composite(nil)
				if err != nil || !bytes.Equal(pixels.Pix, []byte{255, 0, 0, 128}) {
					t.Fatalf("RGB8 compositor ignored fill overlay: %v", err)
				}
				saved := compositeSave(t, editor)
				compositeExpect(t, saved, []byte{255, 127, 127, 128})
				blocks := fillOpacityBlocks(t, saved)
				wanted := 1
				if duplicates {
					wanted = 2
				}
				if len(blocks) != wanted || saved.Header.Version != version || !bytes.Equal(data, original) {
					t.Fatal("source bytes, duplicate count or source file version changed")
				}
				if !duplicates && saved.Layers[0].AdditionalInfo[0].Key != "iOpa" {
					t.Fatal("new fill moved an opaque original final block into the middle of the source records")
				}
				for _, block := range blocks {
					payload, err := saved.ReadSpan(block.Data)
					if err != nil || !bytes.Equal(payload, []byte{128, 0, 0, 0}) {
						t.Fatalf("new or updated iOpa is not canonical4B: %v", err)
					}
				}
				for _, block := range editor.source.Layers[0].AdditionalInfo {
					if block.Key == "iOpa" {
						continue
					}
					for _, retained := range saved.Layers[0].AdditionalInfo {
						if retained.Key != block.Key {
							continue
						}
						before, beforeErr := editor.source.ReadSpan(block.Span)
						after, afterErr := saved.ReadSpan(retained.Span)
						if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
							t.Fatal("unrelated tagged bytes, padding or ordering were rewritten")
						}
					}
				}
				if err := editor.SetFillOpacity(1, 128); err != nil || editor.revision != 1 {
					t.Fatalf("saved same-value overlay became a new edit: %v", err)
				}
				if err := editor.SetFillOpacity(1, 255); err != nil {
					t.Fatal(err)
				}
				if len(fillOpacityBlocks(t, compositeSave(t, editor))) != wanted {
					t.Fatal("restoring full fill removed original or added tagged records")
				}
			})
		}
	}
}

func TestSetFillOpacityRejectsAllBadSourceInstancesBeforeNoop(t *testing.T) {
	for _, bad := range [][]byte{nil, {64}, {64, 0, 0}, {64, 0, 0, 0, 0}, {64, 1, 0, 0}} {
		t.Run(fmt.Sprint(string(bad)), func(t *testing.T) {
			tags := compositeFixtureTag("iOpa", []byte{64, 0, 0, 0})
			tags = append(tags, compositeFixtureTag("iOpa", bad)...)
			editor := compositeEditor(t, compositeFixture(1, 8, 3,
				[]compositeFixtureLayer{{color: []byte{255, 0, 0, 255}, tags: tags}}))
			preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			editor.nativeTextPreview = preview
			before, _ := editor.Changes()
			if err := editor.SetFillOpacity(1, 64); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("same-value repaired an unknown representation: %v", err)
			}
			after, _ := editor.Changes()
			if before != after || len(editor.roots[0].additional) != 0 || editor.roots[0].fillOpacityChanged ||
				editor.nativeTextPreview != preview {
				t.Fatal("failed qualification partially committed edit state")
			}
			if len(bad) != 0 {
				info, err := editor.Layer(1)
				if err != nil || info.FillOpacity != bad[0] {
					t.Fatalf("read-only query cannot retain original value: %v", err)
				}
			}
		})
	}
}

func TestSetFillOpacityVirtualGroupsAndHighDepthExplicitComposite(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, nil))
	if err := editor.SetFillOpacity(editor.roots[0].id, 128); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	if len(saved.Layers) != 1 || len(fillOpacityBlocks(t, saved)) != 1 {
		t.Fatal("virtual background did not materialize canonical fill metadata")
	}
	group, err := editor.AddGroup(0, 1, "group", true)
	if err != nil || editor.SetFillOpacity(group, 64) != nil {
		t.Fatalf("new writable group was refused: %v", err)
	}
	info, err := editor.Layer(group)
	if err != nil || info.FillOpacity != 64 {
		t.Fatalf("group query lost fill overlay: %v", err)
	}
	for _, depth := range []uint16{16, 32} {
		for _, version := range []uint16{1, 2} {
			high := compositeEditor(t, compositeFixture(version, depth, 3,
				[]compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}}))
			if err := high.SetFillOpacity(1, 128); err != nil {
				t.Fatalf("metadata edit incorrectly restricted source depth%d: %v", depth, err)
			}
			var output bytes.Buffer
			if err := high.Write(nil, &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
				t.Fatalf("uncalibrated depth%d automatic preview escaped: %v", depth, err)
			}
			preview, err := high.source.DecodeComposite(nil)
			if err != nil {
				t.Fatal(err)
			}
			result := compositeSave(t, high, SaveOptions{Composite: preview})
			if result.Header.Depth != depth || result.Header.Version != version ||
				len(fillOpacityBlocks(t, result)) != 1 {
				t.Fatal("accurate source-precision preview lost editable parameters")
			}
		}
	}
}

func TestSetFillOpacityIDsContextAndClosedSourceRemainAtomic(t *testing.T) {
	tags := compositeFixtureTag("iOpa", []byte{192, 0, 0, 0})
	editor := compositeEditor(t, compositeFixture(1, 8, 3,
		[]compositeFixtureLayer{{color: []byte{255, 0, 0, 255}, tags: tags}}))
	before, _ := editor.Changes()
	if err := editor.SetFillOpacity(99, 64); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid handle accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetFillOpacityContext(ctx, 1, 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("source setter ignored cancellation: %v", err)
	}
	after, _ := editor.Changes()
	if before != after || editor.roots[0].fillOpacityChanged || len(editor.roots[0].additional) != 0 {
		t.Fatal("failed handle/context edit partially changed overlays")
	}
	if err := editor.source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetFillOpacity(1, 64); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed-source category changed: %v", err)
	}
}

func TestSetFillOpacityRetainedAddedPayloadAndSourceViewOwnership(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3,
		[]compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}}))
	before := editor.ownedBytesLocked(nil)
	if err := editor.SetFillOpacity(1, 64); err != nil || editor.ownedBytesLocked(nil) != before+4 ||
		len(editor.roots[0].additional) != 0 {
		t.Fatalf("deferred absent tag was not reserved as four owned bytes: %v", err)
	}
	info, err := editor.Layer(1)
	if err != nil {
		t.Fatal(err)
	}
	info.FillOpacity = 200
	current, err := editor.Layer(1)
	if err != nil || current.FillOpacity != 64 || reflect.DeepEqual(current, info) {
		t.Fatal("public layer metadata aliases mutable edit state")
	}
}
