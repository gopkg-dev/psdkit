package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tiffPrivateAliasedPreview(t *testing.T) ([]byte, []byte) {
	t.Helper()
	fixture := tiffPrivateTestFixture(0, "", tiffPrivateTestChild())
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	privateOffset := source.Info().Layers.Offset - record.Data.Offset
	data := bytes.Clone(fixture.data)
	binary.LittleEndian.PutUint32(tiffTestEntryOffset(data, 273)[8:], uint32(privateOffset))
	return data, bytes.Clone(data[privateOffset : privateOffset+12])
}

func TestTIFFPrivateSavedPreviewReadsOriginalAliasedMainStrip(t *testing.T) {
	data, originalPixels := tiffPrivateAliasedPreview(t)
	before := bytes.Clone(data)
	parent, record := rasterSource(data)
	defer parent.Close()
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	countSpan := Span{Offset: source.Info().Layers.Offset, Length: 2}
	normalized, err := source.ReadNormalizedSpan(nil, countSpan)
	if err != nil || bytes.Equal(normalized, originalPixels[:2]) {
		t.Fatalf("fixture did not intersect a normalized private field: %v", err)
	}
	preview, err := source.SavedPreview(nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 4 {
		expected := color.NRGBA{R: originalPixels[index*3], G: originalPixels[index*3+1],
			B: originalPixels[index*3+2], A: 255}
		if actual := preview.Raster.Pixels.NRGBAAt(index%2, index/2); actual != expected {
			t.Fatalf("saved preview used normalized metadata as pixels at %d: %v != %v", index, actual, expected)
		}
	}
	if !bytes.Equal(data, before) {
		t.Fatal("saved preview changed original TIFF bytes")
	}
	if preview.Kind != "flattened TIFF main IFD saved preview" {
		t.Fatalf("saved preview lost its original-image provenance: %q", preview.Kind)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SavedPreview(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("saved preview outlived its private source view: %v", err)
	}
	if actual := preview.Raster.Pixels.NRGBAAt(0, 0); actual != (color.NRGBA{1, 0, 0, 255}) {
		t.Fatal("owned preview pixels changed after Close")
	}
}

type tiffPrivatePreviewGate struct {
	reader  io.ReaderAt
	offset  int64
	enabled atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (reader *tiffPrivatePreviewGate) ReadAt(data []byte, offset int64) (int, error) {
	if reader.enabled.Load() && len(data) != 0 && offset == reader.offset {
		reader.once.Do(func() { close(reader.entered) })
		<-reader.release
	}
	return reader.reader.ReadAt(data, offset)
}

func TestTIFFPrivateSavedPreviewCloseWaitsWithoutNestedViewLock(t *testing.T) {
	fixture := tiffPrivateTestFixture(0, "", tiffPrivateTestChild())
	parent, record := rasterSource(fixture.data)
	mainOffset := int64(binary.LittleEndian.Uint32(tiffTestEntryOffset(fixture.data, 273)[8:])) + record.Data.Offset
	gate := &tiffPrivatePreviewGate{reader: parent.reader, offset: mainOffset,
		entered: make(chan struct{}), release: make(chan struct{})}
	parent.reader = gate
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	gate.enabled.Store(true)
	previewDone := make(chan error, 1)
	go func() {
		_, err := source.SavedPreview(nil)
		previewDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		close(gate.release)
		t.Fatal("saved preview did not reach original pixel reader")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- source.Close() }()
	select {
	case err := <-closeDone:
		close(gate.release)
		t.Fatalf("source Close returned before saved preview completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(gate.release)
	select {
	case err := <-previewDone:
		if err != nil {
			t.Fatalf("lifetime-bound saved preview was interrupted by view Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("saved preview recursively locked its view while a Close writer waited")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("private source Close did not finish after saved preview")
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTIFFPrivateSavedPreviewOriginalReaderCausesAndBudgetStayBounded(t *testing.T) {
	fixture := tiffPrivateTestFixture(0, "", tiffPrivateTestChild())
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	preview, err := source.SavedPreview(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.SavedPreview(nil,
		LinkedRasterOptions{MaxWorkingBytes: preview.Raster.WorkingBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SavedPreview(nil,
		LinkedRasterOptions{MaxWorkingBytes: preview.Raster.WorkingBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("original preview decoder ignored its byte deficit: %v", err)
	}
	region, err := source.DecodeChannelRegion(nil, 0, 0, image.Rect(1, 1, 2, 2), RegionOptions{MaxWorkingBytes: 1})
	if err != nil || !bytes.Equal(region.Data, []byte{255}) {
		t.Fatalf("original preview changed the bounded normalized ROI path: %v", err)
	}
	if _, err := source.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 2, 1),
		RegionOptions{MaxWorkingBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("saved-layer ROI working deficit was ignored: %v", err)
	}
	mainOffset := int64(binary.LittleEndian.Uint32(tiffTestEntryOffset(fixture.data, 273)[8:])) + record.Data.Offset
	failure := errors.New("independent original preview source failure")
	for _, cause := range []error{failure, errors.Join(io.EOF, failure)} {
		brokenParent, entry := rasterSource(fixture.data)
		brokenSource, err := brokenParent.OpenLinkedTIFFLayers(nil, entry)
		if err != nil {
			t.Fatal(err)
		}
		brokenParent.reader = tiffFaultReader{source: brokenParent.reader, offset: mainOffset, cause: cause}
		if _, err := brokenSource.SavedPreview(nil); !errors.Is(err, ErrMalformed) || !errors.Is(err, failure) {
			t.Fatalf("original saved preview discarded a full-count source cause: %v", err)
		}
		if err := brokenParent.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := brokenSource.SavedPreview(nil); !errors.Is(err, ErrClosed) {
			t.Fatalf("saved preview outlived its original parent: %v", err)
		}
		if err := brokenSource.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.SavedPreview(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("saved preview ignored cancellation: %v", err)
	}
}
