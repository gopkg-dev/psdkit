package psdfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestDescriptorShortExplicitBooleanKeysRoundTrip(t *testing.T) {
	var entries []descriptorFixtureEntry
	for index := range 64 {
		entries = append(entries, descriptorFixtureEntry{key: "x", kind: "bool", data: []byte{byte(index % 3)}})
	}
	data := append(descriptorFixtureU32(16), descriptorFixtureBody("", "root", entries...)...)
	document := descriptorFixtureDocument(data, DefaultLimits())
	descriptor, err := document.DecodeDescriptor(Span{Length: int64(len(data))}, true)
	if err != nil || len(descriptor.Entries) != len(entries) {
		t.Fatalf("valid one-byte key Boolean entries were rejected: %v", err)
	}
	for index, entry := range descriptor.Entries {
		if entry.Key != "x" || entry.Value != (index%3 != 0) || entry.Span.Length != 10 {
			t.Fatalf("explicit short key or Boolean encoding changed: %+v", entry)
		}
	}
	encoded, err := document.EncodeDescriptor(nil, descriptor, true)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("short key descriptor failed exact source-aware round trip: %v", err)
	}
	limits := DefaultLimits()
	limits.MaxDescriptorItems = len(entries) - 1
	limited := descriptorFixtureDocument(data, limits)
	if _, err := limited.DecodeDescriptor(Span{Length: int64(len(data))}, true); !errors.Is(err, ErrLimit) {
		t.Fatalf("short keys bypassed descriptor item budget: %v", err)
	}
}

func TestTIFFDescriptorShortExplicitBooleanKeysBothOrders(t *testing.T) {
	for _, little := range []bool{false, true} {
		var order binary.ByteOrder = binary.BigEndian
		var appendOrder binary.AppendByteOrder = binary.BigEndian
		if little {
			order, appendOrder = binary.LittleEndian, binary.LittleEndian
		}
		fixture := tiffPrivateDescriptorFixture{order: appendOrder, little: little}
		var entries [][]byte
		for index := range 64 {
			entries = append(entries, fixture.entry("x", "bool", []byte{byte(index % 3)}, false))
		}
		data := append(fixture.u32(16), fixture.body("", "root", false, entries...)...)
		span := Span{Length: int64(len(data))}
		consumed, swaps, err := tiffPrivateDescriptorNormalizeFixture(nil, bytes.NewReader(data), span, true,
			tiffPrivateDescriptorOptions{Order: order})
		if err != nil || consumed != span || !little && len(swaps) != 0 {
			t.Fatalf("short key TIFF traversal, LE=%t: consumed %+v, err %v", little, consumed, err)
		}
		normalized := bytes.Clone(data)
		for _, swap := range swaps {
			for start := swap.span.Offset; start < swap.span.Offset+swap.span.Length; start += int64(swap.width) {
				for index := int64(0); index < int64(swap.width)/2; index++ {
					left, right := start+index, start+int64(swap.width)-1-index
					normalized[left], normalized[right] = normalized[right], normalized[left]
				}
			}
		}
		document := descriptorFixtureDocument(normalized, DefaultLimits())
		decoded, err := document.DecodeDescriptor(span, true)
		if err != nil || len(decoded.Entries) != len(entries) {
			t.Fatalf("short keys failed normalized BE decoding, LE=%t: %v", little, err)
		}
		limits := DefaultLimits()
		limits.MaxDescriptorItems = len(entries) - 1
		if _, err := normalizeTIFFPrivateDescriptor(nil, bytes.NewReader(data), span, true,
			tiffPrivateDescriptorOptions{Order: order, Limits: limits}); !errors.Is(err, ErrLimit) {
			t.Fatalf("short TIFF keys bypassed descriptor item budget, LE=%t: %v", little, err)
		}
	}
}
