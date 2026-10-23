package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestDecodeRasterContentsOwnsPixelsAndProfileAcrossFormats(t *testing.T) {
	pngData, pixels := rasterPNG(t)
	profile := rasterICC()
	pngData = append(append(bytes.Clone(pngData[:33]), pngProfileChunk(t, profile)...), pngData[33:]...)
	var jpegData bytes.Buffer
	jpegPixels := image.NewNRGBA(pixels.Rect)
	for position := 0; position < len(jpegPixels.Pix); position += 4 {
		copy(jpegPixels.Pix[position:position+4], []byte{128, 128, 128, 255})
	}
	if err := jpeg.Encode(&jpegData, jpegPixels, nil); err != nil {
		t.Fatal(err)
	}
	jpegBytes := append(append(bytes.Clone(jpegData.Bytes()[:2]), jpegICCChunk(profile, 1, 1)...),
		jpegData.Bytes()[2:]...)
	tiffData := tiffTestData(binary.LittleEndian, 4, 1, 2, [][]byte{pixels.Pix}, profile)
	for _, sample := range []struct {
		format string
		data   []byte
	}{{"png", pngData}, {"jpeg", jpegBytes}, {"tiff", tiffData}} {
		t.Run(sample.format, func(t *testing.T) {
			encoded := bytes.Clone(sample.data)
			decoded, err := DecodeRasterContents(nil, encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Format != sample.format || decoded.Pixels.Rect != pixels.Rect ||
				!bytes.Equal(decoded.ICCProfile, profile) || !bytes.Equal(encoded, sample.data) {
				t.Fatal("standalone decoding changed the format, bounds, ICC or encoded input")
			}
			if sample.format != "jpeg" && !bytes.Equal(decoded.Pixels.Pix, pixels.Pix) {
				t.Fatal("lossless decoding changed alpha or hidden RGB")
			}
			if sample.format == "jpeg" {
				reference, err := jpeg.Decode(bytes.NewReader(jpegData.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				for y := range pixels.Rect.Dy() {
					for x := range pixels.Rect.Dx() {
						if decoded.Pixels.NRGBAAt(x, y) != color.NRGBAModel.Convert(reference.At(x, y)) {
							t.Fatal("JPEG conversion differs from the selected codec")
						}
					}
				}
			}
			retained := bytes.Clone(decoded.Pixels.Pix)
			clear(encoded)
			if !bytes.Equal(decoded.Pixels.Pix, retained) || !bytes.Equal(decoded.ICCProfile, profile) {
				t.Fatal("decoded output retained a borrowed input slice")
			}
			for _, settings := range []LinkedRasterOptions{
				{MaxWorkingBytes: decoded.WorkingBytes - 1}, {MaxProfileBytes: int64(len(profile) - 1)}, {MaxPixels: 5},
			} {
				if result, err := DecodeRasterContents(nil, sample.data, settings); result != nil || !errors.Is(err, ErrLimit) {
					t.Fatalf("standalone decoder ignored a lowered budget: %v", err)
				}
			}
			if _, err := DecodeRasterContents(nil, sample.data,
				LinkedRasterOptions{MaxWorkingBytes: decoded.WorkingBytes}); err != nil {
				t.Fatalf("exact decoder budget rejected: %v", err)
			}
		})
	}
}

func TestDecodeRasterContentsCancellationAndUnsupportedFormats(t *testing.T) {
	data, _ := rasterPNG(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := DecodeRasterContents(ctx, data); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled standalone decode returned pixels: %v", err)
	}
	for _, data := range [][]byte{nil, []byte("GIF89aunsupported"), []byte("II+\x00\x08\x00\x00\x00")} {
		if result, err := DecodeRasterContents(nil, data); result != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported standalone format returned pixels: %v", err)
		}
	}
	if result, err := DecodeRasterContents(nil, data[:len(data)-10]); result != nil || !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncated standalone image returned pixels: %v", err)
	}
	for _, settings := range [][]LinkedRasterOptions{
		{{MaxPixels: -1}}, {{MaxWorkingBytes: -1}}, {{MaxProfileBytes: -1}}, {{}, {}},
	} {
		if result, err := DecodeRasterContents(nil, data, settings...); result != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid standalone decode options were accepted: %v", err)
		}
	}
}
