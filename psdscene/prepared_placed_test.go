package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdplaced"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func preparedTestPayload(t *testing.T, descriptor *psdfile.Descriptor) []byte {
	t.Helper()
	var nameObjects func(*psdfile.Descriptor)
	nameObjects = func(object *psdfile.Descriptor) {
		if object.ClassID == "" {
			object.ClassID = "null"
			if len(object.Entries) == 1 && object.Entries[0].Key == "Rds " {
				object.ClassID = "GsnB"
			}
		}
		for _, entry := range object.Entries {
			switch value := entry.Value.(type) {
			case *psdfile.Descriptor:
				nameObjects(value)
			case []psdfile.DescriptorValue:
				for _, item := range value {
					if nested, valid := item.Value.(*psdfile.Descriptor); valid {
						nameObjects(nested)
					}
				}
			}
		}
	}
	nameObjects(descriptor)
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("soLD\x00\x00\x00\x04"), encoded...)
}

func preparedTestParent(t *testing.T, masked bool) []byte {
	t.Helper()
	data := externalRasterFixture(t, []externalRasterFixtureLink{{id: "prepared-content", name: "source.psd",
		fileType: "8BPS", contents: cachedTestSource(nil)}}, 0)
	index := cachedOpen(t, data)
	view, err := index.ReadSmartObjectView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	placement := view.Records[0]
	filter := smartFilterPlacement(100).Descriptor.Entries[0]
	stack := filter.Value.(*psdfile.Descriptor)
	stack.Entries = append(stack.Entries, psdfile.DescriptorEntry{Key: "filterMaskEnable", Type: "bool", Value: masked})
	placement.Descriptor.Entries = append(placement.Descriptor.Entries,
		psdfile.DescriptorEntry{Key: "placed", Type: "TEXT", Value: "mask-id"}, filter)
	replacement := artboardTestBlock("SoLd", preparedTestPayload(t, placement.Descriptor))
	delta := int64(len(replacement)) - placement.Block.Span.Length
	for _, position := range []int64{index.LayerInfo.Offset, index.LayerMaskSection.Offset} {
		binary.BigEndian.PutUint32(data[position:], uint32(int64(binary.BigEndian.Uint32(data[position:]))+delta))
	}
	position := index.Layers[0].Extra.Offset - 4
	binary.BigEndian.PutUint32(data[position:], uint32(int64(binary.BigEndian.Uint32(data[position:]))+delta))
	updated := append(bytes.Clone(data[:placement.Block.Span.Offset]), replacement...)
	data = append(updated, data[placement.Block.Span.Offset+placement.Block.Span.Length:]...)
	if !masked {
		return data
	}
	var arrays bytes.Buffer
	for _, value := range []int32{0, 0, 1, 2} {
		_ = binary.Write(&arrays, binary.BigEndian, value)
	}
	_ = binary.Write(&arrays, binary.BigEndian, uint32(8))
	_ = binary.Write(&arrays, binary.BigEndian, uint32(0))
	_ = binary.Write(&arrays, binary.BigEndian, uint32(1))
	_ = binary.Write(&arrays, binary.BigEndian, uint64(4))
	arrays.Write([]byte{0, 0, 99, 201})
	_ = binary.Write(&arrays, binary.BigEndian, uint32(0))
	cache := append([]byte{7}, "mask-id"...)
	cache = binary.BigEndian.AppendUint32(cache, 1)
	cache = binary.BigEndian.AppendUint64(cache, uint64(arrays.Len()))
	cache = append(cache, arrays.Bytes()...)
	cache = append(cache, 1)
	for _, value := range []uint32{0, 0, 1, 2} {
		cache = binary.BigEndian.AppendUint32(cache, value)
	}
	cache = binary.BigEndian.AppendUint64(cache, 4)
	cache = append(cache, 0, 0, 0, 255)
	payload := binary.BigEndian.AppendUint64(artboardTestU32(3), uint64(len(cache)))
	payload = append(payload, cache...)
	payload = append(payload, make([]byte, (4-len(cache)%4)%4)...)
	global := artboardTestBlock("FEid", payload)
	index = cachedOpen(t, data)
	binary.BigEndian.PutUint32(data[index.LayerMaskSection.Offset:],
		binary.BigEndian.Uint32(data[index.LayerMaskSection.Offset:])+uint32(len(global)))
	updated = append(bytes.Clone(data[:index.Composite.Span.Offset]), global...)
	return append(updated, data[index.Composite.Span.Offset:]...)
}

