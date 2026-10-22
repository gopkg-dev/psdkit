package psdfile

import (
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
)

// LinkedRasterOptions lowers limits for embedded PNG/JPEG and flat RGB8 TIFF decoding.
// Zero values use the source pixel/working limits and a four MiB ICC profile limit.
type LinkedRasterOptions struct {
	MaxPixels, MaxWorkingBytes, MaxProfileBytes int64
	// CenteredJPEGChroma selects centered linear 2:1 chroma instead of Go's codec reconstruction.
	// Neither reconstruction guarantees Photoshop's private JPEG decoder rounding.
	CenteredJPEGChroma bool
}

// LinkedRaster contains owned straight RGBA8 pixels and the original embedded ICC profile.
// The profile is metadata; decoding does not silently convert its color encoding.
type LinkedRaster struct {
	Pixels       *image.NRGBA
	ICCProfile   []byte
	Format       string
	WorkingBytes int64
}

// LinkedRasterFormat identifies indexed PNG/JPEG/TIFF content without decoding pixels.
// Other formats return an empty string; this method never opens an external path.
func (d *Document) LinkedRasterFormat(ctx context.Context, record LinkedFileRecord) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if d == nil {
		return "", fileError(ErrMalformed, "linkedRaster", "source", 0, "source is absent")
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return "", err
	}
	if !record.HasData || !d.validSpan(record.Data) || record.Data.Length < 8 {
		return "", nil
	}
	var header [8]byte
	if _, err := io.ReadFull(d.linkedRasterReader(ctx, record.Data), header[:]); err != nil {
		return "", err
	}
	if string(header[:]) == "\x89PNG\r\n\x1a\n" {
		return "png", nil
	}
	if header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff {
		return "jpeg", nil
	}
	if string(header[:4]) == "II*\x00" || string(header[:4]) == "MM\x00*" ||
		string(header[:4]) == "II+\x00" || string(header[:4]) == "MM\x00+" {
		return "tiff", nil
	}
	return "", nil
}

