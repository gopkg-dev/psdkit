package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"io"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type textAutoEdit struct {
	node      *editNode
	source    *psdfile.Document
	revision  uint64
	block     psdfile.AdditionalBlock
	encoded   []byte
	candidate *psdfile.Document
	record    psdfile.TextLayerRecord
	settings  psdscene.Options
	retained  int64
}

func (e *Editor) setTextContentAuto(
	ctx context.Context, id LayerID, text string, provided *psdscene.Options,
) error {
	e.mu.Lock()
	stage, err := e.prepareTextAutoLocked(ctx, id, text, provided)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() { _ = stage.candidate.Close() }()
	// The candidate and policies are private snapshots; explicit font callbacks may reenter the editor.
	var pixels *image.NRGBA
	if policy := stage.settings.ApproximateText; policy != nil {
		pixels, err = psdscene.RenderTextLayer(ctx, stage.candidate, stage.record,
			stage.settings.FontResolver, *policy)
	} else {
		pixels, err = psdscene.RenderOpenTypeTextLayer(ctx, stage.candidate, stage.record,
			stage.settings.FontResolver, *stage.settings.ApproximateOpenTypeText)
	}
	if err != nil {
		return compositeRenderError(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.source != stage.source || e.revision != stage.revision {
		return ErrStaleLinkedEdit
	}
	limits := e.source.Limits()
	remaining := limits.MaxDecodeBytes - e.ownedBytesLocked(nil) - stage.retained
	if pixels == nil || pixels.Rect != textAutoCanvas(e.source) {
		return editError(ErrUnsupported, "layers.text.pixels", "automatic text artwork must cover the parent canvas")
	}
	if _, err := textEditPixelBytes(pixels, limits, remaining); err != nil {
		return err
	}
	if int64(cap(pixels.Pix)) > remaining {
		return editError(psdfile.ErrLimit, "layers.text.pixels", "automatic artwork exceeds the retained peak budget")
	}
	if err := e.textEditGeometry(stage.node, pixels.Rect); err != nil {
		return err
	}
	previous := stage.node.additional[stage.block.Span.Offset]
	if bytes.Equal(previous, stage.encoded) && stage.node.pixels != nil && stage.node.bounds == pixels.Rect &&
		textEditPixelsEqual(ctx, stage.node.pixels, pixels) {
		if _, err := e.source.ReadAt(nil, 0); err != nil {
			return err
		}
		return ctx.Err()
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if stage.node.additional == nil {
		stage.node.additional = make(map[int64][]byte)
	}
	stage.node.additional[stage.block.Span.Offset] = stage.encoded
	stage.node.pixels, stage.node.bounds = pixels, pixels.Rect
	stage.node.setSemanticCost("text", 256+int64(cap(stage.encoded)-len(stage.encoded))+
		int64(cap(pixels.Pix)-len(pixels.Pix)))
	e.markChangedLocked(false)
	return nil
}

func (e *Editor) prepareTextAutoLocked(
	ctx context.Context, id LayerID, text string, provided *psdscene.Options,
) (*textAutoEdit, error) {
	if err := e.checkMutableLocked(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.checkRGBLocked(); err != nil {
		return nil, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return nil, err
	}
	block, err := e.textEditBlock(node)
	if err != nil {
		return nil, err
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	settings, policyBytes, err := ownTextAutoOptions(provided, textAutoCanvas(e.source), available)
	if err != nil {
		return nil, err
	}
	if settings.MaxWorkingBytes != 0 {
		available = min(available, settings.MaxWorkingBytes)
	}
	canvas := textAutoCanvas(e.source)
	if err := e.textEditGeometry(node, canvas); err != nil {
		return nil, err
	}
	width, height := int64(canvas.Dx()), int64(canvas.Dy())
	if available <= policyBytes+4096 || width <= 0 || height <= 0 || width > limits.MaxDecodePixels/height ||
		width > (available-policyBytes-4096)/5/height || width > int64(^uint(0)>>1)/4/height {
		return nil, editError(psdfile.ErrLimit, "layers.text.pixels", "parent canvas exceeds the automatic text budget")
	}
	encoded, err := e.encodeTextEditLocked(ctx, node, block, text,
		available-policyBytes-width*height*4-4096, false)
	if err != nil {
		return nil, err
	}
	candidate, record, candidateBytes, err := textAutoSource(ctx, e.source, encoded, available/3)
	if err != nil {
		return nil, err
	}
	retained := candidateBytes + int64(cap(encoded)) + policyBytes + 4096
	if retained >= available {
		_ = candidate.Close()
		return nil, editError(psdfile.ErrLimit, "layers.text", "candidate consumes the source rendering budget")
	}
	renderBudget := available - retained
	if settings.ApproximateText != nil {
		policy := settings.ApproximateText
		if policy.MaxWorkingBytes == 0 || policy.MaxWorkingBytes > renderBudget {
			policy.MaxWorkingBytes = renderBudget
		}
	} else {
		policy := settings.ApproximateOpenTypeText
		if policy.MaxWorkingBytes == 0 || policy.MaxWorkingBytes > renderBudget {
			policy.MaxWorkingBytes = renderBudget
		}
	}
	return &textAutoEdit{node: node, source: e.source, revision: e.revision, block: block,
		encoded: encoded, candidate: candidate, record: record, settings: settings, retained: retained}, nil
}

func textAutoCanvas(source *psdfile.Document) image.Rectangle {
	return image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
}

func ownTextAutoOptions(
	provided *psdscene.Options, canvas image.Rectangle, budget int64,
) (psdscene.Options, int64, error) {
	if provided == nil || provided.FontResolver == nil ||
		provided.ApproximateText == nil && provided.ApproximateOpenTypeText == nil {
		return psdscene.Options{}, 0, editError(ErrUnsupported, "layers.text.render",
			"automatic text pixels require explicit fonts and one approximate text rendering policy")
	}
	if provided.ApproximateText != nil && provided.ApproximateOpenTypeText != nil {
		return psdscene.Options{}, 0, editError(ErrInvalidEdit, "layers.text.render", "choose one text rendering policy")
	}
	if provided.MaxWorkingBytes < 0 || provided.MaxDepth < 0 || provided.MaxDocuments < 0 {
		return psdscene.Options{}, 0, editError(ErrInvalidEdit, "layers.text.render", "render limits must be nonnegative")
	}
	if provided.OutputProfile != nil || provided.CachedSpecialLayers || provided.CachedTextLayers {
		return psdscene.Options{}, 0, editError(ErrUnsupported, "layers.text.render",
			"automatic text artwork requires live source pixels in the parent encoding")
	}
	if provided.MaxWorkingBytes != 0 {
		budget = min(budget, provided.MaxWorkingBytes)
	}
	settings := psdscene.Options{MaxWorkingBytes: provided.MaxWorkingBytes, FontResolver: provided.FontResolver}
	amount := int64(unsafe.Sizeof(textAutoEdit{})) + int64(unsafe.Sizeof(image.Rectangle{})) + 256
	if provided.ApproximateText != nil {
		amount += int64(unsafe.Sizeof(psdscene.TextRenderOptions{}))
		if provided.ApproximateText.Kerning != nil {
			amount += int64(unsafe.Sizeof(bool(false)))
		}
		if amount > budget {
			return settings, 0, editError(psdfile.ErrLimit, "layers.text.render", "owned text policy exceeds budget")
		}
		owned := *provided.ApproximateText
		if owned.Bounds != nil && *owned.Bounds != canvas {
			return settings, 0, editError(ErrUnsupported, "layers.text.render.bounds",
				"automatic saved text artwork must cover the whole parent canvas")
		}
		owned.Bounds = &canvas
		if owned.Kerning != nil {
			value := *owned.Kerning
			owned.Kerning = &value
		}
		settings.ApproximateText = &owned
		return settings, amount, nil
	}
	policy := provided.ApproximateOpenTypeText
	amount += int64(unsafe.Sizeof(psdscene.OpenTypeTextRenderOptions{}))
	featureBytes := int64(unsafe.Sizeof(psdfont.ShapeFeature{}))
	if amount > budget || int64(len(policy.Shape.Features)) > (budget-amount)/featureBytes {
		return settings, 0, editError(psdfile.ErrLimit, "layers.text.render", "owned OpenType policy exceeds budget")
	}
	amount += int64(len(policy.Shape.Features)) * featureBytes
	owned := *policy
	if owned.Bounds != nil && *owned.Bounds != canvas {
		return settings, 0, editError(ErrUnsupported, "layers.text.render.bounds",
			"automatic saved text artwork must cover the whole parent canvas")
	}
	owned.Bounds = &canvas
	owned.Shape.Features = make([]psdfont.ShapeFeature, len(policy.Shape.Features))
	copy(owned.Shape.Features, policy.Shape.Features)
	settings.ApproximateOpenTypeText = &owned
	return settings, amount, nil
}

// textAutoSource owns a metadata-only candidate with the real parent canvas and original resolution resources.
// Its virtual zero composite is structurally indexed but is never read to produce text artwork.
func textAutoSource(
	ctx context.Context, parent *psdfile.Document, payload []byte, budget int64,
) (*psdfile.Document, psdfile.TextLayerRecord, int64, error) {
	var empty psdfile.TextLayerRecord
	resourceBytes := int64(0)
	for _, resource := range parent.Resources {
		if resource.ID == 1005 {
			if resource.Span.Length > budget-resourceBytes {
				return nil, empty, 0, editError(psdfile.ErrLimit, "layers.text.resources", "resolution resources exceed budget")
			}
			resourceBytes += resource.Span.Length
		}
	}
	capacity := resourceBytes + int64(len(payload)) + 128
	if capacity < 0 || capacity > budget/4 || capacity > math.MaxUint32 ||
		uint64(capacity) > uint64(^uint(0)>>1) {
		return nil, empty, 0, editError(psdfile.ErrLimit, "layers.text", "render candidate envelope exceeds budget")
	}
	data := make([]byte, 26, int(capacity))
	copy(data, "8BPS")
	binary.BigEndian.PutUint16(data[4:], 2)
	binary.BigEndian.PutUint16(data[12:], 3)
	binary.BigEndian.PutUint32(data[14:], parent.Header.Height)
	binary.BigEndian.PutUint32(data[18:], parent.Header.Width)
	binary.BigEndian.PutUint16(data[22:], 8)
	binary.BigEndian.PutUint16(data[24:], 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = binary.BigEndian.AppendUint32(data, uint32(resourceBytes))
	for _, resource := range parent.Resources {
		if resource.ID != 1005 {
			continue
		}
		original, err := textEditReadPayload(ctx, parent, resource.Span)
		if err != nil {
			return nil, empty, 0, err
		}
		data = append(data, original...)
	}
	maskAt := len(data)
	data = binary.BigEndian.AppendUint64(data, 0)
	infoAt := len(data)
	data = binary.BigEndian.AppendUint64(data, 0)
	data = binary.BigEndian.AppendUint16(data, 1)
	for _, value := range []uint32{0, 0, 1, 1} {
		data = binary.BigEndian.AppendUint32(data, value)
	}
	data = binary.BigEndian.AppendUint16(data, 0)
	data = append(data, "8BIMnorm"...)
	data = append(data, 255, 0, 8, 0)
	data = binary.BigEndian.AppendUint32(data, uint32(24+len(payload)+len(payload)%2))
	data = append(data, make([]byte, 12)...)
	data = append(data, "8BIMTySh"...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(payload)))
	data = append(data, payload...)
	if len(payload)%2 != 0 {
		data = append(data, 0)
	}
	if (len(data)-infoAt-8)%2 != 0 {
		data = append(data, 0)
	}
	binary.BigEndian.PutUint64(data[infoAt:], uint64(len(data)-infoAt-8))
	data = binary.BigEndian.AppendUint32(data, 0)
	binary.BigEndian.PutUint64(data[maskAt:], uint64(len(data)-maskAt-8))
	data = binary.BigEndian.AppendUint16(data, 0)
	length := int64(len(data)) + int64(parent.Header.Width)*int64(parent.Header.Height)*3
	limits := parent.Limits()
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, budget/4)
	limits.MaxDecodeBytes = min(limits.MaxDecodeBytes, budget)
	reader := textAutoReader{ctx: ctx, data: data, length: length}
	candidate, err := psdfile.Read(reader, length, psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, empty, 0, err
	}
	view, err := candidate.ReadTextLayerView(ctx, psdfile.MetadataOptions{MaxBytes: limits.MaxMetadataBytes})
	retained := int64(cap(data)) + candidate.IndexMetadataBytes() + view.MetadataBytes
	if err != nil || len(view.Records) != 1 || retained > budget {
		_ = candidate.Close()
		if err == nil {
			err = editError(psdfile.ErrLimit, "layers.text", "render candidate text metadata exceeds budget")
		}
		return nil, empty, 0, err
	}
	return candidate, view.Records[0], retained, nil
}

type textAutoReader struct {
	ctx    context.Context
	data   []byte
	length int64
}

func (reader textAutoReader) ReadAt(data []byte, offset int64) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 || offset > reader.length {
		return 0, io.EOF
	}
	count := int(min(int64(len(data)), reader.length-offset))
	available := int(min(int64(count), max(0, int64(len(reader.data))-offset)))
	if available != 0 {
		copy(data[:available], reader.data[int(offset):int(offset)+available])
	}
	clear(data[available:count])
	if count != len(data) {
		return count, io.EOF
	}
	return count, nil
}
