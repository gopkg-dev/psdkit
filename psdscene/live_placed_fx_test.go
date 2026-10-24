package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func placedFXReplaceBlock(t *testing.T, data []byte, key string, replace []byte, layerIndex int) []byte {
	t.Helper()
	source := cachedOpen(t, data)
	blocks := source.AdditionalInfo
	if layerIndex >= 0 {
		blocks = source.Layers[layerIndex].AdditionalInfo
	}
	for _, block := range blocks {
		if block.Key != key {
			continue
		}
		replacement := artboardTestBlock(key, replace)
		delta := int64(len(replacement)) - block.Span.Length
		binary.BigEndian.PutUint32(data[source.LayerMaskSection.Offset:],
			uint32(int64(binary.BigEndian.Uint32(data[source.LayerMaskSection.Offset:]))+delta))
		if layerIndex >= 0 {
			binary.BigEndian.PutUint32(data[source.LayerInfo.Offset:],
				uint32(int64(binary.BigEndian.Uint32(data[source.LayerInfo.Offset:]))+delta))
			position := source.Layers[layerIndex].Extra.Offset - 4
			binary.BigEndian.PutUint32(data[position:], uint32(source.Layers[layerIndex].Extra.Length+delta))
		}
		result := append(bytes.Clone(data[:block.Span.Offset]), replacement...)
		return append(result, data[block.Span.Offset+block.Span.Length:]...)
	}
	t.Fatalf("missing fixture block %q", key)
	return nil
}

// placedFXSceneData stores original live artwork and unrelated parent layer/merged cache bytes.
func placedFXSceneData(t *testing.T, global bool, feather float64, shift image.Point, external bool) []byte {
	t.Helper()
	data := dropShadowSceneSource(t, true, &global, feather)
	source := cachedOpen(t, data)
	record := source.Layers[1]
	copy(data[record.Extra.Offset-12:], "norm")
	for _, offset := range []int64{record.Mask.Span.Offset} {
		for coordinate, amount := range []int{shift.Y, shift.X, shift.Y, shift.X} {
			field := data[offset+int64(coordinate*4):]
			binary.BigEndian.PutUint32(field, uint32(int32(binary.BigEndian.Uint32(field))+int32(amount)))
		}
	}
	unit := func(name string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: name, Width: 64, Number: value, Bits: math.Float64bits(value)}
	}
	for _, block := range record.AdditionalInfo {
		if block.Key != "lfx2" && block.Key != "SoLd" {
			continue
		}
		prefixLength := int64(4)
		if block.Key == "SoLd" {
			prefixLength = 8
		}
		descriptor, err := source.DecodeDescriptorContext(t.Context(), psdfile.Span{
			Offset: block.Data.Offset + prefixLength, Length: block.Data.Length - prefixLength}, true)
		if err != nil {
			t.Fatal(err)
		}
		for position := range descriptor.Entries {
			entry := &descriptor.Entries[position]
			if entry.Key == "Trnf" {
				for index := range entry.Value.([]psdfile.DescriptorValue) {
					coordinate := &entry.Value.([]psdfile.DescriptorValue)[index]
					amount := shift.X
					if index%2 != 0 {
						amount = shift.Y
					}
					coordinate.Value = coordinate.Value.(float64) + float64(amount)
				}
			}
			if entry.Key == "DrSh" {
				for field := range entry.Value.(*psdfile.Descriptor).Entries {
					parameter := &entry.Value.(*psdfile.Descriptor).Entries[field]
					switch parameter.Key {
					case "Dstn":
						parameter.Value = unit("#Pxl", 16.5)
					case "lagl":
						parameter.Value = unit("#Ang", 180)
					}
				}
			}
		}
		if block.Key == "lfx2" {
			overlay := &psdfile.Descriptor{ClassID: "SoFi", Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: true},
				{Key: "Opct", Type: "UntF", Value: unit("#Prc", 43)},
				{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Scrn"}},
				{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC",
					Entries: []psdfile.DescriptorEntry{
						{Key: "Rd  ", Type: "doub", Value: float64(203)},
						{Key: "Grn ", Type: "doub", Value: float64(47)},
						{Key: "Bl  ", Type: "doub", Value: float64(139)},
					}}},
			}}
			descriptor.Entries = append(descriptor.Entries,
				psdfile.DescriptorEntry{Key: "SoFi", Type: "Objc", Value: overlay})
		}
		encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		prefix := make([]byte, 4)
		if block.Key == "SoLd" {
			prefix = []byte("soLD\x00\x00\x00\x04")
		}
		data = placedFXReplaceBlock(t, data, block.Key, append(prefix, encoded...), 1)
	}
	if external {
		const id = "drop-shadow-original"
		var linked bytes.Buffer
		linked.WriteString("liFE")
		_ = binary.Write(&linked, binary.BigEndian, uint32(1))
		linked.WriteByte(byte(len(id)))
		linked.WriteString(id)
		linked.Write(cachedTestUnicode("explicit-original.psd"))
		linked.WriteString("8BPS8BIM")
		_ = binary.Write(&linked, binary.BigEndian, uint64(0))
		linked.WriteByte(0)
		descriptor, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "null"}, true)
		if err != nil {
			t.Fatal(err)
		}
		linked.Write(descriptor)
		_ = binary.Write(&linked, binary.BigEndian, uint64(len(cachedTestSource(nil))))
		entry := binary.BigEndian.AppendUint64(nil, uint64(linked.Len()))
		entry = append(entry, linked.Bytes()...)
		for len(entry)%4 != 0 {
			entry = append(entry, 0)
		}
		data = placedFXReplaceBlock(t, data, "lnk2", entry, -1)
	}
	return data
}

