package psdfile_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// These fixtures follow Adobe's PSD/PSB format tables rather than production encoders.
// Nonzero padding is deliberate: preservation must never normalize original bytes.
type acceptanceFixture struct {
	data  []byte
	spans map[string]psdfile.Span
}

func acceptanceNewFixture() acceptanceFixture {
	return acceptanceFixture{spans: make(map[string]psdfile.Span)}
}

func (f *acceptanceFixture) add(label string, data []byte) {
	start := int64(len(f.data))
	f.data = append(f.data, data...)
	if label != "" {
		f.spans[label] = psdfile.Span{Offset: start, Length: int64(len(data))}
	}
}

func (f *acceptanceFixture) mark(label string, start int) {
	f.spans[label] = psdfile.Span{Offset: int64(start), Length: int64(len(f.data) - start)}
}

func (f *acceptanceFixture) append(label string, child acceptanceFixture) {
	start := int64(len(f.data))
	f.data = append(f.data, child.data...)
	for name, span := range child.spans {
		span.Offset += start
		f.spans[label+name] = span
	}
}

func (f *acceptanceFixture) u16(label string, value uint16) {
	f.add(label, binary.BigEndian.AppendUint16(nil, value))
}

func (f *acceptanceFixture) u32(label string, value uint32) {
	f.add(label, binary.BigEndian.AppendUint32(nil, value))
}

func (f *acceptanceFixture) u64(label string, value uint64) {
	f.add(label, binary.BigEndian.AppendUint64(nil, value))
}

func (f *acceptanceFixture) length(label string, width int, value int) {
	if width == 8 {
		f.u64(label, uint64(value))
		return
	}
	f.u32(label, uint32(value))
}

func acceptanceVersionWidth(version uint16) int {
	if version == 2 {
		return 8
	}
	return 4
}

func acceptancePascal(raw []byte, alignment int) []byte {
	data := append([]byte{byte(len(raw))}, raw...)
	for len(data)%alignment != 0 {
		data = append(data, byte(0xd0+len(data)%16))
	}
	return data
}

func acceptanceUnicode(value string) []byte {
	units := utf16.Encode([]rune(value))
	data := binary.BigEndian.AppendUint32(nil, uint32(len(units)))
	for _, unit := range units {
		data = binary.BigEndian.AppendUint16(data, unit)
	}
	return data
}

func acceptanceResource(id uint16, name, payload []byte) acceptanceFixture {
	f := acceptanceNewFixture()
	f.add("", []byte("8BIM"))
	f.u16("", id)
	f.add("name", acceptancePascal(name, 2))
	f.u32("length", uint32(len(payload)))
	f.add("data", payload)
	if len(payload)%2 != 0 {
		f.add("padding", []byte{0xee})
	} else {
		f.add("padding", nil)
	}
	f.mark("span", 0)
	return f
}

func acceptanceAdditional(
	signature, key string,
	lengthWidth, alignment int,
	payload acceptanceFixture,
) acceptanceFixture {
	f := acceptanceNewFixture()
	f.add("", []byte(signature+key))
	f.length("length", lengthWidth, len(payload.data))
	start := len(f.data)
	f.append("payload/", payload)
	f.mark("data", start)
	start = len(f.data)
	for (len(f.data)-8-lengthWidth)%alignment != 0 {
		f.add("", []byte{0xa5})
	}
	f.mark("padding", start)
	f.mark("span", 0)
	return f
}

func acceptancePayload(data []byte) acceptanceFixture {
	f := acceptanceNewFixture()
	f.add("", data)
	return f
}

