package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type compositeFixtureLayer struct {
	color   []byte
	mask    []byte
	blend   string
	divider uint32
	tags    []byte
	extraID int16
}

func compositeFixtureTag(key string, data []byte) []byte {
	result := binary.BigEndian.AppendUint32([]byte("8BIM"+key), uint32(len(data)))
	result = append(result, data...)
	if len(data)%2 != 0 {
		result = append(result, 0)
	}
	return result
}

func compositeFixtureLength(data []byte, version uint16, length int) []byte {
	if version == 2 {
		return binary.BigEndian.AppendUint64(data, uint64(length))
	}
	return binary.BigEndian.AppendUint32(data, uint32(length))
}

func compositeFixtureSample(depth uint16, value byte) []byte {
	switch depth {
	case 16:
		return []byte{0, value}
	case 32:
		return binary.BigEndian.AppendUint32(nil, math.Float32bits(float32(value)/255))
	}
	return []byte{value}
}

// compositeFixture creates one-pixel files independently of the production writer.
func compositeFixture(version, depth, channels uint16, layers []compositeFixtureLayer) []byte {
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, channels)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, depth)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = binary.BigEndian.AppendUint32(data, 0)
	count := int16(len(layers))
	if channels == 4 {
		count = -count
	}
	info := binary.BigEndian.AppendUint16(nil, uint16(count))
	var payload []byte
	for _, layer := range layers {
		var record []byte
		maximum := uint32(1)
		if layer.color == nil {
			maximum = 0
		}
		for _, coordinate := range []uint32{0, 0, maximum, maximum} {
			record = binary.BigEndian.AppendUint32(record, coordinate)
		}
		ids := []int16{}
		if layer.color != nil {
			ids = []int16{0, 1, 2, -1}
			if depth == 1 {
				ids = []int16{0}
			}
		}
		if layer.mask != nil {
			ids = append(ids, -2)
		}
		if layer.extraID != 0 {
			ids = append(ids, layer.extraID)
		}
		record = binary.BigEndian.AppendUint16(record, uint16(len(ids)))
		for component, id := range ids {
			record = binary.BigEndian.AppendUint16(record, uint16(id))
			plane := []byte{layer.color[min(component, 3)]}
			if id == -2 {
				plane = layer.mask
			}
			plane = compositeFixtureSample(depth, plane[0])
			record = compositeFixtureLength(record, version, 2+len(plane))
			payload = binary.BigEndian.AppendUint16(payload, 0)
			payload = append(payload, plane...)
		}
		blend := layer.blend
		if blend == "" {
			blend = "norm"
		}
		record = append(record, []byte("8BIM"+blend)...)
		record = append(record, 255, 0, 0, 0)
		var extra []byte
		if layer.mask != nil {
			maskLength := uint32(20)
			if layer.extraID == -3 {
				maskLength = 36
			}
			extra = binary.BigEndian.AppendUint32(extra, maskLength)
			for _, coordinate := range []uint32{0, 0, 1, 1} {
				extra = binary.BigEndian.AppendUint32(extra, coordinate)
			}
			extra = append(extra, 255, 0)
			if layer.extraID == -3 {
				extra = append(extra, 0, 255)
				for _, coordinate := range []uint32{0, 0, 1, 1} {
					extra = binary.BigEndian.AppendUint32(extra, coordinate)
				}
			} else {
				extra = append(extra, 0, 0)
			}
		} else {
			extra = binary.BigEndian.AppendUint32(extra, 0)
		}
		extra = binary.BigEndian.AppendUint32(extra, 0)
		extra = append(extra, 1, 'L', 0, 0)
		if layer.divider != 0 {
			divider := binary.BigEndian.AppendUint32(nil, layer.divider)
			if layer.divider != 3 {
				divider = append(divider, []byte("8BIM"+blend)...)
			}
			extra = append(extra, compositeFixtureTag("lsct", divider)...)
		}
		extra = append(extra, layer.tags...)
		record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
		record = append(record, extra...)
		info = append(info, record...)
	}
	info = append(info, payload...)
	if len(info)%2 != 0 {
		info = append(info, 0)
	}
	section := compositeFixtureLength(nil, version, len(info))
	section = append(section, info...)
	section = binary.BigEndian.AppendUint32(section, 0)
	data = compositeFixtureLength(data, version, len(section))
	data = append(data, section...)
	data = binary.BigEndian.AppendUint16(data, 0)
	for component := range int(channels) {
		value := byte(255)
		if component < 3 {
			value = byte(30 + component*40)
		}
		data = append(data, compositeFixtureSample(depth, value)...)
	}
	return data
}

