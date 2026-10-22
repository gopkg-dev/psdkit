package psdfile

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
)

func rasterSource(data []byte) (*Document, LinkedFileRecord) {
	encoded := append(make([]byte, 13), data...)
	source := &Document{reader: bytes.NewReader(encoded), size: int64(len(encoded)), limits: DefaultLimits()}
	return source, LinkedFileRecord{Kind: "liFD", HasData: true,
		Data: Span{Offset: 13, Length: int64(len(data))}}
}

func rasterPNG(t *testing.T) ([]byte, *image.NRGBA) {
	t.Helper()
	pixels := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	pixels.Pix = []byte{22, 44, 88, 0, 40, 120, 200, 63, 250, 190, 30, 255,
		30, 60, 180, 128, 8, 240, 17, 237, 255, 40, 200, 1}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes(), pixels
}

func TestLinkedRasterPNGOwnsStraightPixelsAndHonorsExactBudget(t *testing.T) {
	data, expected := rasterPNG(t)
	source, record := rasterSource(data)
	format, err := source.LinkedRasterFormat(nil, record)
	if err != nil || format != "png" {
		t.Fatalf("format %q/%v", format, err)
	}
	result, err := source.DecodeLinkedRaster(nil, record)
	if err != nil || !bytes.Equal(result.Pixels.Pix, expected.Pix) || len(result.ICCProfile) != 0 {
		t.Fatalf("straight RGBA changed: %v", err)
	}
	if _, err := source.DecodeLinkedRaster(nil, record,
		LinkedRasterOptions{MaxWorkingBytes: result.WorkingBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DecodeLinkedRaster(nil, record,
		LinkedRasterOptions{MaxWorkingBytes: result.WorkingBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("one-byte deficit ignored: %v", err)
	}
	if _, err := source.DecodeLinkedRaster(nil, record,
		LinkedRasterOptions{MaxPixels: 5}); !errors.Is(err, ErrLimit) {
		t.Fatalf("pixel limit ignored: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pixels.Pix, expected.Pix) {
		t.Fatal("owned pixels invalidated by Close")
	}
	if _, err := source.DecodeLinkedRaster(nil, record); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source accepted: %v", err)
	}
}

func TestLinkedRasterPalettePreservesTransparentRGB(t *testing.T) {
	palette := color.Palette{color.NRGBA{80, 190, 45, 0}, color.NRGBA{125, 25, 240, 63}}
	pixels := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	pixels.Pix = []byte{0, 1, 1, 0}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	source, record := rasterSource(encoded.Bytes())
	defer source.Close()
	result, err := source.DecodeLinkedRaster(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	for y := range 2 {
		for x := range 2 {
			expected := palette[pixels.ColorIndexAt(x, y)].(color.NRGBA)
			if result.Pixels.NRGBAAt(x, y) != expected {
				t.Fatalf("palette RGB changed at %d,%d", x, y)
			}
		}
	}
}

func pngProfileChunk(t *testing.T, profile []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(profile); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := append([]byte("Original\x00\x00"), compressed.Bytes()...)
	result := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	result = append(result, []byte("iCCP")...)
	result = append(result, data...)
	return binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(result[4:]))
}

func rasterICC() []byte {
	result := make([]byte, 132)
	binary.BigEndian.PutUint32(result, uint32(len(result)))
	copy(result[16:], "RGB ")
	copy(result[36:], "acsp")
	return result
}

func TestLinkedRasterPNGICCIsOwnedBoundedAndNotSilentlyApplied(t *testing.T) {
	data, expected := rasterPNG(t)
	profile := rasterICC()
	chunk := pngProfileChunk(t, profile)
	encoded := append(bytes.Clone(data[:33]), chunk...)
	encoded = append(encoded, data[33:]...)
	source, record := rasterSource(encoded)
	defer source.Close()
	decoded, err := source.DecodeLinkedRaster(nil, record)
	if err != nil || !bytes.Equal(decoded.ICCProfile, profile) || !bytes.Equal(decoded.Pixels.Pix, expected.Pix) {
		t.Fatalf("ICC changed pixel encoding or bytes: %v", err)
	}
	decoded.ICCProfile[0] = 0xff
	again, err := source.DecodeLinkedRaster(nil, record)
	if err != nil || !bytes.Equal(again.ICCProfile, profile) {
		t.Fatalf("ICC output aliases source: %v", err)
	}
	for _, options := range []LinkedRasterOptions{{MaxProfileBytes: 131},
		{MaxWorkingBytes: decoded.WorkingBytes - 1}} {
		if _, err := source.DecodeLinkedRaster(nil, record, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("ICC budget ignored: %v", err)
		}
	}
	duplicate := append(bytes.Clone(data[:33]), chunk...)
	duplicate = append(duplicate, chunk...)
	duplicate = append(duplicate, data[33:]...)
	invalid, entry := rasterSource(duplicate)
	defer invalid.Close()
	if _, err := invalid.DecodeLinkedRaster(nil, entry); !errors.Is(err, ErrMalformed) {
		t.Fatalf("duplicate profile accepted: %v", err)
	}
}

func jpegICCChunk(profile []byte, ordinal, count byte) []byte {
	body := append([]byte("ICC_PROFILE\x00"), ordinal, count)
	body = append(body, profile...)
	result := binary.BigEndian.AppendUint16([]byte{0xff, 0xe2}, uint16(len(body)+2))
	return append(result, body...)
}

func TestLinkedRasterJPEGIndependentCodecAndOrderedICCFragments(t *testing.T) {
	_, pixels := rasterPNG(t)
	// Constant chroma independently checks the codec without depending on an upsampling convention.
	for position := 0; position < len(pixels.Pix); position += 4 {
		copy(pixels.Pix[position:position+4], []byte{128, 128, 128, 255})
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, pixels, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	profile := rasterICC()
	// Fragment order in the encoded file deliberately differs from its logical ICC order.
	data := append(bytes.Clone(encoded.Bytes()[:2]), jpegICCChunk(profile[60:], 2, 2)...)
	data = append(data, jpegICCChunk(profile[:60], 1, 2)...)
	data = append(data, encoded.Bytes()[2:]...)
	source, record := rasterSource(data)
	defer source.Close()
	decoded, err := source.DecodeLinkedRaster(nil, record)
	if err != nil || decoded.Format != "jpeg" || !bytes.Equal(decoded.ICCProfile, profile) {
		t.Fatalf("fragment decoding failed: %v", err)
	}
	reference, err := jpeg.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < pixels.Rect.Dy(); y++ {
		for x := 0; x < pixels.Rect.Dx(); x++ {
			expected := color.NRGBAModel.Convert(reference.At(x, y)).(color.NRGBA)
			if decoded.Pixels.NRGBAAt(x, y) != expected {
				t.Fatalf("JPEG conversion changed %d,%d", x, y)
			}
		}
	}
	if _, err := source.DecodeLinkedRaster(nil, record,
		LinkedRasterOptions{MaxProfileBytes: 131}); !errors.Is(err, ErrLimit) {
		t.Fatalf("JPEG profile limit ignored: %v", err)
	}
	missing := append(bytes.Clone(encoded.Bytes()[:2]), jpegICCChunk(profile[60:], 2, 2)...)
	missing = append(missing, encoded.Bytes()[2:]...)
	invalid, entry := rasterSource(missing)
	defer invalid.Close()
	if _, err := invalid.DecodeLinkedRaster(nil, entry); !errors.Is(err, ErrMalformed) {
		t.Fatalf("missing fragment accepted: %v", err)
	}
}

func TestLinkedJPEGCenteredTriangleHasIndependentFourPixelValues(t *testing.T) {
	source := image.NewYCbCr(image.Rect(0, 0, 4, 1), image.YCbCrSubsampleRatio422)
	source.Y = []byte{128, 128, 128, 128}
	source.Cb = []byte{64, 192}
	source.Cr = []byte{128, 128}
	destination := image.NewNRGBA(source.Rect)
	if err := renderLinkedJPEG(context.Background(), destination, source, true); err != nil {
		t.Fatal(err)
	}
	for x, expected := range []color.NRGBA{{128, 150, 15, 255}, {128, 139, 71, 255},
		{128, 117, 185, 255}, {128, 106, 241, 255}} {
		if destination.NRGBAAt(x, 0) != expected {
			t.Fatalf("quarter-sample interpolation differs at %d", x)
		}
	}
}

func TestLinkedRasterCancellationMalformedAndCause(t *testing.T) {
	data, _ := rasterPNG(t)
	source, record := rasterSource(data)
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.DecodeLinkedRaster(ctx, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	invalid, entry := rasterSource(data[:len(data)-10])
	defer invalid.Close()
	if _, err := invalid.DecodeLinkedRaster(nil, entry); !errors.Is(err, ErrMalformed) ||
		!errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated cause lost: %v", err)
	}
	if _, err := source.DecodeLinkedRaster(nil, record, LinkedRasterOptions{}, LinkedRasterOptions{}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("multiple options accepted: %v", err)
	}
	if _, err := (*Document)(nil).DecodeLinkedRaster(nil, record); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nil source accepted: %v", err)
	}
}

type failedRasterRead struct {
	reader *bytes.Reader
	cause  error
}

func (reader failedRasterRead) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.reader.ReadAt(data, offset)
	if err == nil && count > 0 {
		return count, reader.cause
	}
	return count, err
}

func TestLinkedRasterPreservesFullReadNonEOFCause(t *testing.T) {
	pngData, pixels := rasterPNG(t)
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, pixels, nil); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("source transport failed after providing bytes")
	for _, encoded := range [][]byte{pngData, jpegData.Bytes()} {
		source, record := rasterSource(encoded)
		source.reader = failedRasterRead{reader: source.reader.(*bytes.Reader), cause: cause}
		if result, err := source.DecodeLinkedRaster(nil, record); result != nil || !errors.Is(err, cause) {
			t.Fatalf("decoder swallowed accompanying source error: %v/%v", result, err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