func acceptanceEncoded(version, compression uint16, planes [][]byte, height int) acceptanceFixture {
	f := acceptanceNewFixture()
	f.u16("compression", compression)
	switch compression {
	case 0:
		for index, plane := range planes {
			f.add(fmt.Sprintf("plane%d", index), plane)
		}
	case 1:
		rowLength := len(planes[0]) / height
		start := len(f.data)
		for range planes {
			for range height {
				if version == 2 {
					f.u32("", uint32(rowLength+1))
				} else {
					f.u16("", uint16(rowLength+1))
				}
			}
		}
		f.mark("table", start)
		for index, plane := range planes {
			start = len(f.data)
			for row := range height {
				f.add("", []byte{byte(rowLength - 1)})
				f.add("", plane[row*rowLength:(row+1)*rowLength])
			}
			f.mark(fmt.Sprintf("plane%d", index), start)
		}
	case 2, 3:
		var encoded bytes.Buffer
		writer := zlib.NewWriter(&encoded)
		for _, plane := range planes {
			values := bytes.Clone(plane)
			if compression == 3 {
				rowLength := len(values) / height
				for row := range height {
					for column := rowLength - 1; column > 0; column-- {
						values[row*rowLength+column] -= values[row*rowLength+column-1]
					}
				}
			}
			_, _ = writer.Write(values)
		}
		_ = writer.Close()
		f.add("encoded", encoded.Bytes())
	}
	f.spans["data"] = psdfile.Span{Offset: 2, Length: int64(len(f.data) - 2)}
	f.mark("span", 0)
	return f
}

func acceptancePlanes(depth uint16) [][]byte {
	switch depth {
	case 1:
		return [][]byte{{0x80, 0x40}, {0x40, 0x80}, {0xc0, 0}, {0xc0, 0xc0}, {0, 0xc0}}
	case 16:
		return [][]byte{
			{0x01, 0x23, 0xab, 0xcd, 0x45, 0x67, 0x89, 0xef},
			{0x23, 0x45, 0xcd, 0xef, 0x67, 0x89, 0xab, 0x01},
			{0x45, 0x67, 0xef, 0x01, 0x89, 0xab, 0xcd, 0x23},
			{0xff, 0xff, 0x80, 0x00, 0x40, 0x00, 0, 0},
			{0, 0, 0x40, 0x00, 0x80, 0x00, 0xff, 0xff},
		}
	case 32:
		return [][]byte{
			{0x3f, 0x80, 0, 0, 0x3f, 0, 0, 0, 0x3e, 0x80, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 0, 0x3e, 0x80, 0, 0, 0x3f, 0, 0, 0, 0x3f, 0x80, 0, 0},
			{0x3f, 0, 0, 0, 0x3f, 0x80, 0, 0, 0, 0, 0, 0, 0x3e, 0x80, 0, 0},
			{0x3f, 0x80, 0, 0, 0x3f, 0, 0, 0, 0x3e, 0x80, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 0, 0x3e, 0x80, 0, 0, 0x3f, 0, 0, 0, 0x3f, 0x80, 0, 0},
		}
	default:
		return [][]byte{{20, 40, 60, 80}, {10, 30, 50, 70}, {1, 2, 3, 4}, {255, 128, 64, 0}, {0, 80, 160, 255}}
	}
}

