package psdscene_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type manySourceCase struct {
	ID            string
	Input         string
	Width, Height int
	ROIs          [][4]int            `json:"rois"`
	Predictions   map[string][][4]int `json:"predictions"`
}

func manySourceCases(t *testing.T) []manySourceCase {
	t.Helper()
	encoded, err := os.ReadFile("../psdprecision/testdata/manylayers/input-binding.json")
	if err != nil {
		t.Fatal(err)
	}
	var binding struct{ Cases []manySourceCase }
	if err := json.Unmarshal(encoded, &binding, json.MatchCaseInsensitiveNames(true)); err != nil || len(binding.Cases) != 3 {
		t.Fatalf("prospective input family differs: %v", err)
	}
	return binding.Cases
}

type manySourceReader struct {
	*bytes.Reader
	footer  psdfile.Span
	hits    int
	failure error
	cancel  context.CancelFunc
	payload []psdfile.Span
}

func (reader *manySourceReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) != 0 && reader.footer.Length != 0 && offset < reader.footer.Offset+reader.footer.Length &&
		offset+int64(len(data)) > reader.footer.Offset {
		reader.hits++
		return 0, errors.New("prospective footer is forbidden")
	}
	for _, span := range reader.payload {
		if len(data) != 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			if reader.failure != nil {
				return 0, reader.failure
			}
			if reader.cancel != nil {
				reader.cancel()
			}
		}
	}
	return reader.Reader.ReadAt(data, offset)
}

func manySourceOpen(t *testing.T, item manySourceCase) (*psdfile.Document, *manySourceReader) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/manylayers", filepath.Base(item.Input)))
	if err != nil {
		t.Fatal(err)
	}
	reader := &manySourceReader{Reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	reader.footer = source.Composite.Data
	for _, layer := range source.Layers {
		for _, channel := range layer.Channels {
			reader.payload = append(reader.payload, channel.Data)
		}
	}
	return source, reader
}

func manySourceCompare(t *testing.T, actual *psdprecision.FloatImage, rectangle image.Rectangle,
	item manySourceCase) int {
	t.Helper()
	if actual == nil || actual.Width != rectangle.Dx() || actual.Height != rectangle.Dy() || actual.SourceDepth != 16 {
		t.Fatal("many-layer source geometry/depth differs")
	}
	expected := item.Predictions["global-bbox-first-active-adjusted-difference"]
	checks := 0
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
			for component := range 4 {
				code := expected[y*item.Width+x][component]
				got := actual.Pix[((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4+component]
				if got*32768 != float64((code+1)/2) {
					t.Fatalf("%s (%d,%d) component=%d prospective independent prediction differs", item.ID, x, y, component)
				}
				checks++
			}
		}
	}
	return checks
}

func TestPrecisionManyLayersProspectiveFullROIAndVisitor(t *testing.T) {
	checks, visits := 0, 0
	for _, item := range manySourceCases(t) {
		source, reader := manySourceOpen(t, item)
		full, err := psdscene.RenderPrecision(nil, source)
		if err != nil {
			t.Fatal(err)
		}
		canvas := image.Rect(0, 0, item.Width, item.Height)
		checks += manySourceCompare(t, full, canvas, item)
		for _, coordinates := range item.ROIs {
			rectangle := image.Rect(coordinates[0], coordinates[1], coordinates[2], coordinates[3])
			result, err := psdscene.RenderPrecisionTile(nil, source, rectangle)
			if err != nil {
				t.Fatal(err)
			}
			checks += manySourceCompare(t, result.Image, rectangle, item)
		}
		err = psdscene.VisitPrecisionTiles(nil, source, canvas, image.Pt(5, 2),
			func(result *psdprecision.RegionImage) error {
				checks += manySourceCompare(t, result.Image, result.Bounds, item)
				visits++
				return nil
			})
		if err != nil || reader.hits != 0 {
			t.Fatalf("visitor or forbidden footer access: hits=%d err=%v", reader.hits, err)
		}
	}
	if visits != 27 {
		t.Fatalf("actual visitor count=%d expected27", visits)
	}
	t.Logf("prospective source components=%d actual visitor tiles=%d footer hits=0; native pending", checks, visits)
}

