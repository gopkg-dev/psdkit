package psdedit_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestWriterScratchKeepsMetadataOnlyTinyDecodeLimit(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := editAcceptanceDocument(editAcceptanceFixtureOptions{version: version, advanced: true})
		source, err := psdfile.Parse(data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 1}})
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		editor, err := psdedit.New(source)
		if err != nil {
			t.Fatal(err)
		}
		defer editor.Close()
		layers, err := editor.Layers()
		if err != nil || len(layers) == 0 {
			t.Fatal(err)
		}
		if err := editor.Rename(layers[0].ID, "tiny_decode_copy"); err != nil {
			t.Fatal(err)
		}
		var first, second bytes.Buffer
		if err := editor.Write(context.Background(), &first); err != nil {
			t.Fatal("new scratch rejected old streaming limits", err)
		}
		if err := editor.Write(context.Background(), &second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatal("repeated metadata save changed", err)
		}
	}
}