func TestLivePlacedFXSceneFullRegionVisitorAndOffCanvasSource(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, feather := range []float64{0, 1.5} {
			for _, shift := range []image.Point{{}, {X: -24, Y: 0}} {
				t.Run(fmt.Sprintf("global%t_feather%g_shift%d", global, feather, shift.X), func(t *testing.T) {
					data := placedFXSceneData(t, global, feather, shift, false)
					original := sha256.Sum256(data)
					source, reader := sourceArtboardOpen(t, data)
					for _, channel := range source.Layers[1].Channels {
						if channel.ID >= -1 {
							reader.forbidden = append(reader.forbidden, channel.Data)
						}
					}
					compose := psdcompose.Options{ApproximateDropShadow: true}
					full, err := Render(t.Context(), source, Options{Compose: compose})
					if err != nil {
						t.Fatal(err)
					}
					materialized, err := RenderPlacedLayer(t.Context(), source, 1, Options{Compose: compose})
					if err != nil || !materialized.RenderedEffects.Valid(materialized.Pixels,
						materialized.Bounds, materialized.Placed) {
						t.Fatalf("live placed mixed effects lacked owned source qualification: %v", err)
					}
					if materialized.Pixels.Pix[0] != 201 || materialized.Pixels.Pix[3] != 255 {
						t.Fatal("live source was prepainted with parent overlay, opacity or mask")
					}
					ordinary := *materialized
					ordinary.Placed, ordinary.RenderedEffects = nil, nil
					expected, err := psdcompose.Render(t.Context(), &psdmodel.Document{Width: 32, Height: 24,
						Layers: []*psdmodel.Layer{{Children: []*psdmodel.Layer{&ordinary}, BlendMode: "pass",
							Opacity: new(221.0 / 255)}}}, compose)
					if err != nil || !bytes.Equal(full.Pix, expected.Pix) {
						t.Fatalf("live source changed existing independently qualified raster effect stages: %v", err)
					}
					painted := 0
					for index := 3; index < len(full.Pix); index += 4 {
						if full.Pix[index] != 0 {
							painted++
						}
					}
					if painted == 0 || shift.X < 0 && materialized.Bounds.Max.X > 0 {
						t.Fatal("off-canvas live artwork fixture did not cast a visible independent shadow")
					}
					if err := VisitTiles(t.Context(), source, full.Rect, image.Pt(5, 3), func(tile *image.NRGBA) error {
						for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
							for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
								if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
									t.Fatalf("placed mixed-effect tile seam at %d,%d", x, y)
								}
							}
						}
						return nil
					}, TileOptions{Compose: compose}); err != nil {
						t.Fatal(err)
					}
					if reader.reads != 0 || sha256.Sum256(data) != original {
						t.Fatal("placed mixed effects read cache/footer bytes or mutated source")
					}
				})
			}
		}
	}
}

func TestLivePlacedFXSceneExternalMappingPoliciesLimitsAndCancellation(t *testing.T) {
	source := cachedOpen(t, placedFXSceneData(t, false, 1.5, image.Point{}, true))
	compose := psdcompose.Options{ApproximateDropShadow: true}
	if pixels, err := Render(t.Context(), source, Options{Compose: compose}); pixels != nil || !cachedUnsupported(err) {
		t.Fatalf("external placed effect source acquired an implicit source mapping: %v", err)
	}
	calls := 0
	resolver := func(ctx context.Context, record psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		calls++
		if record.Kind != "liFE" || record.ID != "drop-shadow-original" {
			t.Fatal("external mapping did not receive physical source identity")
		}
		return psdfile.Parse(cachedTestSource(nil))
	}
	options := Options{Compose: compose, ExternalResolver: resolver}
	full, err := Render(t.Context(), source, options)
	if err != nil || calls != 1 {
		t.Fatalf("explicit external placed effects failed or resolved same source repeatedly: %d, %v", calls, err)
	}
	for _, policy := range []psdcompose.Options{{}, {ApproximatePlacedColorOverlay: true}} {
		if pixels, err := Render(t.Context(), source, Options{Compose: policy,
			ExternalResolver: resolver}); pixels != nil || !cachedUnsupported(err) {
			t.Fatalf("external mapping implicitly enabled shadow approximation: %v", err)
		}
	}
	region := image.Rect(0, 0, 8, 24)
	partial, err := RenderTile(t.Context(), source, region, TileOptions{Compose: compose, ExternalResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if partial.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("external placed mixed-effect ROI differs at %d,%d", x, y)
			}
		}
	}
	options.MaxWorkingBytes = 1
	if pixels, err := Render(t.Context(), source, options); pixels != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("external placed mixed effects bypassed working budget: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	options.MaxWorkingBytes = 0
	if pixels, err := Render(ctx, source, options); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled external placed mixed effects returned partial output: %v", err)
	}
}

