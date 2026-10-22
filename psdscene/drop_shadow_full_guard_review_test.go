package psdscene

import (
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestIndependentDropShadowFullRenderForbidsPlacedCacheAndFooter(t *testing.T) {
	global := false
	data := dropShadowSceneSource(t, true, &global, 1.5)
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, channel := range source.Layers[1].Channels {
		if channel.ID >= -1 {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	full, err := Render(nil, source, Options{Compose: psdcompose.Options{ApproximateDropShadow: true}})
	if err != nil || reader.reads != 0 || full.Bounds() != image.Rect(0, 0, 32, 24) {
		t.Fatalf("full source render read cached parent/footer: %d / %v", reader.reads, err)
	}
}
