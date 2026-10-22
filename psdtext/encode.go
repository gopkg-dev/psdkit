package psdtext

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"unicode/utf16"
	"unicode/utf8"
)

// Encode validates and writes EngineData, preserving exact bytes of unchanged syntax.
// Edited containers receive canonical spacing; their unchanged child values retain original bytes.
// Cycles and shared nodes are rejected, and caller-owned syntax and payloads are never changed.
func Encode(ctx context.Context, engine *EngineData, options ...EncodeOptions) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if engine == nil || engine.Root == nil {
		return nil, textError(ErrMalformed, "engineData", 0, "missing syntax root")
	}
	if len(options) > 1 {
		return nil, textError(ErrMalformed, "options", 0, "provide at most one options value")
	}
	var provided DecodeOptions
	if len(options) == 1 {
		provided.MaxBytes = options[0].MaxBytes
		provided.MaxNodes = options[0].MaxNodes
		provided.MaxDepth = options[0].MaxDepth
	}
	limits, err := decodeLimits([]DecodeOptions{provided})
	if err != nil {
		return nil, err
	}
	if limits.MaxBytes < 1024 {
		return nil, textError(ErrLimit, "engineData", 0, "encoder work area exceeds MaxBytes")
	}
	encoder := engineEncoder{ctx: ctx, limits: limits, digests: make(map[*Node][32]byte),
		allocated: 1024, hasher: newEngineHasher()}
	if err := encoder.validate(engine.Root, 1); err != nil {
		return nil, err
	}
	if len(engine.raw) != 0 && encoder.digests[engine.Root] == engine.Root.original &&
		engine.rootBegin >= 0 && engine.rootEnd >= engine.rootBegin && engine.rootEnd <= len(engine.raw) {
		if err := encoder.appendBytes(engine.raw); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return encoder.output, nil
	}
	if len(engine.raw) != 0 && engine.rootBegin >= 0 && engine.rootEnd >= engine.rootBegin &&
		engine.rootEnd <= len(engine.raw) {
		if err := encoder.appendBytes(engine.raw[:engine.rootBegin]); err != nil {
			return nil, err
		}
	}
	var encodeErr error
	if engine.implicitRoot {
		if engine.Root.Kind != Dictionary {
			return nil, textError(ErrMalformed, "engineData", engine.Root.Span.Offset, "document root changed kind")
		}
		encodeErr = encoder.dictionary(engine.Root, true)
	} else {
		encodeErr = encoder.node(engine.Root)
	}
	if encodeErr != nil {
		return nil, encodeErr
	}
	if len(engine.raw) != 0 && engine.rootEnd >= 0 && engine.rootEnd <= len(engine.raw) {
		if err := encoder.appendBytes(engine.raw[engine.rootEnd:]); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoder.output, nil
}

type engineEncoder struct {
	ctx       context.Context
	limits    DecodeOptions
	digests   map[*Node][32]byte
	allocated int64
	output    []byte
	hasher    *engineHasher
}

