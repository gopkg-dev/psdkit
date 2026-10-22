package psdtext

import (
	"context"
	"encoding/binary"
	"math"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// Decode parses bounded, non-executable EngineData into an owned syntax tree.
// Unknown operators and procedures return ErrUnsupported; no PostScript code executes.
func Decode(ctx context.Context, data []byte, options ...DecodeOptions) (*EngineData, error) {
	return decodeEngine(ctx, data, false, options...)
}

// DecodeDocument parses a Txt2 dictionary, including its native omitted outer delimiters.
// Source spans, original bytes and unknown properties are preserved without synthetic source bytes.
func DecodeDocument(ctx context.Context, data []byte, options ...DecodeOptions) (*EngineData, error) {
	return decodeEngine(ctx, data, true, options...)
}

func decodeEngine(ctx context.Context, data []byte, document bool, options ...DecodeOptions) (*EngineData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := decodeLimits(options)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxBytes || int64(len(data)) > math.MaxInt64-limits.SourceOffset {
		return nil, textError(ErrLimit, "engineData", limits.SourceOffset, "original payload exceeds byte or offset limit")
	}
	if limits.MaxBytes-int64(len(data)) < 1024 {
		return nil, textError(ErrLimit, "engineData", limits.SourceOffset, "payload and parser work area exceed MaxBytes")
	}
	cursor := engineCursor{ctx: ctx, limits: limits, allocated: int64(len(data)) + 1024, hasher: newEngineHasher()}
	cursor.data = append([]byte(nil), data...)
	cursor.space()
	begin := cursor.position
	implicit := document && cursor.position < len(data) && cursor.data[cursor.position] == '/'
	var root *Node
	if implicit {
		if !cursor.reserve(int64(unsafe.Sizeof(Node{}))) {
			return nil, cursor.err
		}
		if cursor.nodes == cursor.limits.MaxNodes {
			return nil, textError(ErrLimit, "engineData", cursor.offset(), "root exceeds byte or node limits")
		}
		cursor.nodes++
		root = &Node{Kind: Dictionary}
		cursor.dictionaryBody(root, 1, true)
		rootEnd := cursor.position
		if len(root.Entries) != 0 {
			last := root.Entries[len(root.Entries)-1].Value.Span
			rootEnd = int(last.Offset - limits.SourceOffset + last.Length)
		}
		root.Span = Span{Offset: cursor.limits.SourceOffset + int64(begin), Length: int64(rootEnd - begin)}
		root.encoded = cursor.data[begin:rootEnd]
		root.original = cursor.hasher.node(root, nil)
	} else {
		root = cursor.node(1)
	}
	end := cursor.position
	if implicit {
		end = int(root.Span.Offset - limits.SourceOffset + root.Span.Length)
	}
	cursor.space()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cursor.err != nil {
		return nil, cursor.err
	}
	if cursor.position != len(data) {
		return nil, textError(ErrMalformed, "engineData", cursor.offset(), "extra non-padding data follows the root")
	}
	if document && (root == nil || root.Kind != Dictionary) {
		return nil, textError(ErrMalformed, "engineData", limits.SourceOffset, "document text root is not a dictionary")
	}
	return &EngineData{Root: root, Span: Span{Offset: limits.SourceOffset, Length: int64(len(data))},
		raw: cursor.data, rootBegin: begin, rootEnd: end, implicitRoot: implicit}, nil
}

type engineCursor struct {
	ctx       context.Context
	limits    DecodeOptions
	data      []byte
	position  int
	allocated int64
	nodes     int
	err       error
	hasher    *engineHasher
}

func (c *engineCursor) offset() int64 { return c.limits.SourceOffset + int64(c.position) }

func (c *engineCursor) fail(kind error, detail string) {
	if c.err == nil {
		c.err = textError(kind, "engineData", c.offset(), detail)
	}
}

func (c *engineCursor) check() bool {
	if c.err == nil {
		c.err = c.ctx.Err()
	}
	return c.err == nil
}

func (c *engineCursor) reserve(length int64) bool {
	if !c.check() {
		return false
	}
	if length < 0 || length > c.limits.MaxBytes-c.allocated || uint64(length) > uint64(^uint(0)>>1) {
		c.fail(ErrLimit, "owned syntax or decoded values exceed MaxBytes")
		return false
	}
	c.allocated += length
	return true
}

func engineSpace(value byte) bool {
	return value == 0 || value == 9 || value == 10 || value == 12 || value == 13 || value == 32
}

func engineDelimiter(value byte) bool {
	return engineSpace(value) || value == '/' || value == '(' || value == ')' || value == '[' ||
		value == ']' || value == '<' || value == '>' || value == '{' || value == '}' || value == '%'
}

func (c *engineCursor) space() {
	for c.position < len(c.data) && c.err == nil {
		if c.position%1024 == 0 && !c.check() {
			return
		}
		value := c.data[c.position]
		if engineSpace(value) {
			c.position++
			continue
		}
		if value != '%' {
			return
		}
		for c.position < len(c.data) && c.data[c.position] != '\n' && c.data[c.position] != '\r' {
			c.position++
			if c.position%1024 == 0 && !c.check() {
				return
			}
		}
	}
}

func (c *engineCursor) node(depth int) *Node {
	c.space()
	if !c.check() {
		return nil
	}
	if depth > c.limits.MaxDepth || c.nodes == c.limits.MaxNodes {
		c.fail(ErrLimit, "syntax exceeds MaxDepth or MaxNodes")
		return nil
	}
	if c.position == len(c.data) {
		c.fail(ErrMalformed, "missing value or closing delimiter")
		return nil
	}
	if !c.reserve(int64(unsafe.Sizeof(Node{}))) {
		return nil
	}
	c.nodes++
	begin := c.position
	result := &Node{}
	switch c.data[c.position] {
	case '<':
		if c.position+1 < len(c.data) && c.data[c.position+1] == '<' {
			result.Kind = Dictionary
			c.position += 2
			c.dictionary(result, depth)
		} else {
			result.Kind = Bytes
			result.Data = c.hexString()
		}
	case '[':
		result.Kind = List
		c.position++
		for c.err == nil {
			c.space()
			if c.position < len(c.data) && c.data[c.position] == ']' {
				c.position++
				break
			}
			value := c.node(depth + 1)
			if c.err != nil {
				break
			}
			if len(result.Items) == cap(result.Items) {
				capacity := max(1, cap(result.Items)*2)
				if !c.reserve(int64(capacity) * int64(unsafe.Sizeof((*Node)(nil)))) {
					break
				}
				owned := make([]*Node, len(result.Items), capacity)
				copy(owned, result.Items)
				result.Items = owned
			}
			result.Items = append(result.Items, value)
		}
	case '/':
		result.Kind = Name
		result.Name = c.name()
	case '(':
		c.literal(result)
	case ']', '>', ')':
		c.fail(ErrMalformed, "unexpected closing delimiter")
	case '{', '}':
		c.fail(ErrUnsupported, "executable procedures are not EngineData values")
	default:
		c.atom(result)
	}
	if c.err != nil {
		return nil
	}
	result.Span = Span{Offset: c.limits.SourceOffset + int64(begin), Length: int64(c.position - begin)}
	result.encoded = c.data[begin:c.position]
	result.original = c.hasher.node(result, nil)
	return result
}

func (c *engineCursor) dictionary(result *Node, depth int) {
	c.dictionaryBody(result, depth, false)
}

func (c *engineCursor) dictionaryBody(result *Node, depth int, implicit bool) {
	for c.err == nil {
		c.space()
		if implicit && c.position == len(c.data) {
			return
		}
		if c.position+1 < len(c.data) && c.data[c.position] == '>' && c.data[c.position+1] == '>' {
			if implicit {
				c.fail(ErrMalformed, "omitted dictionary has an unmatched closing delimiter")
				return
			}
			c.position += 2
			return
		}
		if c.position == len(c.data) || c.data[c.position] != '/' {
			c.fail(ErrMalformed, "dictionary entry requires a literal name and value")
			return
		}
		begin := c.position
		key := c.name()
		keyEnd := c.position
		value := c.node(depth + 1)
		if c.err != nil {
			return
		}
		if len(result.Entries) == cap(result.Entries) {
			capacity := max(1, cap(result.Entries)*2)
			if !c.reserve(int64(capacity) * int64(unsafe.Sizeof(Entry{}))) {
				return
			}
			owned := make([]Entry, len(result.Entries), capacity)
			copy(owned, result.Entries)
			result.Entries = owned
		}
		result.Entries = append(result.Entries, Entry{Key: key,
			KeySpan: Span{Offset: c.limits.SourceOffset + int64(begin), Length: int64(keyEnd - begin)},
			Value:   value, keyRaw: c.data[begin:keyEnd], keyValue: key})
	}
}

func (c *engineCursor) name() string {
	c.position++
	begin := c.position
	for c.position < len(c.data) && !engineDelimiter(c.data[c.position]) {
		if c.data[c.position] < 33 || c.data[c.position] > 126 {
			c.fail(ErrUnsupported, "non-ASCII literal names are outside the EngineData subset")
			return ""
		}
		c.position++
		if c.position%1024 == 0 && !c.check() {
			return ""
		}
	}
	if !c.reserve(int64(c.position - begin)) {
		return ""
	}
	return string(c.data[begin:c.position])
}

func (c *engineCursor) atom(result *Node) {
	begin := c.position
	for c.position < len(c.data) && !engineDelimiter(c.data[c.position]) {
		c.position++
		if c.position%1024 == 0 && !c.check() {
			return
		}
	}
	data := c.data[begin:c.position]
	if !c.reserve(int64(len(data))) {
		return
	}
	value := string(data)
	if value == "true" || value == "false" {
		result.Kind, result.Bool = Boolean, value == "true"
		return
	}
	if !engineNumber(value) {
		c.fail(ErrUnsupported, "unknown bare token or executable operator")
		return
	}
	result.Kind, result.Lexeme = Number, value
}

func engineNumber(value string) bool {
	index, digits := 0, 0
	if len(value) > 0 && (value[0] == '+' || value[0] == '-') {
		index++
	}
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
		digits++
	}
	if index < len(value) && value[index] == '.' {
		index++
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		index++
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			index++
		}
		begin := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == begin {
			return false
		}
	}
	return index == len(value)
}