func acceptanceLayer(version, depth, compression uint16, index int) (acceptanceFixture, []acceptanceFixture) {
	planes := acceptancePlanes(depth)
	channels := make([]acceptanceFixture, len(planes))
	for channel, plane := range planes {
		channels[channel] = acceptanceEncoded(version, compression, [][]byte{plane}, 2)
	}
	record := acceptanceNewFixture()
	for _, value := range []uint32{0, 0, 2, 2} {
		record.u32("", value)
	}
	record.u16("channelCount", uint16(len(channels)))
	ids := []uint16{0, 1, 2, 65535, 65534}
	for channel, data := range channels {
		record.u16("", ids[channel])
		record.length(fmt.Sprintf("channel%d/length", channel), acceptanceVersionWidth(version), len(data.data))
	}
	record.add("", []byte("8BIMnorm"))
	record.add("", []byte{123, 1, 10, 0})
	extra := acceptanceNewFixture()
	extra.u32("", 20)
	maskStart := len(extra.data)
	for _, value := range []uint32{0, 0, 2, 2} {
		extra.u32("", value)
	}
	extra.add("", []byte{255, 0, 0x91, 0x92})
	extra.mark("mask/data", maskStart)
	extra.mark("mask/span", 0)
	rangesStart := len(extra.data)
	extra.u32("", 8)
	extra.add("ranges/data", []byte{0, 0, 255, 255, 0, 0, 255, 255})
	extra.mark("ranges/span", rangesStart)
	extra.add("name", acceptancePascal([]byte{0x82, byte('A' + index)}, 4))
	extra.append("additional0/", acceptanceAdditional("8BIM", "U001", 4, 2, acceptancePayload([]byte{1, 3, 5})))
	extra.append("additional1/", acceptanceAdditional("8B64", "U002", 8, 2, acceptancePayload([]byte{9, 8, 7, 6})))
	extra.append("additional2/", acceptanceAdditional("8BIM", "U001", 4, 2, acceptancePayload([]byte{0x7f, 0})))
	extra.append("additional3/", acceptanceAdditional("8BIM", "luni", 4, 2,
		acceptancePayload(acceptanceUnicode(fmt.Sprintf("图层😀%d", index)))))
	record.u32("extra/length", uint32(len(extra.data)))
	extraStart := len(record.data)
	record.append("extra/", extra)
	record.mark("extra/data", extraStart)
	record.spans["extra/span"] = psdfile.Span{
		Offset: int64(extraStart - 4), Length: int64(len(extra.data) + 4),
	}
	record.mark("span", 0)
	return record, channels
}

func acceptanceLayerInfo(version, depth, compression uint16, layerCount int) acceptanceFixture {
	f := acceptanceNewFixture()
	f.u16("count", uint16(-int16(layerCount)))
	channelSets := make([][]acceptanceFixture, layerCount)
	for layer := range layerCount {
		record, channels := acceptanceLayer(version, depth, compression, layer)
		f.append(fmt.Sprintf("layer%d/", layer), record)
		channelSets[layer] = channels
	}
	for layer, channels := range channelSets {
		for channel, encoded := range channels {
			f.append(fmt.Sprintf("layer%d/channel%d/", layer, channel), encoded)
		}
	}
	start := len(f.data)
	if len(f.data)%2 != 0 {
		f.add("", []byte{0xb7})
	}
	f.mark("padding", start)
	f.mark("span", 0)
	return f
}

func acceptanceLinked() acceptanceFixture {
	entry := acceptanceNewFixture()
	entry.add("", []byte("liFD"))
	entry.u32("", 1)
	entry.add("", acceptancePascal([]byte("fixture-id"), 1))
	entry.add("", acceptanceUnicode("object.psd"))
	entry.add("", []byte("8BPS8BIM"))
	payload := bytes.Repeat([]byte{0xc3, 0x5a, 0x29, 0xe0}, 20_000)
	entry.u64("", uint64(len(payload)))
	entry.add("", []byte{0})
	entry.add("embedded", payload)
	f := acceptanceNewFixture()
	f.u64("", uint64(len(entry.data)))
	f.append("", entry)
	for len(entry.data)%4 != 0 {
		entry.add("", []byte{0})
		f.add("", []byte{0})
	}
	return f
}

