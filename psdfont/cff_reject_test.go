package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"testing"
)

func cffTestProgram(program []byte, subrs [][]byte) *Face {
	data := append([]byte(nil), program...)
	spans := make([]Span, len(subrs))
	for index, code := range subrs {
		spans[index] = Span{Offset: int64(len(data)), Length: int64(len(code))}
		data = append(data, code...)
	}
	metrics := binary.BigEndian.AppendUint16(nil, 600)
	metrics = binary.BigEndian.AppendUint16(metrics, 13)
	metricOffset := len(data)
	data = append(data, metrics...)
	return &Face{raw: data, UnitsPerEm: 1000, NumGlyphs: 1, horizontalMetrics: 1,
		tables: map[string]Span{"hmtx": {Offset: int64(metricOffset), Length: 4}},
		cff: &cffFont{charstrings: []Span{{Length: int64(len(program))}},
			private: []cffPrivate{{subrs: spans, defaultWidth: 600, nominalWidth: 100,
				matrix: [6]float64{1, 0, 0, 1, 0, 0}}}}}
}

func TestCFFType2MalformedAndUnsupportedPrograms(t *testing.T) {
	controls := []struct {
		name    string
		program []byte
		subrs   [][]byte
		kind    error
	}{
		{"truncated-shortint", []byte{28}, nil, ErrMalformed},
		{"truncated-fixed", []byte{255, 0, 0, 0}, nil, ErrMalformed},
		{"reserved-sequence", append(make([]byte, 49), 14), nil, ErrUnsupported},
		{"no-endchar", []byte{139}, nil, ErrMalformed},
		{"after-endchar", []byte{14, 139}, nil, ErrMalformed},
		{"return-outside-subr", []byte{11}, nil, ErrMalformed},
		{"line-before-move", []byte{139, 139, 5, 14}, nil, ErrMalformed},
		{"wrong-move-count", []byte{139, 139, 139, 139, 21, 14}, nil, ErrMalformed},
		{"empty-hstem", []byte{1, 14}, nil, ErrMalformed},
		{"mask-without-hints", []byte{19, 14}, nil, ErrMalformed},
		{"truncated-mask", []byte{139, 159, 1, 19}, nil, ErrMalformed},
		{"mask-unused-bits", []byte{139, 159, 1, 19, 1, 14}, nil, ErrMalformed},
		{"subr-missing", []byte{32, 10, 14}, nil, ErrMalformed},
		{"subr-cycle", []byte{32, 10, 14}, [][]byte{{32, 10, 11}}, ErrMalformed},
		{"subr-without-return", []byte{32, 10, 14}, [][]byte{{139}}, ErrMalformed},
		{"after-subr-return", []byte{32, 10, 14}, [][]byte{{11, 139}}, ErrMalformed},
		{"endchar-in-subr-then-junk", []byte{32, 10, 14}, [][]byte{{14}}, ErrMalformed},
		{"division-zero", []byte{149, 139, 12, 12, 14}, nil, ErrMalformed},
		{"sqrt-negative", []byte{129, 12, 26, 14}, nil, ErrMalformed},
		{"transient-uninitialized", []byte{139, 12, 21, 14}, nil, ErrMalformed},
		{"transient-range", []byte{139, 171, 12, 20, 14}, nil, ErrMalformed},
		{"dup-underflow", []byte{12, 27, 14}, nil, ErrMalformed},
		{"index-overflow", []byte{139, 149, 12, 29, 14}, nil, ErrMalformed},
		{"roll-negative-count", []byte{129, 139, 12, 30, 14}, nil, ErrMalformed},
		{"ifelse-underflow", []byte{12, 22, 14}, nil, ErrMalformed},
		{"random", []byte{12, 23, 14}, nil, ErrUnsupported},
		{"seac-absent-component-names", []byte{139, 139, 204, 205, 14}, nil, ErrMissingGlyph},
		{"reserved-operator", []byte{0}, nil, ErrUnsupported},
		{"reserved-escaped", []byte{139, 139, 12, 2, 14}, nil, ErrUnsupported},
	}
	for _, control := range controls {
		t.Run(control.name, func(t *testing.T) {
			face := cffTestProgram(control.program, control.subrs)
			glyph, err := face.Glyph(nil, 0)
			if glyph != nil || !errors.Is(err, control.kind) {
				t.Fatalf("glyph=%v err=%v want=%v", glyph, err, control.kind)
			}
		})
	}
	stack := make([]byte, 49)
	for index := range stack {
		stack[index] = 139
	}
	if glyph, err := cffTestProgram(stack, nil).Glyph(nil, 0); glyph != nil || !errors.Is(err, ErrLimit) {
		t.Fatal("49th numeric operand did not enforce fixed stack bound")
	}
	validEnd := cffTestProgram([]byte{32, 10}, [][]byte{{14}})
	if glyph, err := validEnd.Glyph(nil, 0); glyph == nil || err != nil {
		t.Fatal("subroutine endchar did not terminate caller")
	}
	if subrBias(1239) != 107 || subrBias(1240) != 1131 || subrBias(33899) != 1131 || subrBias(33900) != 32768 {
		t.Fatal("Type2 bias threshold differs")
	}
}