func preparedTestCandidate(
	t *testing.T, source *psdfile.Document, radius float64, enabled bool, mutate func(*psdfile.Descriptor),
) (*psdfile.Document, []byte) {
	t.Helper()
	view, err := source.ReadSmartObjectView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	descriptor := view.Records[0].Descriptor
	for _, entry := range descriptor.Entries {
		if entry.Key != "filterFX" {
			continue
		}
		stack := entry.Value.(*psdfile.Descriptor)
		for index := range stack.Entries {
			field := &stack.Entries[index]
			if field.Key == "enab" {
				field.Value = enabled
			}
			if field.Key != "filterFXList" {
				continue
			}
			item := field.Value.([]psdfile.DescriptorValue)[0].Value.(*psdfile.Descriptor)
			for _, itemField := range item.Entries {
				if itemField.Key != "Fltr" {
					continue
				}
				parameters := itemField.Value.(*psdfile.Descriptor)
				parameters.Entries[0].Value = psdfile.DescriptorUnit{Unit: "#Pxl", Number: radius, Width: 64}
			}
		}
	}
	if mutate != nil {
		mutate(descriptor)
	}
	data := cachedTestSource(artboardTestBlock("SoLd", preparedTestPayload(t, descriptor)))
	return cachedOpen(t, data), data
}

func preparedTestParentMask(t *testing.T, data []byte) []byte {
	t.Helper()
	index := cachedOpen(t, data)
	record := index.Layers[0]
	mask := make([]byte, 24)
	binary.BigEndian.PutUint32(mask, 20)
	binary.BigEndian.PutUint32(mask[12:], 1)
	binary.BigEndian.PutUint32(mask[16:], 2)
	channel := binary.BigEndian.AppendUint32([]byte{255, 254}, 4)
	delta := uint32(len(mask) - 4 + len(channel) + 4)
	data = bytes.Clone(data)
	for _, position := range []int64{index.LayerInfo.Offset, index.LayerMaskSection.Offset} {
		binary.BigEndian.PutUint32(data[position:], binary.BigEndian.Uint32(data[position:])+delta)
	}
	binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length)+uint32(len(mask)-4))
	binary.BigEndian.PutUint16(data[record.Span.Offset+16:], uint16(len(record.Channels)+1))
	data[record.Extra.Offset-8] = 64
	last := record.Channels[len(record.Channels)-1].Span
	for _, change := range []struct {
		start, end int64
		value      []byte
	}{
		{last.Offset + last.Length, last.Offset + last.Length, make([]byte, 4)},
		{record.MaskData.Offset, record.MaskData.Offset + record.MaskData.Length, mask},
		{record.Span.Offset + 18 + int64(len(record.Channels))*6,
			record.Span.Offset + 18 + int64(len(record.Channels))*6, channel},
	} {
		updated := append(bytes.Clone(data[:change.start]), change.value...)
		data = append(updated, data[change.end:]...)
	}
	return data
}

func preparedTestPixels(t *testing.T, input *image.NRGBA, radius float64) *image.NRGBA {
	t.Helper()
	result, err := psdfilter.Apply(t.Context(), input, psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{{
		Kind: psdfilter.GaussianBlur, Enabled: true, Opacity: 1, Radius: radius, BlendMode: "norm",
	}}}, psdfilter.Options{ApproximateGaussian: true})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPreparedPlacedLayerUsesCandidateOnceAndOriginalFilterMask(t *testing.T) {
	for _, masked := range []bool{false, true} {
		data := preparedTestParent(t, masked)
		reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
		source, err := psdfile.Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.Close() })
		candidate, candidateBytes := preparedTestCandidate(t, source, 2, true, nil)
		before, candidateBefore := sha256.Sum256(data), sha256.Sum256(candidateBytes)
		for _, channel := range source.Layers[0].Channels {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
		reader.forbidden = append(reader.forbidden, source.Composite.Data)
		if masked {
			caches, err := source.FilterCaches(t.Context())
			if err != nil || len(caches) != 1 {
				t.Fatalf("filter-mask fixture: %v", err)
			}
			reader.forbidden = append(reader.forbidden, caches[0].Arrays[0].Data)
		}
		request := psdplaced.Prepared{Source: source, Candidate: candidate}
		options := Options{SmartFilters: &psdfilter.Options{ApproximateGaussian: true}}
		actual, err := RenderPreparedPlacedLayer(t.Context(), request, options)
		expected := preparedTestPixels(t, externalRasterInput(), 2)
		if masked {
			copy(expected.Pix[:4], externalRasterInput().Pix[:4])
		}
		if err != nil || !bytes.Equal(actual.Pixels.Pix, expected.Pix) || reader.reads != 0 {
			t.Fatalf("candidate filter used stale pixels, ran twice or lost parent mask: masked=%t error=%v", masked, err)
		}
		if sha256.Sum256(data) != before || sha256.Sum256(candidateBytes) != candidateBefore {
			t.Fatal("candidate rendering modified parent or candidate bytes")
		}
		if _, err := source.ReadAt(nil, 0); err != nil {
			t.Fatal("candidate rendering closed its borrowed parent", err)
		}
		if _, err := candidate.ReadAt(nil, 0); err != nil {
			t.Fatal("candidate rendering closed its borrowed candidate", err)
		}
	}
}

