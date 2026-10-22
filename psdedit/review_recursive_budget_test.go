package psdedit

import (
	"bytes"
	"errors"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type reviewNestedHeaderReader struct {
	data         []byte
	headerOffset int64
	headerReads  int
}

func (reader *reviewNestedHeaderReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset == reader.headerOffset && len(data) == 26 {
		reader.headerReads++
	}
	return bytes.NewReader(reader.data).ReadAt(data, offset)
}

func TestReviewSceneStopsBeforeEmbeddedIndexingAtZeroRemainingBudget(t *testing.T) {
	image := smartEditImage([4]color.NRGBA{{1, 2, 3, 255}, {4, 5, 6, 255}, {7, 8, 9, 255}, {10, 11, 12, 255}})
	data := sceneNestedFixture(t, smartEditPrototype(1, image), "")
	reader := &reviewNestedHeaderReader{data: data, headerOffset: -1}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	metadata, err := source.RGB8SnapshotMetadataBytes()
	if err != nil {
		t.Fatal(err)
	}
	placements, err := source.ReadSmartObjectView(nil)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := source.ReadLinkedFileView(nil)
	if err != nil || len(linked.Records) != 1 {
		t.Fatalf("nested fixture metadata: %v", err)
	}
	reader.headerOffset = linked.Records[0].Data.Offset
	limit := metadata + int64(len(source.Layers))*1024 + int64(source.Header.Width)*int64(source.Header.Height)*4
	for _, layer := range source.Layers {
		limit += int64(layer.Bounds.Dx()) * int64(layer.Bounds.Dy()) * 4
	}
	limit += placements.MetadataBytes + linked.MetadataBytes
	_, err = psdscene.Render(nil, source, psdscene.Options{MaxWorkingBytes: limit})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("exactly exhausted parent budget should reject its child: %v", err)
	}
	if reader.headerReads != 0 {
		t.Fatalf("zero child metadata allowance became a default indexing budget: %d child header reads", reader.headerReads)
	}
}
