// Package webfixtures constructs small, original test assets for browser protocol verification.
package webfixtures

import (
	"encoding/binary"
	"slices"
	"unicode/utf16"
)

// Font returns an original synthetic TTF containing A and V, not a redistributable system font.
func Font() []byte {
	u16 := func(value uint16) []byte { return binary.BigEndian.AppendUint16(nil, value) }
	u32 := func(value uint32) []byte { return binary.BigEndian.AppendUint32(nil, value) }
	head, maxp, hhea := make([]byte, 54), make([]byte, 32), make([]byte, 36)
	copy(head, u32(0x00010000))
	copy(head[12:], u32(0x5f0f3cf5))
	copy(head[18:], u16(1000))
	copy(maxp, u32(0x00010000))
	copy(maxp[4:], u16(3))
	copy(hhea, u32(0x00010000))
	copy(hhea[4:], []byte{3, 32, 0xff, 0x38, 0, 100})
	copy(hhea[34:], u16(3))
	hmtx := []byte{}
	for _, advance := range []uint16{600, 600, 650} {
		hmtx = append(hmtx, u16(advance)...)
		hmtx = append(hmtx, 0, 0)
	}
	glyf, loca := []byte{}, []byte{}
	for _, offCurve := range []bool{false, true} {
		loca = append(loca, u16(uint16(len(glyf)/2))...)
		glyph := []byte{0, 1, 0, 0, 0, 0, 3, 232, 3, 232, 0, 2, 0, 0, 1}
		flag := byte(1)
		if offCurve {
			flag = 0
		}
		glyph = append(glyph, flag, 1)
		for _, delta := range []int16{0, 500, 500, 0, 1000, -1000} {
			glyph = append(glyph, u16(uint16(delta))...)
		}
		glyph = append(glyph, 0)
		glyf = append(glyf, glyph...)
	}
	loca = append([]byte{0, 0}, loca...)
	loca = append(loca, u16(uint16(len(glyf)/2))...)
	cmap := []byte{0, 0, 0, 1, 0, 3, 0, 10, 0, 0, 0, 12, 0, 12, 0, 0}
	cmap = append(cmap, u32(40)...)
	cmap = append(cmap, u32(0)...)
	cmap = append(cmap, u32(2)...)
	for index, code := range []uint32{65, 86} {
		cmap = append(cmap, u32(code)...)
		cmap = append(cmap, u32(code)...)
		cmap = append(cmap, u32(uint32(index+1))...)
	}
	nameValue := []byte{}
	for _, unit := range utf16.Encode([]rune("BridgeFixture-Regular")) {
		nameValue = append(nameValue, u16(unit)...)
	}
	name := []byte{0, 0, 0, 1, 0, 18, 0, 3, 0, 1, 4, 9, 0, 6}
	name = append(name, u16(uint16(len(nameValue)))...)
	name = append(name, 0, 0)
	name = append(name, nameValue...)
	tables := map[string][]byte{"head": head, "maxp": maxp, "hhea": hhea, "hmtx": hmtx,
		"loca": loca, "glyf": glyf, "cmap": cmap, "name": name}
	tags := []string{}
	for tag := range tables {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	output := make([]byte, 12+len(tags)*16)
	copy(output, u32(0x00010000))
	copy(output[4:], u16(uint16(len(tags))))
	for index, tag := range tags {
		position := 12 + index*16
		copy(output[position:], tag)
		copy(output[position+8:], u32(uint32(len(output))))
		copy(output[position+12:], u32(uint32(len(tables[tag]))))
		output = append(output, tables[tag]...)
		for len(output)%4 != 0 {
			output = append(output, 0)
		}
	}
	return output
}