func TestPreparedPlacedLayerDisabledStackAndContentsOverrides(t *testing.T) {
	source := cachedOpen(t, preparedTestParent(t, false))
	options := Options{SmartFilters: &psdfilter.Options{ApproximateGaussian: true}}
	for _, enabled := range []bool{true, false} {
		candidate, _ := preparedTestCandidate(t, source, 2, enabled, nil)
		for _, fileType := range []string{"", "PNGf", "8BPS"} {
			input := externalRasterInput()
			request := psdplaced.Prepared{Source: source, Candidate: candidate}
			if fileType != "" {
				input.Pix[0], input.Pix[1] = 41, 240
				request.FileType = fileType
				if fileType == "PNGf" {
					request.Contents = externalRasterPNG(t, input, nil)
				} else {
					request.Contents = cachedTestSource(nil)
					child := cachedOpen(t, request.Contents)
					request.Contents[child.Layers[0].Channels[0].Data.Offset] = 41
					request.Contents[child.Layers[0].Channels[1].Data.Offset] = 240
				}
			}
			actual, err := RenderPreparedPlacedLayer(t.Context(), request, options)
			expected := input
			if enabled {
				expected = preparedTestPixels(t, input, 2)
			}
			if err != nil || !bytes.Equal(actual.Pixels.Pix, expected.Pix) {
				t.Fatalf("candidate disabled/overlay source differs: enabled=%t type=%q error=%v", enabled, fileType, err)
			}
		}
	}
}

func TestPreparedPlacedLayerUsesEditedGeometryWithoutParentMaskOrOpacity(t *testing.T) {
	data := preparedTestParentMask(t, preparedTestParent(t, false))
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	candidate, _ := preparedTestCandidate(t, source, 2, true, func(descriptor *psdfile.Descriptor) {
		for index := range descriptor.Entries {
			if descriptor.Entries[index].Key != "Trnf" {
				continue
			}
			values := descriptor.Entries[index].Value.([]psdfile.DescriptorValue)
			for coordinate := 0; coordinate < len(values); coordinate += 2 {
				values[coordinate].Value = values[coordinate].Value.(float64) + 1
			}
		}
	})
	for _, channel := range source.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	actual, err := RenderPreparedPlacedLayer(t.Context(), psdplaced.Prepared{Source: source, Candidate: candidate},
		Options{SmartFilters: &psdfilter.Options{ApproximateGaussian: true}})
	projected := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(projected.Pix[4:], externalRasterInput().Pix[:4])
	expected := preparedTestPixels(t, projected, 2)
	if err != nil || !bytes.Equal(actual.Pixels.Pix, expected.Pix) || reader.reads != 0 ||
		actual.Mask != nil || actual.Opacity != nil || actual.Effects != nil {
		t.Fatalf("prepared pixels used old geometry or baked parent styling: %v", err)
	}
	if !slices.Equal(actual.Placed.Transform, []float64{1, 0, 3, 0, 3, 1, 1, 1}) {
		t.Fatal("prepared layer did not retain candidate geometry")
	}
}

func TestPreparedPlacedLayerRejectsMismatchedIdentityPolicyAndBudget(t *testing.T) {
	source := cachedOpen(t, preparedTestParent(t, false))
	candidate, _ := preparedTestCandidate(t, source, 2, true, nil)
	request := psdplaced.Prepared{Source: source, Candidate: candidate}
	for _, options := range []Options{{MaxWorkingBytes: 1}, {CachedSpecialLayers: true},
		{CachedTextLayers: true}, {OutputProfile: []byte{}}, {SmartFilters: &psdfilter.Options{}}} {
		if result, err := RenderPreparedPlacedLayer(t.Context(), request, options); result != nil || err == nil {
			t.Fatalf("invalid/insufficient prepared render policy returned artwork: %v", err)
		}
	}
	for _, field := range []string{"Idnt", "placed"} {
		changed, _ := preparedTestCandidate(t, source, 2, true, func(descriptor *psdfile.Descriptor) {
			for index := range descriptor.Entries {
				if descriptor.Entries[index].Key == field {
					descriptor.Entries[index].Value = "different-identity"
				}
			}
		})
		request.Candidate = changed
		if result, err := RenderPreparedPlacedLayer(t.Context(), request); result != nil || !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("candidate changed original %s identity: %v", field, err)
		}
	}
	request.Candidate, request.Contents, request.FileType = candidate, externalRasterPNG(t, externalRasterInput(), nil), "JPEG"
	if result, err := RenderPreparedPlacedLayer(t.Context(), request,
		Options{SmartFilters: &psdfilter.Options{ApproximateGaussian: true}}); result != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("candidate encoding mismatch was accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := RenderPreparedPlacedLayer(ctx, request); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepared renderer returned artwork: %v", err)
	}
}
