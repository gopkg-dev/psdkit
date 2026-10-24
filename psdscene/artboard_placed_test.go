package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func artboardPlacedFixture(t *testing.T, external, secondMissing bool) ([]byte, []byte) {
	t.Helper()
	base := artboardsTestSource(1)
	index := cachedOpen(t, base)
	child := cachedTestSource(nil, 0, 1, 2)
	var records, samples, linked []byte
	for physical, record := range index.Layers {
		extra := bytes.Clone(base[record.Extra.Offset : record.Extra.Offset+record.Extra.Length])
		if physical == 1 || physical == 4 {
			left := float64(0)
			if physical == 4 {
				left = 3
			}
			parent := recursiveTextParentFixture(t, child, external, image.Pt(5, 1),
				[]float64{left, 0, left + 2, 0, left + 2, 1, left, 1})
			placed := cachedOpen(t, parent)
			for _, block := range placed.Layers[0].AdditionalInfo {
				if block.Key != "SoLd" {
					continue
				}
				payload := bytes.Clone(parent[block.Span.Offset : block.Span.Offset+block.Span.Length])
				if physical == 4 && secondMissing {
					payload = bytes.ReplaceAll(payload, cachedTestUnicode("recursive-text")[4:],
						cachedTestUnicode("missing-source")[4:])
				}
				extra = append(extra, payload...)
			}
			if physical == 1 {
				for _, block := range placed.AdditionalInfo {
					linked = append(linked, parent[block.Span.Offset:block.Span.Offset+block.Span.Length]...)
				}
			}
		}
		records = append(records, base[record.Span.Offset:record.Extra.Offset-4]...)
		records = binary.BigEndian.AppendUint32(records, uint32(len(extra)))
		records = append(records, extra...)
		for _, channel := range record.Channels {
			samples = append(samples, base[channel.Span.Offset:channel.Span.Offset+channel.Span.Length]...)
		}
	}
	info := binary.BigEndian.AppendUint16(nil, 0xfff9)
	info = append(info, records...)
	info = append(info, samples...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	section := binary.BigEndian.AppendUint32(nil, uint32(len(info)))
	section = append(section, info...)
	section = append(section, 0, 0, 0, 0)
	for _, block := range index.AdditionalInfo {
		section = append(section, base[block.Span.Offset:block.Span.Offset+block.Span.Length]...)
	}
	section = append(section, linked...)
	data := bytes.Clone(base[:index.LayerMaskSection.Offset])
	data = binary.BigEndian.AppendUint32(data, uint32(len(section)))
	data = append(data, section...)
	return append(data, base[index.Composite.Span.Offset:]...), child
}

func TestIndependentArtboardsResolveLiveContentsOnceAndOwnResults(t *testing.T) {
	for _, external := range []bool{false, true} {
		data, child := artboardPlacedFixture(t, external, false)
		original := sha256.Sum256(data)
		source := cachedOpen(t, data)
		calls := 0
		options := ArtboardRenderOptions{}
		if external {
			if output, err := RenderArtboards(t.Context(), source); output != nil ||
				!errors.Is(err, psdfile.ErrUnsupported) {
				t.Fatalf("external content used saved linked data or artwork: %v", err)
			}
			options.Render = &Options{ExternalResolver: func(
				_ context.Context, _ psdfile.LinkedFileRecord,
			) (*psdfile.Document, error) {
				calls++
				return psdfile.Parse(child)
			}}
		}
		boards, err := RenderArtboards(t.Context(), source, options)
		if err != nil || len(boards) != 2 {
			t.Fatalf("live independent artboards failed: %v", err)
		}
		for _, board := range boards {
			if board.Pixels.Rect != image.Rect(0, 0, 2, 1) ||
				board.Pixels.NRGBAAt(0, 0) != (color.NRGBA{201, 21, 39, 255}) ||
				board.Pixels.NRGBAAt(1, 0) != (color.NRGBA{17, 31, 77, 255}) {
				t.Fatalf("board used saved artwork/footer or outside pixels: %+v", board.Pixels)
			}
		}
		if external && calls != 1 {
			t.Fatalf("shared original source resolved %d times, want 1", calls)
		}
		if sha256.Sum256(data) != original {
			t.Fatal("live artboard rendering changed original source bytes")
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		boards[0].Pixels.Pix[0] = 0
		if boards[1].Pixels.Pix[0] != 201 {
			t.Fatal("independent board outputs alias each other or depend on source lifetime")
		}
	}
}

func TestSelectedArtboardSkipsOtherBoardMissingOriginalSource(t *testing.T) {
	data, _ := artboardPlacedFixture(t, false, true)
	source := cachedOpen(t, data)
	selected := 2
	output, err := RenderArtboard(t.Context(), source, ArtboardRenderOptions{LayerIndex: &selected})
	if err != nil || output == nil || output.NRGBAAt(0, 0).R != 201 {
		t.Fatalf("selected board unnecessarily resolved another board's missing source: %v", err)
	}
	if outputs, err := RenderArtboards(t.Context(), source); outputs != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("failure returned partial boards or saved other-board artwork: %v", err)
	}
}

func TestIndependentArtboardsSkipHiddenAndZeroOpacitySources(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		data, _ := artboardPlacedFixture(t, false, true)
		index := cachedOpen(t, data)
		if hidden {
			data[index.Layers[5].Extra.Offset-6] = 2
		} else {
			data[index.Layers[4].Extra.Offset-8] = 0
		}
		source := cachedOpen(t, data)
		boards, err := RenderArtboards(t.Context(), source)
		expected := []byte{0, 0, 0, 255, 0, 0, 0, 255}
		if hidden {
			expected = make([]byte, 8)
		}
		if err != nil || len(boards) != 2 || boards[0].Pixels.NRGBAAt(0, 0).R != 201 ||
			!bytes.Equal(boards[1].Pixels.Pix, expected) {
			t.Fatalf("hidden=%t: invisible board resolved missing source or leaked background: %v", hidden, err)
		}
	}
}

