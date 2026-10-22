package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
	"unicode/utf16"
)

type tiffPrivateDescriptorFixture struct {
	order  binary.AppendByteOrder
	little bool
}

func TestTIFFPrivateDescriptorBigEndianWalkIsStrictAndNeverSwaps(t *testing.T) {
	data := tiffPrivateDescriptorAllTypes(false)
	consumed, swaps, err := tiffPrivateDescriptorNormalizeFixture(context.Background(), bytes.NewReader(data),
		Span{Length: int64(len(data))}, true, tiffPrivateDescriptorOptions{Order: binary.BigEndian})
	if err != nil || consumed.Length != int64(len(data)) || len(swaps) != 0 {
		t.Fatalf("original BE descriptor was changed or skipped: %v/%v", consumed, err)
	}
	for length := range len(data) {
		_, _, err := tiffPrivateDescriptorNormalizeFixture(context.Background(), bytes.NewReader(data[:length]),
			Span{Length: int64(length)}, true, tiffPrivateDescriptorOptions{Order: binary.BigEndian})
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("BE descriptor truncation %d was accepted: %v", length, err)
		}
	}
	f := tiffPrivateDescriptorFixture{order: binary.BigEndian}
	unknown := append(f.u32(16), f.body("", "null", true, f.entry("type", "zzzz", nil, true))...)
	if _, err := normalizeTIFFPrivateDescriptor(context.Background(), bytes.NewReader(unknown),
		Span{Length: int64(len(unknown))}, true,
		tiffPrivateDescriptorOptions{Order: binary.BigEndian}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown original BE descriptor type was accepted: %v", err)
	}
}

func (f tiffPrivateDescriptorFixture) u32(value uint32) []byte {
	return f.order.AppendUint32(nil, value)
}

func (f tiffPrivateDescriptorFixture) u64(value uint64) []byte {
	return f.order.AppendUint64(nil, value)
}

func (f tiffPrivateDescriptorFixture) signature(value string) []byte {
	data := []byte(value)
	if f.little {
		data[0], data[3] = data[3], data[0]
		data[1], data[2] = data[2], data[1]
	}
	return data
}

func (f tiffPrivateDescriptorFixture) unicode(value string) []byte {
	codes := utf16.Encode([]rune(value))
	data := f.u32(uint32(len(codes)))
	for _, code := range codes {
		data = f.order.AppendUint16(data, code)
	}
	return data
}

func (f tiffPrivateDescriptorFixture) identifier(value string, compact bool) []byte {
	if compact {
		return append(f.u32(0), f.signature(value)...)
	}
	return append(f.u32(uint32(len(value))), value...)
}

func (f tiffPrivateDescriptorFixture) class(name, classID string, compact bool) []byte {
	return append(f.unicode(name), f.identifier(classID, compact)...)
}

func (f tiffPrivateDescriptorFixture) entry(key, kind string, data []byte, compact bool) []byte {
	result := f.identifier(key, compact)
	result = append(result, f.signature(kind)...)
	return append(result, data...)
}

func (f tiffPrivateDescriptorFixture) body(name, classID string, compact bool, entries ...[]byte) []byte {
	data := f.class(name, classID, compact)
	data = append(data, f.u32(uint32(len(entries)))...)
	for _, entry := range entries {
		data = append(data, entry...)
	}
	return data
}

