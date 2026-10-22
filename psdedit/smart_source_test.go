package psdedit

import (
	"context"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestOpenSmartObjectSourceOriginalAndOwnedReplacement(t *testing.T) {
	editor, source := smartEditOpen(t, smartEditFixture(t, 1, ""))
	items, err := editor.SmartObjects(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("smart-object views: %d, %v", len(items), err)
	}
	original, err := editor.OpenSmartObjectSource(context.Background(), items[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	pixels := smartEditImage(smartEditColors())
	if err := editor.ReplaceContents(context.Background(), items[0].ID, smartEditPrototype(1, pixels)); err != nil {
		t.Fatal(err)
	}
	owned, err := editor.OpenSmartObjectSource(context.Background(), items[1].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := original.DecodeComposite(context.Background()); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("borrowed source survived original owner close: %v", err)
	}
	decoded, err := owned.Decode(context.Background())
	if err != nil || decoded.Layers[0].Pixels.NRGBAAt(0, 0) != pixels.NRGBAAt(0, 0) {
		t.Fatalf("replacement source ownership/pixels: %v", err)
	}
}

func TestOpenSmartObjectSourceRejectsNonPlacedAndCanceledLookup(t *testing.T) {
	editor, _ := smartEditOpen(t, smartEditFixture(t, 1, ""))
	items, err := editor.SmartObjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := editor.OpenSmartObjectSource(ctx, items[0].ID, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup = %v", err)
	}
	if _, err := editor.OpenSmartObjectSource(context.Background(), LayerID(99999), nil); err == nil {
		t.Fatal("unknown layer accepted")
	}
}
