package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// dropShadowSceneSource creates original artwork and independent poisoned parent caches entirely in Go.
func dropShadowSceneSource(t *testing.T, placed bool, maskStage *bool, feather float64, prototypes ...[]byte) []byte {
	t.Helper()
	const width, height = 32, 24
	unit := func(name string, number float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: name, Width: 64, Number: number, Bits: math.Float64bits(number)}
	}
	shadow := &psdfile.Descriptor{ClassID: "DrSh", Entries: []psdfile.DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
			{Key: "Rd  ", Type: "doub", Value: float64(30)},
			{Key: "Grn ", Type: "doub", Value: float64(60)},
			{Key: "Bl  ", Type: "doub", Value: float64(90)},
		}}},
		{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: "Mltp"}},
		{Key: "Opct", Type: "UntF", Value: unit("#Prc", 67)},
		{Key: "uglg", Type: "bool", Value: false},
		{Key: "Dstn", Type: "UntF", Value: unit("#Pxl", 4)},
		{Key: "lagl", Type: "UntF", Value: unit("#Ang", 54)},
		{Key: "blur", Type: "UntF", Value: unit("#Pxl", 2)},
		{Key: "Ckmt", Type: "UntF", Value: unit("#Pxl", 4)},
	}}
	descriptor := &psdfile.Descriptor{ClassID: "Lefx", Entries: []psdfile.DescriptorEntry{
		{Key: "masterFXSwitch", Type: "bool", Value: true}, {Key: "DrSh", Type: "Objc", Value: shadow},
	}}
	payload, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	tags := artboardTestBlock("lfx2", append(make([]byte, 4), payload...))
	var global []byte
	bounds := image.Rect(6, 5, 22, 17)
	if placed {
		const id = "drop-shadow-original"
		prototype := cachedTestSource(nil)
		if len(prototypes) != 0 {
			prototype = prototypes[0]
		}
		child := cachedOpen(t, prototype)
		transform := []psdfile.DescriptorValue{}
		for _, coordinate := range []float64{6, 5, 22, 5, 22, 17, 6, 17} {
			transform = append(transform, psdfile.DescriptorValue{Type: "doub", Value: coordinate})
		}
		placement := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
			{Key: "Idnt", Type: "TEXT", Value: id}, {Key: "Type", Type: "long", Value: int32(2)},
			{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
				{Key: "Wdth", Type: "doub", Value: float64(child.Header.Width)},
				{Key: "Hght", Type: "doub", Value: float64(child.Header.Height)},
			}}},
			{Key: "Trnf", Type: "VlLs", Value: transform},
		}}
		payload, err := psdfile.EncodeDescriptor(nil, placement, true)
		if err != nil {
			t.Fatal(err)
		}
		tags = append(tags, artboardTestBlock("SoLd", append([]byte("soLD\x00\x00\x00\x04"), payload...))...)
		var linked bytes.Buffer
		linked.WriteString("liFD")
		_ = binary.Write(&linked, binary.BigEndian, uint32(1))
		linked.WriteByte(byte(len(id)))
		linked.WriteString(id)
		linked.Write(cachedTestUnicode("original.psd"))
		linked.WriteString("8BPS8BIM")
		_ = binary.Write(&linked, binary.BigEndian, uint64(len(prototype)))
		linked.WriteByte(0)
		linked.Write(prototype)
		entry := binary.BigEndian.AppendUint64(nil, uint64(linked.Len()))
		entry = append(entry, linked.Bytes()...)
		for len(entry)%4 != 0 {
			entry = append(entry, 0)
		}
		global = artboardTestBlock("lnk2", entry)
		// The independent magenta saved cache has deliberately unrelated source geometry and samples.
		bounds = image.Rect(0, 0, 1, 1)
	}
	mask := []byte{}
	if maskStage != nil {
		for _, coordinate := range []int32{1, 2, 23, 30} {
			mask = binary.BigEndian.AppendUint32(mask, uint32(coordinate))
		}
		mask = append(mask, 0, 0)
		if feather != 0 {
			mask[17] |= 16
			mask = append(mask, 2)
			mask = binary.BigEndian.AppendUint64(mask, math.Float64bits(feather))
		}
		if len(mask)%2 != 0 {
			mask = append(mask, 0)
		}
		value := byte(0)
		if *maskStage {
			value = 1
		}
		tags = append(tags, artboardTestBlock("lmgm", []byte{value, 0, 0, 0})...)
	}
	var records, channels bytes.Buffer
	_ = binary.Write(&records, binary.BigEndian, int16(-3))
	for index := range 3 {
		layerBounds, ids, mode := image.Rectangle{}, []int16{}, "pass"
		if index == 1 {
			layerBounds, ids, mode = bounds, []int16{0, 1, 2, -1}, "mul "
			if maskStage != nil {
				ids = append(ids, -2)
			}
		}
		for _, coordinate := range []int{layerBounds.Min.Y, layerBounds.Min.X, layerBounds.Max.Y, layerBounds.Max.X} {
			_ = binary.Write(&records, binary.BigEndian, int32(coordinate))
		}
		_ = binary.Write(&records, binary.BigEndian, uint16(len(ids)))
		for _, id := range ids {
			channelBounds := layerBounds
			if id == -2 {
				channelBounds = image.Rect(2, 1, 30, 23)
			}
			_ = binary.Write(&records, binary.BigEndian, id)
			_ = binary.Write(&records, binary.BigEndian, uint32(channelBounds.Dx()*channelBounds.Dy()+2))
			channels.Write([]byte{0, 0})
			for y := channelBounds.Min.Y; y < channelBounds.Max.Y; y++ {
				for x := channelBounds.Min.X; x < channelBounds.Max.X; x++ {
					value := byte((x*19 + y*31 + int(id)*43 + 256) % 256)
					if id == -1 {
						value = byte(40 + (x*11+y*17)%216)
					}
					if placed && id != -2 {
						value = 255
						if id == 1 {
							value = 0
						}
					}
					channels.WriteByte(value)
				}
			}
		}
		records.WriteString("8BIM" + mode)
		records.Write([]byte{221, 0, 0, 0})
		extra := make([]byte, 4)
		if index == 1 {
			binary.BigEndian.PutUint32(extra, uint32(len(mask)))
			extra = append(extra, mask...)
		}
		extra = append(extra, make([]byte, 4)...)
		extra = append(extra, 1, 'L', 0, 0)
		if index == 1 {
			extra = append(extra, tags...)
			extra = append(extra, artboardTestBlock("iOpa", []byte{173, 0, 0, 0})...)
		} else {
			divider := uint32(3)
			if index == 2 {
				divider = 1
			}
			extra = append(extra, artboardTestBlock("lsct", append(artboardTestU32(divider), "8BIMpass"...))...)
		}
		_ = binary.Write(&records, binary.BigEndian, uint32(len(extra)))
		records.Write(extra)
	}
	records.Write(channels.Bytes())
	if records.Len()%2 != 0 {
		records.WriteByte(0)
	}
	section := append(artboardTestU32(uint32(records.Len())), records.Bytes()...)
	section = append(section, make([]byte, 4)...)
	section = append(section, global...)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = binary.BigEndian.AppendUint32(data, height)
	data = binary.BigEndian.AppendUint32(data, width)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	data = append(data, artboardTestU32(uint32(len(section)))...)
	data = append(data, section...)
	data = append(data, 0, 0)
	return append(data, bytes.Repeat([]byte{255}, width*height*4)...)
}