func tiffPrivateDescriptorAllTypes(little bool) []byte {
	var order binary.AppendByteOrder = binary.BigEndian
	if little {
		order = binary.LittleEndian
	}
	f := tiffPrivateDescriptorFixture{order: order, little: little}
	child := f.body("子😀\x00", "Lyr ", true, f.entry("flag", "bool", []byte{2}, true))
	list := f.u32(3)
	list = append(list, f.signature("long")...)
	list = append(list, f.u32(0xfffffff9)...)
	list = append(list, f.signature("GlbO")...)
	list = append(list, child...)
	list = append(list, f.signature("VlLs")...)
	list = append(list, f.u32(0)...)
	references := f.u32(7)
	for _, reference := range []struct {
		kind string
		data []byte
	}{
		{"prop", append(f.class("属性", "null", true), f.identifier("Prpr", true)...)},
		{"Clss", f.class("", "ascii.fourcc", false)},
		{"Enmr", append(append(f.class("", "null", true), f.identifier("Type", true)...),
			f.identifier("valueASCII", false)...)},
		{"rele", append(f.class("", "null", false), f.u32(0xfffffff8)...)},
		{"Idnt", f.u32(0xffffffff)},
		{"indx", f.u32(3)},
		{"name", append(f.class("类别", "null", true), f.unicode("前\x00后😀\x00")...)},
	} {
		references = append(references, f.signature(reference.kind)...)
		references = append(references, reference.data...)
	}
	numbers := f.signature("#Pxl")
	numbers = append(numbers, f.u32(3)...)
	for _, bits := range []uint64{1 << 63, 0x7ff0000000000000, 0xfff8000000001234} {
		numbers = append(numbers, f.u64(bits)...)
	}
	objectArray := f.u32(19)
	objectArray = append(objectArray, f.body("数组", "root", false,
		f.entry("samples", "UnFl", numbers, false),
		f.entry("data", "TEXT", f.unicode("tail"), true),
	)...)
	blob := []byte{0x49, 0x49, 0x2a, 0x00, 'T', 'X', 'E', 'T', 16, 0, 0, 0, 0xff, 0x00, 0x12, 0x34}
	body := f.body("父🌟\x00", "null", true,
		f.entry("same", "long", f.u32(0xfffffff9), true),
		f.entry("same", "comp", f.u64(0xe000000000000009), false),
		f.entry("text", "TEXT", f.unicode("前\x00后😀\x00"), false),
		f.entry("enum", "enum", append(f.identifier("Type", true), f.identifier("Valu", false)...), true),
		f.entry("zero", "doub", f.u64(1<<63), true),
		f.entry("unit", "UntF", append(f.signature("#Pxl"), f.u64(0xfff8000000001234)...), false),
		f.entry("unit", "UnFl", append(f.signature("#Prc"), f.u32(0xffc12345)...), true),
		f.entry("bool", "bool", []byte{2}, true),
		f.entry("clas", "type", f.class("图层", "Lyr ", true), true),
		f.entry("clas", "GlbC", f.class("", "Lyr ", false), false),
		f.entry("alis", "alis", append(f.u32(uint32(len(blob))), blob...), true),
		f.entry("EngineData", "tdta", append(f.u32(uint32(len(blob))), blob...), false),
		f.entry("path", "Pth ", append(f.u32(uint32(len(blob))), blob...), false),
		f.entry("nest", "Objc", child, true),
		f.entry("glob", "GlbO", child, false),
		f.entry("list", "VlLs", list, false),
		f.entry("refs", "obj ", references, true),
		f.entry("arry", "ObAr", objectArray, true),
	)
	return append(f.u32(16), body...)
}

type tiffPrivateDescriptorSwap struct {
	span  Span
	width uint8
}

func tiffPrivateDescriptorNormalizeFixture(
	ctx context.Context, source io.ReaderAt, span Span, versioned bool,
	options tiffPrivateDescriptorOptions,
) (Span, []tiffPrivateDescriptorSwap, error) {
	var swaps []tiffPrivateDescriptorSwap
	options.Swap = func(span Span, width uint8, _ string) error {
		swaps = append(swaps, tiffPrivateDescriptorSwap{span: span, width: width})
		return nil
	}
	consumed, err := normalizeTIFFPrivateDescriptor(ctx, source, span, versioned, options)
	return consumed, swaps, err
}