func compositeEditor(t *testing.T, data []byte, options ...psdfile.LoadOptions) *Editor {
	t.Helper()
	source, err := psdfile.Parse(data, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	return editor
}

func compositeSave(t *testing.T, editor *Editor, options ...SaveOptions) *psdfile.Document {
	t.Helper()
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output, options...); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	return saved
}

func compositeExpect(t *testing.T, document *psdfile.Document, values []byte) {
	t.Helper()
	pixels, err := document.DecodeComposite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var actual []byte
	for _, plane := range pixels.Channels {
		actual = append(actual, plane.Data...)
	}
	if !bytes.Equal(actual, values) {
		t.Fatalf("saved planes = %v, want %v", actual, values)
	}
}

func TestCompositeVisibilityReplacementPreservesMask(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
				{color: []byte{255, 0, 0, 255}, mask: []byte{128}},
				{color: []byte{0, 255, 0, 255}},
			}))
			if err := editor.SetVisible(2, false); err != nil {
				t.Fatal(err)
			}
			saved := compositeSave(t, editor, SaveOptions{Compression: compression})
			mergedCompression := compression
			if mergedCompression >= 2 {
				mergedCompression = 1
			}
			if saved.Composite.Compression != mergedCompression {
				t.Fatalf("merged compression = %d, want %d", saved.Composite.Compression, mergedCompression)
			}
			compositeExpect(t, saved, []byte{255, 127, 127, 128})
			if saved.Header.Channels != 4 || !saved.GlobalAlpha || saved.Header.Version != version {
				t.Fatalf("transparency flag or format lost: %#v", saved.Header)
			}
			pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			copy(pixels.Pix, []byte{0, 0, 255, 255})
			if err := editor.ReplacePixels(1, pixels); err != nil {
				t.Fatal(err)
			}
			compositeExpect(t, compositeSave(t, editor), []byte{127, 127, 255, 128})
			if editor.source.Header.Channels != 3 || editor.source.Layers[1].Flags != 0 {
				t.Fatal("immutable source metadata changed")
			}
			original, err := editor.source.Decode(context.Background())
			if err != nil || original.Layers[0].Pixels.Pix[0] != 255 ||
				original.Layers[0].Mask.Pixels.Pix[0] != 128 {
				t.Fatalf("immutable source pixels changed: %v", err)
			}
		}
	}
}

func TestCompositeOpacityAndRawMaskOverlay(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, mask: []byte{255}},
	}))
	for id, value := range map[int16]byte{0: 20, -2: 128} {
		plane := psdfile.Plane{ID: id, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{value}}
		if err := editor.ReplaceChannel(1, id, plane); err != nil {
			t.Fatal(err)
		}
	}
	compositeExpect(t, compositeSave(t, editor), []byte{137, 127, 127, 128})
	if editor.roots[0].planes[0].Data[0] != 20 || editor.roots[0].planes[-2].Data[0] != 128 {
		t.Fatal("composite mutated channel overlays")
	}
	if err := editor.SetOpacity(1, 128); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{196, 191, 191, 64})
	if editor.source.Layers[0].Opacity != 255 {
		t.Fatal("composite changed source opacity")
	}
}

func TestCompositePhysicalIdentityPassGroupsAndNewRaster(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{divider: 3}, {color: []byte{255, 0, 0, 255}}, {divider: 1, blend: "pass"},
		{color: []byte{0, 0, 255, 255}},
	}))
	if err := editor.Move(4, 3, 0); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{255, 0, 0})
	group, err := editor.AddGroup(0, 1, "L", true)
	if err != nil {
		t.Fatal(err)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(pixels.Pix, []byte{0, 255, 0, 255})
	if _, err := editor.AddRaster(group, 0, "L", pixels, image.Point{}); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{0, 255, 0})
	if err := editor.SetVisible(group, false); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{255, 0, 0})
	if err := editor.SetOpacity(3, 128); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{255, 127, 127, 128})
	if err := editor.SetOpacity(3, 0); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{255, 255, 255, 0})
}

