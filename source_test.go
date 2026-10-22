package psdkit_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/internal/testfixture"
)

func sourceFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	var source bytes.Buffer
	source.WriteString("8BPS")
	values := []any{
		version, [6]byte{}, uint16(3), uint32(1), uint32(2), uint16(8), uint16(3),
		uint32(0), uint32(0),
	}
	if version == 2 {
		values = append(values, uint64(0))
	} else {
		values = append(values, uint32(0))
	}
	values = append(values, uint16(0))
	for _, value := range values {
		if err := binary.Write(&source, binary.BigEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	source.Write([]byte{20, 30, 40, 50, 60, 70})
	return source.Bytes()
}

func TestSourcePublicAPIAndLegacySnapshot(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := sourceFixture(t, version)
		filename := filepath.Join(t.TempDir(), "source.psd")
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			t.Fatal(err)
		}
		factories := map[string]func() (*psdkit.SourceDocument, error){
			"OpenDocument": func() (*psdkit.SourceDocument, error) {
				return psdkit.OpenDocument(filename)
			},
			"ParseDocument": func() (*psdkit.SourceDocument, error) {
				return psdkit.ParseDocument(data)
			},
			"ReadDocument": func() (*psdkit.SourceDocument, error) {
				return psdkit.ReadDocument(bytes.NewReader(data), int64(len(data)))
			},
		}
		for name, factory := range factories {
			t.Run(name, func(t *testing.T) {
				document, err := factory()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := document.Close(); err != nil {
						t.Error(err)
					}
				})
				if document.Header.Version != version || document.Header.Width != 2 || document.Header.Depth != 8 {
					t.Fatal("source header was not preserved")
				}
				plane, err := document.DecodeCompositeChannel(context.Background(), 0)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(plane.Data, []byte{20, 30}) {
					t.Fatal("source red plane changed")
				}
				plane.Data[0] = 0
				decoded, err := document.Decode(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				renderer, err := psdkit.NewRenderer(decoded)
				if err != nil {
					t.Fatal(err)
				}
				pixels, err := renderer.Render(context.Background(), psdkit.DefaultConfig(renderer.Metadata()), nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if pixels.NRGBAAt(0, 0) != (color.NRGBA{R: 20, G: 40, B: 60, A: 255}) {
					t.Fatal("legacy renderer lost source pixel values")
				}
				var preserved bytes.Buffer
				if _, err := document.CopySpan(context.Background(), &preserved, document.Source); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(preserved.Bytes(), data) {
					t.Fatal("decoding or modifying owned planes changed original bytes")
				}
				if err := document.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := document.Decode(context.Background()); !errors.Is(err, psdkit.ErrClosed) {
					t.Fatal("closed source was still decoded")
				}
				if pixels.NRGBAAt(0, 0).R != 20 {
					t.Fatal("closing the source invalidated the independent snapshot")
				}
			})
		}
	}
}

func TestSourceLegacyLimitsAndCancellation(t *testing.T) {
	document, err := psdkit.ParseDocument(sourceFixture(t, 1), psdkit.LoadOptions{
		Limits: psdkit.SourceLimits{MaxDecodePixels: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	if _, err := document.Decode(context.Background()); !errors.Is(err, psdkit.ErrLimit) {
		t.Fatal("configured legacy decode budget was ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := document.Decode(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled legacy decode was not rejected")
	}
}

func TestSourceRealSnapshotMatchesEager(t *testing.T) {
	document, err := psdkit.OpenDocument(testfixture.Sample(t))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	decoded, err := document.Decode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eager, err := psdkit.Open(testfixture.Sample(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Composite.Pix, eager.Composite.Pix) || len(decoded.Layers) != len(eager.Layers) {
		t.Fatal("new source API changed the legacy composite or layer count")
	}
	for index, layer := range decoded.Layers {
		if !bytes.Equal(layer.Pixels.Pix, eager.Layers[index].Pixels.Pix) {
			t.Fatalf("new source API changed layer %d saved pixels", index)
		}
	}
}

func TestSourceCopyReportsPrematureEOF(t *testing.T) {
	data := sourceFixture(t, 1)
	// ZIP payload validation is intentionally deferred, so metadata indexing can accept a short source.
	binary.BigEndian.PutUint16(data[len(data)-8:len(data)-6], 2)
	document, err := psdkit.ReadDocument(bytes.NewReader(data), int64(len(data)+17))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	var output bytes.Buffer
	count, err := document.CopySpan(context.Background(), &output, document.Source)
	if count != int64(len(data)) || !errors.Is(err, psdkit.ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short source copy returned count=%d error=%v", count, err)
	}
	var detail *psdkit.ReadError
	if !errors.As(err, &detail) || detail.Path != "span" || detail.Offset != count {
		t.Fatal("premature source EOF lost its exact field and offset")
	}
}

type nonProgressingSource struct {
	reader    *bytes.Reader
	bulkReads int
}

func (r *nonProgressingSource) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) > 26 {
		r.bulkReads++
		if r.bulkReads > 1 {
			return 0, errors.New("reader was retried without progress")
		}
		return 0, nil
	}
	return r.reader.ReadAt(data, offset)
}

func TestSourceBrokenReaderCannotLoopWithoutProgress(t *testing.T) {
	data := sourceFixture(t, 1)
	reader := &nonProgressingSource{reader: bytes.NewReader(data)}
	document, err := psdkit.ReadDocument(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	_, err = document.CopySpan(context.Background(), io.Discard, document.Source)
	if !errors.Is(err, psdkit.ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) || reader.bulkReads != 1 {
		t.Fatalf("non-progressing source was not stopped after one read: calls=%d error=%v", reader.bulkReads, err)
	}
}
