package psdparse

import (
	"bytes"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func TestVectorMaskUsesRealUserPlaneInsteadOfRenderedCache(t *testing.T) {
	for _, realFlags := range []uint8{0, 2} {
		payload := &psbWriter{}
		for _, edge := range []uint32{0, 0, 2, 2} {
			payload.u32(edge)
		}
		payload.Write([]byte{0, 8, realFlags, 255})
		for _, edge := range []uint32{0, 0, 2, 2} {
			payload.u32(edge)
		}
		encoded := &psbWriter{}
		encoded.block(payload.Bytes())
		p := &parser{document: &psdmodel.Document{Width: 2, Height: 2}}
		record := &layerRecord{layer: &psdmodel.Layer{Bounds: image.Rect(0, 0, 2, 2), VectorMask: &psdvector.Mask{}},
			channels: []channel{{id: -2, length: 6}, {id: -3, length: 6}}}
		r := testPSBCursor(encoded.Bytes())
		p.mask(r, record)
		if r.err != nil {
			t.Fatal(r.err)
		}
		pixels := testPSBCursor([]byte{0, 0, 33, 33, 33, 33, 0, 0, 255, 128, 0, 64})
		p.layerPixels(pixels, record)
		if pixels.err != nil {
			t.Fatal(pixels.err)
		}
		mask := record.layer.Mask
		if mask == nil || mask != record.userMask || mask.Disabled != (realFlags&2 != 0) || *mask.DefaultColor != 255 {
			t.Fatalf("real mask metadata was not selected: %+v", mask)
		}
		if !bytes.Equal(mask.Pixels.Pix, []byte{255, 255, 255, 255, 128, 128, 128, 255, 0, 0, 0, 255, 64, 64, 64, 255}) {
			t.Fatalf("real user plane was replaced by derived coverage: %v", mask.Pixels.Pix)
		}
		if len(record.layer.Unsupported) != 0 {
			t.Fatalf("known vector/user masks were rejected: %v", record.layer.Unsupported)
		}
	}
}

func TestVectorMaskDropsOnlyGeneratedCoverage(t *testing.T) {
	for _, flags := range []uint8{0, 8} {
		payload := &psbWriter{}
		for _, edge := range []uint32{0, 0, 2, 2} {
			payload.u32(edge)
		}
		payload.Write([]byte{0, flags, 0, 0})
		encoded := &psbWriter{}
		encoded.block(payload.Bytes())
		p := &parser{document: &psdmodel.Document{Width: 2, Height: 2}}
		record := &layerRecord{layer: &psdmodel.Layer{Bounds: image.Rect(0, 0, 2, 2), VectorMask: &psdvector.Mask{}},
			channels: []channel{{id: -2, length: 6}}}
		p.mask(testPSBCursor(encoded.Bytes()), record)
		r := testPSBCursor([]byte{0, 0, 100, 100, 100, 100})
		p.layerPixels(r, record)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if (record.layer.Mask == nil) != (flags&8 != 0) {
			t.Fatalf("generated flag=%d mask=%+v", flags, record.layer.Mask)
		}
	}
}
