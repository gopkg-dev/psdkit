package psdedit

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func smartContentsPNG(t *testing.T, pixels *image.NRGBA, profile []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	if len(profile) == 0 {
		return encoded.Bytes()
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(profile); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("Original\x00\x00"), compressed.Bytes()...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(payload)))
	chunk = append(chunk, "iCCP"...)
	chunk = append(chunk, payload...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	data := append(bytes.Clone(encoded.Bytes()[:33]), chunk...)
	return append(data, encoded.Bytes()[33:]...)
}

// smartContentsTIFF is a 2x2 RGB8 TIFF assembled from classic little-endian strip fields.
func smartContentsTIFF(pixels *image.NRGBA) []byte {
	const entries = 9
	const bitsOffset = 8 + 2 + entries*12 + 4
	const pixelsOffset = bitsOffset + 6
	fields := [][4]uint32{
		{256, 3, 1, 2}, {257, 3, 1, 2}, {258, 3, 3, bitsOffset}, {259, 3, 1, 1},
		{262, 3, 1, 2}, {273, 4, 1, pixelsOffset}, {277, 3, 1, 3}, {278, 4, 1, 2}, {279, 4, 1, 12},
	}
	data := []byte{'I', 'I', 42, 0, 8, 0, 0, 0}
	data = binary.LittleEndian.AppendUint16(data, entries)
	for _, field := range fields {
		data = binary.LittleEndian.AppendUint16(data, uint16(field[0]))
		data = binary.LittleEndian.AppendUint16(data, uint16(field[1]))
		data = binary.LittleEndian.AppendUint32(data, field[2])
		data = binary.LittleEndian.AppendUint32(data, field[3])
	}
	data = binary.LittleEndian.AppendUint32(data, 0)
	data = append(data, 8, 0, 8, 0, 8, 0)
	for position := 0; position < len(pixels.Pix); position += 4 {
		data = append(data, pixels.Pix[position:position+3]...)
	}
	return data
}

func TestSmartContentsSourceFormatsPreserveOriginalEncoding(t *testing.T) {
	pixels := smartEditImage(smartEditColors())
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, pixels, nil); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		fileType string
		data     []byte
	}{
		{"8BPS", smartEditPrototype(1, pixels)}, {"8BPB", smartEditPrototype(2, pixels)},
		{"PNGf", smartContentsPNG(t, pixels, nil)}, {"JPEG", jpegData.Bytes()}, {"TIFF", smartContentsTIFF(pixels)},
	} {
		t.Run(sample.fileType, func(t *testing.T) {
			original := bytes.Clone(sample.data)
			source, err := decodeSmartContentsSource(nil, sample.data, psdfile.DefaultLimits(), 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			if source.fileType != sample.fileType || source.width != 2 || source.height != 2 ||
				source.retainedBytes <= 0 || !bytes.Equal(sample.data, original) {
				t.Fatal("source adapter changed format, canvas, reservation or original encoded bytes")
			}
			result, err := source.render(nil, nil, false, 8<<20)
			if err != nil || result.Rect != pixels.Rect {
				t.Fatalf("source render lost its canvas: %v", err)
			}
			if sample.fileType != "JPEG" && !bytes.Equal(result.Pix, pixels.Pix) {
				t.Fatal("lossless source render changed opaque pixels")
			}
			if source.raster != nil && source.retainedBytes != int64(len(source.raster.Pixels.Pix)) {
				t.Fatal("raster retained reservation incorrectly kept transient codec scratch")
			}
			if source.document != nil && source.retainedBytes != source.document.IndexMetadataBytes() {
				t.Fatal("PSD retained reservation lost source indexing storage")
			}
			before := bytes.Clone(result.Pix)
			if err := source.close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.Pix, before) || source.retainedBytes != 0 {
				t.Fatal("closing adapter invalidated returned pixels or retained the reservation")
			}
			if _, err := source.render(nil, nil, false, 8<<20); !errors.Is(err, psdfile.ErrClosed) {
				t.Fatalf("closed adapter rendered source contents: %v", err)
			}
		})
	}
}

