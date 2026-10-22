package psdparse

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestKnockoutTypedPSDAndPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, knockout := range []psdmodel.Knockout{0, 1, 2, 255} {
			t.Run(fmt.Sprintf("version%d_value%d", version, knockout), func(t *testing.T) {
				document, err := ParseBytes(semanticsDocument(version, nil, nil,
					semanticsBlock{"knko", []byte{byte(knockout), 0, 0, 0}}))
				if err != nil {
					t.Fatal(err)
				}
				layer := document.Layers[0]
				if layer.Knockout != knockout || len(layer.Unsupported) != 0 || layer.Background {
					t.Fatalf("Knockout, unknown or ordinary opaque layer metadata lost: %+v", layer)
				}
			})
		}
	}
	for _, data := range [][]byte{nil, {1}, {1, 0, 0}, {1, 0, 0, 0, 0}} {
		if _, err := ParseBytes(semanticsDocument(1, nil, nil, semanticsBlock{"knko", data})); err == nil {
			t.Fatalf("malformed Knockout setting accepted: %x", data)
		}
	}
}

func TestChannelRestrictionsTypedPSDAndPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		data := make([]byte, 12)
		for index, channel := range []uint32{2, 0, 1000} {
			binary.BigEndian.PutUint32(data[index*4:], channel)
		}
		document, err := ParseBytes(semanticsDocument(version, nil, nil, semanticsBlock{"brst", data}))
		if err != nil {
			t.Fatal(err)
		}
		channels := document.Layers[0].ChannelRestrictions
		if len(channels) != 3 || channels[0] != 2 || channels[1] != 0 || channels[2] != 1000 ||
			len(document.Layers[0].Unsupported) != 0 {
			t.Fatalf("unknown channel number or channel order was dropped: %v", channels)
		}
		data[3] = 1
		if channels[0] != 2 {
			t.Fatal("channel restrictions borrow caller bytes")
		}
	}
	for _, data := range [][]byte{{0}, {0, 0}, {0, 0, 0}, make([]byte, 57*4)} {
		if _, err := ParseBytes(semanticsDocument(1, nil, nil, semanticsBlock{"brst", data})); err == nil {
			t.Fatalf("malformed channel restrictions accepted: length=%d", len(data))
		}
	}
}

func TestBackgroundIdentifiedBySourceTransparencyChannel(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, transparency := range []bool{false, true} {
			document, err := ParseBytes(backgroundFixture(version, transparency))
			if err != nil {
				t.Fatal(err)
			}
			layer := document.Layers[0]
			if layer.Background == transparency || layer.Name != "Renamed" || layer.Pixels.Pix[3] != 255 {
				t.Fatalf("background version=%d transparency=%t layer=%+v", version, transparency, layer)
			}
		}
	}
}

// backgroundFixture independently builds opaque source layers whose only difference is channel -1.
func backgroundFixture(version uint16, transparency bool) []byte {
	values := []byte{19, 89, 139}
	ids := []uint16{0, 1, 2}
	if transparency {
		values, ids = append(values, 255), append(ids, 65535)
	}
	record := &psbWriter{}
	for _, edge := range []uint32{0, 0, 1, 1} {
		record.u32(edge)
	}
	record.u16(uint16(len(ids)))
	for _, id := range ids {
		record.u16(id)
		record.length(version, 3)
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	extra := &psbWriter{}
	extra.u32(0)
	extra.u32(0)
	extra.Write([]byte{7, 'R', 'e', 'n', 'a', 'm', 'e', 'd'})
	record.block(extra.Bytes())
	info := &psbWriter{}
	info.u16(1)
	info.Write(record.Bytes())
	for _, value := range values {
		info.Write([]byte{0, 0, value})
	}
	if info.Len()%2 != 0 {
		info.u8(0)
	}
	layers := &psbWriter{}
	layers.length(version, info.Len())
	layers.Write(info.Bytes())
	layers.u32(0)
	file := &psbWriter{}
	file.WriteString("8BPS")
	file.u16(version)
	file.Write(make([]byte, 6))
	file.u16(3)
	file.u32(1)
	file.u32(1)
	file.u16(8)
	file.u16(3)
	file.u32(0)
	file.u32(0)
	file.length(version, layers.Len())
	file.Write(layers.Bytes())
	file.Write([]byte{0, 0, 19, 89, 139})
	return file.Bytes()
}
