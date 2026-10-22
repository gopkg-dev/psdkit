package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

func cachedTestID(value string) []byte {
	length := len(value)
	if length == 4 {
		length = 0
	}
	return append(artboardTestU32(uint32(length)), value...)
}

func cachedTestEnum(class, value string) []byte {
	return append(cachedTestID(class), cachedTestID(value)...)
}

func cachedTestUnicode(value string) []byte {
	units := utf16.Encode([]rune(value))
	data := artboardTestU32(uint32(len(units)))
	for _, unit := range units {
		data = binary.BigEndian.AppendUint16(data, unit)
	}
	return data
}

func cachedTestUnit(unit string, value float64) []byte {
	return append([]byte(unit), artboardTestDouble(value)...)
}

func cachedTestGradient(dither bool) []byte {
	color := artboardTestDescriptor("RGBC", artboardTestEntry{"Rd  ", "doub", artboardTestDouble(20)},
		artboardTestEntry{"Grn ", "doub", artboardTestDouble(80)},
		artboardTestEntry{"Bl  ", "doub", artboardTestDouble(200)})
	colors, opacities := artboardTestU32(2), artboardTestU32(2)
	for _, location := range []uint32{0, 4096} {
		common := []artboardTestEntry{{"Lctn", "long", artboardTestU32(location)},
			{"Mdpn", "long", artboardTestU32(50)}}
		colors = append(colors, "Objc"...)
		colors = append(colors, artboardTestDescriptor("Clrt", append(common,
			artboardTestEntry{"Clr ", "Objc", color},
			artboardTestEntry{"Type", "enum", cachedTestEnum("Clry", "UsrS")})...)...)
		opacities = append(opacities, "Objc"...)
		opacities = append(opacities, artboardTestDescriptor("TrnS", append(common,
			artboardTestEntry{"Opct", "UntF", cachedTestUnit("#Prc", 100)})...)...)
	}
	gradient := artboardTestDescriptor("Grdn", artboardTestEntry{"GrdF", "enum", cachedTestEnum("GrdF", "CstS")},
		artboardTestEntry{"Intr", "doub", artboardTestDouble(4096)},
		artboardTestEntry{"Clrs", "VlLs", colors}, artboardTestEntry{"Trns", "VlLs", opacities})
	ditherByte := byte(0)
	if dither {
		ditherByte = 1
	}
	return append(artboardTestU32(16), artboardTestDescriptor("null",
		artboardTestEntry{"Grad", "Objc", gradient},
		artboardTestEntry{"Type", "enum", cachedTestEnum("GrdT", "Lnr ")},
		artboardTestEntry{"Dthr", "bool", []byte{ditherByte}})...)
}

func cachedTestPattern(scale, angle float64) []byte {
	pattern := artboardTestDescriptor("Ptrn", artboardTestEntry{"Idnt", "TEXT", cachedTestUnicode("fixture-pattern")})
	return append(artboardTestU32(16), artboardTestDescriptor("null",
		artboardTestEntry{"Ptrn", "Objc", pattern},
		artboardTestEntry{"Scl ", "UntF", cachedTestUnit("#Prc", scale)},
		artboardTestEntry{"Angl", "UntF", cachedTestUnit("#Ang", angle)},
		artboardTestEntry{"Lnkd", "bool", []byte{1}})...)
}

