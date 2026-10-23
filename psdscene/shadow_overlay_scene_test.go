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
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func shadowOverlaySceneSource(t *testing.T, global bool, feather float64, shift image.Point) []byte {
	t.Helper()
	data := dropShadowSceneSource(t, false, &global, feather)
	index := cachedOpen(t, data)
	record := index.Layers[1]
	var block psdfile.AdditionalBlock
	for _, candidate := range record.AdditionalInfo {
		if candidate.Key == "lfx2" {
			block = candidate
		}
	}
	descriptor, err := index.DecodeDescriptorContext(t.Context(),
		psdfile.Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4}, true)
	if err != nil {
		t.Fatal(err)
	}
	unit := func(name string, value float64) psdfile.DescriptorUnit {
		return psdfile.DescriptorUnit{Unit: name, Width: 64, Number: value, Bits: math.Float64bits(value)}
	}
	for position := range descriptor.Entries {
		entry := &descriptor.Entries[position]
		if entry.Key != "DrSh" {
			continue
		}
		first := entry.Value.(*psdfile.Descriptor)
		for field := range first.Entries {
			switch first.Entries[field].Key {
			case "Dstn":
				first.Entries[field].Value = unit("#Pxl", 16.5)
			case "lagl":
				first.Entries[field].Value = unit("#Ang", 180)
			}
		}
		second := *first
		second.Entries = append([]psdfile.DescriptorEntry(nil), first.Entries...)
		for field := range second.Entries {
			if second.Entries[field].Key == "Dstn" {
				second.Entries[field].Value = unit("#Pxl", 12.25)
			}
		}
		second.Entries = append(second.Entries,
			psdfile.DescriptorEntry{Key: "layerConceals", Type: "bool", Value: false})
		entry.Key, entry.Type = "dropShadowMulti", "VlLs"
		entry.Value = []psdfile.DescriptorValue{{Type: "Objc", Value: first}, {Type: "Objc", Value: &second}}
	}
	var overlays []psdfile.DescriptorValue
	for _, mode := range []string{"Scrn", "Mltp"} {
		overlay := &psdfile.Descriptor{ClassID: "SoFi", Entries: []psdfile.DescriptorEntry{
			{Key: "enab", Type: "bool", Value: true},
			{Key: "Opct", Type: "UntF", Value: unit("#Prc", 43)},
			{Key: "Md  ", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "BlnM", ValueID: mode}},
			{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
				{Key: "Rd  ", Type: "doub", Value: float64(203)},
				{Key: "Grn ", Type: "doub", Value: float64(47)},
				{Key: "Bl  ", Type: "doub", Value: float64(139)},
			}}},
		}}
		overlays = append(overlays, psdfile.DescriptorValue{Type: "Objc", Value: overlay})
	}
	descriptor.Entries = append(descriptor.Entries,
		psdfile.DescriptorEntry{Key: "solidFillMulti", Type: "VlLs", Value: overlays})
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	replacement := artboardTestBlock("lfx2", append(make([]byte, 4), encoded...))
	delta := int64(len(replacement)) - block.Span.Length
	for _, offset := range []int64{index.LayerMaskSection.Offset, index.LayerInfo.Offset} {
		binary.BigEndian.PutUint32(data[offset:], uint32(int64(binary.BigEndian.Uint32(data[offset:]))+delta))
	}
	binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length+delta))
	copy(data[record.Extra.Offset-12:], "norm")
	for _, offset := range []int64{record.Span.Offset, record.Mask.Span.Offset} {
		for coordinate, amount := range []int{shift.Y, shift.X, shift.Y, shift.X} {
			field := data[offset+int64(coordinate*4):]
			value := int32(binary.BigEndian.Uint32(field))
			binary.BigEndian.PutUint32(field, uint32(value+int32(amount)))
		}
	}
	result := append(bytes.Clone(data[:block.Span.Offset]), replacement...)
	return append(result, data[block.Span.Offset+block.Span.Length:]...)
}

