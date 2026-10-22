package psdparse

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
)

// decodeOracleDataImage reads embedded fixture images without service or filesystem dependencies.
func decodeOracleDataImage(value string) (*image.NRGBA, error) {
	header, encoded, found := strings.Cut(value, ",")
	if !found || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return nil, fmt.Errorf("fixture image must be a base64 image data URL")
	}
	mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	var expectedFormat string
	switch mediaType {
	case "image/png":
		expectedFormat = "png"
	case "image/jpeg":
		expectedFormat = "jpeg"
	case "image/gif":
		expectedFormat = "gif"
	default:
		return nil, fmt.Errorf("unsupported fixture image media type %q", mediaType)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode fixture image base64: %w", err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode fixture image: %w", err)
	}
	if format != expectedFormat {
		return nil, fmt.Errorf("fixture image media type %q does not match decoded %q", mediaType, format)
	}
	orientation := uint16(1)
	if format == "jpeg" {
		orientation = oracleJPEGOrientation(bytes.NewReader(data))
	}
	return oracleOrientedNRGBA(decoded, orientation), nil
}

// oracleJPEGOrientation reads EXIF in marker segments before the compressed JPEG scan.
func oracleJPEGOrientation(reader io.Reader) uint16 {
	scan := bufio.NewReader(reader)
	var header [2]byte
	if _, err := io.ReadFull(scan, header[:]); err != nil || header != [2]byte{0xff, 0xd8} {
		return 1
	}
	for {
		marker, err := scan.ReadByte()
		if err != nil || marker != 0xff {
			return 1
		}
		for marker == 0xff {
			marker, err = scan.ReadByte()
			if err != nil {
				return 1
			}
		}
		switch {
		case marker == 0xda || marker == 0xd9 || marker == 0:
			return 1
		case marker == 0xd8 || marker == 0x01 || marker >= 0xd0 && marker <= 0xd7:
			continue
		}
		if _, err := io.ReadFull(scan, header[:]); err != nil {
			return 1
		}
		length := int(binary.BigEndian.Uint16(header[:])) - 2
		if length < 0 {
			return 1
		}
		if marker == 0xe1 && length >= 6 {
			var signature [6]byte
			if _, err := io.ReadFull(scan, signature[:]); err != nil {
				return 1
			}
			length -= len(signature)
			if bytes.Equal(signature[:], []byte("Exif\x00\x00")) {
				payload := make([]byte, length)
				if _, err := io.ReadFull(scan, payload); err != nil {
					return 1
				}
				if orientation := oracleTIFFOrientation(payload); orientation != 0 {
					return orientation
				}
				continue
			}
		}
		if _, err := scan.Discard(length); err != nil {
			return 1
		}
	}
}