func (c *engineCursor) literal(result *Node) {
	c.position++
	begin, depth := c.position, 1
	nextCheck := c.position + 1024
	for c.position < len(c.data) && c.err == nil {
		value := c.data[c.position]
		c.position++
		if value == '\\' {
			if c.position == len(c.data) {
				break
			}
			c.position++
		} else if value == '(' {
			depth++
			if depth > c.limits.MaxDepth {
				c.fail(ErrLimit, "literal string nesting exceeds MaxDepth")
				return
			}
		} else if value == ')' {
			depth--
			if depth == 0 {
				payload := c.unescape(c.data[begin : c.position-1])
				if c.err != nil {
					return
				}
				result.Kind, result.Data = Bytes, payload
				if len(payload) >= 2 && payload[0] == 0xfe && payload[1] == 0xff {
					result.Kind, result.Data = String, nil
					result.Text = c.unicodeString(payload[2:])
				}
				return
			}
		}
		if c.position >= nextCheck {
			if !c.check() {
				return
			}
			nextCheck = c.position + 1024
		}
	}
	c.fail(ErrMalformed, "unterminated literal string")
}

func (c *engineCursor) unescape(data []byte) []byte {
	if !c.reserve(int64(len(data))) {
		return nil
	}
	result := make([]byte, 0, len(data))
	for index := 0; index < len(data); index++ {
		if index%1024 == 0 && !c.check() {
			return nil
		}
		value := data[index]
		if value != '\\' {
			result = append(result, value)
			continue
		}
		index++
		if index == len(data) {
			c.fail(ErrMalformed, "incomplete literal-string escape")
			return nil
		}
		value = data[index]
		switch value {
		case 'n':
			value = '\n'
		case 'r':
			value = '\r'
		case 't':
			value = '\t'
		case 'b':
			value = '\b'
		case 'f':
			value = '\f'
		case '\n':
			continue
		case '\r':
			if index+1 < len(data) && data[index+1] == '\n' {
				index++
			}
			continue
		default:
			if value >= '0' && value <= '7' {
				decoded := int(value - '0')
				for count := 1; count < 3 && index+1 < len(data) && data[index+1] >= '0' && data[index+1] <= '7'; count++ {
					index++
					decoded = decoded*8 + int(data[index]-'0')
				}
				value = byte(decoded)
			}
		}
		result = append(result, value)
	}
	return result
}

