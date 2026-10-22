package psdedit

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// SetVectorMask replaces an existing vmsk/vsms block with independently owned typed vector geometry.
// Document dimensions, mask density/feather and other source metadata stay unchanged.
// Encoding, decoding and budget checks complete before the editor revision or overlays change.
// Automatic preview reconstruction retains the renderer's documented antialiasing approximation limits.
func (e *Editor) SetVectorMask(ctx context.Context, id LayerID, mask *psdvector.Mask) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if mask == nil || mask.Width != int(e.source.Header.Width) || mask.Height != int(e.source.Header.Height) {
		return editError(ErrInvalidEdit, "layers.vectorMask", "vector dimensions must match the existing document canvas")
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return editError(ErrUnsupported, "layers.vectorMask", "layer has no original vector-mask block")
	}
	var block psdfile.AdditionalBlock
	found := false
	for _, candidate := range e.source.Layers[node.sourceIndex].AdditionalInfo {
		if candidate.Key != "vmsk" && candidate.Key != "vsms" {
			continue
		}
		if found {
			return editError(ErrUnsupported, "layers.vectorMask", "multiple vector-mask blocks are ambiguous")
		}
		block, found = candidate, true
	}
	if !found {
		return editError(ErrUnsupported, "layers.vectorMask", "layer has no original vector-mask block")
	}
	limits := e.source.Limits()
	previous := node.additional[block.Span.Offset]
	// The previous overlay remains reachable until the transaction commits.
	// Reserve both backing arrays that may coexist while the encoder grows its output.
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	encodeLimit := min(limits.MaxMetadataBytes, available/2)
	if encodeLimit <= 0 {
		return editError(psdfile.ErrLimit, "layers.vectorMask", "retained overlays consume the working budget")
	}
	encoded, err := psdvector.EncodeMask(ctx, mask, psdvector.EncodeOptions{
		MaxBytes: encodeLimit, MaxRecords: limits.MaxDescriptorItems,
	})
	if err != nil {
		return vectorEditError(err)
	}
	equal := bytes.Equal(previous, encoded)
	if previous == nil {
		equal, err = equalSourceVector(ctx, e.source, block.Data, encoded)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if equal {
		return nil
	}
	count := int64((len(encoded) - 8) / 26)
	backingBytes := int64(cap(encoded))
	if backingBytes > available || available-backingBytes < 512 ||
		count > (available-backingBytes-512)/256 {
		return editError(psdfile.ErrLimit, "layers.vectorMask", "vector replacement exceeds the peak working budget")
	}
	// DecodeMask includes input length in its accounting; charge unused backing capacity separately.
	backingSlack := backingBytes - int64(len(encoded))
	owned, err := psdvector.DecodeMask(ctx, encoded, mask.Width, mask.Height, psdvector.DecodeOptions{
		MaxBytes: min(limits.MaxMetadataBytes, available-backingSlack), MaxRecords: limits.MaxDescriptorItems,
	})
	if err != nil {
		return vectorEditError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	node.additional[block.Span.Offset] = encoded
	node.vectorMask = owned
	node.setSemanticCost("vector", 512+count*256+backingSlack)
	e.markChangedLocked(false)
	return nil
}

func equalSourceVector(ctx context.Context, source *psdfile.Document, span psdfile.Span, data []byte) (bool, error) {
	if span.Length != int64(len(data)) {
		return false, nil
	}
	var buffer [4096]byte
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		length := min(len(buffer), len(data)-offset)
		count, err := source.ReadAt(buffer[:length], span.Offset+int64(offset))
		if count != length || (err != nil && !errors.Is(err, io.EOF)) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return false, err
		}
		if !bytes.Equal(buffer[:length], data[offset:offset+length]) {
			return false, nil
		}
		offset += length
	}
	return true, nil
}

func vectorEditError(err error) error {
	kind := ErrInvalidEdit
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, psdvector.ErrUnsupported):
		kind = ErrUnsupported
	case errors.Is(err, psdvector.ErrLimit):
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.vectorMask", Detail: err.Error(), Cause: err}
}
