package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
)

func effectsFixtureUnit(unit string, number float64) []byte {
	return binary.BigEndian.AppendUint64([]byte(unit), math.Float64bits(number))
}

func effectsFixturePayload() []byte {
	color := descriptorFixtureBody("", "RGBC",
		descriptorFixtureEntry{key: "Rd  ", kind: "doub", data: descriptorFixtureU64(0)},
		descriptorFixtureEntry{key: "Grn ", kind: "doub", data: descriptorFixtureU64(0)},
		descriptorFixtureEntry{key: "Bl  ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(255))})
	shadow := descriptorFixtureBody("", "DrSh",
		descriptorFixtureEntry{key: "enab", kind: "bool", data: []byte{1}},
		descriptorFixtureEntry{key: "Clr ", kind: "Objc", data: color},
		descriptorFixtureEntry{key: "Opct", kind: "UntF", data: effectsFixtureUnit("#Prc", 75)},
		descriptorFixtureEntry{key: "uglg", kind: "bool", data: []byte{1}},
		descriptorFixtureEntry{key: "Dstn", kind: "UntF", data: effectsFixtureUnit("#Pxl", 3)})
	descriptor := descriptorFixtureBody("", "Lefx",
		descriptorFixtureEntry{key: "masterFXSwitch", kind: "bool", data: []byte{1}},
		descriptorFixtureEntry{key: "DrSh", kind: "Objc", data: shadow},
		descriptorFixtureEntry{key: "unknown", kind: "tdta", data: append(descriptorFixtureU32(3), 4, 5, 6)},
		descriptorFixtureEntry{key: "unknown", kind: "long", data: descriptorFixtureU32(19)})
	data := append(descriptorFixtureU32(0), descriptorFixtureU32(16)...)
	data = append(data, descriptor...)
	return append(data, make([]byte, (4-len(data)%4)%4)...)
}

func TestDecodeEffectsPreservesOrderedSpansDocumentLightAndSourceLifecycle(t *testing.T) {
	for _, key := range []string{"lfx2", "lmfx"} {
		payload := effectsFixturePayload()
		data := binary.BigEndian.AppendUint32(append([]byte(nil), payload...), 90)
		data = append(data, 1)
		document := descriptorFixtureDocument(data, DefaultLimits())
		document.Resources = []ResourceBlock{
			{ID: 1037, Data: Span{Offset: int64(len(payload)), Length: 4}},
			{ID: 1042, Data: Span{Offset: int64(len(payload) + 4), Length: 1}},
		}
		block := AdditionalBlock{Key: key, Data: Span{Length: int64(len(payload))}}
		set, err := document.DecodeEffects(nil, block)
		if err != nil {
			t.Fatal(err)
		}
		if set.ObjectVersion != 0 || set.DescriptorVersion != 16 || set.GlobalAngle != 90 ||
			!set.EffectsVisible || len(set.OrderedEntries) != 4 || set.OrderedEntries[2].Key != "unknown" ||
			set.OrderedEntries[3].Key != "unknown" || set.Items[0].Opacity != 0.75 {
			t.Fatal("source effect versions, ordered duplicates, opacity or global light were lost")
		}
		blob := set.OrderedEntries[2].Value.(psdeffects.Raw)
		raw, err := document.ReadSpan(Span{Offset: blob.Data.Offset, Length: blob.Data.Length})
		if err != nil || !bytes.Equal(raw, []byte{4, 5, 6}) {
			t.Fatalf("unknown effect raw span is not the original payload: %v", err)
		}
		if err := document.Close(); err != nil {
			t.Fatal(err)
		}
		alpha := image.NewAlpha(image.Rect(0, 0, 1, 1))
		alpha.Pix[0] = 255
		known := &psdeffects.Set{Enabled: set.Enabled, EffectsVisible: set.EffectsVisible,
			Scale: set.Scale, GlobalAngle: set.GlobalAngle, Items: set.Items}
		result, err := psdeffects.Rasterize(nil, alpha, known)
		if err != nil || result.Background[0].Coverage.Rect != image.Rect(0, 3, 1, 4) {
			t.Fatalf("owned effects failed after source closure or did not use global angle: %v", err)
		}
		if _, err := document.DecodeEffects(nil, block); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed source effect decode: %v", err)
		}
	}
}

func TestDecodeEffectsLimitsVersionsTailAndCancellation(t *testing.T) {
	payload := effectsFixturePayload()
	block := AdditionalBlock{Key: "lfx2", Data: Span{Length: int64(len(payload))}}
	for _, limits := range []Limits{
		{MaxMetadataBytes: 64}, {MaxDecodeBytes: 1000}, {MaxDescriptorItems: 1}, {MaxDescriptorDepth: 1},
	} {
		document := descriptorFixtureDocument(payload, normalizeTestLimits(t, limits))
		if result, err := document.DecodeEffects(nil, block); !errors.Is(err, ErrLimit) || result != nil {
			t.Fatalf("source effects exceeded limits or returned partial output: %v", err)
		}
	}
	versioned := append([]byte(nil), payload...)
	binary.BigEndian.PutUint32(versioned, 1)
	_, err := descriptorFixtureDocument(versioned, DefaultLimits()).DecodeEffects(nil, block)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown object-effects version was accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = descriptorFixtureDocument(payload, DefaultLimits()).DecodeEffects(ctx, block)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source effects decode: %v", err)
	}
	unknown := append(append([]byte(nil), payload...), 17)
	unknownBlock := AdditionalBlock{Key: "lfx2", Data: Span{Length: int64(len(unknown))}}
	set, err := descriptorFixtureDocument(unknown, DefaultLimits()).DecodeEffects(nil, unknownBlock)
	if err != nil || len(set.Unsupported) == 0 || set.Tail.Length == 0 {
		t.Fatalf("unrecognized trailing bytes did not remain explicit unsupported metadata: %v", err)
	}
}

