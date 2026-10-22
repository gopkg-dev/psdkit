package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type sourceArtboardReader struct {
	data      []byte
	forbidden []psdfile.Span
	reads     int
}

func (reader *sourceArtboardReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if len(data) != 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			reader.reads++
			return 0, errors.New("merged preview samples are forbidden")
		}
	}
	if offset < 0 || offset >= int64(len(reader.data)) && len(data) != 0 {
		return 0, io.EOF
	}
	count := copy(data, reader.data[offset:])
	if count != len(data) {
		return count, io.EOF
	}
	return count, nil
}

func sourceArtboardOpen(t *testing.T, data []byte) (*psdfile.Document, *sourceArtboardReader) {
	t.Helper()
	reader := &sourceArtboardReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	reader.forbidden = []psdfile.Span{source.Composite.Data}
	t.Cleanup(func() { _ = source.Close() })
	return source, reader
}

func sourceArtboardMultipleData(t *testing.T, version uint16) []byte {
	t.Helper()
	data := artboardsTestSource(version)
	index, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	// The ordinary top-level magenta layer paints only the gap between the two boards.
	for _, channel := range index.Layers[6].Channels {
		if channel.ID != -1 {
			continue
		}
		alpha := data[channel.Data.Offset : channel.Data.Offset+channel.Data.Length]
		clear(alpha)
		alpha[2] = 255
	}
	return data
}

func TestSourceArtboardBackgroundsKeepSavedCoordinatesWithoutFooter(t *testing.T) {
	expected := map[uint32]color.NRGBA{1: {255, 102, 102, 255}, 2: {153, 0, 0, 255},
		3: {255, 0, 0, 153}, 4: {157, 32, 64, 255}}
	for background, expectedPixel := range expected {
		for _, origin := range []float64{-8, 0, 8} {
			for _, pass := range []bool{false, true} {
				data := artboardTestPSD(artboardTestConfiguration{
					background: background, origin: origin, pass: pass, groupOpacity: 255})
				original := sha256.Sum256(data)
				source, reader := sourceArtboardOpen(t, data)
				pixels, err := Render(nil, source)
				if err != nil {
					t.Fatal(err)
				}
				if pixels.Rect != image.Rect(0, 0, 2, 1) || pixels.NRGBAAt(0, 0) != expectedPixel {
					t.Fatalf("background=%d origin=%v pass=%t: output differs: %v",
						background, origin, pass, pixels.Pix)
				}
				tile, err := RenderTile(nil, source, image.Rect(1, 0, 2, 1))
				if err != nil || tile.NRGBAAt(1, 0) != pixels.NRGBAAt(1, 0) {
					t.Fatalf("source tile differs from full render: %v", err)
				}
				if reader.reads != 0 || sha256.Sum256(data) != original {
					t.Fatalf("render read the merged preview or changed source bytes: %d", reader.reads)
				}
			}
		}
	}
}

func TestSourceMultipleArtboardsClipChildrenAndKeepOrdinaryGapLayer(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := sourceArtboardMultipleData(t, version)
		original := sha256.Sum256(data)
		source, reader := sourceArtboardOpen(t, data)
		canvas := image.Rect(0, 0, 5, 1)
		pixels, err := Render(nil, source)
		if err != nil {
			t.Fatal(err)
		}
		expected := []color.NRGBA{{227, 140, 165, 255}, {227, 140, 165, 255},
			{255, 0, 255, 255}, {25, 100, 200, 255}, {25, 100, 200, 255}}
		for x, expectedPixel := range expected {
			if actual := pixels.NRGBAAt(x, 0); actual != expectedPixel {
				t.Fatalf("version=%d pixel=%d: got %v, want %v", version, x, actual, expectedPixel)
			}
		}
		assembled := image.NewNRGBA(canvas)
		err = VisitTiles(nil, source, canvas, image.Pt(2, 1), func(tile *image.NRGBA) error {
			for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
				assembled.SetNRGBA(x, 0, tile.NRGBAAt(x, 0))
			}
			return nil
		})
		if err != nil || !bytes.Equal(pixels.Pix, assembled.Pix) {
			t.Fatalf("version=%d: assembled tiles differ from full source: %v", version, err)
		}
		liveTextPolicy, err := RenderTile(nil, source, canvas, TileOptions{
			ApproximateOpenTypeText: &OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}})
		if err != nil || !bytes.Equal(pixels.Pix, liveTextPolicy.Pix) {
			t.Fatalf("version=%d: source artboard virtual background disturbed text planning: %v", version, err)
		}
		empty, err := RenderTile(nil, source, image.Rect(2, 0, 2, 0))
		if err != nil || len(empty.Pix) != 0 {
			t.Fatalf("empty artboard source window: %v", err)
		}
		if reader.reads != 0 || sha256.Sum256(data) != original {
			t.Fatalf("source ownership or merged-preview guard failed: reads=%d", reader.reads)
		}
		retained := bytes.Clone(pixels.Pix)
		if err := source.Close(); err != nil || !bytes.Equal(pixels.Pix, retained) {
			t.Fatalf("owned source result depends on the closed source: %v", err)
		}
	}
}