func TestSmartContentsSourceExplicitProfilePolicyAndConversionBudgets(t *testing.T) {
	pixels := smartEditImage(smartEditColors())
	pixels.Pix[3] = 0
	profile := rasterContentsProfile()
	encoded := smartContentsPNG(t, pixels, profile)
	source, err := decodeSmartContentsSource(nil, encoded, psdfile.DefaultLimits(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if source.retainedBytes != int64(len(pixels.Pix)+len(profile)) {
		t.Fatal("raster source reservation omitted retained profile bytes")
	}
	if result, err := source.render(nil, profile, false, 1); err != nil ||
		result != source.raster.Pixels || !bytes.Equal(result.Pix, pixels.Pix) {
		t.Fatalf("identical profile rendering changed encoded pixels or allocated another copy: %v", err)
	}
	if result, err := source.render(nil, nil, false, 8<<20); result != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("tagged raster silently dropped ICC when targeting an untagged parent: %v", err)
	}
	settings := psdcolor.ProfileTransformOptions{AssumeSRGB: true}
	budget, err := psdcolor.ProfileTransformWorkingBytes(nil, pixels, profile, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := psdcolor.TransformProfile(nil, pixels, profile, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.render(nil, nil, true, budget)
	if err != nil || !bytes.Equal(result.Pix, expected.Pix) || !bytes.Equal(source.raster.Pixels.Pix, pixels.Pix) ||
		!bytes.Equal(source.raster.ICCProfile, profile) {
		t.Fatalf("explicit profile transform changed its inputs or produced a different result: %v", err)
	}
	if result, err := source.render(nil, nil, true, budget-1); result != nil ||
		!errors.Is(err, psdfile.ErrLimit) || !errors.Is(err, psdcolor.ErrLimit) {
		t.Fatalf("transform ignored its exact working budget or discarded the profile error cause: %v", err)
	}
	untagged, err := decodeSmartContentsSource(nil, smartContentsPNG(t, pixels, nil), psdfile.DefaultLimits(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer untagged.close()
	if result, err := untagged.render(nil, profile, false, 8<<20); result != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("untagged raster silently assumed a working profile: %v", err)
	}
	if result, err := untagged.render(nil, nil, false, 1); err != nil || !bytes.Equal(result.Pix, pixels.Pix) {
		t.Fatalf("two untagged encodings changed without an explicit conversion: %v", err)
	}
	unsupported := bytes.Clone(profile)
	copy(unsupported[132:136], "A2B0")
	if result, err := source.render(nil, unsupported, false, 8<<20); result != nil ||
		!errors.Is(err, ErrUnsupported) || !errors.Is(err, psdcolor.ErrUnsupported) {
		t.Fatalf("unsupported profile conversion lost its cause or used a fallback: %v", err)
	}
}

func TestSmartContentsSourceDecodeBudgetsAndCancellation(t *testing.T) {
	pixels := smartEditImage(smartEditColors())
	pngData := smartContentsPNG(t, pixels, nil)
	decoded, err := psdfile.DecodeRasterContents(nil, pngData)
	if err != nil {
		t.Fatal(err)
	}
	limits := psdfile.DefaultLimits()
	for _, budget := range []int64{-1, 0, decoded.WorkingBytes - 1} {
		if source, err := decodeSmartContentsSource(nil, pngData, limits, budget); source != nil ||
			!errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("source decode restored a default or ignored its budget: %v", err)
		}
	}
	source, err := decodeSmartContentsSource(nil, pngData, limits, decoded.WorkingBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	for _, budget := range []int64{-1, 0} {
		if result, err := source.render(nil, nil, false, budget); result != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("source render restored an exhausted budget: %v", err)
		}
	}
	limits.MaxFileBytes = int64(len(pngData) - 1)
	if result, err := decodeSmartContentsSource(nil, pngData, limits, 8<<20); result != nil ||
		!errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("source decode ignored the encoded file size budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := source.render(ctx, nil, false, 8<<20); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rendering returned pixels: %v", err)
	}
	for _, data := range [][]byte{pngData, smartEditPrototype(1, pixels)} {
		if result, err := decodeSmartContentsSource(ctx, data, psdfile.DefaultLimits(), 8<<20); result != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("canceled source decode returned an adapter: %v", err)
		}
		inProgress := &rasterCancelContext{Context: context.Background(), after: 2}
		if result, err := decodeSmartContentsSource(inProgress, data, psdfile.DefaultLimits(), 8<<20); result != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("in-progress source cancellation was ignored: %v", err)
		}
	}
	result, err := decodeSmartContentsSource(nil, []byte("GIF89aunsupported"), psdfile.DefaultLimits(), 8<<20)
	if result != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported raster format was decoded or lost the edit error classification: %v", err)
	}
}

func TestSmartContentsSourcePSDProfileAndRGB8Gate(t *testing.T) {
	pixels := smartEditImage(smartEditColors())
	profile := rasterContentsProfile()
	data, err := BuildRasterContents(nil, pixels, RasterContentsOptions{ICCProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	source, err := decodeSmartContentsSource(nil, data, psdfile.DefaultLimits(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if result, err := source.render(nil, nil, false, 8<<20); result != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nil destination silently preserved a tagged source instead of targeting the untagged parent: %v", err)
	}
	result, err := source.render(nil, profile, false, 8<<20)
	if err != nil || !bytes.Equal(result.Pix, pixels.Pix) {
		t.Fatalf("PSD contents did not render with their declared source encoding: %v", err)
	}
	limited, err := decodeSmartContentsSource(nil, data, psdfile.DefaultLimits(), source.retainedBytes-1)
	if limited != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("PSD contents indexed beyond their remaining metadata budget: %v", err)
	}
	for _, unsupported := range [][]byte{smartEditFlat(2, 2, 16, 3), smartEditFlat(2, 2, 8, 4)} {
		result, err := decodeSmartContentsSource(nil, unsupported, psdfile.DefaultLimits(), 8<<20)
		if result != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported PSD precision or color mode bypassed the RGB8 gate: %v", err)
		}
	}
}