type manySourceContext struct {
	context.Context
	calls, stop int
	cancel      context.CancelCauseFunc
	cause       error
}

func (ctx *manySourceContext) Err() error {
	ctx.calls++
	if ctx.stop != 0 && ctx.calls == ctx.stop {
		ctx.cancel(ctx.cause)
	}
	return ctx.Context.Err()
}

func TestPrecisionManyLayersSharedBudgetsAndEveryCancellation(t *testing.T) {
	for _, item := range manySourceCases(t) {
		source, _ := manySourceOpen(t, item)
		for _, coordinates := range [][4]int{{0, 0, item.Width, item.Height}, {4, 1, 9, 4}, {0, 0, 0, 2}} {
			rectangle := image.Rect(coordinates[0], coordinates[1], coordinates[2], coordinates[3])
			render := func(amount int64) (*psdprecision.RegionImage, error) {
				return psdscene.RenderPrecisionTile(nil, source, rectangle, psdscene.TileOptions{MaxWorkingBytes: amount})
			}
			low, high := int64(1), int64(1<<20)
			for low < high {
				middle := low + (high-low)/2
				_, err := render(middle)
				if errors.Is(err, psdfile.ErrLimit) {
					low = middle + 1
				} else if err != nil {
					t.Fatal(err)
				} else {
					high = middle
				}
			}
			for _, amount := range []int64{low - 1, low, low + 1} {
				result, err := render(amount)
				if amount < low {
					if result != nil || !errors.Is(err, psdfile.ErrLimit) {
						t.Fatalf("working-byte minus one exposed result: %v", err)
					}
				} else if err != nil || result == nil {
					t.Fatalf("exact/plus-one working-byte threshold rejected: %v", err)
				}
			}
			t.Logf("%s window=%v exact source working-byte threshold=%d", item.ID, rectangle, low)
		}
		count := len(source.Layers)
		for _, limit := range []int{count - 1, count, count + 1} {
			result, err := psdscene.RenderPrecision(nil, source, psdscene.PrecisionOptions{MaxLayers: limit})
			if limit < count {
				if result != nil || !errors.Is(err, psdfile.ErrLimit) {
					t.Fatalf("source layer limit escaped: %v", err)
				}
			} else if result == nil || err != nil {
				t.Fatal(err)
			}
		}
	}
	item := manySourceCases(t)[1]
	source, reader := manySourceOpen(t, item)
	probe := &manySourceContext{Context: context.Background()}
	if _, err := psdscene.RenderPrecision(probe, source); err != nil {
		t.Fatal(err)
	}
	for checkpoint := 1; checkpoint <= probe.calls; checkpoint++ {
		parent, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("many-layer source checkpoint")
		ctx := &manySourceContext{Context: parent, cancel: cancel, cause: cause, stop: checkpoint}
		result, err := psdscene.RenderPrecision(ctx, source)
		if result != nil || !errors.Is(err, context.Canceled) || !errors.Is(context.Cause(parent), cause) {
			t.Fatalf("source ctx checkpoint=%d exposed partial output: %v", checkpoint, err)
		}
	}
	failure := errors.New("many-layer reader cause")
	reader.failure = failure
	if result, err := psdscene.RenderPrecision(nil, source); result != nil || !errors.Is(err, failure) {
		t.Fatalf("ReaderAt cause swallowed: %v", err)
	}
	reader.failure = nil
	parent, cancel := context.WithCancel(context.Background())
	reader.cancel = cancel
	if result, err := psdscene.RenderPrecision(parent, source); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ReaderAt cancellation swallowed: %v", err)
	}
	reader.cancel = nil
	visitorCause := errors.New("many-layer visitor cause")
	visits := 0
	err := psdscene.VisitPrecisionTiles(nil, source, image.Rect(0, 0, item.Width, item.Height), image.Pt(5, 2),
		func(*psdprecision.RegionImage) error { visits++; return visitorCause })
	if !errors.Is(err, visitorCause) || visits != 1 || reader.hits != 0 {
		t.Fatalf("visitor cause/count/footer differs: visits=%d hits=%d err=%v", visits, reader.hits, err)
	}
	t.Logf("every actual source ctx check canceled=%d; visitor error propagated after exactly1 callback", probe.calls)
}
