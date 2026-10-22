package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func artboardsTestSource(version uint16) []byte {
	board := func(left, right float64, background uint32) []byte {
		rect := artboardTestDescriptor("classFloatRect",
			artboardTestEntry{"Left", "doub", artboardTestDouble(left)},
			artboardTestEntry{"Top ", "doub", artboardTestDouble(0)},
			artboardTestEntry{"Rght", "doub", artboardTestDouble(right)},
			artboardTestEntry{"Btom", "doub", artboardTestDouble(1)})
		return append(artboardTestU32(16), artboardTestDescriptor("artboard",
			artboardTestEntry{"artboardRect", "Objc", rect},
			artboardTestEntry{"artboardBackgroundType", "long", artboardTestU32(background)})...)
	}
	var records, pixels bytes.Buffer
	_ = binary.Write(&records, binary.BigEndian, int16(-7))
	for index := range 7 {
		bounds, values, mode := image.Rectangle{}, [4]byte{}, "norm"
		channels := 0
		if index == 1 || index == 4 || index == 6 {
			bounds, channels = image.Rect(0, 0, 5, 1), 4
			values = [4]byte{200, 25, 75, 128}
			if index == 4 {
				values = [4]byte{25, 100, 200, 255}
				mode = "scrn"
			}
			if index == 6 {
				values = [4]byte{255, 0, 255, 255}
			}
		}
		for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
			_ = binary.Write(&records, binary.BigEndian, int32(coordinate))
		}
		_ = binary.Write(&records, binary.BigEndian, uint16(channels))
		for component := range channels {
			id := int16(component)
			if component == 3 {
				id = -1
			}
			_ = binary.Write(&records, binary.BigEndian, id)
			length := uint64(bounds.Dx()*bounds.Dy() + 2)
			if version == 2 {
				_ = binary.Write(&records, binary.BigEndian, length)
			} else {
				_ = binary.Write(&records, binary.BigEndian, uint32(length))
			}
			_ = binary.Write(&pixels, binary.BigEndian, uint16(0))
			pixels.Write(bytes.Repeat([]byte{values[component]}, bounds.Dx()*bounds.Dy()))
		}
		records.WriteString("8BIM" + mode)
		records.Write([]byte{255, 0, 0, 0})
		extra := append(make([]byte, 8), 1, byte('A'+index), 0, 0)
		if index == 0 || index == 2 || index == 3 || index == 5 {
			divider := uint32(3)
			if index == 2 || index == 5 {
				divider = 1
			}
			payload := append(artboardTestU32(divider), "8BIMnorm"...)
			extra = append(extra, artboardTestBlock("lsct", payload)...)
		}
		if index == 2 {
			extra = append(extra, artboardTestBlock("artb", board(0, 2, 1))...)
		}
		if index == 5 {
			extra = append(extra, artboardTestBlock("artb", board(3, 5, 2))...)
		}
		_ = binary.Write(&records, binary.BigEndian, uint32(len(extra)))
		records.Write(extra)
	}
	records.Write(pixels.Bytes())
	if records.Len()%2 != 0 {
		records.WriteByte(0)
	}
	point := artboardTestDescriptor("Pnt ",
		artboardTestEntry{"Hrzn", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Vrtc", "doub", artboardTestDouble(0)})
	document := append(artboardTestU32(16), artboardTestDescriptor("artboard",
		artboardTestEntry{"Cnt ", "long", artboardTestU32(2)},
		artboardTestEntry{"origin", "Objc", point}, artboardTestEntry{"autoExpandOffset", "Objc", point})...)
	var layerMask []byte
	if version == 2 {
		layerMask = binary.BigEndian.AppendUint64(nil, uint64(records.Len()))
	} else {
		layerMask = artboardTestU32(uint32(records.Len()))
	}
	layerMask = append(layerMask, records.Bytes()...)
	layerMask = append(layerMask, artboardTestU32(0)...)
	layerMask = append(layerMask, artboardTestBlock("artd", document)...)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = append(data, artboardTestU32(1)...)
	data = append(data, artboardTestU32(5)...)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	if version == 2 {
		data = binary.BigEndian.AppendUint64(data, uint64(len(layerMask)))
	} else {
		data = append(data, artboardTestU32(uint32(len(layerMask)))...)
	}
	data = append(data, layerMask...)
	data = append(data, 0, 0)
	// A solid magenta footer and outside magenta raster must never paint either board.
	for _, value := range []byte{255, 0, 255, 255} {
		data = append(data, bytes.Repeat([]byte{value}, 5)...)
	}
	return data
}

func TestMultipleArtboardsAndPSBUseIndependentBackgroundsAndSavedCanvasCrops(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		source, err := psdfile.Parse(artboardsTestSource(version))
		if err != nil {
			t.Fatal(err)
		}
		boards, err := RenderArtboards(nil, source)
		if err != nil {
			t.Fatal(err)
		}
		if len(boards) != 2 || boards[0].LayerIndex != 2 || boards[1].LayerIndex != 5 ||
			boards[0].Bounds != image.Rect(0, 0, 2, 1) || boards[1].Bounds != image.Rect(3, 0, 5, 1) {
			t.Fatalf("version=%d: physical order or saved board rectangles differ: %+v", version, boards)
		}
		for index, expected := range []color.NRGBA{{227, 140, 165, 255}, {25, 100, 200, 255}} {
			board := boards[index]
			if board.Pixels.Rect != image.Rect(0, 0, 2, 1) || board.Pixels.NRGBAAt(0, 0) != expected ||
				board.Pixels.NRGBAAt(1, 0) != expected {
				t.Fatalf("version=%d board=%d: other-board/outside pixels leaked or background differs: %v",
					version, index, board.Pixels.Pix)
			}
			selected := board.LayerIndex
			result, err := RenderArtboard(nil, source, ArtboardRenderOptions{LayerIndex: &selected})
			if err != nil || !bytes.Equal(result.Pix, board.Pixels.Pix) {
				t.Fatalf("version=%d board=%d: selected artboard differs: %v", version, index, err)
			}
		}
		if _, err := RenderArtboard(nil, source); !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("multiple boards were selected ambiguously: %v", err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		boards[0].Pixels.Pix[0] = 0
		if boards[1].Pixels.Pix[0] != 25 {
			t.Fatal("outputs alias one another or require an open source")
		}
	}
}

func TestMultipleArtboardsSelectorCancellationAndRetainedOutputBudget(t *testing.T) {
	source, err := psdfile.Parse(artboardsTestSource(2))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, selected := range []int{-1, 0, 1, 6, 100} {
		_, selectionErr := RenderArtboard(nil, source, ArtboardRenderOptions{LayerIndex: &selected})
		if !errors.Is(selectionErr, psdfile.ErrMalformed) {
			t.Fatalf("invalid selector %d: %v", selected, selectionErr)
		}
	}
	selected := 2
	_, selectionErr := RenderArtboards(nil, source, ArtboardRenderOptions{LayerIndex: &selected})
	if !errors.Is(selectionErr, psdfile.ErrMalformed) {
		t.Fatalf("all-board entrypoint silently accepted a single-board selector: %v", selectionErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderArtboards(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was ignored: %v", err)
	}
	low, high := int64(1), int64(2<<20)
	for low < high {
		candidate := low + (high-low)/2
		_, err := RenderArtboards(nil, source, ArtboardRenderOptions{MaxWorkingBytes: candidate})
		if errors.Is(err, psdfile.ErrLimit) {
			low = candidate + 1
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		high = candidate
	}
	if _, err := RenderArtboards(nil, source, ArtboardRenderOptions{MaxWorkingBytes: low}); err != nil {
		t.Fatalf("exact aggregate budget failed: %v", err)
	}
	_, budgetErr := RenderArtboards(nil, source, ArtboardRenderOptions{MaxWorkingBytes: low - 1})
	if !errors.Is(budgetErr, psdfile.ErrLimit) {
		t.Fatalf("aggregate budget minus one was accepted: %v", budgetErr)
	}
}

func TestMultipleArtboardsConcurrentCallsHaveOwnedResultsAndRespectHiddenBoards(t *testing.T) {
	data := artboardsTestSource(2)
	index, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// The first group's channel count is zero, so its flags byte follows 16+2+8+2 bytes.
	data[index.Layers[2].Span.Offset+28] = 2
	_ = index.Close()
	original := sha256.Sum256(data)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	results := make(chan error, 8)
	for range cap(results) {
		go func() {
			boards, err := RenderArtboards(nil, source)
			if err == nil && (len(boards) != 2 || !bytes.Equal(boards[0].Pixels.Pix, make([]byte, 8)) ||
				boards[1].Pixels.NRGBAAt(0, 0) != (color.NRGBA{25, 100, 200, 255})) {
				err = fmt.Errorf("hidden first board or visible second board differs")
			}
			if err == nil {
				boards[1].Pixels.Pix[0] = 0
			}
			results <- err
		}()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if sha256.Sum256(data) != original {
		t.Fatal("concurrent source snapshots or owned output writes changed original bytes")
	}
	selected := 5
	result, err := RenderArtboard(nil, source, ArtboardRenderOptions{LayerIndex: &selected})
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{25, 100, 200, 255}) {
		t.Fatalf("returned image writes leaked into later selected renders: %v", err)
	}
}
