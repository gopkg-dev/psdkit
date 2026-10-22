package psdfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdgradient"
)

func gradientFixtureEnum(typeID, value string) []byte {
	return append(descriptorFixtureID(typeID, false), descriptorFixtureID(value, false)...)
}

func gradientFixtureUnit(unit string, value float64) []byte {
	return append([]byte(unit), descriptorFixtureU64(math.Float64bits(value))...)
}

func gradientFixtureStop(location, midpoint uint32, opacity bool) []byte {
	entries := []descriptorFixtureEntry{
		{key: "Lctn", kind: "long", data: descriptorFixtureU32(location)},
		{key: "Mdpn", kind: "long", data: descriptorFixtureU32(midpoint)},
	}
	classID := "Clrt"
	if opacity {
		classID = "TrnS"
		entries = append(entries, descriptorFixtureEntry{key: "Opct", kind: "UntF",
			data: gradientFixtureUnit("#Prc", float64(location)*100/4096)})
	} else {
		rgb := descriptorFixtureBody("", "RGBC",
			descriptorFixtureEntry{key: "Rd  ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(20))},
			descriptorFixtureEntry{key: "Grn ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(80))},
			descriptorFixtureEntry{key: "Bl  ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(200))},
		)
		entries = append(entries, descriptorFixtureEntry{key: "Clr ", kind: "Objc", data: rgb},
			descriptorFixtureEntry{key: "Type", kind: "enum", data: gradientFixtureEnum("Clry", "UsrS")})
	}
	return descriptorFixtureBody("", classID, entries...)
}

func gradientFixtureList(opacity bool) []byte {
	data := descriptorFixtureU32(2)
	for index, location := range []uint32{0, 4096} {
		midpoint := uint32(50)
		if index == 1 {
			midpoint = 30
			if opacity {
				midpoint = 70
			}
		}
		data = append(data, "Objc"...)
		data = append(data, gradientFixtureStop(location, midpoint, opacity)...)
	}
	return data
}

func gradientFixturePayload(extra ...descriptorFixtureEntry) []byte {
	gradient := descriptorFixtureBody("", "Grdn",
		descriptorFixtureEntry{key: "Nm  ", kind: "TEXT", data: descriptorFixtureUnicode("Gradient 中文")},
		descriptorFixtureEntry{key: "GrdF", kind: "enum", data: gradientFixtureEnum("GrdF", "CstS")},
		descriptorFixtureEntry{key: "Intr", kind: "doub", data: descriptorFixtureU64(math.Float64bits(4096))},
		descriptorFixtureEntry{key: "Clrs", kind: "VlLs", data: gradientFixtureList(false)},
		descriptorFixtureEntry{key: "Trns", kind: "VlLs", data: gradientFixtureList(true)},
	)
	offset := descriptorFixtureBody("", "Pnt ",
		descriptorFixtureEntry{key: "Hrzn", kind: "UntF", data: gradientFixtureUnit("#Prc", 7)},
		descriptorFixtureEntry{key: "Vrtc", kind: "UntF", data: gradientFixtureUnit("#Prc", -11)},
	)
	entries := []descriptorFixtureEntry{
		{key: "Grad", kind: "Objc", data: gradient},
		{key: "Angl", kind: "UntF", data: gradientFixtureUnit("#Ang", 30)},
		{key: "Scl ", kind: "UntF", data: gradientFixtureUnit("#Prc", 80)},
		{key: "Type", kind: "enum", data: gradientFixtureEnum("GrdT", "Rdl ")},
		{key: "Rvrs", kind: "bool", data: []byte{1}}, {key: "Algn", kind: "bool", data: []byte{0}},
		{key: "Dthr", kind: "bool", data: []byte{0}}, {key: "Ofst", kind: "Objc", data: offset},
		{key: "gradientsInterpolationMethod", kind: "enum",
			data: gradientFixtureEnum("gradientInterpolationMethodType", "Gcls")},
	}
	entries = append(entries, extra...)
	return append(descriptorFixtureU32(16), descriptorFixtureBody("", "null", entries...)...)
}

func gradientFixtureDocument(t *testing.T, payload []byte, options ...LoadOptions) (*Document, AdditionalBlock) {
	t.Helper()
	data := indexTestDocument(1, 8, 3, nil, indexTestGlobal(1, nil,
		indexTestTag(1, "8BIM", "GdFl", payload, 4)), []byte{0, 0, 0})
	document, err := Parse(data, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	return document, document.AdditionalInfo[0]
}

func TestDecodeGradientFillRetainsOwnedStopsDescriptorAndTail(t *testing.T) {
	payload := append(gradientFixturePayload(), 0, 0)
	document, block := gradientFixtureDocument(t, payload)
	gradient, err := document.DecodeGradientFill(nil, block)
	if err != nil {
		t.Fatal(err)
	}
	fill := gradient.Fill
	if fill.Kind != psdgradient.Radial || fill.Angle != 30 || fill.Scale != 80 || !fill.Reverse || fill.Align ||
		fill.Offset != (psdgradient.Offset{X: 7, Y: -11}) || fill.Interpolation != "classic" || fill.Smoothness != 4096 ||
		fill.Name != "Gradient 中文" || len(fill.ColorStops) != 2 || len(fill.OpacityStops) != 2 ||
		fill.ColorStops[1].Midpoint != 30 || fill.OpacityStops[1].Midpoint != 70 || fill.OpacityStops[1].Opacity != 1 {
		t.Fatal("typed gradient controls differ from independent descriptor bytes")
	}
	if gradient.Descriptor.Span.Offset != block.Data.Offset || gradient.Tail.Length != 2 ||
		gradient.Tail.Offset != block.Data.Offset+int64(len(payload))-2 || gradient.Block != block {
		t.Fatal("descriptor, padding or original block spans lost")
	}
	fill.ColorStops[0].RGB[0] = 1
	gradient.Descriptor.Entries[0].Key = "mutated"
	again, err := document.DecodeGradientFill(nil, block)
	if err != nil || again.Fill.ColorStops[0].RGB[0] != 20.0/255 || again.Descriptor.Entries[0].Key != "Grad" {
		t.Fatalf("typed stops or descriptor entries borrowed source storage: %v", err)
	}
}

func TestDecodeGradientFillUnknownSemanticsRemainAvailable(t *testing.T) {
	for _, payload := range [][]byte{
		gradientFixturePayload(descriptorFixtureEntry{key: "unknown", kind: "bool", data: []byte{1}}),
		gradientFixturePayload(descriptorFixtureEntry{key: "Angl", kind: "UntF", data: gradientFixtureUnit("#Ang", 0)}),
		gradientFixturePayload(descriptorFixtureEntry{key: "gradientInterpolationMethod", kind: "enum",
			data: gradientFixtureEnum("gradientInterpolationMethodType", "Gcls")}),
		append(gradientFixturePayload(), 0x99),
	} {
		document, block := gradientFixtureDocument(t, payload)
		gradient, err := document.DecodeGradientFill(nil, block)
		if !errors.Is(err, ErrUnsupported) || gradient == nil || gradient.Descriptor == nil ||
			gradient.Fill == nil || len(gradient.Fill.Unsupported) == 0 {
			t.Fatalf("unknown metadata discarded or silently ignored: %v", err)
		}
		original, err := document.ReadSpan(block.Data)
		if err != nil || !bytes.Equal(original, payload) {
			t.Fatal("unsupported gradient source bytes changed")
		}
	}
	noise := descriptorFixtureBody("", "Grdn",
		descriptorFixtureEntry{key: "GrdF", kind: "enum", data: gradientFixtureEnum("GrdF", "ClNs")},
		descriptorFixtureEntry{key: "RndS", kind: "long", data: descriptorFixtureU32(123)},
	)
	payload := append(descriptorFixtureU32(16), descriptorFixtureBody("", "null",
		descriptorFixtureEntry{key: "Grad", kind: "Objc", data: noise})...)
	document, block := gradientFixtureDocument(t, payload)
	if gradient, err := document.DecodeGradientFill(nil, block); !errors.Is(err, ErrUnsupported) ||
		gradient == nil || gradient.Fill.Form != "ClNs" {
		t.Fatalf("valid noise form was mistaken for a malformed custom gradient: %v", err)
	}
}

func TestDecodeGradientFillLimitsCancellationAndStrictReaderErrors(t *testing.T) {
	payload := gradientFixturePayload()
	document, block := gradientFixtureDocument(t, payload)
	if _, err := document.DecodeGradientFill(nil, block, MetadataOptions{MaxBytes: 1000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("single-view owned budget ignored: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := document.DecodeGradientFill(ctx, block); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled gradient reader ignored cancellation: %v", err)
	}
	reader := &linkedTestReader{reader: bytes.NewReader(payload)}
	borrowed := &Document{reader: reader, size: int64(len(payload)), limits: DefaultLimits()}
	block = AdditionalBlock{Key: "GdFl", Data: Span{Length: int64(len(payload))}}
	if _, err := borrowed.DecodeGradientFill(nil, block, MetadataOptions{MaxBytes: 1}); !errors.Is(err, ErrLimit) || reader.reads != 0 {
		t.Fatalf("known insufficient metadata budget performed source I/O: %v", err)
	}
	reader.fullEOF = true
	if _, err := borrowed.DecodeGradientFill(nil, block); err != nil {
		t.Fatalf("complete ReaderAt EOF rejected: %v", err)
	}
	wantIO := errors.New("gradient fixture read failure")
	reader.fullEOF, reader.failure = false, wantIO
	if _, err := borrowed.DecodeGradientFill(nil, block); !errors.Is(err, ErrMalformed) || !errors.Is(err, wantIO) {
		t.Fatalf("ReaderAt failure lost category or cause: %v", err)
	}
	reader.failure, reader.zero = nil, true
	if _, err := borrowed.DecodeGradientFill(nil, block); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short ReaderAt accepted: %v", err)
	}
	if err := borrowed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := borrowed.DecodeGradientFill(nil, block); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed gradient source accepted: %v", err)
	}
}

func FuzzDecodeGradientFill(f *testing.F) {
	f.Add(gradientFixturePayload())
	f.Add(gradientFixturePayload(descriptorFixtureEntry{key: "unknown", kind: "bool", data: []byte{1}}))
	f.Add([]byte{0, 0, 0, 16})
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 8192 {
			return
		}
		limits := DefaultLimits()
		limits.MaxMetadataBytes, limits.MaxDecodeBytes, limits.MaxDescriptorItems = 64<<10, 64<<10, 256
		document := descriptorFixtureDocument(payload, limits)
		block := AdditionalBlock{Key: "GdFl", Data: Span{Length: int64(len(payload))}}
		gradient, err := document.DecodeGradientFill(nil, block)
		if err == nil && (gradient == nil || gradient.Fill == nil || gradient.Descriptor == nil) {
			t.Fatal("successful gradient decoder returned no owned semantic view")
		}
	})
}
