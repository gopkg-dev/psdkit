package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func smartFilterNativeFixture(t *testing.T) (*psdfile.Document, *Editor, psdfilter.Stack, *image.NRGBA) {
	t.Helper()
	directory := filepath.Join("..", "output", "smart-filter-native")
	path := filepath.Join(directory, "alpha-r1.psd")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("optional native smart-filter fixture is absent")
	}
	source, err := psdfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { editor.Close() })
	placements, err := source.SmartObjects(nil)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placements: %v", err)
	}
	view, err := psdfile.DecodeSmartFilters(nil, placements[0])
	if err != nil {
		t.Fatal(err)
	}
	stack := psdfilter.Stack{Enabled: view.Enabled}
	for _, record := range view.Filters {
		stack.Filters = append(stack.Filters, record.Operation)
	}
	stack.Filters[0].Radius = 3
	file, err := os.Open(filepath.Join(directory, "alpha-r3.png"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	pixels := image.NewNRGBA(decoded.Bounds())
	draw.Draw(pixels, pixels.Rect, decoded, decoded.Bounds().Min, draw.Src)
	return source, editor, stack, pixels
}

func TestSmartFilterEditOwnsAccurateCacheAndSourceAwareSave(t *testing.T) {
	source, editor, stack, pixels := smartFilterNativeFixture(t)
	before := bytes.Clone(pixels.Pix)
	if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
		t.Fatal(err)
	}
	changes, err := editor.Changes()
	if err != nil || changes.Revision != 1 {
		t.Fatalf("revision: %+v, %v", changes, err)
	}
	if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels}); err != nil {
		t.Fatal(err)
	}
	afterNoop, err := editor.Changes()
	if err != nil || afterNoop.Revision != changes.Revision {
		t.Fatalf("repeat no-op: %+v, %v", afterNoop, err)
	}
	pixels.Pix[0], stack.Filters[0].Radius = 255, 20
	composite, err := editor.Composite(nil)
	if err != nil || !bytes.Equal(composite.Pix, before) {
		t.Fatalf("accurate caller cache is not owned or automatically composited: %v", err)
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	result, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	placements, err := result.SmartObjects(nil)
	if err != nil || len(placements) != 1 {
		t.Fatalf("saved placement: %v", err)
	}
	view, err := psdfile.DecodeSmartFilters(nil, placements[0])
	if err != nil || view.Filters[0].Operation.Radius != 3 || !view.Enabled {
		t.Fatalf("saved live controls: %+v, %v", view, err)
	}
	for _, original := range source.AdditionalInfo {
		found := false
		for _, candidate := range result.AdditionalInfo {
			if candidate.Key != original.Key {
				continue
			}
			before, err := source.ReadSpan(original.Data)
			if err != nil {
				t.Fatal(err)
			}
			after, err := result.ReadSpan(candidate.Data)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unrelated global block %s changed: %v", original.Key, err)
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("global block %s lost", original.Key)
		}
	}
}

func TestSmartFilterEditRejectsMissingPolicyCancellationAndInvalidFamilyAtomically(t *testing.T) {
	_, editor, stack, pixels := smartFilterNativeFixture(t)
	original, err := editor.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing explicit live filter policy: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	editErr := editor.SetSmartFilterParameters(ctx, 1, stack, SmartFilterEditOptions{Pixels: pixels})
	if !errors.Is(editErr, context.Canceled) {
		t.Fatalf("cancelled edit: %v", editErr)
	}
	stack.Filters[0].Kind = "other"
	editErr = editor.SetSmartFilterParameters(nil, 1, stack, SmartFilterEditOptions{Pixels: pixels})
	if !errors.Is(editErr, psdfile.ErrUnsupported) {
		t.Fatalf("family change: %v", editErr)
	}
	after, err := editor.Changes()
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Fatalf("failed edits changed revision: %+v, %v", after, err)
	}
	if len(editor.roots[0].additional) != 0 || editor.roots[0].pixels != nil || editor.roots[0].placed != nil {
		t.Fatal("failed edit left partial overlays")
	}
}
