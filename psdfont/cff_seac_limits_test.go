package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func seacInteger(value int) []byte {
	if value >= -107 && value <= 107 {
		return []byte{byte(value + 139)}
	}
	if value >= 108 && value <= 1131 {
		return []byte{byte((value-108)/256 + 247), byte((value - 108) % 256)}
	}
	if value >= -1131 && value <= -108 {
		return []byte{byte((-value-108)/256 + 251), byte((-value - 108) % 256)}
	}
	return binary.BigEndian.AppendUint16([]byte{28}, uint16(value))
}

func seacEndProgram(base, accent int) []byte {
	program := []byte{139, 139}
	program = append(program, seacInteger(base)...)
	program = append(program, seacInteger(accent)...)
	return append(program, 14)
}

func seacManualFace(programs [][]byte, charset []uint16, names []string, subrs [][]byte) *Face {
	face := &Face{UnitsPerEm: 1000, NumGlyphs: uint16(len(programs)), horizontalMetrics: uint16(len(programs)),
		tables: make(map[string]Span), cff: &cffFont{charset: charset}}
	for _, program := range programs {
		face.cff.charstrings = append(face.cff.charstrings, Span{Offset: int64(len(face.raw)), Length: int64(len(program))})
		face.raw = append(face.raw, program...)
	}
	for _, name := range names {
		face.cff.strings = append(face.cff.strings, Span{Offset: int64(len(face.raw)), Length: int64(len(name))})
		face.raw = append(face.raw, name...)
	}
	private := cffPrivate{defaultWidth: 600, nominalWidth: 100, matrix: cffIdentityMatrix}
	for _, program := range subrs {
		private.subrs = append(private.subrs, Span{Offset: int64(len(face.raw)), Length: int64(len(program))})
		face.raw = append(face.raw, program...)
	}
	face.cff.private = []cffPrivate{private}
	offset := len(face.raw)
	for range programs {
		face.raw = binary.BigEndian.AppendUint16(face.raw, 900)
		face.raw = binary.BigEndian.AppendUint16(face.raw, 13)
	}
	face.tables["hmtx"] = Span{Offset: int64(offset), Length: int64(len(programs) * 4)}
	return face
}

func seacLimitFace(root, base, accent []byte) *Face {
	return seacManualFace([][]byte{{14}, root, base, accent}, []uint16{0, 391, 35, 125},
		[]string{"Composite"}, nil)
}

func TestCFFSeacIllegalNestedCycleCIDAndNameAmbiguity(t *testing.T) {
	leaf := []byte{149, 159, 21, 169, 139, 5, 14}
	for _, control := range []struct {
		name string
		face *Face
		kind error
	}{
		{"nested-base", seacLimitFace(seacEndProgram(66, 194), seacEndProgram(66, 194), leaf), ErrMalformed},
		{"nested-accent", seacLimitFace(seacEndProgram(66, 194), leaf, seacEndProgram(66, 194)), ErrMalformed},
		{"missing-name", seacLimitFace(seacEndProgram(65, 194), leaf, leaf), ErrMissingGlyph},
		{"negative-code", seacLimitFace(seacEndProgram(-1, 194), leaf, leaf), ErrMalformed},
		{"code256", seacLimitFace(seacEndProgram(256, 194), leaf, leaf), ErrMalformed},
		{"fractional-code", seacLimitFace([]byte{139, 139, 255, 0, 1, 128, 0, 247, 86, 14}, leaf, leaf), ErrMalformed},
		{"duplicate-component-name", seacManualFace([][]byte{{14}, seacEndProgram(66, 66), leaf, leaf},
			[]uint16{0, 391, 35, 392}, []string{"Composite", "B"}, nil), ErrUnsupported},
		{"cycle-mother-as-base", seacManualFace([][]byte{{14}, seacEndProgram(65, 194), leaf},
			[]uint16{0, 34, 125}, nil, nil), ErrMalformed},
		{"cycle-mother-as-accent", seacManualFace([][]byte{{14}, seacEndProgram(66, 65), leaf},
			[]uint16{0, 34, 35}, nil, nil), ErrMalformed},
	} {
		t.Run(control.name, func(t *testing.T) {
			glyph, err := control.face.Glyph(nil, 1)
			if glyph != nil || !errors.Is(err, control.kind) {
				t.Fatalf("illegal component partial/incorrect error: %v", err)
			}
		})
	}
	cid := seacLimitFace(seacEndProgram(66, 194), leaf, leaf)
	cid.cff.cid, cid.cff.fdSelect = true, make([]uint8, cid.NumGlyphs)
	if glyph, err := cid.Glyph(nil, 1); glyph != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CID name-domain unsupported: %v", err)
	}
}