func TestDropShadowSceneFullTileAndVisitorUseLiveSources(t *testing.T) {
	for _, placed := range []bool{false, true} {
		for _, globalMask := range []bool{false, true} {
			for _, feather := range []float64{0, 1.5} {
				data := dropShadowSceneSource(t, placed, &globalMask, feather)
				digest := sha256.Sum256(data)
				source := cachedOpen(t, data)
				settings := psdcompose.Options{ApproximateDropShadow: true}
				full, err := Render(nil, source, Options{Compose: settings})
				if err != nil {
					if detail, ok := errors.AsType[*psdfile.Error](err); ok {
						t.Fatalf("placed=%t maskStage=%t feather=%g: %v (cause %v)",
							placed, globalMask, feather, err, detail.Cause)
					}
					t.Fatal(err)
				}
				if full.NRGBAAt(0, 0).A != 0 || bytes.Equal(full.Pix, bytes.Repeat([]byte{255}, len(full.Pix))) {
					t.Fatal("scene used the unrelated opaque merged footer")
				}
				if err := VisitTiles(nil, source, full.Rect, image.Pt(5, 3), func(tile *image.NRGBA) error {
					for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
						for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
							if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
								t.Fatalf("placed=%t stage=%t feather=%g ROI seam at(%d,%d): %v != %v",
									placed, globalMask, feather, x, y, tile.NRGBAAt(x, y), full.NRGBAAt(x, y))
							}
						}
					}
					return nil
				}, TileOptions{Compose: settings}); err != nil {
					t.Fatal(err)
				}
				if sha256.Sum256(data) != digest {
					t.Fatal("source effects, placement or encoded channels were mutated")
				}
			}
		}
	}
}

