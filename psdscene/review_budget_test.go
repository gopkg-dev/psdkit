package psdscene_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func reviewLongNameSource(units int) []byte {
	var name bytes.Buffer
	_ = binary.Write(&name, binary.BigEndian, uint32(units))
	for range units {
		_ = binary.Write(&name, binary.BigEndian, uint16(0x4e00))
	}
	var extra bytes.Buffer
	for _, value := range []uint32{0, 0} {
		_ = binary.Write(&extra, binary.BigEndian, value)
	}
	extra.Write([]byte{1, 'L', 0, 0})
	extra.WriteString("8BIMluni")
	_ = binary.Write(&extra, binary.BigEndian, uint32(name.Len()))
	extra.Write(name.Bytes())
	var layerInfo bytes.Buffer
	_ = binary.Write(&layerInfo, binary.BigEndian, int16(1))
	for _, value := range []int32{0, 0, 1, 1} {
		_ = binary.Write(&layerInfo, binary.BigEndian, value)
	}
	_ = binary.Write(&layerInfo, binary.BigEndian, uint16(3))
	for channel := range 3 {
		_ = binary.Write(&layerInfo, binary.BigEndian, int16(channel))
		_ = binary.Write(&layerInfo, binary.BigEndian, uint32(3))
	}
	layerInfo.WriteString("8BIMnorm")
	layerInfo.Write([]byte{255, 0, 0, 0})
	_ = binary.Write(&layerInfo, binary.BigEndian, uint32(extra.Len()))
	layerInfo.Write(extra.Bytes())
	for channel := range 3 {
		layerInfo.Write([]byte{0, 0, byte(40 + channel)})
	}
	if layerInfo.Len()%2 != 0 {
		layerInfo.WriteByte(0)
	}
	var layerMask bytes.Buffer
	_ = binary.Write(&layerMask, binary.BigEndian, uint32(layerInfo.Len()))
	layerMask.Write(layerInfo.Bytes())
	_ = binary.Write(&layerMask, binary.BigEndian, uint32(0))
	var data bytes.Buffer
	data.WriteString("8BPS")
	_ = binary.Write(&data, binary.BigEndian, uint16(1))
	data.Write(make([]byte, 6))
	for _, value := range []any{uint16(3), uint32(1), uint32(1), uint16(8), uint16(3),
		uint32(0), uint32(0), uint32(layerMask.Len())} {
		_ = binary.Write(&data, binary.BigEndian, value)
	}
	data.Write(layerMask.Bytes())
	data.Write([]byte{0, 0, 40, 41, 42})
	return data.Bytes()
}

func TestReviewRGB8SnapshotBudgetsRetainedUnicodeName(t *testing.T) {
	data := reviewLongNameSource(100_000)
	source, err := psdfile.Parse(data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 8192}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if len(source.Layers) != 1 || len(source.Layers[0].Name) != 300_000 {
		t.Fatal("fixture does not carry the intended large retained Unicode name")
	}
	if _, err := source.Decode(nil); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("300KB owned snapshot name bypassed an 8KB decode budget: %v", err)
	}
}

func TestReviewSceneBudgetsSnapshotNames(t *testing.T) {
	source, err := psdfile.Parse(reviewLongNameSource(100_000))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if _, err := psdscene.Render(nil, source, psdscene.Options{MaxWorkingBytes: 8192}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("scene cloned a 300KB retained name outside an 8KB compound budget: %v", err)
	}
}

func TestReviewRGB8SnapshotBudgetsLinkedNames(t *testing.T) {
	var entry bytes.Buffer
	entry.WriteString("liFD")
	_ = binary.Write(&entry, binary.BigEndian, uint32(1))
	entry.Write([]byte{1, 'I'})
	_ = binary.Write(&entry, binary.BigEndian, uint32(100_000))
	for range 100_000 {
		_ = binary.Write(&entry, binary.BigEndian, uint16(0x4e00))
	}
	entry.WriteString("PSD 8BIM")
	_ = binary.Write(&entry, binary.BigEndian, uint64(0))
	entry.WriteByte(0)
	var linked bytes.Buffer
	_ = binary.Write(&linked, binary.BigEndian, uint64(entry.Len()))
	linked.Write(entry.Bytes())
	for (linked.Len()-8)%4 != 0 {
		linked.WriteByte(0)
	}
	var section bytes.Buffer
	_ = binary.Write(&section, binary.BigEndian, uint32(0))
	_ = binary.Write(&section, binary.BigEndian, uint32(0))
	section.WriteString("8BIMlnk2")
	_ = binary.Write(&section, binary.BigEndian, uint32(linked.Len()))
	section.Write(linked.Bytes())
	var data bytes.Buffer
	data.WriteString("8BPS")
	_ = binary.Write(&data, binary.BigEndian, uint16(1))
	data.Write(make([]byte, 6))
	for _, value := range []any{uint16(3), uint32(1), uint32(1), uint16(8), uint16(3),
		uint32(0), uint32(0), uint32(section.Len())} {
		_ = binary.Write(&data, binary.BigEndian, value)
	}
	data.Write(section.Bytes())
	data.Write([]byte{0, 0, 40, 41, 42})
	source, err := psdfile.Parse(data.Bytes(), psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 8192}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if _, err := source.Decode(nil); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("300KB LinkedNames map string bypassed an 8KB snapshot budget: %v", err)
	}
}
