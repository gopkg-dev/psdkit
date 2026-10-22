package psdscene

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func projectiveTransparentSource(t *testing.T) []byte {
	t.Helper()
	prototype := cachedTestSource(nil)
	child := cachedOpen(t, prototype)
	for _, channel := range child.Layers[0].Channels {
		if channel.ID == -1 {
			prototype[channel.Data.Offset], prototype[channel.Data.Offset+1] = 0, 255
		}
	}
	data := dropShadowSceneSource(t, true, nil, 0, prototype)
	source := cachedOpen(t, data)
	view, err := source.ReadSmartObjectView(nil)
	if err != nil || len(view.Records) != 1 {
		t.Fatalf("original placement fixture is absent: %v", err)
	}
	quad := []float64{6, 5, 22, 7, 20, 17, 8, 16}
	for _, entry := range view.Records[0].Descriptor.Entries {
		if entry.Key != "Trnf" {
			continue
		}
		for index, value := range entry.Value.([]psdfile.DescriptorValue) {
			binary.BigEndian.PutUint64(data[value.Data.Offset:], math.Float64bits(quad[index]))
		}
	}
	metadata, err := source.ReadLayerMetadata(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Disable only the shadow in this source to qualify the projection option independently of effects.
	for _, entry := range metadata.Layer.Effects.Items[0].Descriptor.Entries {
		if entry.Key == "enab" {
			data[entry.Data.Offset] = 0
		}
	}
	return data
}

func TestApproximatePlacedProjectionIsIndependentAndNeverUsesCaches(t *testing.T) {
	data := projectiveTransparentSource(t)
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := RenderPlacedLayer(nil, source, 1); !errors.Is(err, psdrender.ErrUnsupportedCrop) {
		t.Fatalf("default projection unexpectedly accepted unqualified transparent projective borders: %v", err)
	}
	forbidPlacedSnapshots(t, source, 0, reader)
	settings := Options{ApproximatePlacedProjection: true, MaxWorkingBytes: 4 << 20}
	full, err := Render(nil, source, settings)
	if err != nil || reader.reads != 0 {
		t.Fatalf("explicit source projection required shadow policy or used stored caches: %v", err)
	}
	if bytes.Equal(full.Pix, make([]byte, len(full.Pix))) {
		t.Fatal("explicit original-source projection produced no artwork")
	}
	tileSettings := TileOptions{ApproximatePlacedProjection: true, MaxWorkingBytes: 4 << 20}
	if err := VisitTiles(nil, source, full.Rect, image.Pt(3, 4), func(tile *image.NRGBA) error {
		for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
			for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
				if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
					t.Fatalf("explicit source projection seam at (%d,%d)", x, y)
				}
			}
		}
		return nil
	}, tileSettings); err != nil || reader.reads != 0 {
		t.Fatalf("regional source projection used a stored cache or failed: %v", err)
	}
	layer, err := RenderPlacedLayer(nil, source, 1, settings)
	if err != nil || reader.reads != 0 || !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		t.Fatalf("single original placed source did not inherit the explicit projection policy: %v", err)
	}
}
