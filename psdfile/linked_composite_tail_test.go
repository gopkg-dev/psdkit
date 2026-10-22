package psdfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"testing"
)

// linkedTailFixture uses explicit literal rows and four header channels, including transparency.
func linkedTailFixture(version, compression uint16, resources []byte) ([]byte, [][]byte) {
	samples := [][]byte{{1, 2, 3, 4, 5, 6}, {11, 12, 13, 14, 15, 16},
		{21, 22, 23, 24, 25, 26}, {0, 127, 255, 255, 127, 0}}
	var composite bytes.Buffer
	indexTestNumber(&composite, compression)
	if compression == 1 {
		for range 8 {
			if version == 2 {
				indexTestNumber(&composite, uint32(4))
			} else {
				indexTestNumber(&composite, uint16(4))
			}
		}
	}
	for _, plane := range samples {
		for row := range 2 {
			if compression == 1 {
				composite.WriteByte(2)
			}
			composite.Write(plane[row*3 : row*3+3])
		}
	}
	data := indexTestDocument(version, 8, 3, resources, nil, composite.Bytes())
	binary.BigEndian.PutUint16(data[12:14], 4)
	binary.BigEndian.PutUint32(data[14:18], 2)
	binary.BigEndian.PutUint32(data[18:22], 3)
	return data, samples
}

func TestLinkedCompositeRetainsOpaqueCapacityOnlyInsideContainer(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1} {
			for _, kind := range []string{"liFD", "liFE"} {
				for _, realInfo := range []bool{false, true} {
					name := fmt.Sprintf("version%d/compression%d/%s/versionInfo%t", version, compression, kind, realInfo)
					t.Run(name, func(t *testing.T) {
						var resources []byte
						if realInfo {
							resources = smartFixtureVersionInfo(true)
						}
						original, samples := linkedTailFixture(version, compression, resources)
						baseline := linkedFixtureParse(t, original)
						// Signatures and mixed bytes are opaque capacity, independent of their values or allocation size.
						tail := append([]byte("8BPS8BIM8B64"), bytes.Repeat([]byte{0xff, 0, 0x37, 0x80}, 7)...)
						padded := append(bytes.Clone(original), tail...)
						if document, err := Parse(padded); document != nil || !errors.Is(err, ErrMalformed) {
							t.Fatalf("standalone source accepted unassigned composite bytes: %v", err)
						}
						if document, err := Read(io.NewSectionReader(bytes.NewReader(padded), 0, int64(len(padded))),
							int64(len(padded))); document != nil || !errors.Is(err, ErrMalformed) {
							t.Fatalf("ordinary bounded ReaderAt activated linked-content compatibility: %v", err)
						}
						entryVersion := uint32(1)
						if kind == "liFE" {
							entryVersion = 4
						}
						outer := linkedFixtureDocument(3-version, "lnk2", linkedFixtureEntry(kind, entryVersion, nil, padded, nil))
						reader := &indexForbiddenReader{data: outer}
						parent, err := Read(reader, int64(len(outer)))
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = parent.Close() })
						records, err := parent.LinkedFiles(nil)
						if err != nil || len(records) != 1 {
							t.Fatalf("linked container index: %v", err)
						}
						payloadStart := baseline.Composite.Data.Offset
						if compression == 1 {
							payloadStart += int64(baseline.Composite.RowCountWidth) * 8
						}
						reader.forbidden = []Span{{Offset: records[0].Data.Offset + payloadStart,
							Length: int64(len(padded)) - payloadStart}}
						child, err := parent.OpenLinkedDocument(nil, records[0])
						if err != nil {
							t.Fatalf("linked source index read pixels/capacity or rejected its container: %v", err)
						}
						t.Cleanup(func() { _ = child.Close() })
						reader.forbidden = nil
						if child.Source.Length != int64(len(padded)) || child.Composite.Data != baseline.Composite.Data ||
							child.Composite.ReservedTail != (Span{Offset: int64(len(original)), Length: int64(len(tail))}) ||
							child.Composite.Span.Offset != baseline.Composite.Span.Offset ||
							child.Composite.Span.Length != baseline.Composite.Span.Length+int64(len(tail)) {
							t.Fatalf("original source/composite/capacity ranges changed: %+v", child.Composite)
						}
						decoded, err := child.DecodeComposite(nil)
						if err != nil || len(decoded.Channels) != len(samples) {
							t.Fatalf("four-channel composite decode: %v", err)
						}
						for index, plane := range decoded.Channels {
							if !bytes.Equal(plane.Data, samples[index]) {
								t.Fatalf("channel %d changed: %x", index, plane.Data)
							}
							region, err := child.DecodeCompositeChannelRegion(nil, index, image.Rect(1, 0, 3, 2))
							want := append(bytes.Clone(samples[index][1:3]), samples[index][4:6]...)
							if err != nil || !bytes.Equal(region.Data, want) {
								t.Fatalf("channel %d region decode: %v", index, err)
							}
						}
						var preserved bytes.Buffer
						if _, err := child.CopySpan(nil, &preserved, child.Source); err != nil ||
							!bytes.Equal(preserved.Bytes(), padded) {
							t.Fatalf("original linked bytes were lost: %v", err)
						}
					})
				}
			}
		}
	}
}