func TestShadowOverlaySceneFullTileVisitorAndOffCanvasMasks(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, feather := range []float64{0, 1.5} {
			for _, shift := range []image.Point{{}, {X: -18, Y: -8}, {X: -24, Y: 10}} {
				t.Run(fmt.Sprintf("global%t_feather%g_shift%d_%d", global, feather, shift.X, shift.Y), func(t *testing.T) {
					data := shadowOverlaySceneSource(t, global, feather, shift)
					before := sha256.Sum256(data)
					source, reader := sourceArtboardOpen(t, data)
					complete, err := source.DecodeLayers(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					options := psdcompose.Options{ApproximateDropShadow: true}
					expected, err := psdcompose.Render(t.Context(), complete, options)
					if err != nil {
						t.Fatal(err)
					}
					full, err := Render(t.Context(), source, Options{Compose: options})
					if err != nil || !bytes.Equal(full.Pix, expected.Pix) || reader.reads != 0 {
						t.Fatalf("source window changed complete mixed-effect pixels: %v", err)
					}
					if bytes.Equal(full.Pix, make([]byte, len(full.Pix))) {
						t.Fatal("mixed-effect control did not paint informative pixels")
					}
					if err := VisitTiles(t.Context(), source, full.Rect, image.Pt(5, 3), func(tile *image.NRGBA) error {
						for y := tile.Rect.Min.Y; y < tile.Rect.Max.Y; y++ {
							for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
								if tile.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
									t.Fatalf("mixed-effect source halo or feather seam at %d,%d", x, y)
								}
							}
						}
						return nil
					}, TileOptions{Compose: options}); err != nil {
						t.Fatal(err)
					}
					for _, region := range []image.Rectangle{image.Rect(0, 0, 1, 1), image.Rect(0, 14, 3, 24),
						image.Rect(29, 0, 32, 24), image.Rect(4, 5, 5, 6)} {
						pixels, err := RenderTile(t.Context(), source, region, TileOptions{Compose: options})
						if err != nil {
							t.Fatal(err)
						}
						for y := region.Min.Y; y < region.Max.Y; y++ {
							for x := region.Min.X; x < region.Max.X; x++ {
								if pixels.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
									t.Fatalf("edge source region differs at %d,%d", x, y)
								}
							}
						}
					}
					if sha256.Sum256(data) != before || reader.reads != 0 {
						t.Fatal("mixed-effect rendering changed source bytes or read the saved composite")
					}
					if shift.X == -24 {
						if source.Layers[1].Bounds.Max.X > 0 {
							t.Fatal("off-canvas fixture retained visible body pixels")
						}
						for _, layer := range flatten(complete.Layers) {
							if layer != nil && layer.Effects != nil {
								layer.Hidden = true
							}
						}
						cropped, err := psdcompose.Render(t.Context(), complete, options)
						if err != nil || bytes.Equal(cropped.Pix, full.Pix) {
							t.Fatalf("off-canvas source did not affect visible shadows: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestShadowOverlayScenePolicyAndPointwiseOverlayHalo(t *testing.T) {
	layer := &psdmodel.Layer{BlendMode: "normal", Effects: &psdeffects.Set{Enabled: true, EffectsVisible: true,
		Items: []psdeffects.Effect{{Kind: psdeffects.ColorOverlay, Enabled: true, Opacity: 1, Size: 10000, Distance: 10000}}}}
	if halo, err := tileEffectHalo(layer); err != nil || halo != 0 {
		t.Fatalf("pointwise color overlay requested spatial samples: %d, %v", halo, err)
	}
	engine := rgbTileRenderer{settings: TileOptions{Compose: psdcompose.Options{ApproximateDropShadow: true}}}
	if engine.approximateShadowSource(layer) {
		t.Fatal("an overlay-only set was classified as a shadow source")
	}
	layer.Effects.Items = append(layer.Effects.Items, psdeffects.Effect{Kind: psdeffects.DropShadow,
		Enabled: true, Opacity: .5})
	if !engine.approximateShadowSource(layer) {
		t.Fatal("ordinary Normal mixed effects did not select the shadow source window")
	}
	layer.Placed = &psdmodel.Placed{}
	if engine.approximateShadowSource(layer) {
		t.Fatal("mixed placed effects were admitted")
	}
	layer.Effects.Items = layer.Effects.Items[1:]
	if !engine.approximateShadowSource(layer) {
		t.Fatal("original pure placed-shadow policy was removed")
	}
	source := cachedOpen(t, shadowOverlaySceneSource(t, false, 1.5, image.Pt(-24, 10)))
	if _, err := Render(t.Context(), source); !cachedUnsupported(err) {
		t.Fatalf("mixed shadow rendering became implicit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	pixels, err := RenderTile(ctx, source, image.Rect(0, 0, 1, 1), engine.settings)
	if pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("mixed shadow rendering bypassed cancellation: %v", err)
	}
	engine.settings.MaxWorkingBytes = 1
	pixels, err = RenderTile(t.Context(), source, image.Rect(0, 0, 1, 1), engine.settings)
	if pixels != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("mixed shadow rendering bypassed memory limits: %v", err)
	}
}
