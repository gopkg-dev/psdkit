package psdtext

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func textTestString(value string) []byte {
	result := []byte{'(', 0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(value)) {
		for _, valueByte := range []byte{byte(unit >> 8), byte(unit)} {
			if valueByte == '(' || valueByte == ')' || valueByte == '\\' {
				result = append(result, '\\')
			}
			result = append(result, valueByte)
		}
	}
	return append(result, ')')
}

func TestDecodePreservesEngineSyntaxOrderAndExactBytes(t *testing.T) {
	wantText := "中文😀\x00(hi)\\\r"
	data := []byte("\n% retained comment\n<< /same -0002 /same +3e0 /a [false /N .5 99999999999999999999999")
	data = append(data, []byte(" << /raw (hwid) /hex <00 ff0> >>] /text ")...)
	data = append(data, textTestString(wantText)...)
	data = append(data, []byte(" >>\x00\x00")...)
	want := append([]byte(nil), data...)
	engine, err := Decode(nil, data, DecodeOptions{SourceOffset: 400})
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	if engine.Root.Kind != Dictionary || len(engine.Root.Entries) != 4 ||
		engine.Root.Entries[0].Key != "same" || engine.Root.Entries[1].Key != "same" {
		t.Fatal("dictionary order or duplicate properties were lost")
	}
	if engine.Root.Lookup("same").Lexeme != "+3e0" || engine.Root.Lookup("text").Text != wantText {
		t.Fatal("numeric spelling or exact Unicode text was changed")
	}
	items := engine.Root.Lookup("a").Items
	if items[0].Kind != Boolean || items[0].Bool || items[1].Name != "N" || items[2].Lexeme != ".5" {
		t.Fatal("scalar kinds were incorrectly decoded")
	}
	if _, ok := items[3].Int64(); ok {
		t.Fatal("large integer silently overflowed its typed conversion")
	}
	if !bytes.Equal(items[4].Lookup("raw").Data, []byte("hwid")) ||
		!bytes.Equal(items[4].Lookup("hex").Data, []byte{0, 255, 0}) {
		t.Fatal("raw tag or odd-length hex string was changed")
	}
	if engine.Root.Span.Offset <= 400 || engine.Span != (Span{Offset: 400, Length: int64(len(want))}) {
		t.Fatal("original source offsets were not retained")
	}
	original := engine.OriginalBytes()
	clear(original)
	encoded, err := Encode(nil, engine)
	if err != nil || !bytes.Equal(encoded, want) {
		t.Fatalf("unmodified EngineData was not byte-preserved: %v", err)
	}
	engine.Root.Lookup("text").Text = "edited😀\x00)"
	encoded, err = Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("-0002")) || !bytes.Contains(encoded, []byte("<00 ff0>")) ||
		!bytes.HasPrefix(encoded, []byte("\n% retained comment\n")) || !bytes.HasSuffix(encoded, []byte{0, 0}) {
		t.Fatal("edited container lost its unmodified values or outer bytes")
	}
	again, err := Decode(nil, encoded)
	if err != nil || again.Root.Lookup("text").Text != "edited😀\x00)" {
		t.Fatalf("edited UTF-16 literal did not round trip: %v", err)
	}
	if !bytes.Equal(engine.OriginalBytes(), want) {
		t.Fatal("syntax editing changed the retained original bytes")
	}
}

