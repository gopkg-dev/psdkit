package psdfile_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestDecodeLayersIgnoresMalformedSavedPreviewButKeepsLayerPixels(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		fixture := acceptanceDocument(version, 8, 1, 1, "")
		original := acceptanceOpen(t, fixture)
		complete, err := original.Decode(nil)
		if err != nil || len(complete.Layers) != 1 {
			t.Fatalf("independent fixture is invalid: %v", err)
		}
		corrupted := bytes.Clone(fixture.data)
		countWidth := int64(2)
		if version == 2 {
			countWidth = 4
		}
		payload := original.Composite.Data.Offset + int64(original.Header.Height)*int64(original.Header.Channels)*countWidth
		// The indexed row sizes remain valid; a literal run no longer fits its encoded row payload.
		corrupted[payload] = 127
		source, err := psdfile.Parse(corrupted)
		if err != nil {
			t.Fatalf("malformed pixels must stay deferred from valid indexing: %v", err)
		}
		defer source.Close()
		if _, err := source.Decode(nil); !errors.Is(err, psdfile.ErrMalformed) {
			t.Fatalf("saved preview corruption was not detected when explicitly decoded: %v", err)
		}
		layers, err := source.DecodeLayers(nil)
		if err != nil || layers.Composite != nil || len(layers.Layers) != 1 ||
			!bytes.Equal(layers.Layers[0].Pixels.Pix, complete.Layers[0].Pixels.Pix) {
			t.Fatalf("layer-only reconstruction depended on the stale footer: %v", err)
		}
	}
}
