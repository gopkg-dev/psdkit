package psdweb

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestRawStructureAndGuardRejectParentArtwork(t *testing.T) {
	engine, _ := New(Config{})
	opened := bridgeCall(t, engine, "demo.create", "", 32, 24, 2).Data.(map[string]any)
	id := opened["document"].(string)
	structure := bridgeCall(t, engine, "document.structure", "", ref(id))
	view := structure.Data.(map[string]any)["results"].([]any)[0].(map[string]any)
	if view["Resources"] == nil || view["LayerSets"] == nil || view["Source"] == nil {
		t.Fatal("original block structure omitted")
	}
	source := engine.resources[id].value.Interface().(*psdfile.Document)
	block := bridgeCall(t, engine, "document.CopySpan", id, source.HeaderData)
	if len(block.Binaries) != 1 || block.Binaries[0].MIME != "application/octet-stream" ||
		len(block.Binaries[0].Data) != int(source.HeaderData.Length) {
		t.Fatal("raw block export was mislabeled as an entire PSD")
	}
	guarded, audit, owned, err := engine.projectionSource(source)
	if err != nil || !owned || !audit.Verified {
		t.Fatal("failed to index guarded metadata", err)
	}
	defer guarded.Close()
	if audit.reader.denied != 0 || guarded.Header != source.Header {
		t.Fatal("structural indexing read saved artwork")
	}
	for _, span := range audit.reader.spans {
		if span.Length == 0 {
			continue
		}
		if _, err := audit.reader.ReadAt(make([]byte, 1), span.Offset); err == nil {
			t.Fatal("saved parent bytes reached renderer")
		}
	}
	if audit.reader.denied == 0 {
		t.Fatal("no payload ranges were guarded")
	}
}

func TestTileExportContainsRealPositionedPNGs(t *testing.T) {
	engine, _ := New(Config{})
	opened := bridgeCall(t, engine, "demo.create", "", 32, 24, 1).Data.(map[string]any)
	source := engine.resources[opened["document"].(string)].value.Interface().(*psdfile.Document)
	output, err := engine.renderTiles(context.Background(), source, image.Rect(0, 0, 32, 24), image.Pt(16, 12))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Data), int64(len(output.Data)))
	if err != nil || len(archive.File) != 4 {
		t.Fatal("incomplete tile archive", err)
	}
	for _, entry := range archive.File {
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		pixels, err := png.Decode(bytes.NewReader(data))
		if err != nil || pixels.Bounds().Size() != image.Pt(16, 12) {
			t.Fatal("tile is not a real Go-rendered PNG", err)
		}
	}
}