func TestCFFParserIndexDictAndMatrixRejection(t *testing.T) {
	ctx := context.Background()
	parser := func(data []byte) *cffParser {
		face := &Face{raw: data, NumGlyphs: 2}
		return &cffParser{face: face, ctx: ctx, data: data, limits: DecodeOptions{
			MaxBytes: 1 << 20, MaxCFFOperations: 10000}}
	}
	for _, data := range [][]byte{{}, {0}, {0, 1}, {0, 1, 0}, {0, 1, 5}, {0, 1, 1, 0, 1},
		{0, 1, 1, 1, 0}, {0, 1, 1, 1, 3}, {0, 2, 1, 1, 3, 2, 139, 139}} {
		if _, _, err := parser(data).index(0); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid INDEX accepted: %v: %v", data, err)
		}
	}
	for _, data := range [][]byte{{30, 0xdf}, {30, 0xff}, {30, 0x1a}, {139}, {12},
		{139, 17, 139, 17}, {139, 12, 7}, {139, 139, 18, 139, 139, 18}} {
		if _, err := parser(data).dictionary(Span{Length: int64(len(data))}); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid DICT accepted: %v: %v", data, err)
		}
	}
	for _, data := range [][]byte{{0, 2}, {0, 0, 2}, {3, 0, 0}, {3, 0, 1, 0, 1, 0, 0, 2},
		{3, 0, 1, 0, 0, 2, 0, 2}, {3, 0, 1, 0, 0, 0, 0, 1}} {
		if _, err := parser(data).fdSelectors(0, 2); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid FDSelect accepted: %v: %v", data, err)
		}
	}
	face := cffTestFace(t, cffTestOracle(t)[3])
	data := append([]byte(nil), face.raw...)
	span := face.tables["CFF "]
	cff := data[span.Offset : span.Offset+span.Length]
	p := cffParser{face: &Face{raw: data}, ctx: ctx, data: cff, base: span.Offset,
		limits: DecodeOptions{MaxBytes: 1 << 20, MaxCFFOperations: 10000}}
	_, next, err := p.index(int(cff[2]))
	if err != nil {
		t.Fatal(err)
	}
	topIndex, _, err := p.index(next)
	if err != nil {
		t.Fatal(err)
	}
	top, err := p.dictionary(topIndex[0])
	if err != nil || !top.hasMatrix {
		t.Fatal("portable font must supply Top FontMatrix")
	}
	// Replace the known real operand 0.0005 with an equal-length real 0.0006; no directory changes.
	start, end := int(topIndex[0].Offset), int(topIndex[0].Offset+topIndex[0].Length)
	found := false
	for position := start; position < end; position++ {
		value, next, numeric, err := cffNumber(data, position, true)
		if err == nil && numeric && value == 0.0005 && data[position] == 30 {
			for digit := position + 1; digit < next; digit++ {
				if data[digit]>>4 == 5 {
					data[digit], found = data[digit]+0x10, true
					break
				}
				if data[digit]&15 == 5 {
					data[digit], found = data[digit]+1, true
					break
				}
			}
			changed, _, _, err := cffNumber(data, position, true)
			if err != nil || changed != 0.0006 {
				t.Fatal("matrix mutation did not produce the registered 0.0006 value")
			}
			break
		}
	}
	if !found {
		t.Fatal("known FontMatrix real operand was not located")
	}
	if result, err := Decode(nil, data); err != nil || result == nil ||
		result.cff.private[0].matrix != [6]float64{1.2, 0, 0, 1, 0, 0} {
		t.Fatalf("legal noncanonical Top FontMatrix was not normalized: %v", err)
	}
}

func TestCFFSeededMalformedTraversalNoPanic(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261013, 5177))
	for index := range 3000 {
		length := 1 + rng.IntN(192)
		data := make([]byte, length)
		for position := range data {
			data[position] = byte(rng.Uint32())
		}
		face := cffTestProgram(data, nil)
		_, _ = face.Glyph(nil, 0, GlyphOptions{MaxOperations: 128, MaxPoints: 128, MaxWorkingBytes: 64 << 10})
		p := cffParser{face: face, ctx: context.Background(), data: data,
			limits: DecodeOptions{MaxBytes: 64 << 10, MaxCFFOperations: 128}}
		_, _, _ = p.index(index % length)
		_, _ = p.dictionary(Span{Length: int64(length)})
		_, _ = p.fdSelectors(index%length, 2)
		_, _ = p.charset(index%length, 16, index%2 == 0)
	}
}

func TestCFFStemHintLimitAndMaskByteCount(t *testing.T) {
	var code []byte
	for range 4 {
		for range 24 {
			code = append(code, 139, 159)
		}
		code = append(code, 1)
	}
	valid := append([]byte(nil), code...)
	valid = append(valid, 19)
	for range 12 {
		valid = append(valid, 255)
	}
	valid = append(valid, 139, 139, 21, 219, 159, 5, 14)
	if glyph, err := cffTestProgram(valid, nil).Glyph(nil, 0); err != nil || glyph == nil {
		t.Fatalf("96 hints and 12-byte mask must parse without executing hints: %v", err)
	}
	invalid := append(code, 139, 159, 1, 14)
	if glyph, err := cffTestProgram(invalid, nil).Glyph(nil, 0); glyph != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("97th hint was accepted: %v", err)
	}
}