func TestLinkedCompositeCapacityDoesNotRelaxMalformedPixels(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			original, _ := linkedTailFixture(version, 1, smartFixtureVersionInfo(true))
			baseline := linkedFixtureParse(t, original)
			countWidth := int64(baseline.Composite.RowCountWidth)
			var wrongTable bytes.Buffer
			for range 8 {
				if version == 2 {
					indexTestNumber(&wrongTable, uint16(4))
				} else {
					indexTestNumber(&wrongTable, uint32(4))
				}
			}
			wrongWidth := append(bytes.Clone(original[:baseline.Composite.Data.Offset]), wrongTable.Bytes()...)
			wrongWidth = append(wrongWidth, original[baseline.Composite.Data.Offset+countWidth*8:]...)
			wrongWidth = append(wrongWidth, []byte("opaque capacity")...)
			widthParent := linkedFixtureParse(t,
				linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, wrongWidth, nil)))
			widthRecords, err := widthParent.LinkedFiles(nil)
			if err != nil {
				t.Fatal(err)
			}
			if child, err := widthParent.OpenLinkedDocument(nil, widthRecords[0]); child != nil ||
				!errors.Is(err, ErrMalformed) {
				t.Fatalf("RLE table width incorrectly fell back to the other document format: %v", err)
			}
			for _, count := range []uint32{0, 1, 1 << 20} {
				t.Run(fmt.Sprintf("rowLength%d", count), func(t *testing.T) {
					data := append(bytes.Clone(original), []byte("opaque capacity")...)
					if version == 2 {
						binary.BigEndian.PutUint32(data[baseline.Composite.Data.Offset:], count)
					} else {
						binary.BigEndian.PutUint16(data[baseline.Composite.Data.Offset:], uint16(min(count, 0xffff)))
					}
					parent := linkedFixtureParse(t,
						linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, data, nil)))
					records, err := parent.LinkedFiles(nil)
					if err != nil {
						t.Fatal(err)
					}
					if child, err := parent.OpenLinkedDocument(nil, records[0]); child != nil || !errors.Is(err, ErrMalformed) {
						t.Fatalf("invalid row length accepted by container indexing: %v", err)
					}
				})
			}
			truncated := original[:baseline.Composite.Data.Offset+countWidth*8-1]
			parent := linkedFixtureParse(t,
				linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, truncated, nil)))
			records, err := parent.LinkedFiles(nil)
			if err != nil {
				t.Fatal(err)
			}
			if child, err := parent.OpenLinkedDocument(nil, records[0]); child != nil ||
				!errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated row-count table crossed the linked-content boundary: %v", err)
			}
			badRuns := append(bytes.Clone(original), []byte("opaque capacity")...)
			payloadStart := baseline.Composite.Data.Offset + countWidth*8
			copy(badRuns[payloadStart:payloadStart+4], []byte{0x80, 0x80, 0x80, 0x80})
			parent = linkedFixtureParse(t,
				linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, badRuns, nil)))
			records, err = parent.LinkedFiles(nil)
			if err != nil {
				t.Fatal(err)
			}
			child, err := parent.OpenLinkedDocument(nil, records[0])
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Close() })
			if _, err := child.DecodeComposite(nil); !errors.Is(err, ErrMalformed) {
				t.Fatalf("capacity compatibility relaxed PackBits row validation: %v", err)
			}
			if _, err := child.DecodeCompositeChannelRegion(nil, 0, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("capacity compatibility relaxed PackBits region validation: %v", err)
			}
		})
	}
}

func TestLinkedCompositeCapacityDoesNotRelaxZIP(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, corruption := range []string{"trailing-capacity", "checksum"} {
			t.Run(fmt.Sprintf("version%d/%s", version, corruption), func(t *testing.T) {
				// Independent zlib encoding of six samples: 1, 2, 3, 4, 5, 6.
				encoded := decodeHex(t, "789c6364626661650300003e0016")
				if corruption == "checksum" {
					encoded[len(encoded)-1] ^= 1
				} else {
					encoded = append(encoded, []byte("opaque capacity")...)
				}
				data := indexTestDocument(version, 8, 1, smartFixtureVersionInfo(true), nil,
					append([]byte{0, 2}, encoded...))
				binary.BigEndian.PutUint32(data[14:18], 2)
				binary.BigEndian.PutUint32(data[18:22], 3)
				parent := linkedFixtureParse(t,
					linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, data, nil)))
				records, err := parent.LinkedFiles(nil)
				if err != nil {
					t.Fatal(err)
				}
				child, err := parent.OpenLinkedDocument(nil, records[0])
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = child.Close() })
				if child.Composite.ReservedTail != (Span{}) {
					t.Fatal("ZIP stream was trimmed by capacity compatibility")
				}
				if _, err := child.DecodeComposite(nil); !errors.Is(err, ErrMalformed) {
					t.Fatalf("ZIP checksum/stream boundary validation was relaxed: %v", err)
				}
			})
		}
	}
}

func TestLinkedCompositeCapacityDoesNotRelaxLayerChannelLengths(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		layers := indexTestLayerSet(version, image.Rect(0, 0, 1, 1), []int16{0}, [][]byte{{0, 0, 9, 8, 7}}, nil, nil)
		data := indexTestDocument(version, 8, 1, smartFixtureVersionInfo(true),
			indexTestGlobal(version, layers, nil), []byte{0, 0, 5})
		parent := linkedFixtureParse(t,
			linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, data, nil)))
		records, err := parent.LinkedFiles(nil)
		if err != nil {
			t.Fatal(err)
		}
		if child, err := parent.OpenLinkedDocument(nil, records[0]); child != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("version %d layer data accepted composite-only capacity compatibility: %v", version, err)
		}
	}
}
