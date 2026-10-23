package psdedit

import (
	"bytes"
	"fmt"
	"image"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestReplaceContentsIgnoresRemovedIncompatibleSharedInstance(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, problem := range []string{"type", "warp", "filter"} {
			t.Run(fmt.Sprintf("version%d_%s", version, problem), func(t *testing.T) {
				editor, _ := smartEditOpen(t, smartEditFixture(t, version, problem))
				items, err := editor.SmartObjects(t.Context())
				if err != nil || len(items) != 2 {
					t.Fatalf("shared instances: count=%d, error=%v", len(items), err)
				}
				target, removed := smartSurvivingIDs(t, items)
				if err := editor.Remove(removed); err != nil {
					t.Fatal(err)
				}
				wanted := smartEditPrototype(version, smartEditImage(smartEditColors()))
				if err := editor.ReplaceContents(t.Context(), target, wanted); err != nil {
					t.Fatal(err)
				}
				items, err = editor.SmartObjects(t.Context())
				if err != nil || len(items) != 1 || len(items[0].SharedInstances) != 1 ||
					items[0].SharedInstances[0] != target {
					t.Fatalf("removed instance remains in the edited shared view: %+v, %v", items, err)
				}
				saved := smartSurvivingSave(t, editor, 1)
				placements, err := saved.SmartObjects(t.Context())
				if err != nil || len(placements) != 1 {
					t.Fatalf("saved placements: count=%d, error=%v", len(placements), err)
				}
				smartSurvivingSource(t, saved, wanted)
			})
		}
	}
}

func TestReplaceSharedContentsPreservesPixelMaskLifecycle(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				editor, _ := smartEditOpen(t, smartEditFixture(t, version, ""))
				items, err := editor.SmartObjects(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				target, peer := smartSurvivingIDs(t, items)
				mask := lifecycleMaskPlane(8, 64, 173)
				bounds := image.Rect(0, 0, 2, 1)
				if err := editor.AddPixelMask(t.Context(), target, bounds, mask); err != nil {
					t.Fatal(err)
				}
				wantedPixels := smartEditImage(smartEditColors())
				wanted := smartEditPrototype(version, wantedPixels)
				if err := editor.ReplaceContents(t.Context(), peer, wanted); err != nil {
					t.Fatal(err)
				}
				smartEditAssertCaches(t, editor, wantedPixels)
				saved := smartSurvivingSave(t, editor, compression)
				layerIndex := smartSurvivingLayerIndex(t, saved, "First instance")
				channelIndex, channel := lifecycleMaskChannel(t, saved, layerIndex)
				decoded, err := saved.DecodeChannel(t.Context(), layerIndex, channelIndex)
				if err != nil || channel.Bounds != bounds || !bytes.Equal(decoded.Data, mask.Data) {
					t.Fatalf("shared replacement changed the added mask: bounds=%v, error=%v", channel.Bounds, err)
				}
				smartSurvivingSource(t, saved, wanted)
				if err := editor.RemovePixelMask(t.Context(), target); err != nil {
					t.Fatal(err)
				}
				wantedPixels.Pix[0] ^= 0xff
				wanted = smartEditPrototype(version, wantedPixels)
				if err := editor.ReplaceContents(t.Context(), target, wanted); err != nil {
					t.Fatal(err)
				}
				smartEditAssertCaches(t, editor, wantedPixels)
				saved = smartSurvivingSave(t, editor, compression)
				lifecycleExpectMaskRemoved(t, saved, smartSurvivingLayerIndex(t, saved, "First instance"))
				smartSurvivingSource(t, saved, wanted)
			})
		}
	}
}

func smartSurvivingIDs(t *testing.T, items []SmartObjectInfo) (LayerID, LayerID) {
	t.Helper()
	var first, second LayerID
	for _, item := range items {
		switch item.Name {
		case "First instance":
			first = item.ID
		case "Second instance":
			second = item.ID
		}
	}
	if first == 0 || second == 0 {
		t.Fatal("fixture is missing shared instances")
	}
	return first, second
}

func smartSurvivingSave(t *testing.T, editor *Editor, compression uint16) *psdfile.Document {
	t.Helper()
	preview, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "edited.psd")
	if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	reopened := lifecycleReopen(t, saved)
	persisted, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(persisted.Pix, preview.Pix) {
		t.Fatalf("saved and reopened smart-object preview differs: %v", err)
	}
	return saved
}

func smartSurvivingSource(t *testing.T, source *psdfile.Document, wanted []byte) {
	t.Helper()
	links, err := source.LinkedFiles(t.Context())
	if err != nil || len(links) != 1 {
		t.Fatalf("saved linked records: count=%d, error=%v", len(links), err)
	}
	encoded, err := source.ReadSpan(links[0].Data)
	if err != nil || !bytes.Equal(encoded, wanted) {
		t.Fatalf("saved shared source differs from the replacement: %v", err)
	}
	child, err := source.OpenLinkedDocument(t.Context(), links[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Close() }()
	if _, err := child.DecodeComposite(t.Context()); err != nil {
		t.Fatalf("saved shared source cannot decode: %v", err)
	}
}

func smartSurvivingLayerIndex(t *testing.T, source *psdfile.Document, name string) int {
	t.Helper()
	for index, layer := range source.Layers {
		if layer.Name == name {
			return index
		}
	}
	t.Fatalf("saved document is missing layer %q", name)
	return 0
}
