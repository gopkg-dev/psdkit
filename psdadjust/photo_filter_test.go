package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"
)

func photoFilterFixture(version uint16, density uint32, padding int) []byte {
	data := binary.BigEndian.AppendUint16(nil, version)
	if version == 2 {
		for _, word := range []uint16{7, 4494, 1800, 53236, 0} {
			data = binary.BigEndian.AppendUint16(data, word)
		}
	} else {
		for _, word := range []uint32{0x0000ffff, 0x80000001, 0xffffffff} {
			data = binary.BigEndian.AppendUint32(data, word)
		}
	}
	data = binary.BigEndian.AppendUint32(data, density)
	return append(append(data, 1), make([]byte, padding)...)
}

func TestPhotoFilterWireWordsPaddingAndControlledEdits(t *testing.T) {
	for _, version := range []uint16{2, 3} {
		for padding := range 4 {
			data := photoFilterFixture(version, 25, padding)
			adjustment, err := Decode(nil, "phfl", data, DecodeOptions{SourceOffset: 913})
			if err != nil {
				t.Fatal(err)
			}
			if adjustment.Kind != PhotoFilter || adjustment.Version != version || adjustment.Span.Offset != 913 ||
				adjustment.PhotoFilter.Density != 25 || !adjustment.PhotoFilter.PreserveLuminosity {
				t.Fatalf("wire parameters lost: %+v", adjustment)
			}
			if version == 2 && adjustment.PhotoFilter.Components != ([4]uint16{4494, 1800, 53236, 0}) ||
				version == 3 && adjustment.PhotoFilter.XYZWords != ([3]uint32{0xffff, 0x80000001, 0xffffffff}) {
				t.Fatal("native color words changed signedness or precision")
			}
			encoded, err := Encode(nil, *adjustment)
			if err != nil || !bytes.Equal(encoded, data) {
				t.Fatalf("unchanged wire bytes lost: %v", err)
			}
			adjustment.PhotoFilter.Density = 100
			adjustment.PhotoFilter.PreserveLuminosity = false
			encoded, err = Encode(nil, *adjustment)
			if err != nil {
				t.Fatal(err)
			}
			expected := bytes.Clone(data)
			densityOffset := 12
			if version == 3 {
				densityOffset = 14
			}
			binary.BigEndian.PutUint32(expected[densityOffset:], 100)
			expected[densityOffset+4] = 0
			if !bytes.Equal(encoded, expected) || !bytes.Equal(adjustment.Raw, data) {
				t.Fatal("editing controls changed color, padding or owned original")
			}
			if _, err := Compile(*adjustment); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("nonzero photo filter was approximated: %v", err)
			}
		}
	}
}

func TestPhotoFilterNeutralIdentityAndResourceGuards(t *testing.T) {
	source := image.NewNRGBA(image.Rect(11, 17, 15, 18))
	for index, value := range []color.NRGBA{{R: 1, G: 12, B: 254, A: 0}, {R: 254, G: 1, B: 12, A: 1},
		{R: 30, G: 70, B: 130, A: 128}, {R: 255, G: 255, B: 255, A: 255}} {
		source.SetNRGBA(index+11, 17, value)
	}
	for _, version := range []uint16{2, 3} {
		data := photoFilterFixture(version, 0, 0)
		adjustment, err := Decode(nil, "phfl", data)
		if err != nil {
			t.Fatal(err)
		}
		output, err := Apply(nil, source, *adjustment, Options{MaxPixels: 4, MaxWorkingBytes: 144})
		if err != nil || output == nil || output.Rect != source.Rect || !bytes.Equal(output.Pix, source.Pix) {
			t.Fatalf("neutral changed exact hidden RGBA or geometry: %v", err)
		}
		output.Pix[0] = 200
		if source.Pix[0] != 1 {
			t.Fatal("neutral output aliases caller source")
		}
		for _, options := range []Options{{MaxPixels: 3}, {MaxWorkingBytes: 143}} {
			if output, err := Apply(nil, source, *adjustment, options); output != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("neutral output exceeded exact budget: %v %v", output, err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if output, err := Apply(ctx, source, *adjustment); output != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("neutral returned partial output after cancellation: %v", err)
		}
		if data, err := Encode(ctx, *adjustment); data != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("encoding swallowed cancellation: %v", err)
		}
	}
}

func TestPhotoFilterMalformedVersionFlagsTailAndRepresentation(t *testing.T) {
	for _, version := range []uint16{2, 3} {
		data := photoFilterFixture(version, 0, 0)
		for length := range data {
			if _, err := Decode(nil, "phfl", data[:length]); err == nil {
				t.Fatalf("accepted truncated payload length %d", length)
			}
		}
		flag := bytes.Clone(data)
		flag[len(flag)-1] = 2
		if _, err := Decode(nil, "phfl", flag); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted nonboolean preserve flag: %v", err)
		}
		unknown := append(bytes.Clone(data), 0x80)
		if retained, err := Decode(nil, "phfl", unknown); retained == nil || !bytes.Equal(retained.Raw, unknown) ||
			!errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown tail lost or admitted: %v", err)
		}
		adjustment, err := Decode(nil, "phfl", data)
		if err != nil {
			t.Fatal(err)
		}
		adjustment.PhotoFilter.Density = 101
		if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrInvalid) {
			t.Fatalf("out-of-range density encoded: %v", err)
		}
		adjustment.PhotoFilter.Density = 0
		if version == 2 {
			adjustment.PhotoFilter.XYZWords[0] = 1
		} else {
			adjustment.PhotoFilter.Components[0] = 1
		}
		if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrInvalid) {
			t.Fatalf("mixed wire representations encoded: %v", err)
		}
	}
}