func TestDropShadowScenePolicyBudgetAndLifecycle(t *testing.T) {
	maskStage := false
	source := cachedOpen(t, dropShadowSceneSource(t, true, &maskStage, 0))
	if _, err := Render(nil, source); !cachedUnsupported(err) {
		t.Fatalf("default policy silently selected approximate shadow: %v", err)
	}
	settings := TileOptions{Compose: psdcompose.Options{ApproximateDropShadow: true}, MaxWorkingBytes: 1024}
	if _, err := RenderTile(nil, source, image.Rect(12, 8, 13, 9), settings); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("source effect or placement budget was bypassed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	settings.MaxWorkingBytes = 4 << 20
	if _, err := RenderTile(ctx, source, image.Rect(12, 8, 13, 9), settings); !errors.Is(err, context.Canceled) {
		t.Fatalf("source shadow lost cancellation: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderTile(nil, source, image.Rect(12, 8, 13, 9), settings); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source became an implicit retained placement cache: %v", err)
	}
}

func TestRenderPlacedLayerKeepsLiveProvenanceAndIgnoresSavedCaches(t *testing.T) {
	maskStage := false
	data := dropShadowSceneSource(t, true, &maskStage, 1.5)
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, channel := range source.Layers[1].Channels {
		if channel.ID >= -1 {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	settings := Options{Compose: psdcompose.Options{ApproximateDropShadow: true}, MaxWorkingBytes: 4 << 20}
	layer, err := RenderPlacedLayer(nil, source, 1, settings)
	if err != nil {
		t.Fatal(err)
	}
	if reader.reads != 0 || layer.Bounds != image.Rect(6, 5, 22, 17) ||
		layer.Placed == nil || layer.Placed.ID != "drop-shadow-original" ||
		!layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		t.Fatal("materialized source used saved caches, lost original geometry or omitted runtime provenance")
	}
	if layer.Pixels.NRGBAAt(layer.Pixels.Rect.Min.X, layer.Pixels.Rect.Min.Y).R != 201 ||
		layer.Mask == nil || layer.Mask.Pixels == nil || layer.Mask.Pixels.Pix[0] == 255 {
		t.Fatal("source color projection or independent mask samples were not retained")
	}
	composed, err := psdcompose.Render(nil, &psdmodel.Document{Width: 32, Height: 24,
		Layers: []*psdmodel.Layer{layer}}, settings.Compose)
	if err != nil {
		t.Fatal(err)
	}
	full, err := RenderTile(nil, source, image.Rect(0, 0, 32, 24), TileOptions{Compose: settings.Compose})
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := Render(nil, source, settings)
	if err != nil || !bytes.Equal(rebuilt.Pix, full.Pix) || reader.reads != 0 {
		t.Fatalf("complete source rendering read saved parent artwork or differs from regional reconstruction: %v", err)
	}
	// The source fixture's parent group opacity is represented explicitly in this comparison.
	grouped, err := psdcompose.Render(nil, &psdmodel.Document{Width: 32, Height: 24,
		Layers: []*psdmodel.Layer{{Children: []*psdmodel.Layer{layer}, BlendMode: "pass",
			Opacity: func() *float64 { value := float64(221) / 255; return &value }()}}}, settings.Compose)
	if err != nil || !bytes.Equal(grouped.Pix, full.Pix) || bytes.Equal(composed.Pix, full.Pix) {
		t.Fatalf("materialized layer mixed parent group paint into its source, or differs from source composition: %v", err)
	}
	original := source.Layers[1].Bounds
	if original != image.Rect(0, 0, 1, 1) {
		t.Fatal("materialization changed the source's unrelated saved-cache bounds")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if layer.Pixels.Pix[0] != 201 || !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		t.Fatal("returned artwork depends on the source lifetime")
	}
}

func TestRenderPlacedLayerRejectsInvalidInputsAndBudget(t *testing.T) {
	source := cachedOpen(t, dropShadowSceneSource(t, true, nil, 0))
	for _, index := range []int{-1, 3} {
		if _, err := RenderPlacedLayer(nil, source, index); !errors.Is(err, psdfile.ErrMalformed) {
			t.Fatalf("invalid original source index was accepted: %v", err)
		}
	}
	if _, err := RenderPlacedLayer(nil, source, 0); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("folder was materialized as a placed artwork: %v", err)
	}
	if _, err := RenderPlacedLayer(nil, source, 1, Options{MaxWorkingBytes: 1024}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("source materialization exceeded its owned working budget: %v", err)
	}
	profileOptions := Options{OutputProfile: []byte{}}
	if _, err := RenderPlacedLayer(nil, source, 1, profileOptions); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("materialized pixels could change the parent color encoding: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderPlacedLayer(ctx, source, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("materialized source lost context cancellation: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderPlacedLayer(nil, source, 1); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed original source accepted live projection: %v", err)
	}
}

func shiftDropShadowPlacement(t *testing.T, data []byte, displacement image.Point) []byte {
	t.Helper()
	data = bytes.Clone(data)
	source := cachedOpen(t, data)
	view, err := source.ReadSmartObjectView(nil)
	if err != nil || len(view.Records) != 1 {
		t.Fatalf("expected one original placed source: %v", err)
	}
	for _, entry := range view.Records[0].Descriptor.Entries {
		if entry.Key != "Trnf" {
			continue
		}
		values := entry.Value.([]psdfile.DescriptorValue)
		for index, item := range values {
			delta := displacement.X
			if index%2 != 0 {
				delta = displacement.Y
			}
			if item.Data.Length != 8 {
				t.Fatal("fixture transform lost exact scalar binding")
			}
			binary.BigEndian.PutUint64(data[item.Data.Offset:], math.Float64bits(item.Value.(float64)+float64(delta)))
		}
	}
	return data
}

func TestDropShadowScenePreservesOffCanvasSourceHalo(t *testing.T) {
	for _, displacement := range []image.Point{{X: 20, Y: -12}, {X: 24, Y: 5}} {
		for _, maskStage := range []*bool{nil, new(false), new(true)} {
			data := shiftDropShadowPlacement(t, dropShadowSceneSource(t, true, maskStage, 1.5), displacement)
			source := cachedOpen(t, data)
			settings := psdcompose.Options{ApproximateDropShadow: true}
			full, err := Render(nil, source, Options{Compose: settings})
			if err != nil {
				t.Fatal(err)
			}
			if err := VisitTiles(nil, source, full.Rect, image.Pt(3, 4), func(tile *image.NRGBA) error {
				for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
					for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
						if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
							t.Fatalf("off-canvas source seam %v at (%d,%d): %v != %v",
								displacement, x, y, tile.NRGBAAt(x, y), full.NRGBAAt(x, y))
						}
					}
				}
				return nil
			}, TileOptions{Compose: settings}); err != nil {
				t.Fatal(err)
			}
			layer, err := RenderPlacedLayer(nil, source, 1, Options{Compose: settings})
			if err != nil || layer.Bounds.In(full.Rect) {
				t.Fatalf("materialized source clipped required off-canvas coverage: %v, %v", layer, err)
			}
			if maskStage == nil {
				complete, err := psdcompose.Render(nil,
					&psdmodel.Document{Width: 32, Height: 24, Layers: []*psdmodel.Layer{layer}}, settings)
				if err != nil {
					t.Fatal(err)
				}
				cropped := *layer
				cropped.Bounds = layer.Bounds.Intersect(full.Rect)
				cropped.Pixels = image.NewNRGBA(cropped.Bounds)
				for y := cropped.Bounds.Min.Y; y < cropped.Bounds.Max.Y; y++ {
					for x := cropped.Bounds.Min.X; x < cropped.Bounds.Max.X; x++ {
						cropped.Pixels.SetNRGBA(x, y, layer.Pixels.NRGBAAt(
							layer.Pixels.Rect.Min.X+x-layer.Bounds.Min.X, layer.Pixels.Rect.Min.Y+y-layer.Bounds.Min.Y))
					}
				}
				cropped.Placed, cropped.RenderedEffects = nil, nil
				clipped, err := psdcompose.Render(nil,
					&psdmodel.Document{Width: 32, Height: 24, Layers: []*psdmodel.Layer{&cropped}}, settings)
				if err != nil || bytes.Equal(complete.Pix, clipped.Pix) {
					t.Fatalf("off-canvas control lacks informative shadow coverage: %v", err)
				}
			}
		}
	}
}