func TestCFFSeacRawOffsetBeforeFontMatrixAndSameComponent(t *testing.T) {
	leaf := []byte{28, 51, 51, 139, 21, 149, 139, 5, 14}
	offset := -13107.0 + 1.0/65536
	program := binary.BigEndian.AppendUint32([]byte{255}, uint32(int32(offset*65536)))
	program = append(program, 139)
	program = append(program, seacInteger(66)...)
	program = append(program, seacInteger(66)...)
	program = append(program, 14)
	face := seacLimitFace(program, leaf, leaf)
	face.cff.private[0].matrix = [6]float64{.1, 0, 0, 1, 0, 0}
	glyph, err := face.Glyph(nil, 1)
	if err != nil || glyph == nil || len(glyph.ComponentSources) != 2 ||
		glyph.ComponentSources[0] != glyph.ComponentSources[1] {
		t.Fatalf("samecomponent provenance: %v", err)
	}
	point := 13107.0
	matrix := face.cff.private[0].matrix[0]
	expected := matrix * (point + offset)
	separated := matrix*point + matrix*offset
	if math.Float64bits(expected) == math.Float64bits(separated) {
		t.Fatal("offset-order controlled negative failed to distinguish the two models")
	}
	if len(glyph.Path) != 6 || math.Float64bits(glyph.Path[3].X) != math.Float64bits(expected) {
		t.Fatal("accent did not transform raw shifted point before FontMatrix")
	}
}

type seacCountContext struct {
	context.Context
	count, cancelAt int
	cause           error
}

func (c *seacCountContext) Err() error {
	c.count++
	if c.cancelAt > 0 && c.count >= c.cancelAt {
		return c.cause
	}
	return nil
}

func TestCFFSeacWholeOperationsDepthAndMidContextCause(t *testing.T) {
	for _, font := range seacOracle(t) {
		face := seacFace(t, font)
		id := font.Cases[0].ID
		low, high := 1, 10000
		for low < high {
			middle := low + (high-low)/2
			glyph, err := face.Glyph(nil, id, GlyphOptions{MaxOperations: middle})
			if err == nil && glyph != nil {
				high = middle
			} else if glyph == nil && errors.Is(err, ErrLimit) {
				low = middle + 1
			} else {
				t.Fatalf("exact operations search wrong error: %v", err)
			}
		}
		for _, delta := range []int{-1, 0, 1} {
			glyph, err := face.Glyph(nil, id, GlyphOptions{MaxOperations: low + delta})
			if delta < 0 {
				if glyph != nil || !errors.Is(err, ErrLimit) {
					t.Fatal("shared operations minus1 returned partial/noerror")
				}
			} else if glyph == nil || err != nil {
				t.Fatalf("operations exact/+1: %v", err)
			}
		}
		ctx := &seacCountContext{Context: context.Background()}
		if glyph, err := face.Glyph(ctx, id); glyph == nil || err != nil || ctx.count < 128 {
			t.Fatalf("full actual context traversal: %v/%d", err, ctx.count)
		}
		cause := errors.New("author seac midcontext cause")
		for _, at := range []int{1, ctx.count / 4, ctx.count / 2, ctx.count - 1, ctx.count} {
			cancelled := &seacCountContext{Context: context.Background(), cancelAt: at, cause: cause}
			if glyph, err := face.Glyph(cancelled, id); glyph != nil || !errors.Is(err, cause) {
				t.Fatalf("midcontext partial/changedcause at%d:%v", at, err)
			}
		}
		t.Logf("font%s shared operations exact=%d contextchecks=%d", font.File, low, ctx.count)
	}
	leaf := []byte{149, 159, 21, 169, 139, 5, 14}
	subrs := make([][]byte, 10)
	for index := range 9 {
		subrs[index] = append(seacInteger(index+1-107), 10, 11)
	}
	subrs[9] = []byte{11}
	base := append(seacInteger(-107), 10)
	base = append(base, leaf...)
	face := seacManualFace([][]byte{{14}, seacEndProgram(66, 194), base, leaf}, []uint16{0, 391, 35, 125},
		[]string{"Composite"}, subrs)
	if glyph, err := face.Glyph(nil, 1, GlyphOptions{MaxDepth: 10}); glyph != nil || !errors.Is(err, ErrLimit) {
		t.Fatal("component plus10subrs did not share physical depth budget")
	}
	for _, depth := range []int{11, 12} {
		if glyph, err := face.Glyph(nil, 1, GlyphOptions{MaxDepth: depth}); glyph == nil || err != nil {
			t.Fatalf("legal10subrs under exact physicaldepth11: %v", err)
		}
	}
}
