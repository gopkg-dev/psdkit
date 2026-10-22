package psdfile

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
)

func TestPhotoshopGeneratedEmptyMaskSentinel(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, flags := range []byte{0, 8} {
			var mask bytes.Buffer
			for _, value := range []int32{0, 0, -1, 0} {
				indexTestNumber(&mask, value)
			}
			mask.Write([]byte{0, flags, 0xaa, 0xbb})
			layers := indexTestLayerSet(version, image.Rect(0, 0, 1, 1), []int16{0, -2},
				[][]byte{{0, 0, 20}, {0, 1}}, mask.Bytes(), nil)
			data := indexTestDocument(version, 8, 1, nil, indexTestGlobal(version, layers, nil), []byte{0, 0, 20})
			document, err := Parse(data)
			if flags == 0 {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("unmarked negative rectangle accepted: %v", err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := document.Layers[0].Mask.Bounds; got != (image.Rectangle{}) {
				t.Fatalf("sentinel must have empty interpreted geometry: %v", got)
			}
			plane, err := document.DecodeChannel(context.Background(), 0, 1)
			if err != nil || len(plane.Data) != 0 || plane.Width != 0 || plane.Height != 0 {
				t.Fatalf("empty generated plane = %+v, %v", plane, err)
			}
			var copied bytes.Buffer
			if _, err := document.CopySpan(context.Background(), &copied, document.Layers[0].Mask.Span); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(copied.Bytes(), mask.Bytes()) {
				t.Fatal("sentinel encoding was not retained exactly")
			}
			_ = document.Close()
		}
	}
}
