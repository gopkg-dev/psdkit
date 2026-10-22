package psdweb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"reflect"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// ProjectionAudit separates renderer input reads from legitimate PSD raw-copy export reads.
type ProjectionAudit struct {
	Verified              bool   `json:"verified"`
	ParentSavedPixelReads int    `json:"parentSavedPixelReads"`
	MetadataReadBytes     int64  `json:"metadataReadBytes"`
	SourceRGBASHA256      string `json:"sourceRGBASHA256"`
	reader                *projectionReader
}

type projectionReader struct {
	*bytes.Reader
	spans     []psdfile.Span
	denied    int
	readBytes int64
}

type projectionFailure struct {
	cause error
	audit ProjectionAudit
}

func (failure *projectionFailure) Error() string { return failure.cause.Error() }
func (failure *projectionFailure) Unwrap() error { return failure.cause }

func (reader *projectionReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.spans {
		if len(data) > 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			reader.denied++
			return 0, fmt.Errorf("%w: saved parent artwork cannot enter live Warp", ErrProtocol)
		}
	}
	count, err := reader.Reader.ReadAt(data, offset)
	reader.readBytes += int64(count)
	return count, err
}

func (e *Engine) projectionSource(source *psdfile.Document) (*psdfile.Document, ProjectionAudit, bool, error) {
	if source == nil {
		return nil, ProjectionAudit{}, false, ErrProtocol
	}
	var data []byte
	for _, item := range e.resources {
		candidate, valid := reflect.TypeAssert[*psdfile.Document](item.value)
		if valid && candidate == source && item.owner == item.id {
			if original := e.assets[item.assetID]; original != nil {
				data = original.data
				break
			}
		}
	}
	if data == nil {
		return source, ProjectionAudit{}, false, nil
	}
	reader := &projectionReader{Reader: bytes.NewReader(data)}
	for _, layer := range source.Layers {
		for _, channel := range layer.Channels {
			span := channel.Data
			if channel.Compression == 1 {
				countWidth := int64(2)
				if source.Header.Version == 2 {
					countWidth = 4
				}
				countBytes := countWidth * int64(channel.Bounds.Dy())
				span.Offset, span.Length = span.Offset+countBytes, span.Length-countBytes
			}
			reader.spans = append(reader.spans, span)
		}
	}
	span := source.Composite.Data
	if source.Composite.Compression == 1 {
		countBytes := int64(source.Composite.RowCountWidth) * int64(source.Header.Height) * int64(source.Header.Channels)
		span.Offset, span.Length = span.Offset+countBytes, span.Length-countBytes
	}
	reader.spans = append(reader.spans, span)
	guarded, err := psdfile.Read(reader, int64(len(data)), e.loadOptions())
	return guarded, ProjectionAudit{Verified: true, reader: reader}, true, err
}

func pixelHash(pixels *image.NRGBA) string {
	if pixels == nil {
		return ""
	}
	hash := sha256.New()
	for row := range pixels.Rect.Dy() {
		hash.Write(pixels.Pix[row*pixels.Stride : row*pixels.Stride+pixels.Rect.Dx()*4])
	}
	return hex.EncodeToString(hash.Sum(nil))
}
