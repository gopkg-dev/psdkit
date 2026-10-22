package psdedit_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"unicode/utf16"
)

// Fixtures are constructed from Adobe's field layout, independently of the library writer.
// Deliberately nonzero padding and duplicate unknown keys expose accidental normalization.
type editAcceptanceFixtureOptions struct {
	version, depth, mode uint16
	count                int
	advanced             bool
	textCache            bool
	alternate            string
	parallel             bool
	unknownDivider       bool
}

func editAcceptanceLength(data []byte, version uint16, length int) []byte {
	if version == 2 {
		return binary.BigEndian.AppendUint64(data, uint64(length))
	}
	return binary.BigEndian.AppendUint32(data, uint32(length))
}

func editAcceptancePascal(value []byte, alignment int) []byte {
	data := append([]byte{byte(len(value))}, value...)
	for len(data)%alignment != 0 {
		data = append(data, 0xd7)
	}
	return data
}

func editAcceptanceUnicode(value string) []byte {
	units := utf16.Encode([]rune(value))
	data := binary.BigEndian.AppendUint32(nil, uint32(len(units)))
	for _, unit := range units {
		data = binary.BigEndian.AppendUint16(data, unit)
	}
	return data
}

func editAcceptanceTag(signature, key string, width, alignment int, payload []byte) []byte {
	data := []byte(signature + key)
	if width == 8 {
		data = binary.BigEndian.AppendUint64(data, uint64(len(payload)))
	} else {
		data = binary.BigEndian.AppendUint32(data, uint32(len(payload)))
	}
	data = append(data, payload...)
	for len(payload)%alignment != 0 {
		data = append(data, 0xa9)
		payload = append(payload, 0)
	}
	return data
}

func editAcceptanceResource(id uint16, name, payload []byte) []byte {
	data := binary.BigEndian.AppendUint16([]byte("8BIM"), id)
	data = append(data, editAcceptancePascal(name, 2)...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(payload)))
	data = append(data, payload...)
	if len(payload)%2 != 0 {
		data = append(data, 0xea)
	}
	return data
}

func editAcceptancePlaneData(depth uint16, channel, layer int) []byte {
	switch depth {
	case 1:
		return []byte{0x80, 0x40}
	case 16:
		return []byte{0x01, 0x23, 0xab, 0xcd, 0x45, 0x67, 0x89, 0xef}
	case 32:
		return []byte{
			0x3f, 0x80, 0, 0, 0x3f, 0, 0, 0,
			0x3e, 0x80, 0, 0, 0, 0, 0, 0,
		}
	}
	if channel == 3 {
		return []byte{255, 255, 255, 255}
	}
	return bytes.Repeat([]byte{byte(40 + channel*40 + layer*10)}, 4)
}

func editAcceptanceChannels(options editAcceptanceFixtureOptions) []int16 {
	if options.mode == 0 {
		return []int16{0}
	}
	channels := []int16{0, 1, 2, -1}
	if options.advanced {
		channels = append(channels, -2, 4)
	}
	return channels
}

func editAcceptanceLayer(options editAcceptanceFixtureOptions, index int) ([]byte, []byte) {
	var record []byte
	for _, coordinate := range []uint32{0, 0, 2, 2} {
		record = binary.BigEndian.AppendUint32(record, coordinate)
	}
	ids := editAcceptanceChannels(options)
	record = binary.BigEndian.AppendUint16(record, uint16(len(ids)))
	var pixels []byte
	for channel, id := range ids {
		plane := editAcceptancePlaneData(options.depth, channel, index)
		record = binary.BigEndian.AppendUint16(record, uint16(id))
		record = editAcceptanceLength(record, options.version, len(plane)+2)
		pixels = binary.BigEndian.AppendUint16(pixels, 0)
		pixels = append(pixels, plane...)
	}
	record = append(record, []byte("8BIMnorm")...)
	record = append(record, 255, 0, 0, 0)
	var extra []byte
	if options.advanced {
		extra = binary.BigEndian.AppendUint32(extra, 20)
		for _, coordinate := range []uint32{0, 0, 2, 2} {
			extra = binary.BigEndian.AppendUint32(extra, coordinate)
		}
		extra = append(extra, 255, 0, 0xb4, 0xb5)
		extra = binary.BigEndian.AppendUint32(extra, 8)
		extra = append(extra, 0, 0, 255, 255, 0, 0, 255, 255)
	} else {
		extra = binary.BigEndian.AppendUint32(extra, 0)
		extra = binary.BigEndian.AppendUint32(extra, 0)
	}
	extra = append(extra, editAcceptancePascal([]byte{0x82, byte('A' + index)}, 4)...)
	extra = append(extra, editAcceptanceTag("8BIM", "U001", 4, 2, []byte{7, 3, 9})...)
	extra = append(extra, editAcceptanceTag("8B64", "U002", 8, 2, []byte{0xb1, 0xb2})...)
	extra = append(extra, editAcceptanceTag("8BIM", "U001", 4, 2, []byte{5, 1})...)
	extra = append(extra, editAcceptanceTag("8BIM", "luni", 4, 2,
		editAcceptanceUnicode(fmt.Sprintf("源图层😀%d", index)))...)
	extra = append(extra, editAcceptanceTag("8BIM", "lyid", 4, 2,
		binary.BigEndian.AppendUint32(nil, uint32(17+index)))...)
	if options.textCache {
		extra = append(extra, editAcceptanceTag("8BIM", "TySh", 4, 2, []byte{1, 2, 3, 4})...)
	}
	if options.unknownDivider {
		extra = append(extra, editAcceptanceTag("8BIM", "lsct", 4, 2,
			binary.BigEndian.AppendUint32(nil, 99))...)
	}
	record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
	record = append(record, extra...)
	return record, pixels
}

