package psdedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type smartContentsBoundedReader struct {
	reader  *bytes.Reader
	largest int
	onRead  func(int64)
}

func (reader *smartContentsBoundedReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.largest = max(reader.largest, len(data))
	if reader.onRead != nil {
		reader.onRead(offset)
	}
	return reader.reader.ReadAt(data, offset)
}

func smartContentsOpenBounded(
	t *testing.T, encoded []byte, limit int64,
) (*Editor, *psdfile.Document, *smartContentsBoundedReader) {
	t.Helper()
	reader := &smartContentsBoundedReader{reader: bytes.NewReader(encoded)}
	source, err := psdfile.Read(reader, int64(len(encoded)),
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxReadBytes: limit}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	return editor, source, reader
}

func smartContentsLargeFixture(t *testing.T, version uint16) ([]byte, []byte) {
	t.Helper()
	pixels := smartDimensionsPixels(32, 32)
	contents := smartSizedPrototype(version, pixels.Rect.Size(), pixels.Rect, pixels)
	tags := linkedContainerTag(version, false, "lnk2", writerLinkedEntry("liFD", "shared", 7, contents).data)
	return linkedContainerFixture(t, version, false, tags, "shared", "shared"), contents
}

func TestReadSmartObjectContentsChunksOriginalOverlayAndReopenedSources(t *testing.T) {
	const limit = int64(1024)
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			fixture, expected := smartContentsLargeFixture(t, version)
			if int64(len(expected)) <= limit {
				t.Fatal("fixture source must exceed one permitted read")
			}
			editor, _, reader := smartContentsOpenBounded(t, fixture, limit)
			objects, err := editor.SmartObjects(t.Context())
			if err != nil || len(objects) != 2 {
				t.Fatalf("source placements: %v", err)
			}
			reader.largest = 0
			original, err := editor.ReadSmartObjectContents(t.Context(), objects[0].ID, nil)
			if err != nil || !bytes.Equal(original, expected) {
				t.Fatalf("original source cannot be read in bounded chunks: %v", err)
			}
			originalLargest := reader.largest
			pixels := smartDimensionsPixels(32, 32)
			pixels.Pix[0] ^= 0xff
			replacement := smartSizedPrototype(version, image.Pt(32, 32), pixels.Rect, pixels)
			if err := editor.ReplaceContents(t.Context(), objects[1].ID, replacement); err != nil {
				t.Fatal(err)
			}
			reader.largest = 0
			overlay, err := editor.ReadSmartObjectContents(t.Context(), objects[0].ID, nil)
			if err != nil || !bytes.Equal(overlay, replacement) {
				t.Fatalf("edited source bytes differ: %v", err)
			}
			overlayLargest := reader.largest
			var output bytes.Buffer
			if err := editor.Write(t.Context(), &output, SaveOptions{Compression: 1}); err != nil {
				t.Fatal(err)
			}
			reopened, _, reopenedReader := smartContentsOpenBounded(t, output.Bytes(), limit)
			objects, err = reopened.SmartObjects(t.Context())
			if err != nil || len(objects) != 2 {
				t.Fatalf("reopened placements: %v", err)
			}
			reopenedReader.largest = 0
			persisted, err := reopened.ReadSmartObjectContents(t.Context(), objects[1].ID, nil)
			if err != nil || !bytes.Equal(persisted, overlay) {
				t.Fatalf("saved source cannot be read using the same chunk budget: %v", err)
			}
			if int64(max(originalLargest, overlayLargest, reopenedReader.largest)) > limit {
				t.Fatalf("source reads exceeded MaxReadBytes: original=%d overlay=%d reopened=%d",
					originalLargest, overlayLargest, reopenedReader.largest)
			}
		})
	}
}

func TestReadSmartObjectContentsCancelsBetweenEncodedSourceChunks(t *testing.T) {
	fixture, _ := smartContentsLargeFixture(t, 1)
	editor, source, reader := smartContentsOpenBounded(t, fixture, 1024)
	objects, err := editor.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	links, err := source.LinkedFiles(t.Context())
	if err != nil || len(links) != 1 {
		t.Fatalf("linked source: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	span := links[0].Data
	reader.onRead = func(offset int64) {
		if offset >= span.Offset && offset < span.Offset+span.Length {
			cancel()
		}
	}
	result, err := editor.ReadSmartObjectContents(ctx, objects[0].ID, nil)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("source chunk cancellation returned partial bytes or the wrong error: %v", err)
	}
	if editor.revision != 0 || len(editor.linkedOverlays) != 0 {
		t.Fatal("canceled source read changed editor state")
	}
}