// DecodeLinkedRaster decodes indexed embedded PNG/JPEG and flat RGB8 TIFF content with bounded storage.
// Straight RGBA8 output is owned, including for 16-bit PNG input. Source ICC data is returned explicitly.
// TIFF accepts classic, single-image, top-left chunky RGB8 strips using Raw or PackBits encoding.
// Explicit TIFF associated alpha is unpremultiplied; unassociated alpha retains hidden RGB values.
// BigTIFF, private Photoshop layers, tiles and unsupported TIFF encodings return ErrUnsupported.
func (d *Document) DecodeLinkedRaster(
	ctx context.Context, record LinkedFileRecord, options ...LinkedRasterOptions,
) (*LinkedRaster, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	format, err := d.LinkedRasterFormat(ctx, record)
	if err != nil {
		return nil, err
	}
	if format == "" {
		return nil, fileError(ErrUnsupported, "linkedRaster", "format", record.Data.Offset,
			"content is not an indexed PNG, JPEG or TIFF")
	}
	if len(options) > 1 {
		return nil, fileError(ErrMalformed, "linkedRaster", "options", 0, "at most one options value is accepted")
	}
	limits := d.Limits()
	configuration := LinkedRasterOptions{MaxPixels: limits.MaxDecodePixels,
		MaxWorkingBytes: limits.MaxDecodeBytes, MaxProfileBytes: 4 << 20}
	if len(options) == 1 {
		provided := options[0]
		configuration.CenteredJPEGChroma = provided.CenteredJPEGChroma
		if provided.MaxPixels < 0 || provided.MaxWorkingBytes < 0 || provided.MaxProfileBytes < 0 {
			return nil, fileError(ErrMalformed, "linkedRaster", "options", 0, "limits must be nonnegative")
		}
		if provided.MaxPixels != 0 {
			configuration.MaxPixels = min(configuration.MaxPixels, provided.MaxPixels)
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = min(configuration.MaxWorkingBytes, provided.MaxWorkingBytes)
		}
		if provided.MaxProfileBytes != 0 {
			configuration.MaxProfileBytes = min(configuration.MaxProfileBytes, provided.MaxProfileBytes)
		}
	}
	if format == "tiff" {
		return d.decodeLinkedTIFF(ctx, record, configuration)
	}
	var header image.Config
	if format == "png" {
		header, err = png.DecodeConfig(d.linkedRasterReader(ctx, record.Data))
	} else {
		header, err = jpeg.DecodeConfig(d.linkedRasterReader(ctx, record.Data))
	}
	if err != nil {
		return nil, linkedRasterError(ctx, record, err)
	}
	if _, cmyk := header.ColorModel.Convert(color.Black).(color.CMYK); cmyk {
		return nil, fileError(ErrUnsupported, "linkedRaster", "color", record.Data.Offset,
			"CMYK JPEG requires a source-precision managed decoder")
	}
	width, height := int64(header.Width), int64(header.Height)
	if width <= 0 || height <= 0 || width > math.MaxInt64/height ||
		width*height > configuration.MaxPixels || width*height > (math.MaxInt64-(1<<20))/20 {
		return nil, fileError(ErrLimit, "linkedRaster", "dimensions", record.Data.Offset,
			"raster dimensions exceed the pixel or allocation limit")
	}
	decoderArea := width * height
	if format == "jpeg" {
		// JPEG sampling factors may pad both axes to a 32-pixel MCU. Progressive scans also retain DCT blocks.
		// Counting padded planes and coefficients avoids undercharging narrow or short progressive images.
		paddedWidth, paddedHeight := ((width+31)/32)*32, ((height+31)/32)*32
		if paddedWidth > (math.MaxInt64-(1<<20))/20/paddedHeight {
			return nil, fileError(ErrLimit, "linkedRaster", "working", record.Data.Offset,
				"JPEG decoder allocation overflows")
		}
		decoderArea = paddedWidth * paddedHeight
	}
	// Covers decoder planes/coefficients, row filters, conversion output and codec scratch.
	working := decoderArea*20 + 1<<20
	if width > (math.MaxInt64-working)/32 {
		return nil, fileError(ErrLimit, "linkedRaster", "working", record.Data.Offset,
			"raster scratch allocation overflows")
	}
	working += width * 32
	if working > configuration.MaxWorkingBytes || working > math.MaxInt {
		return nil, fileError(ErrLimit, "linkedRaster", "working", record.Data.Offset,
			"decoder and owned output exceed the working limit")
	}
	profileLimit := min(configuration.MaxProfileBytes, configuration.MaxWorkingBytes-working)
	var profile []byte
	if format == "png" {
		profile, err = d.linkedPNGProfile(ctx, record.Data, profileLimit)
	} else {
		profile, err = d.linkedJPEGProfile(ctx, record.Data, profileLimit)
	}
	if err != nil {
		return nil, linkedRasterError(ctx, record, err)
	}
	working += int64(len(profile))
	var decoded image.Image
	if format == "png" {
		decoded, err = png.Decode(d.linkedRasterReader(ctx, record.Data))
	} else {
		decoded, err = jpeg.Decode(d.linkedRasterReader(ctx, record.Data))
	}
	if err != nil {
		return nil, linkedRasterError(ctx, record, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, header.Width, header.Height))
	if native, ok := decoded.(*image.NRGBA); ok {
		for row := 0; row < header.Height; row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			copy(pixels.Pix[row*pixels.Stride:row*pixels.Stride+header.Width*4],
				native.Pix[row*native.Stride:row*native.Stride+header.Width*4])
		}
	} else if native, ok := decoded.(*image.NRGBA64); ok {
		for row := 0; row < header.Height; row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := 0; column < header.Width; column++ {
				for component := range 4 {
					pixels.Pix[row*pixels.Stride+column*4+component] =
						native.Pix[row*native.Stride+column*8+component*2]
				}
			}
		}
	} else if native, ok := decoded.(*image.Paletted); ok {
		for row := 0; row < header.Height; row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := 0; column < header.Width; column++ {
				index := native.Pix[row*native.Stride+column]
				value := color.NRGBAModel.Convert(native.Palette[index]).(color.NRGBA)
				position := row*pixels.Stride + column*4
				pixels.Pix[position], pixels.Pix[position+1] = value.R, value.G
				pixels.Pix[position+2], pixels.Pix[position+3] = value.B, value.A
			}
		}
	} else if native, ok := decoded.(*image.YCbCr); ok {
		if err := renderLinkedJPEG(ctx, pixels, native, configuration.CenteredJPEGChroma); err != nil {
			return nil, err
		}
	} else {
		for row := 0; row < header.Height; row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			draw.Draw(pixels, image.Rect(0, row, header.Width, row+1), decoded,
				decoded.Bounds().Min.Add(image.Pt(0, row)), draw.Src)
		}
	}
	return &LinkedRaster{Pixels: pixels, ICCProfile: profile, Format: format, WorkingBytes: working}, nil
}