func (e *engineEncoder) validate(node *Node, depth int) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if node == nil {
		return textError(ErrMalformed, "engineData", 0, "nil child value")
	}
	if _, found := e.digests[node]; found {
		return textError(ErrMalformed, "engineData", node.Span.Offset, "cyclic or shared syntax node")
	}
	if depth > e.limits.MaxDepth || len(e.digests) >= e.limits.MaxNodes || e.limits.MaxBytes-e.allocated < 160 {
		return textError(ErrLimit, "engineData", node.Span.Offset, "validation exceeds node, depth or byte limit")
	}
	e.allocated += 160
	e.digests[node] = [32]byte{}
	payload := max(len(node.Name), len(node.Lexeme), len(node.Text), len(node.Data))
	if int64(payload) > (e.limits.MaxBytes-e.allocated)/2 {
		return textError(ErrLimit, "engineData", node.Span.Offset, "node payload exceeds validation budget")
	}
	e.allocated += int64(payload) * 2
	switch node.Kind {
	case Dictionary:
		for _, entry := range node.Entries {
			if int64(len(entry.Key)) > e.limits.MaxBytes-e.allocated {
				return textError(ErrLimit, "engineData.key", entry.KeySpan.Offset, "key exceeds validation budget")
			}
			e.allocated += int64(len(entry.Key))
			if !engineName(entry.Key) {
				return textError(ErrUnsupported, "engineData.key", entry.KeySpan.Offset, "unsupported literal name")
			}
			if err := e.validate(entry.Value, depth+1); err != nil {
				return err
			}
		}
	case List:
		for _, item := range node.Items {
			if err := e.validate(item, depth+1); err != nil {
				return err
			}
		}
	case Name:
		if !engineName(node.Name) {
			return textError(ErrUnsupported, "engineData.name", node.Span.Offset, "unsupported literal name")
		}
	case Number:
		if !engineNumber(node.Lexeme) {
			return textError(ErrMalformed, "engineData.number", node.Span.Offset, "invalid numeric spelling")
		}
	case String:
		if !utf8.ValidString(node.Text) {
			return textError(ErrMalformed, "engineData.text", node.Span.Offset, "text is not valid UTF-8")
		}
	case Boolean, Bytes:
	default:
		return textError(ErrUnsupported, "engineData.kind", node.Span.Offset, "unknown syntax node kind")
	}
	e.digests[node] = e.hasher.node(node, e.digests)
	return nil
}

func engineName(value string) bool {
	for index := range len(value) {
		if value[index] < 33 || value[index] > 126 || engineDelimiter(value[index]) {
			return false
		}
	}
	return true
}

type engineHasher struct {
	digest hash.Hash
	chunk  [512]byte
	length [8]byte
}

func newEngineHasher() *engineHasher { return &engineHasher{digest: sha256.New()} }

func (h *engineHasher) data(value []byte) {
	binary.BigEndian.PutUint64(h.length[:], uint64(len(value)))
	_, _ = h.digest.Write(h.length[:])
	_, _ = h.digest.Write(value)
}

func (h *engineHasher) text(value string) {
	binary.BigEndian.PutUint64(h.length[:], uint64(len(value)))
	_, _ = h.digest.Write(h.length[:])
	for len(value) != 0 {
		count := copy(h.chunk[:], value)
		_, _ = h.digest.Write(h.chunk[:count])
		value = value[count:]
	}
}

func (h *engineHasher) node(node *Node, children map[*Node][32]byte) [32]byte {
	h.digest.Reset()
	h.chunk[0] = byte(node.Kind)
	_, _ = h.digest.Write(h.chunk[:1])
	childDigest := func(child *Node) {
		value := child.original
		if children != nil {
			value = children[child]
		}
		copy(h.chunk[:], value[:])
		_, _ = h.digest.Write(h.chunk[:32])
	}
	switch node.Kind {
	case Dictionary:
		for _, entry := range node.Entries {
			h.text(entry.Key)
			childDigest(entry.Value)
		}
	case List:
		for _, item := range node.Items {
			childDigest(item)
		}
	case Name:
		h.text(node.Name)
	case Number:
		h.text(node.Lexeme)
	case String:
		h.text(node.Text)
	case Bytes:
		h.data(node.Data)
	case Boolean:
		value := byte(0)
		if node.Bool {
			value = 1
		}
		h.chunk[0] = value
		_, _ = h.digest.Write(h.chunk[:1])
	}
	var result [32]byte
	h.digest.Sum(h.chunk[:0])
	copy(result[:], h.chunk[:32])
	return result
}