// oracleTIFFOrientation reads the inline SHORT orientation field in IFD0.
func oracleTIFFOrientation(data []byte) uint16 {
	if len(data) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(data[2:4]) != 42 {
		return 0
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset < 8 || offset+2 > uint64(len(data)) {
		return 0
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	offset += 2
	if count > (uint64(len(data))-offset)/12 {
		return 0
	}
	for index := range count {
		entry := data[offset+index*12 : offset+(index+1)*12]
		if order.Uint16(entry[:2]) != 0x0112 || order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			continue
		}
		orientation := order.Uint16(entry[8:10])
		if orientation >= 1 && orientation <= 8 {
			return orientation
		}
	}
	return 0
}

func oracleOrientedNRGBA(source image.Image, orientation uint16) *image.NRGBA {
	rect := source.Bounds()
	width, height := rect.Dx(), rect.Dy()
	outputWidth, outputHeight := width, height
	if orientation >= 5 && orientation <= 8 {
		outputWidth, outputHeight = height, width
	}
	output := image.NewNRGBA(image.Rect(0, 0, outputWidth, outputHeight))
	if orientation < 2 || orientation > 8 {
		draw.Draw(output, output.Bounds(), source, rect.Min, draw.Src)
		return output
	}
	for y := range height {
		for x := range width {
			destinationX, destinationY := x, y
			switch orientation {
			case 2:
				destinationX = width - 1 - x
			case 3:
				destinationX, destinationY = width-1-x, height-1-y
			case 4:
				destinationY = height - 1 - y
			case 5:
				destinationX, destinationY = y, x
			case 6:
				destinationX, destinationY = height-1-y, x
			case 7:
				destinationX, destinationY = height-1-y, width-1-x
			case 8:
				destinationX, destinationY = y, width-1-x
			}
			output.Set(destinationX, destinationY, source.At(rect.Min.X+x, rect.Min.Y+y))
		}
	}
	return output
}

func TestOracleDataImageFormats(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	source.SetNRGBA(0, 0, color.NRGBA{R: 230, G: 20, B: 30, A: 255})
	for _, test := range []struct {
		mediaType string
		encode    func(io.Writer) error
	}{
		{"image/png", func(writer io.Writer) error { return png.Encode(writer, source) }},
		{"image/jpeg", func(writer io.Writer) error { return jpeg.Encode(writer, source, nil) }},
		{"image/gif", func(writer io.Writer) error { return gif.Encode(writer, source, nil) }},
	} {
		t.Run(test.mediaType, func(t *testing.T) {
			var encoded bytes.Buffer
			if err := test.encode(&encoded); err != nil {
				t.Fatal(err)
			}
			value := "data:" + test.mediaType + ";base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
			actual, err := decodeOracleDataImage(value)
			if err != nil || actual.Bounds() != source.Bounds() {
				t.Fatalf("decode %s: dimensions differ or error: %v", test.mediaType, err)
			}
			if test.mediaType == "image/png" {
				mismatched := strings.Replace(value, "image/png", "image/jpeg", 1)
				if _, err := decodeOracleDataImage(mismatched); err == nil {
					t.Fatal("mismatched fixture media type accepted")
				}
			}
		})
	}
	for _, value := range []string{
		"https://example.invalid/image.png", "data:image/webp;base64,AA==",
		"data:image/png,raw", "data:image/png;base64,invalid", "data:image/png;base64,AA==",
	} {
		if _, err := decodeOracleDataImage(value); err == nil {
			t.Fatalf("unsupported or malformed fixture image accepted: %q", value)
		}
	}
}

func TestOracleDataImageJPEGOrientations(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	copy(source.Pix, []byte{
		230, 20, 20, 255, 20, 220, 20, 255, 20, 20, 230, 255,
		220, 210, 20, 255, 230, 20, 210, 255, 20, 210, 220, 255,
	})
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	value := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	plain, err := decodeOracleDataImage(value)
	if err != nil {
		t.Fatal(err)
	}
	// Independent permutations of ABC/DEF in displayed row-major order.
	permutations := [8][6]int{
		{0, 1, 2, 3, 4, 5}, {2, 1, 0, 5, 4, 3}, {5, 4, 3, 2, 1, 0}, {3, 4, 5, 0, 1, 2},
		{0, 3, 1, 4, 2, 5}, {3, 0, 4, 1, 5, 2}, {5, 2, 4, 1, 3, 0}, {2, 5, 1, 4, 0, 3},
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for orientation := uint16(1); orientation <= 8; orientation++ {
			t.Run(fmt.Sprintf("%s-%d", order, orientation), func(t *testing.T) {
				payload := make([]byte, 32)
				copy(payload, "Exif\x00\x00")
				tiff := payload[6:]
				copy(tiff, "II")
				if order == binary.BigEndian {
					copy(tiff, "MM")
				}
				order.PutUint16(tiff[2:4], 42)
				order.PutUint32(tiff[4:8], 8)
				order.PutUint16(tiff[8:10], 1)
				order.PutUint16(tiff[10:12], 0x0112)
				order.PutUint16(tiff[12:14], 3)
				order.PutUint32(tiff[14:18], 1)
				order.PutUint16(tiff[18:20], orientation)
				data := append([]byte{}, encoded.Bytes()[:2]...)
				data = append(data, 0xff, 0xe1, byte((len(payload)+2)>>8), byte(len(payload)+2))
				data = append(data, payload...)
				data = append(data, encoded.Bytes()[2:]...)
				value := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
				actual, err := decodeOracleDataImage(value)
				if err != nil {
					t.Fatal(err)
				}
				width, height := 3, 2
				if orientation >= 5 {
					width, height = 2, 3
				}
				if actual.Bounds() != image.Rect(0, 0, width, height) {
					t.Fatalf("oriented dimensions = %v", actual.Bounds())
				}
				for index, sourceIndex := range permutations[orientation-1] {
					if !bytes.Equal(actual.Pix[index*4:index*4+4], plain.Pix[sourceIndex*4:sourceIndex*4+4]) {
						t.Fatalf("pixel %d differs from source pixel %d", index, sourceIndex)
					}
				}
			})
		}
	}
}