func TestDecodeLiteralEscapesAndUnsupportedBoundaries(t *testing.T) {
	engine, err := Decode(nil, []byte("[(a(b)c\\n\\050\\051\\\\\\\r\nX) <f>]"))
	if err != nil || !bytes.Equal(engine.Root.Items[0].Data, []byte("a(b)c\n()\\X")) ||
		!bytes.Equal(engine.Root.Items[1].Data, []byte{0xf0}) {
		t.Fatalf("literal string escapes are inconsistent: %v", err)
	}
	for _, test := range []struct {
		data []byte
		kind error
	}{
		{[]byte("<< /x [1"), ErrMalformed},
		{[]byte("<< /x >>"), ErrMalformed},
		{[]byte("(abc\\"), ErrMalformed},
		{[]byte("(\xfe\xff\x00)"), ErrMalformed},
		{[]byte("(\xfe\xff\xd8\x00)"), ErrMalformed},
		{[]byte("(\xfe\xff\xdc\x00)"), ErrMalformed},
		{[]byte("<gg>"), ErrMalformed},
		{[]byte("1 2"), ErrMalformed},
		{[]byte("{1}"), ErrUnsupported},
		{[]byte("<< /x exec >>"), ErrUnsupported},
		{[]byte("--(.-0"), ErrUnsupported},
		{[]byte("/\xff"), ErrUnsupported},
	} {
		if _, err := Decode(nil, test.data); !errors.Is(err, test.kind) {
			t.Errorf("syntax boundary %q returned %v", test.data, err)
		}
	}
}

func TestEngineBudgetGraphValidationAndCancellation(t *testing.T) {
	for _, options := range []DecodeOptions{
		{MaxBytes: 1}, {MaxBytes: 64}, {MaxNodes: 1}, {MaxDepth: 1}, {MaxDepth: 257},
	} {
		if _, err := Decode(nil, []byte("<< /x [1 2] >>"), options); !errors.Is(err, ErrLimit) {
			t.Fatalf("parse ignored budget %+v: %v", options, err)
		}
	}
	root := &Node{Kind: List}
	root.Items = []*Node{root}
	if _, err := Encode(nil, &EngineData{Root: root}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("cyclic syntax was accepted: %v", err)
	}
	value := &Node{Kind: Boolean}
	root.Items = []*Node{value, value}
	if _, err := Encode(nil, &EngineData{Root: root}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("shared syntax bypassed node budgeting: %v", err)
	}
	root.Items = []*Node{{Kind: String, Text: string([]byte{0xff})}}
	if _, err := Encode(nil, &EngineData{Root: root}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid UTF-8 was replaced rather than rejected: %v", err)
	}
	root.Items = []*Node{{Kind: String, Text: strings.Repeat("x", 10000)}}
	if _, err := Encode(nil, &EngineData{Root: root}, EncodeOptions{MaxBytes: 1000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("large edited payload bypassed validation budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, []byte("1")); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled decoding returned syntax data")
	}
	if _, err := Encode(ctx, &EngineData{Root: value}); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled encoding returned output")
	}
	ctx, cancel = context.WithCancel(context.Background())
	counted := &textTestCancelContext{Context: ctx, cancel: cancel, after: 6}
	if _, err := Decode(counted, []byte("("+strings.Repeat("\\)", 10000)+")")); !errors.Is(err, context.Canceled) {
		t.Fatalf("long escaped string did not observe cancellation: %v", err)
	}
}

type textTestCancelContext struct {
	context.Context
	cancel context.CancelFunc
	after  int
	calls  int
}

func (c *textTestCancelContext) Err() error {
	c.calls++
	if c.calls >= c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestEnginePreservesNegativeZeroAndExactLargeNumber(t *testing.T) {
	engine, err := Decode(nil, []byte("[-0.0 9007199254740993 1e99999]"))
	if err != nil {
		t.Fatal(err)
	}
	value, ok := engine.Root.Items[1].Int64()
	if !ok || value != 9007199254740993 {
		t.Fatal("large integer was rounded through float64")
	}
	if _, ok := engine.Root.Items[2].Float64(); ok {
		t.Fatal("infinite numeric conversion was accepted")
	}
	encoded, err := Encode(nil, engine)
	if err != nil || !bytes.Equal(encoded, []byte("[-0.0 9007199254740993 1e99999]")) {
		t.Fatal("exact numeric lexemes were lost")
	}
}
