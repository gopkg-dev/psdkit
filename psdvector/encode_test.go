package psdvector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestEncodeMaskRoundTripPreservesUnknownAndAlignmentBytes(t *testing.T) {
	data := vectorRectangle(7)
	unknown := vectorRecord(42)
	unknown[4], unknown[23] = 0xab, 0xcd
	data = append(data, unknown...)
	data = append(data, 0, 0)
	mask, err := DecodeMask(nil, data, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeMask(nil, mask, EncodeOptions{MaxBytes: int64(len(data))})
	if err != nil || !bytes.Equal(encoded, data) || cap(encoded) > len(data) {
		t.Fatalf("unmodified raw vector payload changed or exceeded capacity budget: %v", err)
	}
	clear(encoded)
	equal, err := EqualEncoding(nil, mask, data)
	if err != nil || !equal || mask.Records[len(mask.Records)-1].Raw[23] != 0xcd {
		t.Fatalf("encoded bytes borrowed provenance buffers: %v", err)
	}
	pathData, err := EncodePath(nil, &Mask{
		Width: 4, Height: 4, FillRule: mask.FillRule, HasFillRule: mask.HasFillRule,
		Subpaths: mask.Subpaths, Records: mask.Records, Padding: mask.Padding,
	})
	if err != nil || !bytes.Equal(pathData, data[8:]) {
		t.Fatalf("headerless output changed retained record offsets or bytes: %v", err)
	}
}

func TestEncodeMaskPixelEditsFlagsAndOpenSelectorsAreHonored(t *testing.T) {
	original := vectorRectangle(7)
	mask, err := DecodeMask(nil, original, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	mask.Invert = false
	mask.Subpaths[0].Closed = false
	knot := &mask.Subpaths[0].Knots[0]
	knot.Preceding.X += 0.5
	knot.Anchor.X += 0.5
	knot.Leaving.X += 0.5
	encoded, err := EncodeMask(context.Background(), mask)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMask(nil, encoded, 4, 4)
	if err != nil || decoded.Flags != 6 || decoded.Invert || !decoded.NotLinked || !decoded.Disabled ||
		decoded.Subpaths[0].Closed || decoded.Subpaths[0].Knots[0].Anchor.X != 1.5 {
		t.Fatalf("typed geometry and vector flag edits did not reach the payload: %#v, %v", decoded, err)
	}
	for _, current := range decoded.Subpaths[0].Knots {
		if decoded.Records[current.RecordIndex].Selector != 5 {
			t.Fatal("open subpath retained closed-knot selector")
		}
	}
	if binary.BigEndian.Uint32(original[4:]) != 7 || mask.Records[2].Raw[1] != 2 {
		t.Fatal("encoding changed caller provenance or original source bytes")
	}
}

func TestEncodeMaskRawCoordinateAndFlagWordEditsAreHonored(t *testing.T) {
	mask, err := DecodeMask(nil, vectorRectangle(0), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	mask.Flags = 1
	mask.Subpaths[0].Knots[0].Anchor.RawX += 1 << 22
	encoded, err := EncodeMask(nil, mask)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMask(nil, encoded, 4, 4)
	if err != nil || !decoded.Invert || decoded.Subpaths[0].Knots[0].Anchor.X != 2 {
		t.Fatalf("direct flag word or fixed-point edits were ignored: %#v, %v", decoded, err)
	}
	mask.Subpaths[0].Knots[0].Anchor.X = 1.5
	if encoded, err := EncodeMask(nil, mask); !errors.Is(err, ErrMalformed) || encoded != nil {
		t.Fatalf("conflicting fixed-point and pixel edits were accepted: %v", err)
	}
}

func TestEncodeMaskNewRemovedAndReorderedPaths(t *testing.T) {
	initial := &Mask{Width: 4, Height: 4, Version: 3, Subpaths: []Subpath{
		rectangleSubpath(0, 0, 2, 2, 1), rectangleSubpath(1, 1, 3, 3, 2),
	}}
	data, err := EncodeMask(nil, initial)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := DecodeMask(nil, data, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	added := rectangleSubpath(3, 0, 4, 1, 1)
	added.RecordIndex = -1
	mask.Subpaths = []Subpath{added, mask.Subpaths[1], mask.Subpaths[0]}
	encoded, err := EncodeMask(nil, mask)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMask(nil, encoded, 4, 4)
	if err != nil || len(decoded.Subpaths) != 3 {
		t.Fatalf("new ordered path geometry was not written: %v", err)
	}
	for index, expected := range []float64{3, 1, 0} {
		if decoded.Subpaths[index].Knots[0].Anchor.X != expected {
			t.Fatal("typed path order was replaced by the original source record order")
		}
	}
	decoded.Subpaths = nil
	emptyData, err := EncodeMask(nil, decoded)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := DecodeMask(nil, emptyData, 4, 4)
	if err != nil || len(empty.Subpaths) != 0 || len(empty.Records) != 2 {
		t.Fatalf("removed paths left orphan knot records: %#v, %v", empty, err)
	}
}

func TestEncodeMaskBudgetsCancellationAndUnrepresentableCoordinates(t *testing.T) {
	mask, err := DecodeMask(nil, vectorRectangle(0), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []EncodeOptions{{MaxBytes: 8}, {MaxRecords: 2}} {
		if output, err := EncodeMask(nil, mask, options); !errors.Is(err, ErrLimit) || output != nil {
			t.Fatalf("encoder returned partial over-budget bytes: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EncodeMask(ctx, mask); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled encoder: %v", err)
	}
	mask.Subpaths[0].Knots[0].Anchor.X = math.NaN()
	if _, err := EncodeMask(nil, mask); !errors.Is(err, ErrMalformed) {
		t.Fatalf("encoder accepted nonfinite geometry: %v", err)
	}
	mask.Subpaths[0].Knots[0].Anchor.X = 1000
	if _, err := EncodeMask(nil, mask); !errors.Is(err, ErrLimit) {
		t.Fatalf("encoder wrapped signed 8.24 coordinates: %v", err)
	}
}