// cachedTestSource encodes layer caches and a deliberately unrelated green merged preview independently.
func cachedTestSource(tags []byte, ids ...int16) []byte {
	if ids == nil {
		ids = []int16{0, 1, 2, -1}
	}
	var record, pixels bytes.Buffer
	_ = binary.Write(&record, binary.BigEndian, int16(-1))
	for _, value := range []int32{0, 0, 1, 2} {
		_ = binary.Write(&record, binary.BigEndian, value)
	}
	_ = binary.Write(&record, binary.BigEndian, uint16(len(ids)))
	values := map[int16][]byte{0: {201, 17}, 1: {21, 31}, 2: {39, 77}, -1: {255, 128}, -2: {255, 255}}
	for _, id := range ids {
		_ = binary.Write(&record, binary.BigEndian, id)
		_ = binary.Write(&record, binary.BigEndian, uint32(4))
		pixels.Write([]byte{0, 0})
		pixels.Write(values[id])
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	extra := append(make([]byte, 8), 1, 'L', 0, 0)
	extra = append(extra, tags...)
	_ = binary.Write(&record, binary.BigEndian, uint32(len(extra)))
	record.Write(extra)
	record.Write(pixels.Bytes())
	if record.Len()%2 != 0 {
		record.WriteByte(0)
	}
	section := append(artboardTestU32(uint32(record.Len())), record.Bytes()...)
	section = append(section, 0, 0, 0, 0)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = append(data, artboardTestU32(1)...)
	data = append(data, artboardTestU32(2)...)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	data = append(data, artboardTestU32(uint32(len(section)))...)
	data = append(data, section...)
	return append(data, 0, 0, 0, 0, 255, 255, 0, 0, 255, 255)
}

func cachedOpen(t *testing.T, data []byte) *psdfile.Document {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source
}

func cachedUnsupported(err error) bool {
	return errors.Is(err, psdfile.ErrUnsupported) || errors.Is(err, psdcompose.ErrUnsupported)
}

func TestCachedSpecialLayersAlwaysSelectsSavedGradientInsteadOfLiveSource(t *testing.T) {
	source := cachedOpen(t, cachedTestSource(artboardTestBlock("GdFl", cachedTestGradient(false))))
	live, err := Render(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(live.Pix, []byte{20, 80, 200, 255, 20, 80, 200, 255}) {
		t.Fatal("default gradient did not use its supported live descriptor")
	}
	actual, err := Render(nil, source, Options{CachedSpecialLayers: true})
	if err != nil || !bytes.Equal(actual.Pix, []byte{201, 21, 39, 255, 17, 31, 77, 128}) {
		t.Fatalf("explicit policy did not choose actual saved layer pixels: %v", err)
	}
	preview, err := source.DecodeComposite(nil)
	if err != nil || preview.Channels[1].Data[0] != 255 || actual.Pix[1] == 255 {
		t.Fatal("cached policy used the poisoned merged footer")
	}
	again, err := Render(nil, source)
	if err != nil || !bytes.Equal(again.Pix, live.Pix) {
		t.Fatal("cache selection changed the source's future live semantics")
	}
}

func TestCachedSpecialLayersUnsupportedDescriptorsUseSavedRasters(t *testing.T) {
	for _, sample := range []struct {
		key     string
		payload []byte
	}{
		{"GdFl", cachedTestGradient(true)}, {"GdFl", artboardTestU32(17)},
		{"PtFl", cachedTestPattern(50, 0)}, {"PtFl", cachedTestPattern(100, 7)},
		{"PtFl", artboardTestU32(17)}, {"TySh", []byte{0, 0}}, {"vstk", []byte{0, 0}},
	} {
		data := cachedTestSource(artboardTestBlock(sample.key, sample.payload))
		digest := sha256.Sum256(data)
		source := cachedOpen(t, data)
		if _, err := Render(nil, source); !cachedUnsupported(err) {
			t.Fatalf("default unsupported %s unexpectedly rendered: %v", sample.key, err)
		}
		actual, err := Render(nil, source, Options{CachedSpecialLayers: true})
		if err != nil || !bytes.Equal(actual.Pix, []byte{201, 21, 39, 255, 17, 31, 77, 128}) {
			t.Fatalf("explicit cached %s failed or used another source: %v", sample.key, err)
		}
		if sha256.Sum256(data) != digest {
			t.Fatal("cached selection changed original descriptor or sample bytes")
		}
		_ = source.Close()
		if actual.Pix[0] != 201 {
			t.Fatal("owned cached scene pixels depended on the open source")
		}
	}
}

func TestCachedSpecialLayersPreserveOtherBlockersAndRejectMissingCaches(t *testing.T) {
	gradient := artboardTestBlock("GdFl", cachedTestGradient(true))
	filtered := append(bytes.Clone(gradient), artboardTestBlock("FEid", []byte{1, 2, 3, 4})...)
	_, err := Render(nil, cachedOpen(t, cachedTestSource(filtered)), Options{CachedSpecialLayers: true})
	if !cachedUnsupported(err) {
		t.Fatalf("cached gradient hid a smart-filter blocker: %v", err)
	}
	for _, ids := range [][]int16{{0, 1, -1}, {0, 0, 1, 2}, {-1}} {
		_, err := Render(nil, cachedOpen(t, cachedTestSource(gradient, ids...)), Options{CachedSpecialLayers: true})
		if !cachedUnsupported(err) {
			t.Fatalf("missing or ambiguous saved RGB channels became a cache: %v", err)
		}
	}
	layer := &psdmodel.Layer{Pixels: image.NewNRGBA(image.Rect(0, 0, 2, 1)),
		Gradient: &psdgradient.Fill{}, Pattern: &psdpattern.Fill{}, PatternTile: image.NewNRGBA(image.Rect(0, 0, 1, 1)),
		Unsupported: []string{"unsupported gradient descriptor", "unsupported pattern fill descriptor", "other blocker"}}
	record := psdfile.LayerRecord{Bounds: image.Rect(0, 0, 2, 1),
		Channels:       []psdfile.ChannelRecord{{ID: 0}, {ID: 1}, {ID: 2}},
		AdditionalInfo: []psdfile.AdditionalBlock{{Key: "GdFl"}}}
	if err := selectCachedSpecial(context.Background(), record, layer, false); err != nil {
		t.Fatal(err)
	}
	if layer.Gradient != nil || layer.Pattern == nil || layer.PatternTile == nil ||
		!slices.Equal(layer.Unsupported, []string{"unsupported pattern fill descriptor", "other blocker"}) {
		t.Fatal("cache selection removed another source kind, tile or unsupported reason")
	}
}

func TestCachedSpecialLayersContextAndBudgetRemainStrict(t *testing.T) {
	source := cachedOpen(t, cachedTestSource(artboardTestBlock("PtFl", cachedTestPattern(50, 7))))
	_, err := Render(nil, source, Options{CachedSpecialLayers: true, MaxWorkingBytes: 32})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("cached layer bypassed total working budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, source, Options{CachedSpecialLayers: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached layer bypassed cancellation: %v", err)
	}
}

func TestCachedSpecialLayersExplicitTextPolicyNeverFallsBack(t *testing.T) {
	source := cachedOpen(t, cachedTestSource(artboardTestBlock("TySh", []byte{0, 0})))
	if _, err := Render(nil, source, Options{CachedSpecialLayers: true}); err != nil {
		t.Fatalf("the saved text raster was unavailable: %v", err)
	}
	_, err := Render(nil, source, Options{CachedSpecialLayers: true, ApproximateText: &TextRenderOptions{}})
	if err == nil {
		t.Fatal("explicit live text with invalid metadata silently fell back to saved pixels")
	}
}

func TestCachedSpecialLayersNativeUnsupportedPatternAngle(t *testing.T) {
	if os.Getenv("PSDKIT_CACHED_SPECIAL_NATIVE") != "1" {
		t.Skip("controlled native cache files are optional")
	}
	data, err := os.ReadFile(filepath.Join("..", "output", "pattern-native", "rgb-grid-2x2.psd"))
	if err != nil {
		t.Fatal(err)
	}
	original := cachedOpen(t, data)
	baseline, err := Render(nil, original, Options{CachedSpecialLayers: true})
	if err != nil {
		t.Fatal(err)
	}
	var modified []byte
	patched := false
	for _, layer := range original.Layers {
		for _, block := range layer.AdditionalInfo {
			if block.Key != "PtFl" {
				continue
			}
			fill, err := original.DecodePatternFill(nil, block)
			if err != nil {
				t.Fatal(err)
			}
			fill.Descriptor.Entries = append(fill.Descriptor.Entries,
				psdfile.DescriptorEntry{Key: "Scl ", Type: "UntF",
					Value: psdfile.DescriptorUnit{Unit: "#Prc", Width: 64, Number: float64(50)}},
				psdfile.DescriptorEntry{Key: "Angl", Type: "UntF",
					Value: psdfile.DescriptorUnit{Unit: "#Ang", Width: 64, Number: float64(7)}})
			payload, err := original.EncodeDescriptor(nil, fill.Descriptor, true)
			if err != nil {
				t.Fatal(err)
			}
			updated := artboardTestBlock("PtFl", payload)
			modified = append(bytes.Clone(data[:block.Span.Offset]), updated...)
			modified = append(modified, data[block.Span.Offset+block.Span.Length:]...)
			delta := len(updated) - int(block.Span.Length)
			binary.BigEndian.PutUint32(modified[layer.Extra.Offset-4:], uint32(int(layer.Extra.Length)+delta))
			binary.BigEndian.PutUint32(modified[original.LayerInfo.Offset:], uint32(int(original.LayerInfo.Length)-4+delta))
			binary.BigEndian.PutUint32(modified[original.LayerMaskSection.Offset:],
				uint32(int(original.LayerMaskSection.Length)-4+delta))
			patched = true
		}
	}
	if !patched {
		t.Fatal("controlled pattern fixture does not expose a pattern descriptor")
	}
	changed := cachedOpen(t, modified)
	if _, err := Render(nil, changed); !cachedUnsupported(err) {
		t.Fatalf("unverified native pattern angle silently rendered live: %v", err)
	}
	cached, err := Render(nil, changed, Options{CachedSpecialLayers: true})
	if err != nil || !bytes.Equal(cached.Pix, baseline.Pix) {
		t.Fatalf("explicit native cache depended on unverified scale/angle metadata: %v", err)
	}
}

func TestCachedSpecialLayersNativeCachesIgnoreMergedFooter(t *testing.T) {
	if os.Getenv("PSDKIT_CACHED_SPECIAL_NATIVE") != "1" {
		t.Skip("controlled native cache files are optional")
	}
	var paths []string
	for _, directory := range []string{"gradient-native", "pattern-native"} {
		items, err := filepath.Glob(filepath.Join("..", "output", directory, "*.psd"))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if filepath.Base(item) == "rgb-grid-2x2-tile.psd" || filepath.Base(item) == "alpha-grid-4x3-tile.psd" {
				continue
			}
			paths = append(paths, item)
		}
	}
	if len(paths) < 12 {
		t.Fatal("the native cache matrix is incomplete")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := cachedOpen(t, data)
			expectedCache, err := cachedNativeSourceExpectation(source)
			if err != nil {
				t.Fatal(err)
			}
			area := int(source.Header.Width) * int(source.Header.Height)
			preview := psdfile.CompositePixels{Width: int(source.Header.Width), Height: int(source.Header.Height),
				Depth: 8, ColorMode: 3}
			for index := range int(source.Header.Channels) {
				values := make([]byte, area)
				if index == 1 || index >= 3 {
					for offset := range values {
						values[offset] = 255
					}
				}
				preview.Channels = append(preview.Channels, psdfile.Plane{ID: int16(index), Width: preview.Width,
					Height: preview.Height, Depth: 8, RowBytes: preview.Width, Data: values})
			}
			footer, err := psdfile.EncodeComposite(nil, preview, 0, source.Header.Version)
			if err != nil {
				t.Fatal(err)
			}
			poisoned := append(bytes.Clone(data[:source.Composite.Span.Offset]), footer...)
			actual, err := Render(nil, cachedOpen(t, poisoned), Options{CachedSpecialLayers: true})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual.Pix, expectedCache.Pix) || actual.Rect != expectedCache.Rect {
				t.Fatal("cached scene did not use independently decoded layer channels after footer poisoning")
			}
			file, err := os.Open(path[:len(path)-4] + ".png")
			if err != nil {
				t.Fatal(err)
			}
			expected, err := png.Decode(file)
			closeErr := file.Close()
			if err != nil || closeErr != nil || actual.Bounds() != expected.Bounds() {
				t.Fatalf("native PNG geometry/read: %v/%v", err, closeErr)
			}
			maximum := 0
			for row := range actual.Rect.Dy() {
				for column := range actual.Rect.Dx() {
					wanted := color.NRGBAModel.Convert(expected.At(column, row)).(color.NRGBA)
					got := actual.NRGBAAt(column, row)
					for _, pair := range [][2]byte{{got.R, wanted.R}, {got.G, wanted.G},
						{got.B, wanted.B}, {got.A, wanted.A}} {
						difference := int(pair[0]) - int(pair[1])
						if difference < 0 {
							difference = -difference
						}
						maximum = max(maximum, difference)
					}
				}
			}
			t.Logf("saved cache vs native live PNG: max_difference=%d; source-cache contract matched exactly", maximum)
		})
	}
}

