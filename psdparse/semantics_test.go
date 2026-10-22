package psdparse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type semanticsBlock struct {
	key  string
	data []byte
}

// semanticsDocument builds raw PSD/PSB records without using production writer or codec functions.
func semanticsDocument(version uint16, maskData, ranges []byte, blocks ...semanticsBlock) []byte {
	channels := [][]byte{
		{0, 0, 20, 40, 60, 80}, {0, 0, 10, 30, 50, 70},
		{0, 0, 1, 2, 3, 4}, {0, 0, 255, 128, 64, 0},
	}
	ids := []uint16{0, 1, 2, 65535}
	if len(maskData) != 0 {
		channels = append(channels, []byte{0, 0, 0, 80, 160, 255})
		ids = append(ids, 65534)
	}
	record := &psbWriter{}
	for _, edge := range []uint32{20, 10, 22, 12} {
		record.u32(edge)
	}
	record.u16(uint16(len(channels)))
	for index, channelData := range channels {
		record.u16(ids[index])
		record.length(version, len(channelData))
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	extra := &psbWriter{}
	extra.block(maskData)
	extra.block(ranges)
	extra.Write([]byte{1, 'S', 0, 0})
	for _, block := range blocks {
		extra.WriteString("8BIM" + block.key)
		extra.block(block.data)
		if len(block.data)%2 != 0 {
			extra.u8(0)
		}
	}
	record.block(extra.Bytes())
	info := &psbWriter{}
	info.u16(1)
	info.Write(record.Bytes())
	for _, channelData := range channels {
		info.Write(channelData)
	}
	if info.Len()%2 != 0 {
		info.u8(0)
	}
	layers := &psbWriter{}
	layers.length(version, info.Len())
	layers.Write(info.Bytes())
	layers.u32(0)
	file := &psbWriter{}
	file.WriteString("8BPS")
	file.u16(version)
	file.Write(make([]byte, 6))
	file.u16(3)
	file.u32(32)
	file.u32(32)
	file.u16(8)
	file.u16(3)
	file.u32(0)
	file.u32(0)
	file.length(version, layers.Len())
	file.Write(layers.Bytes())
	file.u16(0)
	file.Write(make([]byte, 32*32*3))
	return file.Bytes()
}

func semanticsMask(flags uint8, parameters uint8, density uint8, feather float64) []byte {
	w := &psbWriter{}
	for _, edge := range []int32{-2, -1, 0, 1} {
		w.u32(uint32(edge))
	}
	w.u8(128)
	w.u8(flags)
	if flags&16 != 0 {
		w.u8(parameters)
		if parameters&1 != 0 {
			w.u8(density)
		}
		if parameters&2 != 0 {
			w.u64(math.Float64bits(feather))
		}
		if parameters&4 != 0 {
			w.u8(255)
		}
		if parameters&8 != 0 {
			w.u64(0)
		}
		w.u8(3)
		w.u8(255)
		for _, edge := range []int32{-2, -1, 0, 1} {
			w.u32(uint32(edge))
		}
	} else {
		w.Write([]byte{0, 0})
	}
	return w.Bytes()
}

func TestBlendRangesTypedPSDAndPSB(t *testing.T) {
	ranges := []byte{
		16, 64, 128, 240, 0, 0, 255, 255,
		0, 0, 255, 255, 32, 64, 200, 220,
		100, 200, 80, 160, 0, 0, 255, 255,
		0, 0, 255, 255, 0, 0, 255, 255,
	}
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			document, err := ParseBytes(semanticsDocument(version, nil, ranges))
			if err != nil {
				t.Fatal(err)
			}
			layer := document.Layers[0]
			if len(layer.BlendRanges) != 4 {
				t.Fatalf("Blend If components=%d", len(layer.BlendRanges))
			}
			if layer.BlendRanges[0].Source != (psdmodel.BlendSlider{
				BlackLow: 16, BlackHigh: 64, WhiteLow: 128, WhiteHigh: 240,
			}) || layer.BlendRanges[1].Destination != (psdmodel.BlendSlider{
				BlackLow: 32, BlackHigh: 64, WhiteLow: 200, WhiteHigh: 220,
			}) || layer.BlendRanges[2].Source != (psdmodel.BlendSlider{
				BlackLow: 100, BlackHigh: 200, WhiteLow: 80, WhiteHigh: 160,
			}) {
				t.Fatalf("Blend If component order or split endpoints lost: %+v", layer.BlendRanges)
			}
			if len(layer.Unsupported) != 0 || layer.BlendClipped != nil {
				t.Fatalf("supported Blend If or absent advanced defaults changed: %+v", layer)
			}
		})
	}
	defaultRanges := []byte{0, 0, 255, 255, 0, 0, 255, 255}
	for _, ranges := range [][]byte{nil, defaultRanges, bytes.Repeat(defaultRanges, 5)} {
		document, err := ParseBytes(semanticsDocument(1, nil, ranges))
		if err != nil || len(document.Layers[0].Unsupported) != 0 {
			t.Fatalf("default Blend If rejected: %v", err)
		}
		if len(document.Layers[0].BlendRanges) != len(ranges)/8 {
			t.Fatal("default extra-channel blending range metadata was dropped")
		}
	}
}

func TestBlendRangesRejectMalformedStructure(t *testing.T) {
	for _, ranges := range [][]byte{
		{0, 0, 255, 255}, {0, 0, 255, 255, 0, 0, 255},
		{80, 40, 255, 255, 0, 0, 255, 255},
		{0, 0, 255, 255, 0, 0, 240, 220}, make([]byte, 58*8),
	} {
		if _, err := ParseBytes(semanticsDocument(1, nil, ranges)); err == nil {
			t.Fatalf("malformed Blend If accepted: %x", ranges)
		}
	}
}