func TestCompositeExplicitRGBAWhiteMatteAndOwnedBuffers(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}},
	}))
	pixels := image.NewNRGBA(image.Rect(9, 8, 10, 9))
	copy(pixels.Pix, []byte{20, 80, 140, 128})
	saved := compositeSave(t, editor, SaveOptions{CompositeImage: pixels})
	compositeExpect(t, saved, []byte{137, 167, 197, 128})
	if !bytes.Equal(pixels.Pix, []byte{20, 80, 140, 128}) || editor.source.GlobalAlpha {
		t.Fatal("explicit preview or source was mutated")
	}
	for _, options := range []SaveOptions{
		{CompositeImage: &image.NRGBA{Rect: image.Rect(0, 0, 1, 1), Stride: 4, Pix: []byte{1}}},
		{CompositeImage: pixels, Composite: &psdfile.CompositePixels{}},
		{CompositeImage: image.NewNRGBA(image.Rect(0, 0, 2, 2))},
	} {
		var output bytes.Buffer
		if err := editor.Write(context.Background(), &output, options); !errors.Is(err, ErrInvalidEdit) || output.Len() != 0 {
			t.Fatalf("invalid preview produced output: %v, bytes=%d", err, output.Len())
		}
	}
}

func TestCompositeExplicitSourcePrecisionPreserved(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(2, 16, 4, []compositeFixtureLayer{
		{color: []byte{10, 20, 30, 255}},
	}))
	if err := editor.SetOpacity(1, 64); err != nil {
		t.Fatal(err)
	}
	planes, err := editor.source.DecodeComposite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	planes.Channels[0].Data = []byte{0x12, 0x34}
	saved := compositeSave(t, editor, SaveOptions{Composite: planes, Compression: 3})
	if saved.Composite.Compression != 0 {
		t.Fatal("high-precision explicit ZIP preview did not select Native-compatible Raw")
	}
	compositeExpect(t, saved, []byte{0x12, 0x34, 0, 70, 0, 110, 0, 255})
	if saved.Header.Depth != 16 || saved.Header.Channels != 4 || saved.Header.ColorMode != 3 {
		t.Fatal("source-precision preview changed color metadata")
	}
	planes.Channels[0].ID = 1
	var output bytes.Buffer
	err = editor.Write(context.Background(), &output, SaveOptions{Composite: planes})
	if !errors.Is(err, ErrInvalidEdit) || output.Len() != 0 {
		t.Fatalf("inconsistent source plane produced output: %v", err)
	}
	if err := editor.Write(context.Background(), &output); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("high depth automatically rendered: %v", err)
	}
}

func TestCompositeUnsupportedRenderingRequiresExplicitPreview(t *testing.T) {
	artboard, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "artboard"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range []compositeFixtureLayer{
		{color: []byte{255, 0, 0, 255}, blend: "????"},
		{color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag("knko", []byte{99, 0, 0, 0})},
		{color: []byte{255, 0, 0, 255}, extraID: 4},
		{color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag("artb", artboard)},
		{color: []byte{255, 0, 0, 255}, divider: 99},
	} {
		editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{layer}))
		if err := editor.SetOpacity(1, 128); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := editor.Write(context.Background(), &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
			t.Fatalf("unsupported layer produced preview: %v, bytes=%d", err, output.Len())
		}
		preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		copy(preview.Pix, []byte{100, 100, 100, 255})
		compositeExpect(t, compositeSave(t, editor, SaveOptions{CompositeImage: preview}), []byte{100, 100, 100})
	}
}

func TestCompositeMalformedArtboardCannotProduceAutomaticPreview(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, []compositeFixtureLayer{{
		color: []byte{255, 0, 0, 255}, tags: compositeFixtureTag("artb", nil),
	}}))
	if err := editor.SetOpacity(1, 128); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	var output bytes.Buffer
	if err := editor.Write(t.Context(), &output); !errors.Is(err, psdfile.ErrMalformed) || output.Len() != 0 {
		t.Fatalf("malformed artboard metadata produced an automatic preview: %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("failed artboard metadata decoding changed the edit state")
	}
}

func TestCompositeBudgetCancellationAndUnchangedRawPreview(t *testing.T) {
	data := compositeFixture(1, 8, 3, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
	editor := compositeEditor(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 12}})
	if err := editor.Rename(1, "renamed"); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{30, 70, 110})
	if err := editor.SetOpacity(1, 128); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); !errors.Is(err, psdfile.ErrLimit) || output.Len() != 0 {
		t.Fatalf("composite budget exceeded after output: %v, bytes=%d", err, output.Len())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.Write(ctx, &output); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("canceled save produced output: %v", err)
	}
	other := compositeEditor(t, data)
	if err := other.SetOpacity(1, 128); err != nil {
		t.Fatal(err)
	}
	err := other.Write(context.Background(), &output, SaveOptions{MaxOutputBytes: 3})
	if !errors.Is(err, psdfile.ErrLimit) || output.Len() != 0 {
		t.Fatalf("tiny output budget produced output: %v", err)
	}
}

