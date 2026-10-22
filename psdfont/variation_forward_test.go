package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func variationTestFeatureRecords(records [][3]uint32) []byte {
	data := make([]byte, 128)
	binary.BigEndian.PutUint32(data[10:], 14)
	binary.BigEndian.PutUint32(data[14:], 0x10000)
	binary.BigEndian.PutUint32(data[18:], uint32(len(records)))
	for index, record := range records {
		position := 22 + index*8
		binary.BigEndian.PutUint32(data[position:], record[0])
		binary.BigEndian.PutUint32(data[position+4:], record[1])
		if record[1] != 0 {
			at := int(record[1]) + 14
			binary.BigEndian.PutUint32(data[at:], record[2])
			binary.BigEndian.PutUint16(data[at+4:], 1)
			binary.BigEndian.PutUint32(data[at+8:], 12)
		}
	}
	return data
}

func TestCFF2FeatureVariationForwardCompatibilityRecords(t *testing.T) {
	for _, row := range []struct {
		name     string
		records  [][3]uint32
		selected int
	}{
		{"universal-first", [][3]uint32{{0, 50, 0x10000}, {0, 80, 0x10000}}, 76},
		{"null-substitution-stops", [][3]uint32{{0, 0, 0}, {0, 50, 0x10000}}, -1},
		{"unknown-version-continues", [][3]uint32{{0, 50, 0x20000}, {0, 80, 0x10000}}, 106},
	} {
		t.Run(row.name, func(t *testing.T) {
			p := otParser{data: variationTestFeatureRecords(row.records), tag: "GSUB",
				b: &shapeBudget{ctx: context.Background(), maxNodes: 100, maxBytes: 1 << 20}}
			alternates, err := p.featureAlternates(0x10001, 1)
			if err != nil {
				t.Fatal(err)
			}
			at, exists := alternates[0]
			if row.selected < 0 && exists || row.selected >= 0 && (!exists || at != row.selected) {
				t.Fatalf("alternate%v expected%d", alternates, row.selected)
			}
		})
	}
	data := []byte{0, 1, 0, 0, 0, 6, 0, 1, 0, 1, 0, 0, 0x40, 0}
	p := otParser{data: data, tag: "GSUB", face: &Face{variation: &fontVariationData{normalized: []int16{0}}},
		b: &shapeBudget{ctx: context.Background(), maxNodes: 100, maxBytes: 1 << 20}}
	matched, err := p.conditionsMatch(0)
	if matched || err != nil {
		t.Fatalf("invalid axis must fail only this record: %v", err)
	}
}

type variationSnapshotContext struct {
	requests []VariationCoordinate
	calls    int
}

func (c *variationSnapshotContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *variationSnapshotContext) Done() <-chan struct{}       { return nil }
func (c *variationSnapshotContext) Value(key any) any           { return nil }
func (c *variationSnapshotContext) Err() error {
	c.calls++
	if c.calls == 1 {
		c.requests[0] = VariationCoordinate{Tag: "wdth", Value: 900}
	}
	return nil
}

func TestCFF2RequestSnapshotBeforeFirstContextCallback(t *testing.T) {
	cff2VerifyPortableFonts(t)
	requests := []VariationCoordinate{{Tag: "wght", Value: .5}}
	ctx := &variationSnapshotContext{requests: requests}
	face, err := Open(ctx, "testdata/cff2/fonts/InstancePair-v2.otf",
		DecodeOptions{Variations: requests})
	if err != nil || face == nil {
		t.Fatal(err)
	}
	coords, err := face.VariationCoordinates(nil)
	if err != nil || len(coords) != 2 || coords[0].Tag != "wght" || coords[0].Normalized != 8192 ||
		coords[1].Tag != "wdth" || coords[1].Normalized != 0 {
		t.Fatalf("entry request changed through first context callback: %+v %v", coords, err)
	}
	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := face.VariationCoordinates(ctxCanceled); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("getter cancellation must preserve original cause and nil result: %v", err)
	}
}
