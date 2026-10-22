package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func TestGlyphTwoByTwoTransformScaledOffsetsAndRepeatFlags(t *testing.T) {
	tables := fontTestTables(false, false, 6)
	compound := []byte{0xff, 0xff, 0xfc, 0x18, 0, 0, 0, 0, 3, 232}
	for _, value := range []int16{0x83, 1, 0, 0, 0, 16384, -16384, 0} {
		compound = append(compound, fontTestU16(uint16(value))...)
	}
	copy(tables["glyf"][60:88], append(compound, 0, 0))
	face, err := Decode(nil, fontTestBuild(tables))
	if err != nil {
		t.Fatal(err)
	}
	glyph, err := face.Glyph(nil, 3)
	if err != nil || len(glyph.Contours) != 1 || glyph.Contours[0][1].X != -1000 ||
		glyph.Contours[0][1].Y != 500 || glyph.Contours[0][2].Y != 1000 {
		t.Fatalf("two-by-two transform transposed the basis vectors: %v", err)
	}
	tables = fontTestTables(false, false, 6)
	data := tables["glyf"][60:88]
	binary.BigEndian.PutUint16(data[10:], 0x082b)
	face, err = Decode(nil, fontTestBuild(tables))
	if err != nil {
		t.Fatal(err)
	}
	glyph, err = face.Glyph(nil, 3)
	if err != nil || glyph.Contours[0][0].X != 5 || glyph.Contours[0][0].Y != 10 {
		t.Fatalf("scaled offset was interpreted in parent coordinates: %v", err)
	}
	tables = fontTestTables(false, false, 6)
	repeated := []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0x39, 1}
	copy(tables["glyf"][88:], repeated)
	face, err = Decode(nil, fontTestBuild(tables))
	if err != nil {
		t.Fatal(err)
	}
	glyph, err = face.Glyph(nil, 5)
	if err != nil || len(glyph.Contours[0]) != 2 || glyph.Contours[0][1].X != 0 ||
		!glyph.Contours[0][1].OnCurve {
		t.Fatalf("packed repeat or zero-vector flag decode: %v", err)
	}
	tables["glyf"][103] = 2
	face, err = Decode(nil, fontTestBuild(tables))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := face.Glyph(nil, 5); !errors.Is(err, ErrMalformed) {
		t.Fatal("repeat count exceeding contour endpoint was accepted")
	}
}

func TestFontCancellationDuringTablesGlyphsAndCoverage(t *testing.T) {
	data := fontTestBuild(fontTestTables(false, false, 6))
	ctx, cancel := context.WithCancel(context.Background())
	counted := &fontCancelContext{Context: ctx, cancel: cancel, after: 4}
	if _, err := Decode(counted, data); !errors.Is(err, context.Canceled) {
		t.Fatalf("table traversal ignored cancellation: %v", err)
	}
	face, err := Decode(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	counted = &fontCancelContext{Context: ctx, cancel: cancel, after: 7}
	if _, err := face.Glyph(counted, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("compound traversal ignored cancellation: %v", err)
	}
}

type fontCancelContext struct {
	context.Context
	cancel context.CancelFunc
	after  int
	calls  int
}

func (c *fontCancelContext) Err() error {
	c.calls++
	if c.calls >= c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestLayoutSupplementaryLatinPreservesUTF16Clusters(t *testing.T) {
	tables := fontTestTables(true, false, 6)
	cmap := tables["cmap"]
	binary.BigEndian.PutUint32(cmap[76:], 0x1df00)
	binary.BigEndian.PutUint32(cmap[80:], 0x1df00)
	face, err := Decode(nil, fontTestBuild(tables))
	if err != nil {
		t.Fatal(err)
	}
	resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
	run, err := Layout(nil, "\U0001df00A\r\nV", Style{FontName: face.PostScriptName, Size: 10}, resolver,
		LayoutOptions{MaxGlyphs: 4})
	if err != nil || run.UTF16Units != 6 || len(run.Glyphs) != 4 || run.Glyphs[0].ClusterLength != 2 ||
		run.Glyphs[1].ClusterStart != 2 || run.Glyphs[2].ClusterStart != 3 ||
		run.Glyphs[2].ClusterLength != 2 || run.Glyphs[3].ClusterStart != 5 {
		t.Fatalf("supplementary Latin or CRLF was counted as UTF-8 bytes: %v", err)
	}
}

func FuzzBoundedTrueTypeGlyphDecode(f *testing.F) {
	f.Add(fontTestBuild(fontTestTables(false, false, 6)))
	f.Add(fontTestBuild(fontTestTables(true, true, 3)))
	f.Add([]byte("OTTO"))
	f.Fuzz(func(t *testing.T, data []byte) {
		face, err := Decode(nil, data, DecodeOptions{MaxBytes: 1 << 20, MaxTables: 32, MaxFaces: 4,
			MaxGlyphs: 512, MaxCmapEntries: 1024})
		if err != nil {
			return
		}
		for id := uint16(0); id < min(face.NumGlyphs, 16); id++ {
			_, _ = face.Glyph(nil, id, GlyphOptions{MaxWorkingBytes: 64 << 10, MaxPoints: 256,
				MaxContours: 64, MaxComponents: 128, MaxDepth: 8})
		}
	})
}