func (d *Document) linkedRasterReader(ctx context.Context, span Span) io.Reader {
	return io.NewSectionReader(strictRasterReaderAt{context: ctx, source: d}, span.Offset, span.Length)
}

type strictRasterReaderAt struct {
	context context.Context
	source  *Document
}

func (reader strictRasterReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	if err := reader.context.Err(); err != nil {
		return 0, err
	}
	count, err := reader.source.ReadAt(data, offset)
	if cancellation := reader.context.Err(); cancellation != nil {
		return 0, cancellation
	}
	// Some image decoders discard an accompanying error after consuming a full buffer.
	// Never hand them bytes from a failed non-EOF source operation.
	if err != nil && err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		return 0, err
	}
	if count < 0 || count > len(data) {
		return 0, fileError(ErrMalformed, "linkedRaster", "source", offset, "invalid source read count")
	}
	return count, err
}

func linkedRasterError(ctx context.Context, record LinkedFileRecord, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, ErrLimit) || errors.Is(cause, ErrUnsupported) || errors.Is(cause, ErrClosed) {
		return cause
	}
	return &Error{Kind: ErrMalformed, Feature: "linkedRaster", Path: "data", Offset: record.Data.Offset,
		Detail: "embedded raster could not be decoded", Cause: cause}
}

func (d *Document) linkedPNGProfile(ctx context.Context, span Span, limit int64) ([]byte, error) {
	position, end := span.Offset+8, span.Offset+span.Length
	var result []byte
	for position < end {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var header [8]byte
		if end-position < 12 {
			return nil, io.ErrUnexpectedEOF
		}
		if _, err := io.ReadFull(d.linkedRasterReader(ctx, Span{Offset: position, Length: 8}), header[:]); err != nil {
			return nil, err
		}
		length := int64(binary.BigEndian.Uint32(header[:4]))
		if length > end-position-12 {
			return nil, io.ErrUnexpectedEOF
		}
		kind := string(header[4:])
		if kind == "iCCP" {
			if result != nil {
				return nil, fileError(ErrMalformed, "linkedRaster", "icc", position, "duplicate PNG ICC chunk")
			}
			reader := d.linkedRasterReader(ctx, Span{Offset: position + 8, Length: length})
			var octet [1]byte
			terminated := false
			for count := range 80 {
				if _, err := io.ReadFull(reader, octet[:]); err != nil {
					return nil, err
				}
				if octet[0] == 0 {
					terminated = count != 0
					break
				}
			}
			if !terminated {
				return nil, fileError(ErrMalformed, "linkedRaster", "icc", position, "invalid PNG ICC keyword")
			}
			if _, err := io.ReadFull(reader, octet[:]); err != nil {
				return nil, err
			}
			if octet[0] != 0 {
				return nil, fileError(ErrUnsupported, "linkedRaster", "icc", position, "unknown PNG ICC compression")
			}
			inflated, err := zlib.NewReader(reader)
			if err != nil {
				return nil, err
			}
			result, err = readLinkedICC(inflated, limit)
			closeErr := inflated.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		position += length + 12
		if kind == "IEND" {
			break
		}
	}
	return result, nil
}

func readLinkedICC(reader io.Reader, limit int64) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	length := int64(binary.BigEndian.Uint32(header[:]))
	if length < 132 {
		return nil, fileError(ErrMalformed, "linkedRaster", "icc", 0, "incomplete ICC header")
	}
	if length > limit {
		return nil, fileError(ErrLimit, "linkedRaster", "icc", 0, "ICC profile exceeds the working limit")
	}
	result := make([]byte, int(length))
	copy(result, header[:])
	if _, err := io.ReadFull(reader, result[4:]); err != nil {
		return nil, err
	}
	var extra [1]byte
	count, err := reader.Read(extra[:])
	if count != 0 || err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
		if err != nil && err != io.EOF { //nolint:errorlint // Only bare EOF ends a read; joined errors retain source faults.
			return nil, err
		}
		return nil, fileError(ErrMalformed, "linkedRaster", "icc", 0, "ICC stream exceeds its declared length")
	}
	return result, nil
}