func TestTIFFPrivateDescriptorNormalizesAllKnownTypesExactly(t *testing.T) {
	little, big := tiffPrivateDescriptorAllTypes(true), tiffPrivateDescriptorAllTypes(false)
	prefix := bytes.Repeat([]byte{0xcc}, 37)
	data := append(append(append([]byte{}, prefix...), little...), 0xaa, 0xbb, 0xcc)
	reader := &descriptorRecordingReader{reader: bytes.NewReader(data)}
	span := Span{Offset: int64(len(prefix)), Length: int64(len(little) + 3)}
	consumed, swaps, err := tiffPrivateDescriptorNormalizeFixture(
		nil, reader, span, true, tiffPrivateDescriptorOptions{},
	)
	if err != nil || consumed != (Span{Offset: span.Offset, Length: int64(len(little))}) {
		t.Fatalf("descriptor consumption = %+v, %v", consumed, err)
	}
	result := bytes.Clone(data)
	lastEnd := span.Offset
	var widths [9]bool
	for _, swap := range swaps {
		if swap.width != 2 && swap.width != 4 && swap.width != 8 ||
			swap.span.Length <= 0 || swap.span.Length%int64(swap.width) != 0 ||
			swap.span.Offset < lastEnd || swap.span.Offset+swap.span.Length > span.Offset+consumed.Length {
			t.Fatalf("invalid or overlapping swap run: %+v", swap)
		}
		widths[swap.width] = true
		for start := swap.span.Offset; start < swap.span.Offset+swap.span.Length; start += int64(swap.width) {
			for index := int64(0); index < int64(swap.width)/2; index++ {
				left, right := start+index, start+int64(swap.width)-1-index
				result[left], result[right] = result[right], result[left]
			}
		}
		lastEnd = swap.span.Offset + swap.span.Length
	}
	if !widths[2] || !widths[4] || !widths[8] ||
		!bytes.Equal(result[span.Offset:span.Offset+consumed.Length], big) ||
		!bytes.Equal(result[:span.Offset], prefix) || !bytes.Equal(result[len(result)-3:], []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatal("normalization changed opaque bytes, explicit ASCII IDs, UTF-16 codes, or numeric IEEE bits")
	}
	for _, read := range reader.reads {
		if read.Length != 4 {
			t.Fatalf("walker read a payload instead of fixed structural fields: %+v", read)
		}
	}
	document := descriptorFixtureDocument(result, DefaultLimits())
	descriptor, err := document.DecodeDescriptor(consumed, true)
	if err != nil || len(descriptor.Entries) != 18 {
		t.Fatalf("normalized bytes cannot use the unchanged BE descriptor decoder: %v", err)
	}
	if descriptor.Entries[0].Key != "same" || descriptor.Entries[1].Key != "same" ||
		descriptor.Entries[5].Value.(DescriptorUnit).Bits != 0xfff8000000001234 ||
		descriptor.Entries[6].Value.(DescriptorUnit).Bits != 0xffc12345 {
		t.Fatal("compact versus explicit ASCII identifier or float bits changed")
	}
}

func TestTIFFPrivateDescriptorSkipsOpaquePayloadsAndUnicodeReads(t *testing.T) {
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	payload := bytes.Repeat([]byte{0xa5}, 1<<20)
	text := append(f.u32(1<<19), payload...)
	data := f.body("", "root", true,
		f.entry("EngineData", "tdta", append(f.u32(uint32(len(payload))), payload...), false),
		f.entry("text", "TEXT", text, true),
	)
	reader := &descriptorRecordingReader{reader: bytes.NewReader(data)}
	var reservation int64
	consumed, swaps, err := tiffPrivateDescriptorNormalizeFixture(
		context.Background(), reader, Span{Length: int64(len(data))}, false,
		tiffPrivateDescriptorOptions{Reserve: func(length int64, _ string, _ int64) error {
			reservation += length
			return nil
		}},
	)
	if err != nil || consumed.Length != int64(len(data)) || reservation > 4096 {
		t.Fatalf("opaque payload contributed allocation or failed: reservation=%d, %v", reservation, err)
	}
	var readBytes int64
	for _, read := range reader.reads {
		readBytes += read.Length
	}
	if readBytes >= 100 || len(swaps) > 16 {
		t.Fatalf("payload was materialized: readBytes=%d, swapRuns=%d", readBytes, len(swaps))
	}
	if final := swaps[len(swaps)-1]; final.width != 2 || final.span.Length != int64(len(payload)) {
		t.Fatalf("UTF-16 was not one lazy two-byte swap run: %+v", final)
	}
}