func (c *engineCursor) unicodeString(data []byte) string {
	if len(data)%2 != 0 {
		c.fail(ErrMalformed, "UTF-16BE string has an odd byte length")
		return ""
	}
	if !c.reserve(int64(len(data)) * 4) {
		return ""
	}
	result := make([]byte, 0, len(data)*2)
	for index := 0; index < len(data); index += 2 {
		if index%1024 == 0 && !c.check() {
			return ""
		}
		unit := binary.BigEndian.Uint16(data[index:])
		value := rune(unit)
		if unit >= 0xd800 && unit <= 0xdbff {
			if index+3 >= len(data) {
				c.fail(ErrMalformed, "UTF-16BE string ends with an unpaired high surrogate")
				return ""
			}
			low := binary.BigEndian.Uint16(data[index+2:])
			if low < 0xdc00 || low > 0xdfff {
				c.fail(ErrMalformed, "UTF-16BE string contains an unpaired high surrogate")
				return ""
			}
			value = utf16.DecodeRune(rune(unit), rune(low))
			index += 2
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			c.fail(ErrMalformed, "UTF-16BE string contains an unpaired low surrogate")
			return ""
		}
		result = utf8.AppendRune(result, value)
	}
	return string(result)
}

func engineHex(value byte) int {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0')
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10
	case value >= 'A' && value <= 'F':
		return int(value-'A') + 10
	default:
		return -1
	}
}

func (c *engineCursor) hexString() []byte {
	c.position++
	begin := c.position
	for c.position < len(c.data) && c.data[c.position] != '>' {
		if c.position%1024 == 0 && !c.check() {
			return nil
		}
		c.position++
	}
	if c.position == len(c.data) {
		c.fail(ErrMalformed, "unterminated hex string")
		return nil
	}
	if !c.reserve(int64(c.position-begin+1) / 2) {
		return nil
	}
	result := make([]byte, 0, (c.position-begin+1)/2)
	nibble := -1
	for index := begin; index < c.position; index++ {
		if index%1024 == 0 && !c.check() {
			return nil
		}
		value := c.data[index]
		if engineSpace(value) {
			continue
		}
		digit := engineHex(value)
		if digit < 0 {
			c.fail(ErrMalformed, "hex string contains a non-hexadecimal byte")
			return nil
		}
		if nibble < 0 {
			nibble = digit
		} else {
			result = append(result, byte(nibble*16+digit))
			nibble = -1
		}
	}
	if nibble >= 0 {
		result = append(result, byte(nibble*16))
	}
	c.position++
	return result
}