func acceptanceDocument(version, depth, compression uint16, layerCount int, alternate string) acceptanceFixture {
	f := acceptanceNewFixture()
	f.add("", []byte("8BPS"))
	f.u16("version", version)
	f.add("", make([]byte, 6))
	f.u16("channels", 4)
	f.u32("height", 2)
	f.u32("width", 2)
	f.u16("depth", depth)
	f.u16("mode", 3)
	f.mark("header", 0)
	f.u32("colorLength", 0)
	f.spans["color"] = psdfile.Span{Offset: 26, Length: 4}
	resources := acceptanceNewFixture()
	resources.append("resource0/", acceptanceResource(30000, []byte{0x82, 'R'}, []byte{9, 7, 5}))
	resources.append("resource1/", acceptanceResource(30001, nil, []byte{2, 4, 6, 8}))
	resources.append("resource2/", acceptanceResource(30000, []byte("same"), []byte{0x7a}))
	resourceStart := len(f.data)
	f.u32("resourcesLength", uint32(len(resources.data)))
	f.append("resources/", resources)
	f.mark("resources", resourceStart)
	section := acceptanceNewFixture()
	info := acceptanceLayerInfo(version, depth, compression, layerCount)
	if alternate == "" {
		section.length("layerInfoLength", acceptanceVersionWidth(version), len(info.data))
		section.append("info/", info)
		section.mark("layerInfo", 0)
	} else {
		section.length("layerInfoLength", acceptanceVersionWidth(version), 0)
		section.mark("layerInfo", 0)
	}
	maskStart := len(section.data)
	section.u32("", 16)
	section.add("globalMaskData", []byte{0, 0, 0, 1, 0, 2, 0, 3, 0, 4, 0, 100, 128, 0, 0, 0})
	section.mark("globalMask", maskStart)
	section.append("global0/", acceptanceAdditional("8BIM", "G001", 4, 4,
		acceptancePayload([]byte{1, 2, 3})))
	linkedWidth := 4
	if version == 2 {
		linkedWidth = 8
	}
	section.append("global1/", acceptanceAdditional("8BIM", "lnk2", linkedWidth, 4, acceptanceLinked()))
	section.append("global2/", acceptanceAdditional("8B64", "G001", 8, 4,
		acceptancePayload([]byte{4, 5, 6, 7})))
	if alternate != "" {
		section.append("alternate/", acceptanceAdditional("8BIM", alternate,
			acceptanceVersionWidth(version), 4, info))
	}
	layerStart := len(f.data)
	f.length("layerMaskLength", acceptanceVersionWidth(version), len(section.data))
	f.append("layers/", section)
	f.mark("layerMask", layerStart)
	f.append("composite/", acceptanceEncoded(version, compression, acceptancePlanes(depth)[:4], 2))
	f.mark("source", 0)
	return f
}

func acceptanceFlat(version, depth, mode, channels uint16, color []byte) acceptanceFixture {
	f := acceptanceNewFixture()
	f.add("", []byte("8BPS"))
	f.u16("version", version)
	f.add("", make([]byte, 6))
	f.u16("channels", channels)
	f.u32("height", 2)
	f.u32("width", 2)
	f.u16("depth", depth)
	f.u16("mode", mode)
	f.mark("header", 0)
	f.u32("colorLength", uint32(len(color)))
	f.add("color/data", color)
	f.spans["color"] = psdfile.Span{Offset: 26, Length: int64(len(color) + 4)}
	f.u32("resourcesLength", 0)
	f.length("layerMaskLength", acceptanceVersionWidth(version), 0)
	planes := make([][]byte, channels)
	rowBytes := (2*int(depth) + 7) / 8
	for index := range planes {
		planes[index] = bytes.Repeat([]byte{byte(33 + index)}, rowBytes*2)
	}
	f.append("composite/", acceptanceEncoded(version, 0, planes, 2))
	f.mark("source", 0)
	return f
}

type acceptanceReader struct {
	reader io.ReaderAt
	reads  []psdfile.Span
	closed bool
}

func (r *acceptanceReader) ReadAt(data []byte, offset int64) (int, error) {
	r.reads = append(r.reads, psdfile.Span{Offset: offset, Length: int64(len(data))})
	return r.reader.ReadAt(data, offset)
}

func (r *acceptanceReader) Close() error {
	r.closed = true
	return nil
}

func acceptanceOverlaps(left, right psdfile.Span) bool {
	return left.Length > 0 && right.Length > 0 &&
		left.Offset < right.Offset+right.Length && right.Offset < left.Offset+left.Length
}