func editAcceptanceLayerInfo(options editAcceptanceFixtureOptions) []byte {
	count := int16(options.count)
	if options.mode == 3 {
		count = -count
	}
	info := binary.BigEndian.AppendUint16(nil, uint16(count))
	var pixels []byte
	for index := range options.count {
		record, channels := editAcceptanceLayer(options, index)
		info = append(info, record...)
		pixels = append(pixels, channels...)
	}
	info = append(info, pixels...)
	if len(info)%2 != 0 {
		info = append(info, 0xe6)
	}
	return info
}

func editAcceptanceDocument(options editAcceptanceFixtureOptions) []byte {
	if options.version == 0 {
		options.version = 1
	}
	if options.depth == 0 {
		options.depth = 8
	}
	if options.depth != 1 && options.mode == 0 {
		options.mode = 3
	}
	if options.count == 0 {
		options.count = 1
	}
	channels := uint16(4)
	if options.mode == 0 {
		channels = 1
	}
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), options.version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, channels)
	data = binary.BigEndian.AppendUint32(data, 2)
	data = binary.BigEndian.AppendUint32(data, 2)
	data = binary.BigEndian.AppendUint16(data, options.depth)
	data = binary.BigEndian.AppendUint16(data, options.mode)
	data = binary.BigEndian.AppendUint32(data, 0)
	resources := editAcceptanceResource(30000, []byte{0x82, 'R'}, []byte{9, 7, 5})
	resources = append(resources, editAcceptanceResource(1044, nil,
		binary.BigEndian.AppendUint32(nil, 100))...)
	resources = append(resources, editAcceptanceResource(30001, nil, []byte{2, 4, 6, 8})...)
	resources = append(resources, editAcceptanceResource(30000, []byte("dup"), []byte{0x7a})...)
	resources = append(resources, editAcceptanceResource(1024, nil, []byte{0, 0})...)
	resources = append(resources, editAcceptanceResource(1026, nil, []byte{0, 0})...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(resources)))
	data = append(data, resources...)
	info := editAcceptanceLayerInfo(options)
	var section []byte
	if options.alternate == "" || options.parallel {
		section = editAcceptanceLength(section, options.version, len(info))
		section = append(section, info...)
	} else {
		section = editAcceptanceLength(section, options.version, 0)
	}
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, editAcceptanceTag("8BIM", "G001", 4, 4, []byte{1, 2, 3})...)
	linkedWidth := 4
	if options.version == 2 {
		linkedWidth = 8
	}
	section = append(section, editAcceptanceTag("8BIM", "lnk2", linkedWidth, 4,
		editAcceptanceLinked())...)
	section = append(section, editAcceptanceTag("8B64", "G001", 8, 4, []byte{4, 5, 6, 7})...)
	if options.alternate != "" {
		section = append(section, editAcceptanceTag("8BIM", options.alternate,
			4+(int(options.version)-1)*4, 4, info)...)
	}
	data = editAcceptanceLength(data, options.version, len(section))
	data = append(data, section...)
	data = binary.BigEndian.AppendUint16(data, 0)
	for channel := range int(channels) {
		data = append(data, editAcceptancePlaneData(options.depth, channel, 0)...)
	}
	return data
}

func editAcceptanceLinked() []byte {
	entry := binary.BigEndian.AppendUint32([]byte("liFD"), 1)
	entry = append(entry, editAcceptancePascal([]byte("fixture-reference"), 1)...)
	entry = append(entry, editAcceptanceUnicode("embedded.psd")...)
	entry = append(entry, []byte("8BPS8BIM")...)
	payload := bytes.Repeat([]byte{0xc3, 0x5a, 0x29, 0xe0}, 20_001)
	entry = binary.BigEndian.AppendUint64(entry, uint64(len(payload)))
	entry = append(entry, 0)
	entry = append(entry, payload...)
	result := binary.BigEndian.AppendUint64(nil, uint64(len(entry)))
	result = append(result, entry...)
	for len(entry)%4 != 0 {
		result = append(result, 0xae)
		entry = append(entry, 0)
	}
	return result
}

func editAcceptanceImage() *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	pixels.Pix = []byte{10, 50, 190, 255, 220, 90, 30, 128, 100, 210, 70, 64, 99, 77, 55, 0}
	return pixels
}