func TestSourceArtboardPlacedOriginalRetainsLiveRecursiveSource(t *testing.T) {
	child := artboardTestPSD(artboardTestConfiguration{background: 4, groupOpacity: 255})
	data := recursiveTextParentFixture(t, child, false, image.Pt(4, 3),
		[]float64{1, 1, 3, 1, 3, 2, 1, 2})
	source, reader := sourceArtboardOpen(t, data)
	for _, channel := range source.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	links, err := source.LinkedFiles(nil)
	if err != nil || len(links) != 1 {
		t.Fatalf("live artboard prototype reference: %v", err)
	}
	prototype, err := source.OpenLinkedDocument(nil, links[0])
	if err != nil {
		t.Fatal(err)
	}
	reader.forbidden = append(reader.forbidden, psdfile.Span{
		Offset: links[0].Data.Offset + prototype.Composite.Data.Offset,
		Length: prototype.Composite.Data.Length})
	_ = prototype.Close()
	pixels, err := Render(nil, source, Options{ApproximatePlacedProjection: true})
	if err != nil {
		t.Fatal(err)
	}
	if pixels.NRGBAAt(1, 1) != (color.NRGBA{157, 32, 64, 255}) ||
		pixels.NRGBAAt(2, 1) != (color.NRGBA{78, 32, 32, 255}) || pixels.NRGBAAt(0, 0).A != 0 {
		t.Fatalf("live recursive artboard projection used a cached image or changed coordinates: %v", pixels.Pix)
	}
	if reader.reads != 0 {
		t.Fatalf("live artboard prototype decoded merged or placed cache samples: %d", reader.reads)
	}
}

func TestSourceArtboardUnsupportedMetadataRejectsBeforeChannelReads(t *testing.T) {
	for _, settings := range []artboardTestConfiguration{
		{background: 4, groupOpacity: 255, duplicate: true},
		{background: 4, groupOpacity: 255, unknown: true},
		{background: 0, groupOpacity: 255},
		{background: 4, groupOpacity: 128},
	} {
		source, reader := sourceArtboardOpen(t, artboardTestPSD(settings))
		for _, record := range source.Layers {
			for _, channel := range record.Channels {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
		if _, err := Render(nil, source); !errors.Is(err, psdfile.ErrUnsupported) || reader.reads != 0 {
			t.Fatalf("unsupported artboard semantics reached sample I/O: %+v, reads=%d, err=%v",
				settings, reader.reads, err)
		}
	}
}

func TestSourceArtboardCancellationBudgetAndClosedSource(t *testing.T) {
	source, reader := sourceArtboardOpen(t, sourceArtboardMultipleData(t, 2))
	for _, record := range source.Layers {
		for _, channel := range record.Channels {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source artboard render: %v", err)
	}
	if _, err := Render(nil, source, Options{MaxWorkingBytes: 1}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("artboard metadata exceeded the working budget: %v", err)
	}
	if reader.reads != 0 {
		t.Fatalf("canceled or low-budget artboard render read samples: %d", reader.reads)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(nil, source); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed artboard source: %v", err)
	}
}

func TestSourceArtboardRegionalClipDoesNotAllocateSavedCanvas(t *testing.T) {
	const dimension = 300_000
	data := sourceArtboardMultipleData(t, 2)
	index, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	prefix := bytes.Clone(data[:index.Composite.Data.Offset])
	_ = index.Close()
	binary.BigEndian.PutUint32(prefix[14:18], dimension)
	binary.BigEndian.PutUint32(prefix[18:22], dimension)
	length := int64(len(prefix)) + int64(dimension)*dimension*4
	reader := &tilePlacedSparseReader{prefix: prefix, length: length}
	source, err := psdfile.Read(reader, length, psdfile.LoadOptions{Limits: psdfile.Limits{
		MaxFileBytes: length, MaxDecodeBytes: 4 << 20, MaxDecodePixels: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader.forbidden = []psdfile.Span{source.Composite.Data}
	pixels, err := RenderTile(nil, source, image.Rect(0, 0, 1, 1),
		TileOptions{MaxWorkingBytes: 4 << 20, MaxPixels: 1})
	if err != nil || pixels.NRGBAAt(0, 0) != (color.NRGBA{227, 140, 165, 255}) || reader.reads != 0 {
		t.Fatalf("tiny artboard region depended on a full saved canvas or merged preview: %v, reads=%d", err, reader.reads)
	}
}
