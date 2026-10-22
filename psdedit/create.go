package psdedit

import (
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// CreateOptions selects PSD version 1 or PSB version 2 and source allocation limits.
// Zero Version selects PSD. New documents contain one transparent RGB8 canvas-sized raster layer.
// ICCProfile explicitly supplies a validated RGB matrix/TRC profile preserved in resource 1039.
type CreateOptions struct {
	Version    uint16
	Limits     psdfile.Limits
	ICCProfile []byte
}

// Create constructs an owned editable transparent RGB8 document without allocating blank pixel planes.
// Width and height must fit the bounded RGB8 snapshot path; source precision creation is separate.
// The first layer has handle 1 and can be populated with ReplacePixels, renamed or grouped.
func Create(ctx context.Context, width, height int, options ...CreateOptions) (*Editor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(options) > 1 {
		return nil, editError(ErrInvalidEdit, "create.options", "provide at most one options value")
	}
	configuration := CreateOptions{Version: 1}
	if len(options) == 1 {
		configuration = options[0]
		if configuration.Version == 0 {
			configuration.Version = 1
		}
	}
	if configuration.Version != 1 && configuration.Version != 2 {
		return nil, editError(ErrInvalidEdit, "create.version", "version must select PSD or PSB")
	}
	if width <= 0 || height <= 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension ||
		int64(width) > int64(psdmodel.MaxPixels)/int64(height) {
		return nil, editError(psdfile.ErrLimit, "create.size", "canvas exceeds the bounded RGB8 dimensions")
	}
	reader := blankDocumentSource(configuration.Version, width, height)
	source, err := psdfile.Read(reader, reader.size, psdfile.LoadOptions{Limits: configuration.Limits})
	if err != nil {
		return nil, err
	}
	if int64(width)*int64(height) > source.Limits().MaxDecodePixels {
		return nil, errors.Join(editError(psdfile.ErrLimit, "create.size", "canvas exceeds source pixel limits"),
			source.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, source.Close())
	}
	if len(configuration.ICCProfile) != 0 {
		source, err = createProfileSource(ctx, reader, source, configuration.ICCProfile)
		if err != nil {
			return nil, err
		}
	}
	editor, err := New(source)
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	editor.ownsSource, editor.revision = true, 1
	return editor, nil
}

func createProfileSource(ctx context.Context, reader *blankSource, source *psdfile.Document,
	profile []byte,
) (*psdfile.Document, error) {
	limits := source.Limits()
	resourceBytes := int64(12 + len(profile) + len(profile)%2)
	owned := source.IndexMetadataBytes() + int64(cap(reader.prefix)) + 1024
	if resourceBytes > limits.MaxFileBytes-reader.size ||
		resourceBytes+owned+256 > limits.MaxDecodeBytes {
		return nil, errors.Join(editError(psdfile.ErrLimit, "create.profile", "ICC resource exceeds creation budgets"),
			source.Close())
	}
	if err := validateRasterProfile(ctx, profile, limits.MaxDecodeBytes-owned); err != nil {
		return nil, errors.Join(err, source.Close())
	}
	if err := source.Close(); err != nil {
		return nil, err
	}
	source = nil
	prefix := make([]byte, len(reader.prefix)+int(resourceBytes))
	copy(prefix, reader.prefix[:30])
	binary.BigEndian.PutUint32(prefix[30:34], uint32(resourceBytes))
	copy(prefix[34:38], "8BIM")
	binary.BigEndian.PutUint16(prefix[38:40], 1039)
	binary.BigEndian.PutUint32(prefix[42:46], uint32(len(profile)))
	copy(prefix[46:], profile)
	copy(prefix[34+int(resourceBytes):], reader.prefix[34:])
	reader.prefix, reader.size = prefix, reader.size+resourceBytes
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := psdfile.Read(reader, reader.size, psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, err
	}
	if result.IndexMetadataBytes()+int64(cap(reader.prefix))+1024 > limits.MaxDecodeBytes {
		return nil, errors.Join(editError(psdfile.ErrLimit, "create.profile", "owned indexed source exceeds budget"),
			result.Close())
	}
	return result, nil
}

// blankSource stores only the structural prefix; initial channels and footer are virtual zero bytes.
type blankSource struct {
	prefix []byte
	size   int64
}

func (reader *blankSource) ReadAt(target []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, editError(ErrInvalidEdit, "create.reader", "negative source offset")
	}
	if offset >= reader.size {
		if len(target) == 0 && offset == reader.size {
			return 0, nil
		}
		return 0, io.EOF
	}
	count := min(int64(len(target)), reader.size-offset)
	clear(target[:int(count)])
	if offset < int64(len(reader.prefix)) {
		copy(target[:int(count)], reader.prefix[int(offset):])
	}
	if count != int64(len(target)) {
		return int(count), io.EOF
	}
	return int(count), nil
}

func blankDocumentSource(version uint16, width, height int) *blankSource {
	area := int64(width) * int64(height)
	appendLength := func(target []byte, value int64) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(target, uint64(value))
		}
		return binary.BigEndian.AppendUint32(target, uint32(value))
	}
	var record []byte
	for _, value := range []uint32{0, 0, uint32(height), uint32(width)} {
		record = binary.BigEndian.AppendUint32(record, value)
	}
	record = binary.BigEndian.AppendUint16(record, 4)
	for _, id := range []uint16{0, 1, 2, 0xffff} {
		record = binary.BigEndian.AppendUint16(record, id)
		record = appendLength(record, area+2)
	}
	record = append(record, "8BIMnorm"...)
	record = append(record, 255, 0, 0, 0)
	record = binary.BigEndian.AppendUint32(record, 16)
	record = append(record, make([]byte, 8)...)
	record = append(record, 7)
	record = append(record, "Layer 1"...)
	lengthWidth := int64(versionLengthWidth(version))
	layerInfoLength := 2 + int64(len(record)) + 4*(area+2)
	sectionLength := lengthWidth + layerInfoLength + 4
	prefix := append([]byte(nil), "8BPS"...)
	prefix = binary.BigEndian.AppendUint16(prefix, version)
	prefix = append(prefix, make([]byte, 6)...)
	prefix = binary.BigEndian.AppendUint16(prefix, 4)
	prefix = binary.BigEndian.AppendUint32(prefix, uint32(height))
	prefix = binary.BigEndian.AppendUint32(prefix, uint32(width))
	prefix = binary.BigEndian.AppendUint16(prefix, 8)
	prefix = binary.BigEndian.AppendUint16(prefix, 3)
	prefix = append(prefix, make([]byte, 8)...)
	prefix = appendLength(prefix, sectionLength)
	prefix = appendLength(prefix, layerInfoLength)
	prefix = binary.BigEndian.AppendUint16(prefix, 0xffff)
	prefix = append(prefix, record...)
	return &blankSource{prefix: prefix, size: int64(len(prefix)) + 4*(area+2) + 4 + 2 + 4*area}
}