func cachedNativeSourceExpectation(source *psdfile.Document) (*image.NRGBA, error) {
	snapshot, err := source.Decode(nil)
	if err != nil {
		return nil, err
	}
	snapshot.Composite = nil
	for index, layer := range flatten(snapshot.Layers) {
		if layer == nil {
			continue
		}
		gradient, pattern := false, false
		for _, block := range source.Layers[index].AdditionalInfo {
			gradient = gradient || block.Key == "GdFl"
			pattern = pattern || block.Key == "PtFl"
		}
		if !gradient && !pattern {
			continue
		}
		pixels := image.NewNRGBA(image.Rect(0, 0, layer.Bounds.Dx(), layer.Bounds.Dy()))
		for offset := 3; offset < len(pixels.Pix); offset += 4 {
			pixels.Pix[offset] = 255
		}
		for channelIndex, channel := range source.Layers[index].Channels {
			component := int(channel.ID)
			if channel.ID == -1 {
				component = 3
			}
			if component < 0 || component > 3 {
				continue
			}
			plane, err := source.DecodeChannel(nil, index, channelIndex)
			if err != nil {
				return nil, err
			}
			for offset, value := range plane.Data {
				pixels.Pix[offset*4+component] = value
			}
		}
		layer.Pixels = pixels
		if gradient {
			layer.Gradient = nil
		}
		if pattern {
			layer.Pattern, layer.PatternTile = nil, nil
		}
		var reasons []string
		for _, reason := range layer.Unsupported {
			if gradient && reason == "unsupported gradient descriptor" ||
				pattern && reason == "unsupported pattern fill descriptor" {
				continue
			}
			reasons = append(reasons, reason)
		}
		layer.Unsupported = reasons
	}
	return psdcompose.Render(nil, snapshot)
}