func TestMaskRelativeInvertDensityPSDAndPSB(t *testing.T) {
	maskData := semanticsMask(1|4|16, 15, 96, 0)
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			document, err := ParseBytes(semanticsDocument(version, maskData, nil))
			if err != nil {
				t.Fatal(err)
			}
			mask := document.Layers[0].Mask
			if mask.Bounds != image.Rect(-1, -2, 1, 0) || !mask.Relative || !mask.Invert || mask.Disabled {
				t.Fatalf("mask coordinate/flag semantics lost: %+v", mask)
			}
			if mask.Density == nil || *mask.Density != 96 || mask.Feather == nil || *mask.Feather != 0 {
				t.Fatalf("mask parameters lost: %+v", mask)
			}
			if *mask.DefaultColor != 128 || len(mask.Unsupported) != 0 ||
				!bytes.Equal(mask.Pixels.Pix, []byte{
					0, 0, 0, 255, 80, 80, 80, 255, 160, 160, 160, 255, 255, 255, 255, 255,
				}) {
				t.Fatalf("density/invert must retain the raw mask pixels and default: %+v", mask)
			}
		})
	}
	document, err := ParseBytes(semanticsDocument(1, semanticsMask(2, 0, 0, 0), nil))
	if err != nil {
		t.Fatal(err)
	}
	mask := document.Layers[0].Mask
	if mask.Bounds != image.Rect(-1, -2, 1, 0) || mask.Relative || mask.Invert || !mask.Disabled ||
		mask.Density != nil || mask.Feather != nil {
		t.Fatalf("absolute/default mask semantics changed: %+v", mask)
	}
}

func TestMaskFeatherRetainsLiveRadius(t *testing.T) {
	document, err := ParseBytes(semanticsDocument(1, semanticsMask(16, 3, 180, 1.5), nil))
	if err != nil {
		t.Fatal(err)
	}
	mask := document.Layers[0].Mask
	if mask.Feather == nil || *mask.Feather != 1.5 || len(mask.Unsupported) != 0 {
		t.Fatalf("nonzero feather was silently ignored: %+v", mask)
	}
	for _, feather := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := ParseBytes(semanticsDocument(1, semanticsMask(16, 2, 0, feather), nil)); err == nil {
			t.Fatalf("invalid feather accepted: %v", feather)
		}
	}
	maskData := semanticsMask(16, 3, 255, 0)
	if _, err := ParseBytes(semanticsDocument(1, maskData[:19], nil)); err == nil {
		t.Fatal("truncated mask density/feather accepted")
	}
}

func TestAdvancedBlendFlagsExplicitAndDefaults(t *testing.T) {
	blocks := []semanticsBlock{
		{"clbl", []byte{0, 0, 0, 0}}, {"infx", []byte{1, 0, 0, 0}},
		{"lmgm", []byte{1, 0, 0, 0}}, {"vmgm", []byte{1, 0, 0, 0}}, {"tsly", []byte{1, 0, 0, 0}},
	}
	document, err := ParseBytes(semanticsDocument(1, nil, nil, blocks...))
	if err != nil {
		t.Fatal(err)
	}
	layer := document.Layers[0]
	if layer.BlendClipped == nil || *layer.BlendClipped || layer.BlendInterior == nil || !*layer.BlendInterior ||
		layer.MaskAsGlobal == nil || !*layer.MaskAsGlobal || layer.VectorMaskAsGlobal == nil ||
		!*layer.VectorMaskAsGlobal || layer.TransparencyShapes == nil || !*layer.TransparencyShapes {
		t.Fatalf("advanced blending flags lost: %+v", layer)
	}
	if len(layer.Unsupported) != 0 {
		t.Fatalf("interior/global mask flags without effects should retain their no-effect behavior: %+v", layer)
	}
	document, err = ParseBytes(semanticsDocument(1, nil, nil, semanticsBlock{"tsly", []byte{0, 0, 0, 0}}))
	if err != nil {
		t.Fatal(err)
	}
	if value := document.Layers[0].TransparencyShapes; value == nil || *value || len(document.Layers[0].Unsupported) != 0 {
		t.Fatal("disabled transparency-shapes flag was not retained for the compositor")
	}
	for _, key := range []string{"clbl", "infx", "lmgm", "vmgm", "tsly"} {
		for _, data := range [][]byte{nil, {1}, {1, 0, 0}, {2, 0, 0, 0}, {1, 0, 0, 0, 0}} {
			if _, err := ParseBytes(semanticsDocument(1, nil, nil, semanticsBlock{key, data})); err == nil {
				t.Fatalf("invalid %s flag accepted: %x", key, data)
			}
		}
	}
}

func TestGroupMarkerAndUnknownDivider(t *testing.T) {
	for _, divider := range []uint32{0, 1, 2, 3, 99} {
		data := make([]byte, 4)
		binary.BigEndian.PutUint32(data, divider)
		r := testPSBCursor(data)
		record := &layerRecord{layer: &psdmodel.Layer{}}
		(&parser{document: &psdmodel.Document{}}).additionalEntry(r, "lsct", record)
		if r.err != nil || record.layer.Group != (divider == 1 || divider == 2) {
			t.Fatalf("group marker type %d differs: %+v %v", divider, record.layer, r.err)
		}
		if divider > 3 && (len(record.layer.Unsupported) == 0 ||
			!strings.Contains(record.layer.Unsupported[0], "未知图层组")) {
			t.Fatal("unknown divider was silently rendered as a raster leaf")
		}
	}
}
