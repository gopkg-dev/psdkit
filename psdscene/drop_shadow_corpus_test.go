package psdscene

import (
	"errors"
	"image"
	"os"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

type dropShadowCorpusReader struct {
	file      *os.File
	forbidden []psdfile.Span
	attempts  int
}

func (reader *dropShadowCorpusReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) != 0 {
		for _, span := range reader.forbidden {
			if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
				reader.attempts++
				return 0, errors.New("real-template saved placed pixels and merged footer are forbidden")
			}
		}
	}
	return reader.file.ReadAt(data, offset)
}

// TestDropShadowRealTemplate keeps user-owned templates outside the portable test fixtures.
// Set PSDKIT_DROP_SHADOW_PSD to an exact source path and PSDKIT_DROP_SHADOW_LAYER to its physical layer index.
func TestDropShadowRealTemplate(t *testing.T) {
	path := os.Getenv("PSDKIT_DROP_SHADOW_PSD")
	if path == "" {
		t.Skip("PSDKIT_DROP_SHADOW_PSD selects an original read-only template")
	}
	index, err := strconv.Atoi(os.Getenv("PSDKIT_DROP_SHADOW_LAYER"))
	if err != nil {
		t.Fatal("PSDKIT_DROP_SHADOW_LAYER must select one physical original placed layer")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader := &dropShadowCorpusReader{file: file}
	source, err := psdfile.Read(reader, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if index < 0 || index >= len(source.Layers) {
		t.Fatal("real source index is out of range")
	}
	view, err := source.ReadLayerMetadata(nil, index)
	if err != nil || view.Layer == nil || view.Layer.Placed == nil || !activeLayerEffects(view.Layer) {
		t.Fatalf("selected real source is not a placed layer with active effects: %v", err)
	}
	shadows := 0
	for _, effect := range view.Layer.Effects.Items {
		if effect.Enabled && effect.Opacity != 0 {
			if effect.Kind != psdeffects.DropShadow {
				t.Fatalf("selected source has additional active effect kind %q", effect.Kind)
			}
			shadows++
		}
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	for _, channel := range source.Layers[index].Channels {
		if channel.ID >= -1 {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
	}
	configuration := psdcompose.Options{ApproximateDropShadow: true, MaxWorkingBytes: 256 << 20}
	projection := os.Getenv("PSDKIT_APPROXIMATE_PLACED_PROJECTION") == "1"
	settings := Options{Compose: configuration, MaxWorkingBytes: 256 << 20,
		ApproximatePlacedProjection: projection}
	layer, err := RenderPlacedLayer(nil, source, index, settings)
	if err != nil {
		t.Fatalf("original template %q layer %d %q: %v", path, index, view.Layer.Name, err)
	}
	if reader.attempts != 0 || !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		t.Fatal("real-template materialization used saved placed or merged pixels, or omitted runtime provenance")
	}
	t.Logf("Original template %s, layer %d %q, %d shadow(s), parent=%dx%d, projected=%v, cached/footer reads=0",
		path, index, layer.Name, shadows, source.Header.Width, source.Header.Height, layer.Bounds)
	// The complete template renderer may decode ordinary raster sources; only its merged footer remains forbidden.
	reader.forbidden = reader.forbidden[:1]
	full, err := Render(nil, source, settings)
	if err != nil {
		t.Fatalf("complete original template has unsupported independent semantics: %v", err)
	}
	center := layer.Bounds.Intersect(full.Rect)
	if center.Empty() {
		t.Fatal("selected actual template shadow has no informative parent-canvas source intersection")
	}
	window := image.Rect(center.Min.X, center.Min.Y,
		min(center.Max.X, center.Min.X+67), min(center.Max.Y, center.Min.Y+53))
	compare := func(tile *image.NRGBA) error {
		for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
			for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
				if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
					t.Fatalf("real-template full/ROI mismatch (%d,%d): %v != %v",
						x, y, tile.NRGBAAt(x, y), full.NRGBAAt(x, y))
				}
			}
		}
		return nil
	}
	tileSettings := TileOptions{Compose: configuration, MaxWorkingBytes: 256 << 20,
		ApproximatePlacedProjection: projection}
	tile, err := RenderTile(nil, source, window, tileSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := compare(tile); err != nil {
		t.Fatal(err)
	}
	if err := VisitTiles(nil, source, window, image.Pt(17, 11), compare, tileSettings); err != nil {
		t.Fatal(err)
	}
	if reader.attempts != 0 {
		t.Fatal("complete/ROI/visitor original source rendering attempted to read the saved merged footer")
	}
}