func TestCompositeCachedPlacedRasterAllowsWarpButRejectsFilters(t *testing.T) {
	layer := &psdmodel.Layer{
		Bounds: image.Rect(0, 0, 1, 1), Pixels: image.NewNRGBA(image.Rect(0, 0, 1, 1)),
		BlendMode: "normal", Placed: &psdmodel.Placed{Warp: &psdmodel.Warp{Style: "warpArc"}},
	}
	copy(layer.Pixels.Pix, []byte{35, 80, 120, 255})
	document := &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer}}
	pixels, err := psdcompose.Render(context.Background(), document)
	if err != nil {
		t.Fatalf("cached smart-object pixels unnecessarily require reprojection: %v", err)
	}
	if !bytes.Equal(pixels.Pix, layer.Pixels.Pix) {
		t.Fatal("cached smart-object pixels were changed")
	}
	layer.Placed.Unsupported = []string{"smart filter"}
	if _, err := psdcompose.Render(context.Background(), document); !errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("unsupported smart filter was ignored: %v", err)
	}
}

func TestCompositeVirtualBackgroundKeepsSourceAndCanBecomeTransparent(t *testing.T) {
	editor := compositeEditor(t, compositeFixture(1, 8, 3, nil))
	if len(editor.roots) != 1 || editor.roots[0].sourceIndex != -2 {
		t.Fatal("merged-only RGB source has no virtual background")
	}
	node := editor.roots[0]
	first, err := editor.nodePixelsLocked(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	first.Pix[0] = 200
	second, err := editor.nodePixelsLocked(context.Background(), node)
	if err != nil || second.Pix[0] != 30 {
		t.Fatalf("virtual decode borrowed source samples: %v", err)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(pixels.Pix, []byte{255, 0, 0, 128})
	if _, err := editor.AddRaster(0, 1, "overlay", pixels, image.Point{}); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{143, 35, 55})
	if err := editor.SetVisible(node.id, false); err != nil {
		t.Fatal(err)
	}
	compositeExpect(t, compositeSave(t, editor), []byte{255, 127, 127, 128})
	if len(editor.source.Layers) != 0 || editor.source.Header.Channels != 3 {
		t.Fatal("saving virtual background mutated source layer structure")
	}
}

func TestCompositeExtraMergedAlphaIsNotAssumedTransparency(t *testing.T) {
	data := compositeFixture(1, 8, 4, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	countOffset := source.LayerSets[source.ActiveLayerSet].Span.Offset
	_ = source.Close()
	binary.BigEndian.PutUint16(data[countOffset:], 1)
	editor := compositeEditor(t, data)
	if err := editor.SetOpacity(1, 128); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); !errors.Is(err, ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("extra alpha channel silently interpreted as transparency: %v", err)
	}
}

func TestCompositeRGB8ZIPRequestsUseRLESavedImageAndRetainLayerZIP(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, requested := range []uint16{2, 3} {
			for _, saveFile := range []bool{false, true} {
				t.Run(fmt.Sprintf("version%d_compression%d_file%t", version, requested, saveFile), func(t *testing.T) {
					editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
						{color: []byte{255, 0, 0, 255}},
					}))
					pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
					copy(pixels.Pix, []byte{20, 80, 140, 128})
					if err := editor.ReplacePixels(1, pixels); err != nil {
						t.Fatal(err)
					}
					option := SaveOptions{Compression: requested}
					var saved *psdfile.Document
					if saveFile {
						filename := filepath.Join(t.TempDir(), "saved.psd")
						if version == 2 {
							filename = filepath.Join(t.TempDir(), "saved.psb")
						}
						if err := editor.Save(context.Background(), filename, option); err != nil {
							t.Fatal(err)
						}
						var err error
						saved, err = psdfile.Open(filename)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = saved.Close() })
					} else {
						saved = compositeSave(t, editor, option)
					}
					if saved.Header.Version != version || saved.Composite.Compression != 1 {
						t.Fatalf("saved format=%d merged compression=%d", saved.Header.Version, saved.Composite.Compression)
					}
					for _, channel := range saved.Layers[0].Channels {
						if channel.Compression != requested {
							t.Fatalf("layer channel %d compression=%d, want %d", channel.ID, channel.Compression, requested)
						}
					}
					compositeExpect(t, saved, []byte{137, 167, 197, 128})
				})
			}
		}
	}
}