func normalizeTestLimits(t *testing.T, limits Limits) Limits {
	t.Helper()
	resolved := DefaultLimits()
	if limits.MaxMetadataBytes != 0 {
		resolved.MaxMetadataBytes = limits.MaxMetadataBytes
	}
	if limits.MaxDecodeBytes != 0 {
		resolved.MaxDecodeBytes = limits.MaxDecodeBytes
	}
	if limits.MaxDescriptorItems != 0 {
		resolved.MaxDescriptorItems = limits.MaxDescriptorItems
	}
	if limits.MaxDescriptorDepth != 0 {
		resolved.MaxDescriptorDepth = limits.MaxDescriptorDepth
	}
	return resolved
}

type effectsErrorReader struct {
	data []byte
	err  error
}

func (r effectsErrorReader) ReadAt(output []byte, offset int64) (int, error) {
	count, err := bytes.NewReader(r.data).ReadAt(output, offset)
	if len(output) > 0 && offset == 0 && count == len(output) {
		return count, r.err
	}
	return count, err
}

func TestDecodeEffectsPreservesFullReadNonEOFError(t *testing.T) {
	payload := effectsFixturePayload()
	block := AdditionalBlock{Key: "lfx2", Data: Span{Length: int64(len(payload))}}
	failure := errors.New("injected effects reader failure")
	document := &Document{reader: effectsErrorReader{data: payload, err: failure}, size: int64(len(payload)),
		limits: DefaultLimits()}
	if _, err := document.DecodeEffects(nil, block); !errors.Is(err, failure) || !errors.Is(err, ErrMalformed) {
		t.Fatalf("full-count non-EOF I/O error was lost: %v", err)
	}
	document.reader = effectsErrorReader{data: payload, err: io.EOF}
	if _, err := document.DecodeEffects(nil, block); err != nil {
		t.Fatalf("full-count EOF should retain valid effects metadata: %v", err)
	}
}

func FuzzDecodeEffects(f *testing.F) {
	f.Add(effectsFixturePayload())
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 16})
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 64<<10 {
			t.Skip()
		}
		limits := DefaultLimits()
		limits.MaxMetadataBytes, limits.MaxDecodeBytes = 256<<10, 256<<10
		limits.MaxDescriptorItems, limits.MaxDescriptorDepth = 1000, 16
		document := descriptorFixtureDocument(payload, limits)
		original := append([]byte(nil), payload...)
		_, _ = document.DecodeEffects(nil, AdditionalBlock{Key: "lfx2", Data: Span{Length: int64(len(payload))}})
		if !bytes.Equal(original, payload) {
			t.Fatal("decoding effects changed original source bytes")
		}
	})
}