func TestTIFFPrivateDescriptorLimitsAndSharedCallbacks(t *testing.T) {
	data := tiffPrivateDescriptorAllTypes(true)
	for _, test := range []struct {
		name    string
		options tiffPrivateDescriptorOptions
		noReads bool
	}{
		{"metadata", tiffPrivateDescriptorOptions{Limits: Limits{MaxMetadataBytes: 1}}, true},
		{"root-frame", tiffPrivateDescriptorOptions{Limits: Limits{MaxMetadataBytes: 215}}, true},
		{"read", tiffPrivateDescriptorOptions{Limits: Limits{MaxReadBytes: 3}}, true},
		{"items", tiffPrivateDescriptorOptions{Limits: Limits{MaxDescriptorItems: 1}}, false},
		{"depth", tiffPrivateDescriptorOptions{Limits: Limits{MaxDescriptorDepth: 1}}, false},
		{"unsafe-depth", tiffPrivateDescriptorOptions{Limits: Limits{MaxDescriptorDepth: 257}}, true},
		{"reserve", tiffPrivateDescriptorOptions{Reserve: func(int64, string, int64) error {
			return ErrLimit
		}}, true},
		{"count", tiffPrivateDescriptorOptions{Count: func(uint32, string, int64) error {
			return ErrLimit
		}}, false},
		{"swap", tiffPrivateDescriptorOptions{Swap: func(Span, uint8, string) error {
			return ErrLimit
		}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &descriptorRecordingReader{reader: bytes.NewReader(data)}
			_, err := normalizeTIFFPrivateDescriptor(
				context.Background(), reader, Span{Length: int64(len(data))}, true, test.options,
			)
			if !errors.Is(err, ErrLimit) || test.noReads && len(reader.reads) != 0 {
				t.Fatalf("limit performed premature I/O or lost its category: reads=%d, %v", len(reader.reads), err)
			}
		})
	}
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	nested := f.body("", "root", true, f.entry("flag", "bool", []byte{1}, true))
	data = f.body("", "root", true, f.entry("nest", "Objc", nested, true))
	var sharedItems uint32
	_, err := normalizeTIFFPrivateDescriptor(nil, bytes.NewReader(data), Span{Length: int64(len(data))}, false,
		tiffPrivateDescriptorOptions{Count: func(count uint32, _ string, _ int64) error {
			sharedItems += count
			return nil
		}})
	if err != nil || sharedItems != 2 {
		t.Fatalf("nested counts did not reach the owning budget: items=%d, %v", sharedItems, err)
	}
}

type tiffPrivateDescriptorFaultReader struct {
	reader *bytes.Reader
	cause  error
	short  bool
	cancel context.CancelFunc
	reads  int
}

func (r *tiffPrivateDescriptorFaultReader) ReadAt(data []byte, offset int64) (int, error) {
	r.reads++
	count, _ := r.reader.ReadAt(data, offset)
	if r.cancel != nil {
		r.cancel()
	}
	if r.short && count > 0 {
		count--
	}
	return count, r.cause
}

