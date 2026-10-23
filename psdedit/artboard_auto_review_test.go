package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdartboard"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestArtboardAutoReviewExhaustedPreparationBudgetCannotReboundToDefault(t *testing.T) {
	editor := compositeEditor(t, artboardAutoFixture(t, 2, 1, 9))
	boards, metadataBytes, err := editor.prepareCompositeArtboardsLocked(t.Context(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := editor.source.DecodeLayers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	physical := flattenSnapshot(snapshot.Layers)
	preparationBytes := metadataBytes
	for index, board := range boards {
		_, cost, err := psdartboard.Prepare(t.Context(), physical[index], board.data, image.Rect(0, 0, 5, 1), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		preparationBytes += cost
	}
	before, _ := editor.Changes()
	for _, limit := range []int64{1, metadataBytes, preparationBytes - 1, preparationBytes} {
		pixels, err := editor.Composite(t.Context(), psdcompose.Options{MaxWorkingBytes: limit})
		if pixels != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("budget %d rebounded to an implicit compositor default: %v", limit, err)
		}
		if after, _ := editor.Changes(); after != before {
			t.Fatal("failed preparation budget changed revisions")
		}
	}
	expected := artboardAutoExpected(color.NRGBA{R: 255, G: 127, B: 127, A: 255},
		color.NRGBA{G: 200, B: 100, A: 255})
	for _, options := range []psdcompose.Options{{}, {MaxWorkingBytes: 1 << 20}} {
		pixels, err := editor.Composite(t.Context(), options)
		if err != nil || !bytes.Equal(pixels.Pix, expected.Pix) {
			t.Fatalf("failed budget attempt corrupted subsequent valid rendering: %v", err)
		}
	}
	if pixels, err := editor.Composite(t.Context(), psdcompose.Options{MaxLayers: 1}); pixels != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("synthetic background bypassed compositor layer budget: %v", err)
	}
}

func TestArtboardAutoReviewSelectedOrdinaryGroupExcludesAncestorClipAndBackground(t *testing.T) {
	editor := compositeEditor(t, artboardAutoFixture(t, 2, 1, -17))
	left, right := artboardAutoLayer(t, editor, "left-board"), artboardAutoLayer(t, editor, "right-board")
	group, err := editor.AddGroup(left, 1, "ordinary-inside-board", true)
	if err != nil {
		t.Fatal(err)
	}
	blue := artboardAutoSolid(color.NRGBA{B: 255, A: 128})
	if _, err := editor.AddRaster(group, 0, "ordinary-wide-child", blue, image.Point{}); err != nil {
		t.Fatal(err)
	}
	if err := editor.Move(right, 0, 0); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	selected, err := editor.CompositeGroup(t.Context(), group)
	if err != nil || selected.Rect != blue.Rect || !bytes.Equal(selected.Pix, blue.Pix) {
		t.Fatalf("ordinary group inherited its artboard ancestor or lost document coordinates: %v", err)
	}
	if after, _ := editor.Changes(); before != after {
		t.Fatal("ordinary group preview changed revision")
	}
	// The same child is clipped when rendering its parent board, after source indices have been reordered.
	board, err := editor.CompositeGroup(t.Context(), left)
	if err != nil || board.Rect != image.Rect(0, 0, 2, 1) || board.NRGBAAt(0, 0).A != 255 {
		t.Fatalf("reordered original board identity lost its own clipping or background: %v", err)
	}
	metadata := artboardAutoMetadata(t, editor.source)
	saved := compositeSave(t, editor, SaveOptions{Compression: 3})
	if len(saved.Layers) != 10 || !reflect.DeepEqual(metadata, artboardAutoMetadata(t, saved)) {
		t.Fatal("snapshot backgrounds leaked into serialized layers or changed original board metadata")
	}
	for _, record := range saved.Layers {
		if record.Name == "Artboard background" {
			t.Fatal("synthetic background was serialized into PSB")
		}
	}
	reopened, err := New(saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	group = artboardAutoLayer(t, reopened, "ordinary-inside-board")
	selected, err = reopened.CompositeGroup(t.Context(), group)
	if err != nil || selected.Rect != blue.Rect || !bytes.Equal(selected.Pix, blue.Pix) {
		t.Fatalf("ordinary-group isolation changed after PSB reopen: %v", err)
	}
}

func TestArtboardAutoReviewRepeatedAndFailedSnapshotsDoNotPolluteMasksOrWriterTree(t *testing.T) {
	editor := compositeEditor(t, editableDisabledArtboardFixture(t, 2))
	metadata := disabledArtboardSavedMetadata(t, editor.source)
	blue := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	blue.SetNRGBA(0, 0, color.NRGBA{B: 255, A: 128})
	if err := editor.ReplacePixels(2, blue); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	layersBefore, _ := editor.Layers()
	physicalBefore := editor.writeLayersLocked()
	ownedBefore := editor.ownedBytesLocked(nil)
	for range 3 {
		pixels, err := editor.Composite(t.Context())
		if err != nil || pixels.NRGBAAt(0, 0) != (color.NRGBA{R: 127, G: 127, B: 255, A: 255}) {
			t.Fatalf("repeated artboard snapshot changed pixels: %v", err)
		}
		clear(pixels.Pix)
		group, err := editor.CompositeGroup(t.Context(), 3)
		if err != nil || group.NRGBAAt(0, 0) != (color.NRGBA{R: 127, G: 127, B: 255, A: 255}) {
			t.Fatalf("caller mutation or repeated snapshot changed saved mask semantics: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if pixels, err := editor.CompositeGroup(ctx, 3); pixels != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled group snapshot exposed output: %v", err)
		}
	}
	layersAfter, _ := editor.Layers()
	if after, _ := editor.Changes(); before != after || !reflect.DeepEqual(layersBefore, layersAfter) ||
		!reflect.DeepEqual(physicalBefore, editor.writeLayersLocked()) || ownedBefore != editor.ownedBytesLocked(nil) {
		t.Fatal("artboard preparation changed edit nodes, revisions or retained allocation")
	}
	saved := compositeSave(t, editor, SaveOptions{Compression: 3})
	if len(saved.Layers) != 3 || !reflect.DeepEqual(metadata, disabledArtboardSavedMetadata(t, saved)) {
		t.Fatal("temporary clipping replaced the real mask metadata/samples or added fake output layers")
	}
	if err := editor.SetOpacity(3, 254); err != nil {
		t.Fatal(err)
	}
	before, _ = editor.Changes()
	var output bytes.Buffer
	if err := editor.Write(t.Context(), &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("unsupported board group opacity wrote a partial file: %v", err)
	}
	if after, _ := editor.Changes(); before != after || len(editor.writeLayersLocked()) != 3 {
		t.Fatal("failed automatic save changed revisions or the real serialization tree")
	}
	if err := editor.SetOpacity(3, 255); err != nil {
		t.Fatal(err)
	}
	again := compositeSave(t, editor)
	if !reflect.DeepEqual(metadata, disabledArtboardSavedMetadata(t, again)) {
		t.Fatal("recovering from a rejected automatic save changed original masks")
	}
}
