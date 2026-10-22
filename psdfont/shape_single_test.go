package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func singleTestSubtable(format uint16, input, output uint16) []byte {
	data := make([]byte, 14)
	binary.BigEndian.PutUint16(data, format)
	coverageAt := uint16(6)
	if format == 2 {
		coverageAt = 8
		binary.BigEndian.PutUint16(data[4:], 1)
		binary.BigEndian.PutUint16(data[6:], output)
	} else {
		binary.BigEndian.PutUint16(data[4:], output-input)
	}
	binary.BigEndian.PutUint16(data[2:], coverageAt)
	binary.BigEndian.PutUint16(data[coverageAt:], 1)
	binary.BigEndian.PutUint16(data[coverageAt+2:], 1)
	binary.BigEndian.PutUint16(data[coverageAt+4:], input)
	return data
}

func TestShapeSingleFormatsModuloAndExactBudget(t *testing.T) {
	for _, row := range []struct {
		name                              string
		format, input, output, glyphCount uint16
	}{
		{"delta-positive", 1, 1, 2, 3},
		{"delta-negative", 1, 2, 1, 3},
		{"delta-wrap-positive", 1, 65534, 1, 65535},
		{"delta-wrap-negative", 1, 1, 65534, 65535},
		{"array", 2, 1, 2, 3},
	} {
		t.Run(row.name, func(t *testing.T) {
			p := otParser{data: singleTestSubtable(row.format, row.input, row.output), tag: "GSUB", numGlyphs: row.glyphCount,
				b: &shapeBudget{ctx: context.Background(), maxNodes: 100, maxVisits: 100, maxBytes: 10000}}
			sub, err := p.singleSub(0)
			if err != nil || len(sub.singleIDs) != 1 || sub.singleIDs[0] != row.output {
				t.Fatalf("singlemodulo:%+v %v", sub, err)
			}
			bytes, nodes := p.b.bytes, p.b.nodes
			for _, delta := range []int64{-1, 0, 1} {
				q := p
				q.b = &shapeBudget{ctx: context.Background(), maxNodes: nodes, maxVisits: 100, maxBytes: bytes + delta}
				got, err := q.singleSub(0)
				if delta < 0 {
					if got.singleIDs != nil || !errors.Is(err, ErrLimit) {
						t.Fatalf("single prealloc short:%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			run := &ShapedRun{Glyphs: []ShapedGlyph{{GlyphID: row.input, Rune: 'A', ClusterStart: 7, ClusterLength: 2}}}
			matched, err := sub.ligature(run, 0, p.b)
			if err != nil || !matched || run.Glyphs[0].GlyphID != row.output || run.Glyphs[0].Rune != 'A' ||
				run.Glyphs[0].ClusterStart != 7 || run.Glyphs[0].ClusterLength != 2 {
				t.Fatal("single changed cluster or Rune")
			}
		})
	}
}

func TestShapeSingleMalformedAndZeroOutputNoFallback(t *testing.T) {
	for _, format := range []uint16{1, 2} {
		p := otParser{data: singleTestSubtable(format, 1, 3), tag: "GSUB", numGlyphs: 3,
			b: &shapeBudget{ctx: context.Background(), maxNodes: 100, maxBytes: 1000}}
		if _, err := p.singleSub(0); !errors.Is(err, ErrMalformed) {
			t.Fatalf("out ofrange accepted:%v", err)
		}
	}
	face := copyShapeTestFace(shapeTestFace(t, "testdata/opentype/fonts/selected-single-substitution.ttf"))
	data := shapeTestTable(face, "GSUB")
	lookup, sub := shapeTestFirstLookup(data)
	if u16(data, lookup) != 1 || u16(data, sub) != 1 {
		t.Fatal("own font must have deltaSingle")
	}
	putShapeTest16(data, sub+4, uint16(65534))
	run, err := Shape(nil, face, "A", shapeTestOptions())
	if run != nil || !errors.Is(err, ErrMissingGlyph) {
		t.Fatalf("substituted glyphzero silentlyfellback: %+v %v", run, err)
	}
}

func TestShapeSingleExtensionChainRequiredAndInstanceAdvance(t *testing.T) {
	cff2VerifyPortableFonts(t)
	for _, name := range []string{"SingleChain", "SingleExtensionChain", "SingleRequired"} {
		face, err := Open(nil, "testdata/cff2/fonts/"+name+".otf",
			DecodeOptions{Variations: []VariationCoordinate{{Tag: "wght", Value: .5}}})
		if err != nil {
			t.Fatal(err)
		}
		selected := ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}}}
		text, want, advance, precise := "AA", uint16(4), int64(499), 498.5
		if name == "SingleRequired" {
			text, want, advance, precise = "A", 2, 500, 499.5
			selected.Features = nil
		}
		run, err := Shape(nil, face, text, selected)
		if err != nil || run == nil || len(run.Glyphs) != 1 || run.Glyphs[0].GlyphID != want ||
			run.Glyphs[0].Rune != 'A' || run.Glyphs[0].ClusterStart != 0 || run.Glyphs[0].ClusterLength != int64(len(text)) ||
			run.Glyphs[0].XAdvance != advance || run.Glyphs[0].InstancePosition == nil ||
			run.Glyphs[0].InstancePosition.XAdvance != precise {
			t.Fatalf("single/chain required%v got%+v %v", name, run, err)
		}
		if name != "SingleRequired" {
			plain, err := Shape(nil, face, "AA")
			if err != nil || plain == nil || len(plain.Glyphs) != 2 ||
				plain.Glyphs[0].GlyphID != 1 || plain.Glyphs[1].GlyphID != 1 {
				t.Fatalf("unselected single/ligature executed: %+v %v", plain, err)
			}
		}
		for _, delta := range []int64{-1, 0, 1} {
			o := selected
			o.MaxWorkingBytes = run.WorkingBytes() + delta
			bounded, err := Shape(nil, face, text, o)
			if delta < 0 {
				if bounded != nil || !errors.Is(err, ErrLimit) {
					t.Fatalf("one-short single chain:%v", err)
				}
			} else if bounded == nil || err != nil {
				t.Fatal(err)
			}
		}
	}
}