func TestTIFFPrivateDescriptorPreservesSourceFaultsAndCancellation(t *testing.T) {
	data := tiffPrivateDescriptorAllTypes(true)
	failure := errors.New("injected descriptor fault")
	for _, cause := range []error{failure, errors.Join(io.EOF, failure), ErrClosed, context.Canceled} {
		reader := &tiffPrivateDescriptorFaultReader{reader: bytes.NewReader(data), cause: cause}
		_, err := normalizeTIFFPrivateDescriptor(nil, reader, Span{Length: int64(len(data))}, true,
			tiffPrivateDescriptorOptions{})
		if !errors.Is(err, cause) || !errors.Is(err, ErrMalformed) {
			t.Fatalf("full-count source fault was swallowed: %v", err)
		}
	}
	reader := &tiffPrivateDescriptorFaultReader{reader: bytes.NewReader(data), cause: io.EOF}
	if _, err := normalizeTIFFPrivateDescriptor(nil, reader, Span{Length: int64(len(data))}, true,
		tiffPrivateDescriptorOptions{}); err != nil {
		t.Fatalf("complete read with bare EOF was rejected: %v", err)
	}
	reader = &tiffPrivateDescriptorFaultReader{reader: bytes.NewReader(data), short: true}
	if _, err := normalizeTIFFPrivateDescriptor(nil, reader, Span{Length: int64(len(data))}, true,
		tiffPrivateDescriptorOptions{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short nil-error read lost its cause: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader = &tiffPrivateDescriptorFaultReader{reader: bytes.NewReader(data)}
	if _, err := normalizeTIFFPrivateDescriptor(ctx, reader, Span{Length: int64(len(data))}, true,
		tiffPrivateDescriptorOptions{}); !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("pre-canceled walk performed I/O: reads=%d, %v", reader.reads, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader = &tiffPrivateDescriptorFaultReader{reader: bytes.NewReader(data), cancel: cancel}
	var swaps int
	_, err := normalizeTIFFPrivateDescriptor(ctx, reader, Span{Length: int64(len(data))}, true,
		tiffPrivateDescriptorOptions{Swap: func(Span, uint8, string) error {
			swaps++
			return nil
		}})
	if !errors.Is(err, context.Canceled) || reader.reads != 1 || swaps != 0 {
		t.Fatalf("cancellation during source read was delayed: reads=%d, swaps=%d, %v", reader.reads, swaps, err)
	}
}

func TestTIFFPrivateDescriptorObservesCancellationFromFinalCallbacks(t *testing.T) {
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	for _, callback := range []string{"count", "swap"} {
		t.Run(callback, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			data := f.body("", "root", true)
			options := tiffPrivateDescriptorOptions{}
			if callback == "count" {
				options.Count = func(uint32, string, int64) error {
					cancel()
					return nil
				}
			} else {
				data = f.body("", "root", true, f.entry("last", "long", f.u32(19), true))
				options.Swap = func(span Span, _ uint8, _ string) error {
					if span.Offset+span.Length == int64(len(data)) {
						cancel()
					}
					return nil
				}
			}
			consumed, err := normalizeTIFFPrivateDescriptor(ctx, bytes.NewReader(data),
				Span{Length: int64(len(data))}, false, options)
			if !errors.Is(err, context.Canceled) || consumed != (Span{}) {
				t.Fatalf("final %s callback cancellation returned successful consumption: %+v, %v", callback, consumed, err)
			}
		})
	}
}

func TestTIFFPrivateDescriptorRejectsTruncationAndUnknownTypes(t *testing.T) {
	data := tiffPrivateDescriptorAllTypes(true)
	for length := range data {
		_, err := normalizeTIFFPrivateDescriptor(nil, bytes.NewReader(data[:length]),
			Span{Length: int64(length)}, true, tiffPrivateDescriptorOptions{})
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncation at %d was not rejected as malformed: %v", length, err)
		}
	}
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	for _, kind := range []string{"ZZZZ", "obj "} {
		payload := f.u32(0)
		if kind == "obj " {
			payload = append(f.u32(1), f.signature("ZZZZ")...)
		}
		unknown := f.body("", "root", true, f.entry("test", kind, payload, true))
		_, err := normalizeTIFFPrivateDescriptor(nil, bytes.NewReader(unknown), Span{Length: int64(len(unknown))},
			false, tiffPrivateDescriptorOptions{})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown %q did not fail closed: %v", kind, err)
		}
	}
	data[0] = 17
	if _, err := normalizeTIFFPrivateDescriptor(nil, bytes.NewReader(data), Span{Length: int64(len(data))}, true,
		tiffPrivateDescriptorOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown descriptor version was accepted: %v", err)
	}
	for _, span := range []Span{{Offset: -1}, {Length: -1}, {Offset: math.MaxInt64, Length: 1}} {
		if _, err := normalizeTIFFPrivateDescriptor(nil, bytes.NewReader(data), span, true,
			tiffPrivateDescriptorOptions{}); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid span was accepted: %+v, %v", span, err)
		}
	}
}
