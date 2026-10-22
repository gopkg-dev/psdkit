package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func forbidPlacedSnapshots(t *testing.T, source *psdfile.Document, base int64, reader *tilePlacedSparseReader) {
	t.Helper()
	reader.forbidden = append(reader.forbidden,
		psdfile.Span{Offset: base + source.Composite.Data.Offset, Length: source.Composite.Data.Length})
	for index, record := range source.Layers {
		view, err := source.ReadLayerMetadata(nil, index)
		if err != nil {
			t.Fatal(err)
		}
		if view.Layer != nil && view.Layer.Placed != nil {
			for _, channel := range record.Channels {
				if channel.ID >= -1 {
					reader.forbidden = append(reader.forbidden,
						psdfile.Span{Offset: base + channel.Data.Offset, Length: channel.Data.Length})
				}
			}
		}
	}
	view, err := source.ReadLinkedFileView(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range view.Records {
		child, err := source.OpenLinkedDocument(nil, record)
		if err != nil {
			t.Fatal(err)
		}
		forbidPlacedSnapshots(t, child, base+record.Data.Offset, reader)
		if err := child.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDropShadowSceneRecursiveSourcesAvoidCachesAndRespectDepth(t *testing.T) {
	child := dropShadowSceneSource(t, true, nil, 0)
	data := dropShadowSceneSource(t, true, nil, 0, child)
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	forbidPlacedSnapshots(t, source, 0, reader)
	settings := Options{Compose: psdcompose.Options{ApproximateDropShadow: true}, MaxWorkingBytes: 4 << 20}
	full, err := Render(nil, source, settings)
	if err != nil || reader.reads != 0 {
		t.Fatalf("nested live sources read placed cache/footer bytes or failed: %v", err)
	}
	tile, err := RenderTile(nil, source, image.Rect(4, 3, 27, 20), TileOptions{Compose: settings.Compose})
	if err != nil || reader.reads != 0 {
		t.Fatalf("nested ROI source read placed cache/footer bytes or failed: %v", err)
	}
	for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
		for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
			if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("nested source ROI differs at (%d,%d)", x, y)
			}
		}
	}
	if bytes.Equal(full.Pix, bytes.Repeat([]byte{255}, len(full.Pix))) {
		t.Fatal("nested source used the poison merged footer")
	}
	for _, depth := range []int{1, 2} {
		settings.MaxDepth = depth
		if _, err := Render(nil, source, settings); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("recursive complete source depth was reset by regional delegation: depth=%d error=%v", depth, err)
		}
	}
	settings.MaxDepth, settings.MaxDocuments = 16, 2
	if _, err := Render(nil, source, settings); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("nested source document quota was reset by regional delegation: %v", err)
	}
}

func TestDropShadowSourcePolicyRejectsExternalAncestorCycle(t *testing.T) {
	source, reader, _ := tilePlacedLargeSource(t, true)
	settings := TileOptions{Compose: psdcompose.Options{ApproximateDropShadow: true},
		MaxWorkingBytes: 4 << 20, ExternalResolver: func(
			context.Context, psdfile.LinkedFileRecord,
		) (*psdfile.Document, error) {
			return source, nil
		}}
	rectangle := image.Rect(234_567, 299_999, 234_569, 300_000)
	if _, err := RenderTile(nil, source, rectangle, settings); !errors.Is(err, psdfile.ErrMalformed) || reader.reads != 0 {
		t.Fatalf("source effect policy accepted an ancestor document or read a saved cache: %v", err)
	}
}