func (e *engineEncoder) appendBytes(data []byte) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if int64(len(data)) > e.limits.MaxBytes-e.allocated-int64(len(e.output)) {
		return textError(ErrLimit, "engineData", 0, "encoded bytes exceed MaxBytes")
	}
	if len(data) > int(^uint(0)>>1)-len(e.output) {
		return textError(ErrLimit, "engineData", 0, "encoded length exceeds platform slice bounds")
	}
	needed := len(e.output) + len(data)
	if needed > cap(e.output) {
		capacity := max(needed, max(256, cap(e.output)*2))
		available := e.limits.MaxBytes - e.allocated
		if int64(capacity) > available {
			capacity = needed
		}
		if int64(capacity)+int64(cap(e.output)) > available {
			return textError(ErrLimit, "engineData", 0, "output growth exceeds working byte limit")
		}
		owned := make([]byte, len(e.output), capacity)
		copy(owned, e.output)
		e.output = owned
	}
	e.output = append(e.output, data...)
	return nil
}

func (e *engineEncoder) node(node *Node) error {
	if len(node.encoded) != 0 && e.digests[node] == node.original {
		return e.appendBytes(node.encoded)
	}
	switch node.Kind {
	case Dictionary:
		return e.dictionary(node, false)
	case List:
		if err := e.appendBytes([]byte("[")); err != nil {
			return err
		}
		for _, item := range node.Items {
			if err := e.appendBytes([]byte(" ")); err != nil {
				return err
			}
			if err := e.node(item); err != nil {
				return err
			}
		}
		return e.appendBytes([]byte(" ]"))
	case Name:
		return e.appendBytes([]byte("/" + node.Name))
	case Number:
		return e.appendBytes([]byte(node.Lexeme))
	case Boolean:
		if node.Bool {
			return e.appendBytes([]byte("true"))
		}
		return e.appendBytes([]byte("false"))
	case String:
		if err := e.appendBytes([]byte{'(', 0xfe, 0xff}); err != nil {
			return err
		}
		for _, value := range node.Text {
			if value <= 0xffff {
				if err := e.unit(uint16(value)); err != nil {
					return err
				}
			} else {
				high, low := utf16.EncodeRune(value)
				if err := e.unit(uint16(high)); err != nil {
					return err
				}
				if err := e.unit(uint16(low)); err != nil {
					return err
				}
			}
		}
		return e.appendBytes([]byte(")"))
	case Bytes:
		if err := e.appendBytes([]byte("<")); err != nil {
			return err
		}
		const digits = "0123456789abcdef"
		for _, value := range node.Data {
			if err := e.appendBytes([]byte{digits[value>>4], digits[value&15]}); err != nil {
				return err
			}
		}
		return e.appendBytes([]byte(">"))
	}
	return textError(ErrUnsupported, "engineData.kind", node.Span.Offset, "unknown node kind")
}

func (e *engineEncoder) dictionary(node *Node, implicit bool) error {
	if !implicit {
		if err := e.appendBytes([]byte("<<")); err != nil {
			return err
		}
	}
	for _, entry := range node.Entries {
		if err := e.appendBytes([]byte(" ")); err != nil {
			return err
		}
		if entry.Key == entry.keyValue && len(entry.keyRaw) != 0 {
			if err := e.appendBytes(entry.keyRaw); err != nil {
				return err
			}
		} else if err := e.appendBytes([]byte("/" + entry.Key)); err != nil {
			return err
		}
		if err := e.appendBytes([]byte(" ")); err != nil {
			return err
		}
		if err := e.node(entry.Value); err != nil {
			return err
		}
	}
	if implicit {
		return nil
	}
	return e.appendBytes([]byte(" >>"))
}

func (e *engineEncoder) unit(unit uint16) error {
	for _, value := range []byte{byte(unit >> 8), byte(unit)} {
		if value == '(' || value == ')' || value == '\\' {
			if err := e.appendBytes([]byte{'\\'}); err != nil {
				return err
			}
		}
		if err := e.appendBytes([]byte{value}); err != nil {
			return err
		}
	}
	return nil
}
