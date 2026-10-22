package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestCreateTransparentPSDAndPSBSupportsEditingAndSaving(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			editor, err := Create(nil, 3, 2, CreateOptions{Version: version})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = editor.Close() })
			changes, err := editor.Changes()
			if err != nil || !changes.Dirty || changes.Revision != 1 {
				t.Fatal("new unsaved document was reported as saved")
			}
			layers, err := editor.Layers()
			if err != nil || len(layers) != 1 || layers[0].ID != 1 || layers[0].Bounds != image.Rect(0, 0, 3, 2) {
				t.Fatalf("initial editable layer: %v", err)
			}
			preview, err := editor.Composite(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range preview.Pix {
				if value != 0 {
					t.Fatal("new transparent document had opaque or colored samples")
				}
			}
			pixels := image.NewNRGBA(layers[0].Bounds)
			pixels.SetNRGBA(1, 1, color.NRGBA{R: 180, G: 50, B: 90, A: 128})
			if err := editor.ReplacePixels(1, pixels); err != nil {
				t.Fatal(err)
			}
			if err := editor.Rename(1, "新建图层"); err != nil {
				t.Fatal(err)
			}
			pixels.SetNRGBA(1, 1, color.NRGBA{G: 255, A: 255})
			var encoded bytes.Buffer
			if err := editor.Write(nil, &encoded, SaveOptions{Compression: 1}); err != nil {
				t.Fatal(err)
			}
			changes, err = editor.Changes()
			if err != nil || changes.Dirty {
				t.Fatal("successful creation save did not mark current revision saved")
			}
			source, err := psdfile.Parse(encoded.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			if source.Header.Version != version || len(source.Layers) != 1 || !source.GlobalAlpha ||
				source.Layers[0].Name != "新建图层" {
				t.Fatal("new document lost format, alpha or editable Unicode name")
			}
			alphaIndex := -1
			for index, channel := range source.Layers[0].Channels {
				if channel.ID == -1 {
					alphaIndex = index
				}
			}
			plane, err := source.DecodeChannel(nil, 0, alphaIndex)
			if err != nil || !bytes.Equal(plane.Data, []byte{0, 0, 0, 0, 128, 0}) {
				t.Fatalf("created layer alpha or owned input changed: %v", err)
			}
			reopened, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := reopened.Composite(nil)
			if err != nil || actual.NRGBAAt(1, 1) != (color.NRGBA{R: 180, G: 50, B: 90, A: 128}) {
				t.Fatalf("created document did not rebuild original layer pixels: %v", err)
			}
			_ = reopened.Close()
		})
	}
}

func TestCreateDoesNotMaterializeBlankPixelPlanes(t *testing.T) {
	reader := blankDocumentSource(2, 1000, 1000)
	if len(reader.prefix) > 512 || reader.size < 8_000_000 {
		t.Fatal("blank source allocated its virtual channel or composite storage")
	}
	data := []byte{9, 9, 9, 9}
	if count, err := reader.ReadAt(data, reader.size-2); count != 2 || !errors.Is(err, io.EOF) ||
		!bytes.Equal(data, []byte{0, 0, 9, 9}) {
		t.Fatalf("virtual final samples did not follow ReaderAt semantics: %d, %v, %v", count, err, data)
	}
	for _, dimensions := range [][2]int{{0, 1}, {-1, 1}, {30001, 1}, {5000, 5000}} {
		if _, err := Create(nil, dimensions[0], dimensions[1]); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("invalid canvas created: %v", err)
		}
	}
	if _, err := Create(nil, 1, 1, CreateOptions{Version: 3}); !errors.Is(err, ErrInvalidEdit) {
		t.Fatal("unknown PSD version created")
	}
	if _, err := Create(nil, 2, 2,
		CreateOptions{Limits: psdfile.Limits{MaxDecodePixels: 3}}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatal("new canvas ignored caller pixel budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Create(ctx, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled creation returned an editor")
	}
}
