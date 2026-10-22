package psdkit_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit"
)

func TestPublicReadRenderEncodePipeline(t *testing.T) {
	var source bytes.Buffer
	source.WriteString("8BPS")
	values := []any{
		uint16(1), [6]byte{}, uint16(3), uint32(1), uint32(2), uint16(8), uint16(3),
		uint32(0), uint32(0), uint32(0), uint16(0),
	}
	for _, value := range values {
		if err := binary.Write(&source, binary.BigEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	source.Write([]byte{20, 30, 40, 50, 60, 70})
	filename := filepath.Join(t.TempDir(), "minimal.psd")
	if err := os.WriteFile(filename, source.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	readers := map[string]func() (*psdkit.Document, error){
		"Open":  func() (*psdkit.Document, error) { return psdkit.Open(filename) },
		"Parse": func() (*psdkit.Document, error) { return psdkit.Parse(source.Bytes()) },
		"Read": func() (*psdkit.Document, error) {
			return psdkit.Read(bytes.NewReader(source.Bytes()), int64(source.Len()))
		},
	}
	for name, readDocument := range readers {
		t.Run(name, func(t *testing.T) {
			document, err := readDocument()
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := psdkit.Inspect(document)
			if err != nil {
				t.Fatal(err)
			}
			if metadata.Width != 2 || metadata.Height != 1 || len(metadata.BlockingIssues) != 0 {
				t.Fatalf("unexpected report: %dx%d, issues %v", metadata.Width, metadata.Height, metadata.BlockingIssues)
			}
			original := bytes.Clone(document.Composite.Pix)
			renderer, err := psdkit.NewRenderer(document)
			if err != nil {
				t.Fatal(err)
			}
			pixels, err := renderer.Render(context.Background(), psdkit.DefaultConfig(metadata), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if pixels != document.Composite {
				t.Fatal("composite-only rendering must preserve the borrowed pixel contract")
			}
			pixels, err = psdkit.PrepareOutput(pixels, psdkit.Options{Format: "png"})
			if err != nil {
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			if err := psdkit.Encode(&encoded, pixels, "png"); err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(&encoded)
			if err != nil {
				t.Fatal(err)
			}
			want := []color.NRGBA{{R: 20, G: 40, B: 60, A: 255}, {R: 30, G: 50, B: 70, A: 255}}
			for x, shade := range want {
				if actual := color.NRGBAModel.Convert(decoded.At(x, 0)); actual != shade {
					t.Fatalf("pixel %d: got %v, want %v", x, actual, shade)
				}
			}
			if !bytes.Equal(document.Composite.Pix, original) {
				t.Fatal("rendering or encoding changed the borrowed document pixels")
			}
		})
	}
}