func placedFXSharedSource(t *testing.T) []byte {
	t.Helper()
	first := placedFXSceneData(t, false, 1.5, image.Point{}, true)
	second := placedFXSceneData(t, true, 0, image.Pt(7, 4), true)
	firstIndex, secondIndex := cachedOpen(t, first), cachedOpen(t, second)
	for _, block := range secondIndex.Layers[1].AdditionalInfo {
		if block.Key != "lfx2" {
			continue
		}
		descriptor, err := secondIndex.DecodeDescriptorContext(t.Context(),
			psdfile.Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4}, true)
		if err != nil {
			t.Fatal(err)
		}
		for position := range descriptor.Entries {
			entry := &descriptor.Entries[position]
			if entry.Key != "SoFi" {
				continue
			}
			for parameter := range entry.Value.(*psdfile.Descriptor).Entries {
				field := &entry.Value.(*psdfile.Descriptor).Entries[parameter]
				if field.Key == "Clr " {
					field.Value = &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
						{Key: "Rd  ", Type: "doub", Value: float64(23)},
						{Key: "Grn ", Type: "doub", Value: float64(197)},
						{Key: "Bl  ", Type: "doub", Value: float64(81)},
					}}
				}
			}
		}
		encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		second = placedFXReplaceBlock(t, second, "lfx2", append(make([]byte, 4), encoded...), 1)
		secondIndex = cachedOpen(t, second)
		break
	}
	var layerInfo bytes.Buffer
	_ = binary.Write(&layerInfo, binary.BigEndian, int16(-4))
	for _, item := range []struct {
		data   []byte
		record psdfile.LayerRecord
	}{{first, firstIndex.Layers[0]}, {first, firstIndex.Layers[1]},
		{second, secondIndex.Layers[1]}, {first, firstIndex.Layers[2]}} {
		layerInfo.Write(item.data[item.record.Span.Offset : item.record.Span.Offset+item.record.Span.Length])
	}
	for _, item := range []struct {
		data   []byte
		record psdfile.LayerRecord
	}{{first, firstIndex.Layers[1]}, {second, secondIndex.Layers[1]}} {
		for _, channel := range item.record.Channels {
			layerInfo.Write(item.data[channel.Span.Offset : channel.Span.Offset+channel.Span.Length])
		}
	}
	if layerInfo.Len()%2 != 0 {
		layerInfo.WriteByte(0)
	}
	section := binary.BigEndian.AppendUint32(nil, uint32(layerInfo.Len()))
	section = append(section, layerInfo.Bytes()...)
	tailStart := firstIndex.LayerInfo.Offset + firstIndex.LayerInfo.Length
	tailEnd := firstIndex.LayerMaskSection.Offset + firstIndex.LayerMaskSection.Length
	section = append(section, first[tailStart:tailEnd]...)
	result := bytes.Clone(first[:firstIndex.LayerMaskSection.Offset])
	result = binary.BigEndian.AppendUint32(result, uint32(len(section)))
	result = append(result, section...)
	return append(result, first[tailEnd:]...)
}

func TestLivePlacedFXSceneSharedSourceKeepsPerInstanceStyles(t *testing.T) {
	source, reader := sourceArtboardOpen(t, placedFXSharedSource(t))
	for _, record := range source.Layers {
		for _, channel := range record.Channels {
			if channel.ID >= -1 {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
	}
	calls := 0
	resolver := func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		calls++
		return psdfile.Parse(cachedTestSource(nil))
	}
	compose := psdcompose.Options{ApproximateDropShadow: true}
	options := Options{Compose: compose, ExternalResolver: resolver}
	actual, err := Render(t.Context(), source, options)
	if err != nil || calls != 1 || reader.reads != 0 {
		t.Fatalf("shared live source was repeatedly resolved or read from saved caches: %d/%d/%v", calls, reader.reads, err)
	}
	var ordinary []*psdmodel.Layer
	for _, index := range []int{1, 2} {
		layer, err := RenderPlacedLayer(t.Context(), source, index, options)
		if err != nil {
			t.Fatal(err)
		}
		if layer.Pixels.Pix[0] != 201 || layer.Pixels.Pix[3] != 255 {
			t.Fatal("shared source cache was contaminated by a particular instance's effects")
		}
		layer.Placed, layer.RenderedEffects = nil, nil
		ordinary = append(ordinary, layer)
	}
	expected, err := psdcompose.Render(t.Context(), &psdmodel.Document{Width: 32, Height: 24,
		Layers: []*psdmodel.Layer{{Children: ordinary, BlendMode: "pass", Opacity: new(221.0 / 255)}}}, compose)
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("shared live source lost per-instance placement, mask or effects: %v", err)
	}
	if ordinary[0].Effects.Items[1].Color == ordinary[1].Effects.Items[1].Color {
		t.Fatal("shared source fixture did not retain different instance overlays")
	}
}