func (d *Document) linkedJPEGProfile(ctx context.Context, span Span, limit int64) ([]byte, error) {
	position, end := span.Offset+2, span.Offset+span.Length
	var chunks [255]Span
	total, expected, present := int64(0), 0, 0
	for position < end {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var header [4]byte
		if end-position < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		if _, err := io.ReadFull(d.linkedRasterReader(ctx, Span{Offset: position, Length: 4}), header[:]); err != nil {
			return nil, err
		}
		if header[0] != 0xff {
			return nil, fileError(ErrMalformed, "linkedRaster", "jpeg", position, "invalid JPEG marker")
		}
		if header[1] == 0xff {
			position++
			continue
		}
		if header[1] == 0xda || header[1] == 0xd9 {
			break
		}
		if header[1] == 0x01 || header[1] >= 0xd0 && header[1] <= 0xd8 {
			position += 2
			continue
		}
		length := int64(binary.BigEndian.Uint16(header[2:]))
		if length < 2 || length > end-position-2 {
			return nil, io.ErrUnexpectedEOF
		}
		if header[1] == 0xe2 && length >= 16 {
			var identifier [14]byte
			if _, err := io.ReadFull(d.linkedRasterReader(ctx, Span{Offset: position + 4, Length: 14}),
				identifier[:]); err != nil {
				return nil, err
			}
			if string(identifier[:12]) == "ICC_PROFILE\x00" {
				ordinal, count := int(identifier[12]), int(identifier[13])
				if ordinal == 0 || count == 0 || ordinal > count ||
					expected != 0 && count != expected || chunks[ordinal-1].Length != 0 || length == 16 {
					return nil, fileError(ErrMalformed, "linkedRaster", "icc", position, "invalid JPEG ICC fragments")
				}
				expected, present = count, present+1
				chunks[ordinal-1] = Span{Offset: position + 18, Length: length - 16}
				total += length - 16
				if total > limit {
					return nil, fileError(ErrLimit, "linkedRaster", "icc", position, "ICC profile exceeds the working limit")
				}
			}
		}
		position += length + 2
	}
	if expected == 0 {
		return nil, nil
	}
	if present != expected || total < 132 {
		return nil, fileError(ErrMalformed, "linkedRaster", "icc", span.Offset, "incomplete JPEG ICC fragments")
	}
	result := make([]byte, int(total))
	position = 0
	for index := 0; index < expected; index++ {
		chunk := chunks[index]
		if _, err := io.ReadFull(d.linkedRasterReader(ctx, chunk), result[position:position+chunk.Length]); err != nil {
			return nil, err
		}
		position += chunk.Length
	}
	if int64(binary.BigEndian.Uint32(result[:4])) != total {
		return nil, fileError(ErrMalformed, "linkedRaster", "icc", span.Offset, "ICC declared length differs from fragments")
	}
	return result, nil
}
