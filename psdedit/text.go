package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdtext"
)

// TextEditOptions supplies accurate replacement pixels for the edited text layer.
// Pixels contains straight-alpha RGB8 source artwork before layer opacity, masks and effects.
// Its rectangle is the new absolute document-coordinate layer rectangle, including any transparent padding.
// The caller must ensure that these pixels represent text with the retained typography and affine transform.
// The library validates geometry and ownership, not agreement between the supplied artwork and text.
type TextEditOptions struct {
	Pixels *image.NRGBA
}

// SetTextContent changes an existing single-style Unicode point-text layer without a document-wide Txt2 block.
// Accurate new layer pixels are mandatory: Photoshop does not automatically refresh its saved text raster on open.
// Newlines are normalized to carriage returns and character/paragraph ranges use UTF-16 code units.
// Control characters other than CR/LF are unsupported, as are Unicode line and paragraph separators.
// Ordered descriptors, unknown engine properties, typography, affine bits, warp, TySh bounds and tail are preserved.
// The replacement source artwork is copied and committed atomically with the updated TySh payload.
// Modern Txt2 text, multiple character/paragraph styles, paragraph text and warps return ErrUnsupported.
// Moving relative masks or resizing unknown extra layer channels is also unsupported.
func (e *Editor) SetTextContent(ctx context.Context, id LayerID, text string, options TextEditOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.checkRGBLocked(); err != nil {
		return err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	if options.Pixels == nil {
		return editError(ErrUnsupported, "layers.text.pixels", "accurate replacement text pixels are required")
	}
	block, err := e.textEditBlock(node)
	if err != nil {
		return err
	}
	if err := e.textEditGeometry(node, options.Pixels.Rect); err != nil {
		return err
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	pixelBytes, err := textEditPixelBytes(options.Pixels, limits, available)
	if err != nil {
		return err
	}
	work := available - pixelBytes - 4096
	if work <= 0 || int64(len(text)) > work/64 {
		return editError(psdfile.ErrLimit, "layers.text", "new text and pixels exceed the peak working budget")
	}
	text, err = textEditNormalize(ctx, text)
	if err != nil {
		return err
	}
	bridgeBudget := min(limits.MaxMetadataBytes, work/4)
	payload := node.additional[block.Span.Offset]
	if payload == nil {
		if block.Data.Length > bridgeBudget/4 {
			return editError(psdfile.ErrLimit, "layers.text", "source text payload exceeds the working budget")
		}
		payload, err = textEditReadPayload(ctx, e.source, block.Data)
		if err != nil {
			return err
		}
	}
	bridge, record, err := textEditSource(ctx, payload, limits, bridgeBudget)
	if err != nil {
		return textEditError(err)
	}
	defer func() { _ = bridge.Close() }()
	engineBudget, metadataBudget := min(limits.MaxMetadataBytes, work/6), min(limits.MaxMetadataBytes, work/12)
	engine, err := bridge.DecodeTextEngine(ctx, record, psdtext.DecodeOptions{MaxBytes: engineBudget})
	if err != nil {
		return textEditError(err)
	}
	metadata, err := engine.Metadata(ctx, psdtext.MetadataOptions{MaxBytes: metadataBudget,
		MaxRuns: limits.MaxDescriptorItems, MaxFonts: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	if err := textEditCapability(ctx, record, metadata); err != nil {
		return err
	}
	if record.Text == text && node.pixels != nil && node.bounds == options.Pixels.Rect &&
		textEditPixelsEqual(ctx, node.pixels, options.Pixels) {
		return ctx.Err()
	}
	patchBudget := work / 12
	if err := patchTextEngine(ctx, engine, metadata, text, patchBudget, limits.MaxDescriptorItems); err != nil {
		return err
	}
	encodedEngine, err := psdtext.Encode(ctx, engine, psdtext.EncodeOptions{MaxBytes: work / 12,
		MaxDepth: min(256, limits.MaxDescriptorDepth), MaxNodes: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	for index := range record.TextDescriptor.Entries {
		entry := &record.TextDescriptor.Entries[index]
		switch entry.Key {
		case "Txt ":
			entry.Value = text
		case "EngineData":
			entry.Value = encodedEngine
		}
	}
	encodeLimits := limits
	encodeLimits.MaxMetadataBytes = min(limits.MaxMetadataBytes, work/12)
	encodedDescriptor, err := bridge.EncodeDescriptor(ctx, record.TextDescriptor, true,
		psdfile.DescriptorEncodeOptions{Limits: encodeLimits, MaxEncodedBytes: work / 12})
	if err != nil {
		return textEditError(err)
	}
	start := record.Block.Data.Offset
	prefixLength := record.Prefix.Length
	suffixStart := record.WarpVersionData.Offset - start
	length := prefixLength + int64(len(encodedDescriptor)) + int64(len(payload)) - suffixStart
	// Native TySh counts its final alignment bytes inside the local tagged-block payload.
	length += (4 - length%4) % 4
	if length < 0 || length > work/12 || uint64(length) > uint64(^uint(0)>>1) {
		return editError(psdfile.ErrLimit, "layers.text", "updated text payload exceeds the working budget")
	}
	encoded := make([]byte, int(length))
	position := copy(encoded, payload[:prefixLength])
	position += copy(encoded[position:], encodedDescriptor)
	copy(encoded[position:], payload[suffixStart:])
	owned := image.NewNRGBA(image.Rect(0, 0, options.Pixels.Rect.Dx(), options.Pixels.Rect.Dy()))
	for row := range owned.Rect.Dy() {
		for column := 0; column < owned.Stride; column += 32 << 10 {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(column+32<<10, owned.Stride)
			copy(owned.Pix[row*owned.Stride+column:row*owned.Stride+end],
				options.Pixels.Pix[row*options.Pixels.Stride+column:row*options.Pixels.Stride+end])
		}
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	node.additional[block.Span.Offset] = encoded
	node.pixels, node.bounds = owned, options.Pixels.Rect
	node.setSemanticCost("text", 256)
	e.markChangedLocked(false)
	return nil
}

func textEditReadPayload(ctx context.Context, source *psdfile.Document, span psdfile.Span) ([]byte, error) {
	if span.Length < 0 || span.Length > source.Limits().MaxReadBytes ||
		uint64(span.Length) > uint64(^uint(0)>>1) {
		return nil, editError(psdfile.ErrLimit, "layers.text", "text payload exceeds the bounded read budget")
	}
	data := make([]byte, int(span.Length))
	for offset := 0; offset < len(data); offset += 32 << 10 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(offset+32<<10, len(data))
		count, err := source.ReadAt(data[offset:end], span.Offset+int64(offset))
		if err != nil || count != end-offset {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return nil, textEditError(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (e *Editor) textEditBlock(node *editNode) (psdfile.AdditionalBlock, error) {
	var result psdfile.AdditionalBlock
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) || len(node.planes) != 0 {
		return result, editError(ErrUnsupported, "layers.text", "an existing text layer without channel overlays is required")
	}
	for _, block := range e.source.AdditionalInfo {
		if block.Key == "Txt2" {
			return result, editError(ErrUnsupported, "layers.text.Txt2",
				"document-wide private text state cannot be synchronized")
		}
	}
	count := 0
	var colors [3]int
	alpha := 0
	layer := e.source.Layers[node.sourceIndex]
	for _, channel := range layer.Channels {
		if channel.ID >= 0 && channel.ID < 3 {
			colors[channel.ID]++
		}
		if channel.ID == -1 {
			alpha++
		}
	}
	if colors != [3]int{1, 1, 1} || alpha > 1 || layer.Divider != 0 {
		return result, editError(ErrUnsupported, "layers.text.channels", "three unambiguous source RGB channels are required")
	}
	for _, block := range layer.AdditionalInfo {
		switch block.Key {
		case "TySh":
			result, count = block, count+1
		case "tySh", "Txt2", "SoLd", "SoLE", "PlLd", "SoCo", "GdFl", "PtFl", "vscg", "vstk", "PxSc", "PxSD",
			"brit", "levl", "curv", "expA", "vibA", "hue ", "hue2", "blnc", "blwh", "phfl", "mixr",
			"clrL", "nvrt", "post", "thrs", "grdm", "selc", "CgEd":
			return result, editError(ErrUnsupported, "layers.text", "incompatible semantic source block")
		}
	}
	if count != 1 {
		return result, editError(ErrUnsupported, "layers.text", "one unambiguous modern TySh block is required")
	}
	return result, nil
}

func (e *Editor) textEditGeometry(node *editNode, bounds image.Rectangle) error {
	layer := e.source.Layers[node.sourceIndex]
	if bounds.Min != layer.Bounds.Min && layer.Mask != nil &&
		(layer.Mask.Flags&1 != 0 || layer.Mask.HasReal && layer.Mask.RealFlags&1 != 0) {
		return editError(ErrUnsupported, "layers.text.mask", "moving layer-relative source masks is unsupported")
	}
	if bounds.Dx() == layer.Bounds.Dx() && bounds.Dy() == layer.Bounds.Dy() {
		return nil
	}
	for _, channel := range layer.Channels {
		if channel.ID < -3 || channel.ID > 2 {
			return editError(ErrUnsupported, "layers.text.channels", "resizing unknown source channels is unsupported")
		}
	}
	return nil
}

func textEditPixelBytes(pixels *image.NRGBA, limits psdfile.Limits, budget int64) (int64, error) {
	for _, coordinate := range []int{pixels.Rect.Min.X, pixels.Rect.Min.Y, pixels.Rect.Max.X, pixels.Rect.Max.Y} {
		if int64(coordinate) < math.MinInt32 || int64(coordinate) > math.MaxInt32 {
			return 0, editError(ErrInvalidEdit, "layers.text.pixels", "absolute layer rectangle exceeds signed 32-bit fields")
		}
	}
	width, height := int64(pixels.Rect.Max.X)-int64(pixels.Rect.Min.X),
		int64(pixels.Rect.Max.Y)-int64(pixels.Rect.Min.Y)
	if width <= 0 || height <= 0 || pixels.Stride < 0 {
		return 0, editError(ErrInvalidEdit, "layers.text.pixels", "positive pixel dimensions and stride are required")
	}
	if width > limits.MaxDecodePixels/height || width > budget/4/height ||
		width > int64(^uint(0)>>1)/4/height {
		return 0, editError(psdfile.ErrLimit, "layers.text.pixels", "pixel samples exceed the peak working budget")
	}
	rowBytes := int(width * 4)
	if pixels.Stride < rowBytes || len(pixels.Pix) < rowBytes ||
		height-1 > int64((len(pixels.Pix)-rowBytes)/pixels.Stride) {
		return 0, editError(ErrInvalidEdit, "layers.text.pixels", "pixel stride or backing buffer is truncated")
	}
	return width * height * 4, nil
}

func textEditPixelsEqual(ctx context.Context, owned, supplied *image.NRGBA) bool {
	if owned.Rect.Dx() != supplied.Rect.Dx() || owned.Rect.Dy() != supplied.Rect.Dy() {
		return false
	}
	for row := range owned.Rect.Dy() {
		if ctx.Err() != nil || !bytes.Equal(owned.Pix[row*owned.Stride:(row+1)*owned.Stride],
			supplied.Pix[row*supplied.Stride:row*supplied.Stride+owned.Stride]) {
			return false
		}
	}
	return true
}

func textEditNormalize(ctx context.Context, value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", editError(ErrInvalidEdit, "layers.text.content", "text is not valid UTF-8")
	}
	nextCheck := 0
	for index, character := range value {
		if index >= nextCheck {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			nextCheck = index + 512
		}
		if character == '\r' || character == '\n' {
			continue
		}
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' {
			return "", editError(ErrUnsupported, "layers.text.content",
				"control characters and Unicode line/paragraph separators are unsupported; use CR or LF")
		}
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\r"), "\n", "\r")
	return value, ctx.Err()
}

func textEditSource(
	ctx context.Context, payload []byte, limits psdfile.Limits, budget int64,
) (*psdfile.Document, psdfile.TextLayerRecord, error) {
	var empty psdfile.TextLayerRecord
	padding := len(payload) % 2
	length := int64(len(payload)) + int64(padding) + 111
	if length > budget/4 || length > math.MaxUint32 || uint64(length) > uint64(^uint(0)>>1) {
		return nil, empty, editError(psdfile.ErrLimit, "layers.text", "text envelope exceeds the working budget")
	}
	data := make([]byte, int(length))
	copy(data, "8BPS")
	binary.BigEndian.PutUint16(data[4:], 1)
	binary.BigEndian.PutUint16(data[12:], 3)
	binary.BigEndian.PutUint32(data[14:], 1)
	binary.BigEndian.PutUint32(data[18:], 1)
	binary.BigEndian.PutUint16(data[22:], 8)
	binary.BigEndian.PutUint16(data[24:], 3)
	binary.BigEndian.PutUint32(data[34:], uint32(68+len(payload)+padding))
	binary.BigEndian.PutUint32(data[38:], uint32(60+len(payload)+padding))
	binary.BigEndian.PutUint16(data[42:], 1)
	binary.BigEndian.PutUint32(data[52:], 1)
	binary.BigEndian.PutUint32(data[56:], 1)
	copy(data[62:], "8BIMnorm")
	data[70], data[72] = 255, 8
	binary.BigEndian.PutUint32(data[74:], uint32(24+len(payload)+padding))
	copy(data[90:], "8BIMTySh")
	binary.BigEndian.PutUint32(data[98:], uint32(len(payload)))
	copy(data[102:], payload)
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, budget/4)
	limits.MaxDecodeBytes = budget
	source, err := psdfile.Read(gradientEditReader{ctx: ctx, reader: bytes.NewReader(data)}, length,
		psdfile.LoadOptions{Limits: limits})
	if err != nil {
		return nil, empty, err
	}
	view, err := source.ReadTextLayerView(ctx, psdfile.MetadataOptions{MaxBytes: limits.MaxMetadataBytes})
	if err != nil || len(view.Records) != 1 ||
		length+int64(len(payload))+source.IndexMetadataBytes()+view.MetadataBytes > budget {
		_ = source.Close()
		if err == nil {
			err = editError(psdfile.ErrLimit, "layers.text", "text view exceeds the working budget")
		}
		return nil, empty, err
	}
	return source, view.Records[0], nil
}

func textEditCapability(ctx context.Context, record psdfile.TextLayerRecord, metadata *psdtext.Metadata) error {
	textCount, engineCount := 0, 0
	for _, entry := range record.TextDescriptor.Entries {
		switch entry.Key {
		case "Txt ":
			textCount++
		case "EngineData":
			engineCount++
		}
		if entry.Key == "Txt " && entry.Type != "TEXT" || entry.Key == "EngineData" && entry.Type != "tdta" {
			return editError(ErrUnsupported, "layers.text", "unexpected text descriptor value type")
		}
	}
	if textCount != 1 || engineCount != 1 || len(metadata.Styles) != 1 ||
		metadata.Text != record.Text+"\r" || len(metadata.Paragraphs) == 0 {
		return editError(ErrUnsupported, "layers.text", "one complete character style and matching engine text are required")
	}
	if _, err := textEditNormalize(ctx, record.Text); err != nil {
		return err
	}
	if len(metadata.Paragraphs) != strings.Count(record.Text, "\r")+1 {
		return editError(ErrUnsupported, "layers.text.paragraph", "paragraph ranges do not match explicit line boundaries")
	}
	for _, key := range []string{"Ornt", "textGridding"} {
		value := gradientFind(record.TextDescriptor, key)
		if value == nil {
			return editError(ErrUnsupported, "layers.text", "explicit horizontal text and gridding metadata are required")
		}
		enumeration, ok := value.Value.(psdfile.DescriptorEnum)
		if !ok || key == "Ornt" && enumeration.ValueID != "Hrzn" ||
			key == "textGridding" && enumeration.ValueID != "None" {
			return editError(ErrUnsupported, "layers.text", "vertical or gridded text is unsupported")
		}
	}
	warp := gradientFind(record.Warp, "warpStyle")
	if warp == nil {
		return editError(ErrUnsupported, "layers.text.warp", "explicit unwarped source metadata is required")
	}
	style, ok := warp.Value.(psdfile.DescriptorEnum)
	if !ok || style.ValueID != "warpNone" {
		return editError(ErrUnsupported, "layers.text.warp", "warped text editing is unsupported")
	}
	children := metadata.Engine.Lookup("Rendered").Lookup("Shapes").Lookup("Children")
	if children == nil || children.Kind != psdtext.List || len(children.Items) != 1 {
		return editError(ErrUnsupported, "layers.text.shape", "one explicit point-text shape is required")
	}
	shape := children.Items[0]
	for _, value := range []*psdtext.Node{shape.Lookup("ShapeType"),
		shape.Lookup("Cookie").Lookup("Photoshop").Lookup("ShapeType")} {
		kind, found := value.Int64()
		if !found || kind != 0 {
			return editError(ErrUnsupported, "layers.text.shape", "paragraph text is unsupported")
		}
	}
	for _, paragraph := range metadata.Paragraphs[1:] {
		if !textEditNodeEqual(metadata.Paragraphs[0].Data, paragraph.Data) {
			return editError(ErrUnsupported, "layers.text.paragraph", "different paragraph styles cannot be propagated safely")
		}
	}
	return ctx.Err()
}

func patchTextEngine(
	ctx context.Context, engine *psdtext.EngineData, metadata *psdtext.Metadata, text string, budget int64, maxNodes int,
) error {
	dictionary, err := textEditUnique(engine.Root, "EngineDict", psdtext.Dictionary)
	if err != nil {
		return err
	}
	editor, err := textEditUnique(dictionary, "Editor", psdtext.Dictionary)
	if err != nil {
		return err
	}
	textValue, err := textEditUnique(editor, "Text", psdtext.String)
	if err != nil {
		return err
	}
	style, err := textEditUnique(dictionary, "StyleRun", psdtext.Dictionary)
	if err != nil {
		return err
	}
	styleLengths, err := textEditUnique(style, "RunLengthArray", psdtext.List)
	if err != nil {
		return err
	}
	paragraph, err := textEditUnique(dictionary, "ParagraphRun", psdtext.Dictionary)
	if err != nil {
		return err
	}
	paragraphs, err := textEditUnique(paragraph, "RunArray", psdtext.List)
	if err != nil {
		return err
	}
	paragraphLengths, err := textEditUnique(paragraph, "RunLengthArray", psdtext.List)
	if err != nil {
		return err
	}
	count := strings.Count(text, "\r") + 1
	if count > maxNodes/4 || int64(count) > budget/512 || int64(len(text)) > budget/8 {
		return editError(psdfile.ErrLimit, "layers.text.runs", "UTF-16 range reconstruction exceeds the working budget")
	}
	parts := strings.Split(text, "\r")
	newParagraphs := make([]*psdtext.Node, count)
	newLengths := make([]*psdtext.Node, count)
	allocated := int64(count)*32 + int64(len(text))*4 + 512
	units, nodes := int64(0), 0
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := int64(1)
		for _, character := range part {
			length++
			if character > 0xffff {
				length++
			}
		}
		units += length
		cloned, err := textEditClone(ctx, metadata.Paragraphs[0].Data, &allocated, budget, &nodes, maxNodes)
		if err != nil {
			return err
		}
		newParagraphs[index] = cloned
		newLengths[index] = &psdtext.Node{Kind: psdtext.Number, Lexeme: strconv.FormatInt(length, 10)}
	}
	textValue.Text = text + "\r"
	styleLengths.Items = []*psdtext.Node{{Kind: psdtext.Number, Lexeme: strconv.FormatInt(units, 10)}}
	paragraphs.Items, paragraphLengths.Items = newParagraphs, newLengths
	return ctx.Err()
}

func textEditUnique(node *psdtext.Node, key string, kind psdtext.Kind) (*psdtext.Node, error) {
	var result *psdtext.Node
	if node != nil {
		for _, entry := range node.Entries {
			if entry.Key == key {
				if result != nil {
					return nil, editError(ErrUnsupported, "layers.text.engine", "ambiguous duplicate edited engine property")
				}
				result = entry.Value
			}
		}
	}
	if result == nil || result.Kind != kind {
		return nil, editError(ErrUnsupported, "layers.text.engine", "missing or unexpected edited engine property")
	}
	return result, nil
}

func textEditClone(
	ctx context.Context, source *psdtext.Node, allocated *int64, budget int64, nodes *int, maxNodes int,
) (*psdtext.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*nodes++
	reservation := int64(unsafe.Sizeof(psdtext.Node{})) +
		int64(len(source.Entries))*int64(unsafe.Sizeof(psdtext.Entry{})) + int64(len(source.Items))*8
	if *nodes > maxNodes || reservation > budget-*allocated {
		return nil, editError(psdfile.ErrLimit, "layers.text.runs",
			"owned paragraph reconstruction exceeds the working budget")
	}
	*allocated += reservation
	cloned := *source
	cloned.Entries = make([]psdtext.Entry, len(source.Entries))
	cloned.Items = make([]*psdtext.Node, len(source.Items))
	for index, entry := range source.Entries {
		value, err := textEditClone(ctx, entry.Value, allocated, budget, nodes, maxNodes)
		if err != nil {
			return nil, err
		}
		cloned.Entries[index], cloned.Entries[index].Value = entry, value
	}
	for index, item := range source.Items {
		value, err := textEditClone(ctx, item, allocated, budget, nodes, maxNodes)
		if err != nil {
			return nil, err
		}
		cloned.Items[index] = value
	}
	return &cloned, nil
}

func textEditNodeEqual(left, right *psdtext.Node) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.Kind != right.Kind || left.Name != right.Name || left.Lexeme != right.Lexeme || left.Bool != right.Bool ||
		left.Text != right.Text || !bytes.Equal(left.Data, right.Data) ||
		len(left.Entries) != len(right.Entries) || len(left.Items) != len(right.Items) {
		return false
	}
	for index, entry := range left.Entries {
		if entry.Key != right.Entries[index].Key || !textEditNodeEqual(entry.Value, right.Entries[index].Value) {
			return false
		}
	}
	for index, item := range left.Items {
		if !textEditNodeEqual(item, right.Items[index]) {
			return false
		}
	}
	return true
}

func textEditError(err error) error {
	if errors.Is(err, psdfile.ErrClosed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalidEdit
	if errors.Is(err, psdtext.ErrLimit) || errors.Is(err, psdfile.ErrLimit) {
		kind = psdfile.ErrLimit
	} else if errors.Is(err, psdtext.ErrUnsupported) || errors.Is(err, psdfile.ErrUnsupported) {
		kind = ErrUnsupported
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "layers.text",
		Detail: "text metadata operation failed", Cause: err}
}
