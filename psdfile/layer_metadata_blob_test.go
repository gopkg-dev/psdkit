package psdfile

import (
	"testing"
)

func TestLayerMetadataDoesNotChargeDeferredPlacementBlobAsDecodedTree(t *testing.T) {
	entries := smartFixtureEntries()
	blob := append(descriptorFixtureU32(2<<20), make([]byte, 2<<20)...)
	entries = append(entries, descriptorFixtureEntry{key: "unrecognizedFilterMedia", kind: "tdta", data: blob})
	source := smartFixtureParse(t, 1, "SoLd", smartFixturePlacement(4, entries, nil))
	view, err := source.ReadLayerMetadata(nil, 0, MetadataOptions{MaxBytes: 256 << 10})
	if err != nil || view.Layer == nil || view.Layer.Placed == nil || view.WorkingBytes >= 256<<10 {
		t.Fatalf("lazy blob incorrectly counted as retained semantic data: %+v/%v", view, err)
	}
	if view.Layer.Placed.Width != 100 || view.Layer.Placed.Height != 50 {
		t.Fatalf("placement geometry changed while skipping opaque media: %+v", view.Layer.Placed)
	}
}
