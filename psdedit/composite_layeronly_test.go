package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

var errCompositeLayerOnlyFooter = errors.New("saved preview payload is unavailable")
var errCompositeLayerOnlyPixels = errors.New("source layer pixels are unavailable")

type compositeLayerOnlyReader struct {
	reader       *bytes.Reader
	footer       psdfile.Span
	blockFooter  bool
	footerReads  int
	failure      psdfile.Span
	failureCause error
}

func (reader *compositeLayerOnlyReader) ReadAt(target []byte, offset int64) (int, error) {
	intersects := func(span psdfile.Span) bool {
		return len(target) != 0 && span.Length != 0 && offset < span.Offset+span.Length &&
			span.Offset < offset+int64(len(target))
	}
	if intersects(reader.footer) {
		reader.footerReads++
		if reader.blockFooter {
			return 0, errCompositeLayerOnlyFooter
		}
	}
	if reader.failureCause != nil && intersects(reader.failure) {
		return 0, reader.failureCause
	}
	return reader.reader.ReadAt(target, offset)
}

func compositeLayerOnlyEditor(
	t *testing.T, data []byte, options ...psdfile.LoadOptions,
) (*Editor, *psdfile.Document, *compositeLayerOnlyReader) {
	t.Helper()
	reader := &compositeLayerOnlyReader{reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	reader.footer = source.Composite.Data
	return editor, source, reader
}

func TestCompositeLayerOnlyRebuildDoesNotReadSavedPreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, mode := range []string{"damaged-preview", "denied-preview"} {
			t.Run(fmt.Sprintf("version_%d/%s", version, mode), func(t *testing.T) {
				data := compositeFixture(version, 8, 3, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
				if mode == "damaged-preview" {
					indexed, err := psdfile.Parse(data)
					if err != nil {
						t.Fatal(err)
					}
					footer := indexed.Composite.Span.Offset
					if err := indexed.Close(); err != nil {
						t.Fatal(err)
					}
					// The known ZIP compression flag indexes without decoding the damaged saved pixels.
					binary.BigEndian.PutUint16(data[footer:], 2)
				}
				original := bytes.Clone(data)
				editor, source, reader := compositeLayerOnlyEditor(t, data)
				reader.blockFooter = mode == "denied-preview"
				if err := editor.SetOpacity(1, 128); err != nil {
					t.Fatal(err)
				}
				pixels, err := editor.Composite(context.Background())
				if err != nil || pixels == nil || !bytes.Equal(pixels.Pix, []byte{255, 0, 0, 128}) {
					t.Fatalf("layer render unnecessarily depends on saved preview: %v", err)
				}
				filename := filepath.Join(t.TempDir(), "rebuilt.psd")
				if err := editor.SavePSD(context.Background(), filename); err != nil {
					t.Fatalf("automatic saved-preview rebuild requires old pixels: %v", err)
				}
				saved, err := psdfile.Open(filename)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = saved.Close() })
				compositeExpect(t, saved, []byte{255, 127, 127, 128})
				if saved.Header.Version != version || reader.footerReads != 0 || !bytes.Equal(data, original) {
					t.Fatalf("source bytes, source format or footer isolation changed: reads=%d", reader.footerReads)
				}
				layer, err := source.DecodeLayer(context.Background(), 0)
				if err != nil {
					t.Fatal(err)
				}
				var sourceSamples []byte
				for _, plane := range layer.Channels {
					sourceSamples = append(sourceSamples, plane.Data...)
				}
				if !bytes.Equal(sourceSamples, []byte{255, 0, 0, 255}) || reader.footerReads != 0 {
					t.Fatal("automatic preview rebuild changed the original RGBA layer")
				}
			})
		}
	}
}

func TestCompositeLayerOnlyFlatSourceStillRequiresSavedPreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			data := compositeFixture(version, 8, 3, nil)
			editor, source, reader := compositeLayerOnlyEditor(t, data)
			if len(source.Layers) != 0 || len(editor.roots) != 1 || !editor.roots[0].virtualComposite {
				t.Fatal("flat source lost its virtual background")
			}
			if err := editor.SetOpacity(editor.roots[0].id, 128); err != nil {
				t.Fatal(err)
			}
			reader.blockFooter = true
			pixels, err := editor.Composite(context.Background())
			if !errors.Is(err, errCompositeLayerOnlyFooter) || pixels != nil || reader.footerReads == 0 {
				t.Fatalf("flat source no longer requires original saved pixels: %v", err)
			}
			reader.blockFooter, reader.footerReads = false, 0
			pixels, err = editor.Composite(context.Background())
			if err != nil || pixels == nil || !bytes.Equal(pixels.Pix, []byte{30, 70, 110, 128}) ||
				reader.footerReads == 0 {
				t.Fatalf("flat source changed its virtual saved-pixel background: %v", err)
			}
		})
	}
}

func TestCompositeLayerOnlyMetadataSaveStillCopiesOriginalPreview(t *testing.T) {
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
	editor, source, reader := compositeLayerOnlyEditor(t, data)
	original, err := source.ReadSpan(source.Composite.Span)
	if err != nil {
		t.Fatal(err)
	}
	reader.footerReads = 0
	if err := editor.Rename(1, "metadata only"); err != nil {
		t.Fatal(err)
	}
	saved := compositeSave(t, editor)
	actual, err := saved.ReadSpan(saved.Composite.Span)
	if err != nil || !bytes.Equal(actual, original) || reader.footerReads == 0 {
		t.Fatalf("metadata-only save stopped copying its unchanged saved preview: %v", err)
	}
}

func TestCompositeLayerOnlyPreservesCancellationClosedSourceAndReadCause(t *testing.T) {
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
	t.Run("canceled", func(t *testing.T) {
		editor, _, reader := compositeLayerOnlyEditor(t, data)
		reader.blockFooter = true
		if err := editor.SetOpacity(1, 128); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		pixels, err := editor.Composite(ctx)
		if !errors.Is(err, context.Canceled) || pixels != nil || reader.footerReads != 0 {
			t.Fatalf("canceled composition returned pixels or touched the footer: %v", err)
		}
		filename := filepath.Join(t.TempDir(), "canceled.psd")
		if err := editor.SavePSD(ctx, filename); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled save lost its context cause: %v", err)
		}
		if _, err := os.Stat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled save published a destination: %v", err)
		}
	})
	t.Run("closed-source", func(t *testing.T) {
		editor, source, _ := compositeLayerOnlyEditor(t, data)
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		pixels, err := editor.Composite(context.Background())
		if !errors.Is(err, psdfile.ErrClosed) || pixels != nil {
			t.Fatalf("closed source changed its public error category: %v", err)
		}
	})
	t.Run("required-layer-read", func(t *testing.T) {
		editor, source, reader := compositeLayerOnlyEditor(t, data)
		reader.failure, reader.failureCause = source.Layers[0].Channels[0].Data, errCompositeLayerOnlyPixels
		pixels, err := editor.Composite(context.Background())
		if !errors.Is(err, errCompositeLayerOnlyPixels) || pixels != nil {
			t.Fatalf("required layer IO failure lost its original cause: %v", err)
		}
	})
	t.Run("existing-working-budget", func(t *testing.T) {
		editor, _, reader := compositeLayerOnlyEditor(t, data,
			psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 12}})
		reader.blockFooter = true
		pixels, err := editor.Composite(context.Background())
		if !errors.Is(err, psdfile.ErrLimit) || pixels != nil || reader.footerReads != 0 {
			t.Fatalf("working-budget check changed or moved behind source IO: %v", err)
		}
	})
}

var _ io.ReaderAt = (*compositeLayerOnlyReader)(nil)