func TestCompositeRGB8ExplicitZIPRequestsAlsoUseRLE(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, requested := range []uint16{2, 3} {
			editor := compositeEditor(t, compositeFixture(version, 8, 3, []compositeFixtureLayer{
				{color: []byte{255, 0, 0, 255}},
			}))
			planes, err := editor.source.DecodeComposite(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			saved := compositeSave(t, editor, SaveOptions{Composite: planes, Compression: requested})
			if saved.Composite.Compression != 1 {
				t.Fatalf("explicit RGB8 source planes use merged compression=%d", saved.Composite.Compression)
			}
			pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			copy(pixels.Pix, []byte{30, 70, 110, 255})
			saved = compositeSave(t, editor, SaveOptions{CompositeImage: pixels, Compression: requested})
			if saved.Composite.Compression != 1 {
				t.Fatalf("explicit RGBA preview uses merged compression=%d", saved.Composite.Compression)
			}
		}
	}
}

func TestCompositeMetadataEditsPreserveOriginalZIPBytes(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := compositeFixture(version, 8, 3, []compositeFixtureLayer{{color: []byte{255, 0, 0, 255}}})
		source, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		planes, err := source.DecodeComposite(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := psdfile.EncodeComposite(context.Background(), *planes, 3, version)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data[:source.Composite.Span.Offset], encoded...)
		_ = source.Close()
		editor := compositeEditor(t, data)
		if err := editor.Rename(1, "metadata only"); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor, SaveOptions{Compression: 3})
		if saved.Composite.Compression != 3 {
			t.Fatal("unchanged merged compression was normalized")
		}
		actual, err := saved.ReadSpan(saved.Composite.Span)
		if err != nil || !bytes.Equal(actual, encoded) {
			t.Fatalf("unchanged merged bytes were rewritten: %v", err)
		}
		compositeExpect(t, saved, []byte{30, 70, 110})
	}
}

func TestCompositeSourcePrecisionZIPRequestsUseNativeMergedCompression(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{1, 8, 16, 32} {
			for _, requested := range []uint16{0, 1, 2, 3} {
				t.Run(fmt.Sprintf("version%d_depth%d_compression%d", version, depth, requested), func(t *testing.T) {
					channels := uint16(4)
					if depth == 1 {
						channels = 1
					}
					data := compositeFixture(version, depth, channels, []compositeFixtureLayer{
						{color: []byte{10, 20, 30, 255}},
					})
					if depth == 1 {
						binary.BigEndian.PutUint16(data[24:26], 0)
					}
					if depth == 8 {
						// Exercise a non-RGB color mode through explicit source planes.
						binary.BigEndian.PutUint16(data[24:26], 1)
					}
					editor := compositeEditor(t, data)
					planes, err := editor.source.DecodeComposite(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					layerPlane, err := editor.source.DecodeChannel(context.Background(), 0, 0)
					if err != nil {
						t.Fatal(err)
					}
					if err := editor.ReplaceChannel(1, 0, *layerPlane); err != nil {
						t.Fatal(err)
					}
					if requested == 3 && depth == 1 {
						// Packed-bit layer prediction remains unsupported independently of preview policy.
						var output bytes.Buffer
						err := editor.Write(context.Background(), &output, SaveOptions{Composite: planes, Compression: requested})
						if !errors.Is(err, psdfile.ErrUnsupported) || output.Len() != 0 {
							t.Fatalf("packed-bit layer prediction unexpectedly succeeded: %v", err)
						}
						return
					}
					saved := compositeSave(t, editor, SaveOptions{Composite: planes, Compression: requested})
					mergedCompression := requested
					if requested >= 2 {
						mergedCompression = 0
						if depth <= 8 {
							mergedCompression = 1
						}
					}
					if saved.Composite.Compression != mergedCompression {
						t.Fatalf("merged compression=%d, want %d", saved.Composite.Compression, mergedCompression)
					}
					layerCompression := requested
					if depth == 32 && requested == 2 {
						// Photoshop interprets 32-bit ZIP layer data as ZIP prediction.
						layerCompression = 3
					}
					if saved.Layers[0].Channels[0].Compression != layerCompression {
						t.Fatalf(
							"layer compression=%d, want %d", saved.Layers[0].Channels[0].Compression, layerCompression,
						)
					}
					decoded, err := saved.DecodeComposite(context.Background())
					if err != nil || decoded.Depth != depth || decoded.ColorMode != planes.ColorMode {
						t.Fatalf("saved precision or mode changed: %v", err)
					}
					for index, plane := range planes.Channels {
						if !bytes.Equal(decoded.Channels[index].Data, plane.Data) {
							t.Fatal("Native merged policy changed original source samples")
						}
					}
				})
			}
		}
	}
}