func TestIndependentArtboardsAllowNonemptyGroupBoundsWithoutRasterChannels(t *testing.T) {
	data, _ := artboardPlacedFixture(t, false, false)
	index := cachedOpen(t, data)
	for _, record := range index.Layers {
		if record.Divider == 0 {
			continue
		}
		binary.BigEndian.PutUint32(data[record.Span.Offset+8:], 1)
		binary.BigEndian.PutUint32(data[record.Span.Offset+12:], 1)
	}
	source := cachedOpen(t, data)
	boards, err := RenderArtboards(t.Context(), source)
	if err != nil || len(boards) != 2 || boards[0].Pixels.NRGBAAt(0, 0).R != 201 {
		t.Fatalf("group/end rectangle incorrectly required ordinary raster channels: %v", err)
	}
}

func TestIndependentArtboardLivePoliciesCyclesAndWorkingBudget(t *testing.T) {
	data, _ := artboardPlacedFixture(t, true, false)
	source := cachedOpen(t, data)
	provided := Options{ExternalResolver: func(
		_ context.Context, _ psdfile.LinkedFileRecord,
	) (*psdfile.Document, error) {
		return source, nil
	}}
	outputs, err := RenderArtboards(t.Context(), source, ArtboardRenderOptions{Render: &provided})
	if outputs != nil || !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("ancestor resolver was accepted: %v", err)
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		t.Fatalf("rejected ancestor resolver closed the caller's source: %v", err)
	}
	for _, policy := range []Options{{CachedSpecialLayers: true}, {CachedTextLayers: true},
		{OutputProfile: []byte{}}, {MaxWorkingBytes: -1}, {MaxDepth: -1}, {MaxDocuments: -1}} {
		outputs, err := RenderArtboards(t.Context(), source, ArtboardRenderOptions{Render: &policy})
		if outputs != nil || err == nil {
			t.Fatalf("invalid live policy returned artboard output: %+v, %v", policy, err)
		}
	}
	data, _ = artboardPlacedFixture(t, false, false)
	source = cachedOpen(t, data)
	for _, limit := range []Options{{MaxWorkingBytes: 1}, {MaxDocuments: 1}, {MaxDepth: 1}} {
		outputs, err := RenderArtboards(t.Context(), source, ArtboardRenderOptions{Render: &limit})
		if outputs != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("source rendering ignored explicit source limit: %+v, %v", limit, err)
		}
	}
	low, high := int64(1), int64(4<<20)
	for low < high {
		middle := low + (high-low)/2
		_, err := RenderArtboards(t.Context(), source, ArtboardRenderOptions{MaxWorkingBytes: middle})
		if errors.Is(err, psdfile.ErrLimit) {
			low = middle + 1
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		high = middle
	}
	if _, err := RenderArtboards(t.Context(), source, ArtboardRenderOptions{MaxWorkingBytes: low}); err != nil {
		t.Fatalf("exact shared source/snapshot/output budget failed: %v", err)
	}
	outputs, err = RenderArtboards(t.Context(), source, ArtboardRenderOptions{MaxWorkingBytes: low - 1})
	if outputs != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("aggregate live budget minus one returned output: %v", err)
	}
}

func TestIndependentArtboardResolverCancellationClosureAndFailuresReturnNoOutput(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "fail"} {
		data, child := artboardPlacedFixture(t, true, false)
		source := cachedOpen(t, data)
		ctx, cancel := context.WithCancel(t.Context())
		sentinel := errors.New("original source resolver failed")
		options := Options{ExternalResolver: func(
			_ context.Context, _ psdfile.LinkedFileRecord,
		) (*psdfile.Document, error) {
			switch mode {
			case "cancel":
				cancel()
			case "close":
				if err := source.Close(); err != nil {
					t.Fatal(err)
				}
			case "fail":
				return nil, sentinel
			}
			return psdfile.Parse(child)
		}}
		output, err := RenderArtboards(ctx, source, ArtboardRenderOptions{Render: &options})
		cancel()
		wanted := sentinel
		switch mode {
		case "cancel":
			wanted = context.Canceled
		case "close":
			wanted = psdfile.ErrClosed
		}
		if output != nil || !errors.Is(err, wanted) {
			t.Fatalf("%s resolver returned partial/stale output or lost its cause: %v", mode, err)
		}
	}
}

func artboardWrapGroupFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	source := cachedOpen(t, data)
	var records, samples []byte
	rect := artboardTestDescriptor("classFloatRect",
		artboardTestEntry{"Left", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Top ", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Rght", "doub", artboardTestDouble(float64(source.Header.Width))},
		artboardTestEntry{"Btom", "doub", artboardTestDouble(float64(source.Header.Height))})
	board := append(artboardTestU32(16), artboardTestDescriptor("artboard",
		artboardTestEntry{"artboardRect", "Objc", rect},
		artboardTestEntry{"artboardBackgroundType", "long", artboardTestU32(3)})...)
	for _, record := range source.Layers {
		prefix := bytes.Clone(data[record.Span.Offset : record.Extra.Offset-4])
		extra := bytes.Clone(data[record.Extra.Offset : record.Extra.Offset+record.Extra.Length])
		if record.Divider == 1 {
			prefix[len(prefix)-4] = 255
			extra = append(extra, artboardTestBlock("artb", board)...)
		}
		records = append(records, prefix...)
		records = binary.BigEndian.AppendUint32(records, uint32(len(extra)))
		records = append(records, extra...)
		for _, channel := range record.Channels {
			samples = append(samples, data[channel.Span.Offset:channel.Span.Offset+channel.Span.Length]...)
		}
	}
	info := binary.BigEndian.AppendUint16(nil, uint16(65536-len(source.Layers)))
	info = append(info, records...)
	info = append(info, samples...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	section := binary.BigEndian.AppendUint32(nil, uint32(len(info)))
	section = append(section, info...)
	section = append(section, 0, 0, 0, 0)
	for _, block := range source.AdditionalInfo {
		section = append(section, data[block.Span.Offset:block.Span.Offset+block.Span.Length]...)
	}
	donor := artboardTestPSD(artboardTestConfiguration{background: 3, groupOpacity: 255})
	index := cachedOpen(t, donor)
	for _, block := range index.AdditionalInfo {
		if block.Key == "artd" {
			section = append(section, donor[block.Span.Offset:block.Span.Offset+block.Span.Length]...)
		}
	}
	result := bytes.Clone(data[:source.LayerMaskSection.Offset])
	result = binary.BigEndian.AppendUint32(result, uint32(len(section)))
	result = append(result, section...)
	return append(result, data[source.Composite.Span.Offset:]...)
}

func TestIndependentArtboardParentAndRecursiveSourceEffectsRequireSeparatePolicies(t *testing.T) {
	plain := cachedTestSource(nil, 0, 1, 2)
	data := artboardWrapGroupFixture(t, dropShadowSceneSource(t, true, nil, 0, plain))
	source := cachedOpen(t, data)
	paint := psdcompose.Options{ApproximateDropShadow: true}
	if _, err := RenderArtboard(t.Context(), source); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("parent shadow approximation was implicitly enabled: %v", err)
	}
	actual, err := RenderArtboard(t.Context(), source, ArtboardRenderOptions{Compose: paint})
	if err != nil {
		t.Fatalf("explicit parent shadow incorrectly required a nested source effect policy: %v", err)
	}
	expected, err := RenderTile(t.Context(), source, image.Rect(0, 0, 32, 24), TileOptions{Compose: paint})
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("parent placed shadow lost its full live source or runtime effect identity: %v", err)
	}
	child := dropShadowSceneSource(t, false, nil, 0)
	data = artboardWrapGroupFixture(t, dropShadowSceneSource(t, true, nil, 0, child))
	source = cachedOpen(t, data)
	for _, openType := range []bool{false, true} {
		policy := Options{ApproximatePlacedProjection: true}
		if openType {
			policy.ApproximateOpenTypeText = &OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
		}
		_, err := RenderArtboard(t.Context(), source, ArtboardRenderOptions{Compose: paint, Render: &policy})
		if !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("OpenType=%t: parent shadow policy silently enabled recursive source shadow: %v", openType, err)
		}
		policy.Compose = paint
		_, err = RenderArtboard(t.Context(), source, ArtboardRenderOptions{Render: &policy})
		if !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("OpenType=%t: recursive source policy silently enabled parent shadow: %v", openType, err)
		}
		if _, err := RenderArtboard(t.Context(), source, ArtboardRenderOptions{Compose: paint, Render: &policy}); err != nil {
			t.Fatalf("OpenType=%t: separate explicit parent and child shadow policies failed: %v", openType, err)
		}
	}
}
