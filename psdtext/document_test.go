package psdtext

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func documentFixture(text []byte, paragraph, style string) []byte {
	var data bytes.Buffer
	data.WriteString(" %source\n /98 << /0 7 >> /0 << /unknown <0001> >> /1 << /1 [ << /0 << /0 (")
	data.Write(text)
	data.WriteString(") /5 << /0 [ << /0 << /p true >> /1 " + paragraph + " >> ] >>")
	data.WriteString(" /6 << /0 [ << /0 << /s 1 >> /1 " + style + " >> ] >> >>")
	data.WriteString(" /1 << /0 [] /1 << /0 [ << /0 << /a 1 >> /1 " + paragraph + " >> ] >> /2 [] >> >> ] >> \n%tail\n\x00")
	return data.Bytes()
}

func TestDocumentImplicitSourceAndEncode(t *testing.T) {
	data := documentFixture([]byte{0xfe, 0xff, 0, 'A', 0, '\r'}, "2", "2")
	engine, err := DecodeDocument(nil, data, DecodeOptions{SourceOffset: 4096})
	if err != nil {
		t.Fatal(err)
	}
	view, err := engine.DocumentMetadata(nil)
	if err != nil || len(view.Texts) != 1 || view.Texts[0].Text != "A\r" || view.Texts[0].UTF16Units != 2 {
		t.Fatalf("view %+v: %v", view, err)
	}
	text := view.Texts[0].Data.Lookup("0").Lookup("0")
	position := bytes.Index(data, []byte{'(', 0xfe, 0xff, 0, 'A'})
	if text.Span.Offset != 4096+int64(position) {
		t.Fatalf("wrong original span: %+v", text.Span)
	}
	unchanged, err := Encode(nil, engine)
	if err != nil || !bytes.Equal(unchanged, data) {
		t.Fatalf("unchanged bytes: %v", err)
	}
	text.Text = "B\r"
	encoded, err := Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encoded, []byte(" %source\n ")) || !bytes.HasSuffix(encoded, []byte(" \n%tail\n\x00")) {
		t.Fatalf("lost envelope whitespace/comments: %q", encoded)
	}
	reparsed, err := DecodeDocument(nil, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if reparsed.Root.Lookup("1").Lookup("1").Items[0].Lookup("0").Lookup("0").Text != "B\r" {
		t.Fatal("updated text not reparsed")
	}
	if !bytes.Equal(engine.OriginalBytes(), data) {
		t.Fatal("original bytes mutated")
	}
}

func TestDocumentUnsupportedAndMalformed(t *testing.T) {
	base := documentFixture([]byte{0xfe, 0xff, 0, 'A', 0, '\r'}, "2", "2")
	for _, test := range []struct {
		name     string
		data     []byte
		category error
	}{
		{"schema", bytes.Replace(base, []byte("/0 7"), []byte("/0 8"), 1), ErrUnsupported},
		{"duplicate", bytes.Replace(base, []byte("/98 <<"), []byte("/98 << /0 7"), 1), ErrUnsupported},
		{"short-style", documentFixture([]byte{0xfe, 0xff, 0, 'A', 0, '\r'}, "2", "1"), ErrMalformed},
		{"negative-style", documentFixture([]byte{0xfe, 0xff, 0, 'A', 0, '\r'}, "2", "-1"), ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, err := DecodeDocument(nil, test.data)
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.DocumentMetadata(nil)
			if !errors.Is(err, test.category) {
				t.Fatalf("category: %v", err)
			}
			encoded, err := Encode(nil, engine)
			if err != nil || !bytes.Equal(encoded, test.data) {
				t.Fatalf("rejected view lost original: %v", err)
			}
		})
	}
	for _, data := range [][]byte{[]byte("/98 << /0 7 >> >>"), []byte("/98"), []byte("5"), nil} {
		if _, err := DecodeDocument(nil, data); !errors.Is(err, ErrMalformed) {
			t.Fatalf("decode %q: %v", data, err)
		}
	}
}

func TestDocumentUTF16AndBudgets(t *testing.T) {
	data := documentFixture([]byte{0xfe, 0xff, 0xd8, 0x3d, 0xde, 0, 0, '\r'}, "3", "3")
	engine, err := DecodeDocument(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	view, err := engine.DocumentMetadata(nil)
	if err != nil || view.Texts[0].UTF16Units != 3 {
		t.Fatalf("surrogate units: %+v %v", view, err)
	}
	array := engine.Root.Lookup("1").Lookup("1").Items[0].Lookup("0").Lookup("6").Lookup("0")
	array.Items = append(array.Items, &Node{Kind: Dictionary, Entries: []Entry{
		{Key: "0", Value: &Node{Kind: Dictionary}}, {Key: "1", Value: &Node{Kind: Number, Lexeme: "2"}},
	}})
	array.Items[0].Lookup("1").Lexeme = "1"
	if _, err := engine.DocumentMetadata(nil); !errors.Is(err, ErrMalformed) || !strings.Contains(err.Error(), "surrogate") {
		t.Fatalf("split surrogate: %v", err)
	}
	engine, err = DecodeDocument(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []DocumentMetadataOptions{{MaxBytes: 1}, {MaxRuns: 1}, {MaxBytes: -1}, {MaxTexts: -1}, {MaxRuns: -1}} {
		if _, err := engine.DocumentMetadata(nil, option); !errors.Is(err, ErrLimit) {
			t.Fatalf("budget: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeDocument(ctx, data); !errors.Is(err, context.Canceled) {
		t.Fatalf("decode cancel: %v", err)
	}
	if _, err := engine.DocumentMetadata(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("view cancel: %v", err)
	}
	if _, err := DecodeDocument(nil, data, DecodeOptions{MaxNodes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("nodes: %v", err)
	}
	if _, err := DecodeDocument(nil, data, DecodeOptions{MaxDepth: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("depth: %v", err)
	}
}

func TestDocumentOptionalParagraphAdjustments(t *testing.T) {
	data := documentFixture([]byte{0xfe, 0xff, 0, 'A', 0, '\r'}, "2", "2")
	adjustment := []byte(" /1 << /0 [ << /0 << /a 1 >> /1 2 >> ] >>")
	data = bytes.Replace(data, adjustment, nil, 1)
	engine, err := DecodeDocument(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	view, err := engine.DocumentMetadata(nil)
	if err != nil || view.Texts[0].HasParagraphAdjustments || view.Texts[0].ParagraphAdjustments != nil {
		t.Fatalf("absent adjustment invented: %+v %v", view, err)
	}
	encoded, err := Encode(nil, engine)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("optional absence lost source bytes: %v", err)
	}
	rendered := engine.Root.Lookup("1").Lookup("1").Items[0].Lookup("1")
	rendered.Entries = append(rendered.Entries, Entry{Key: "1", Value: &Node{Kind: Number, Lexeme: "1"}})
	if _, err := engine.DocumentMetadata(nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("optional malformed type: %v", err)
	}
	rendered.Entries[len(rendered.Entries)-1].Value = nil
	if _, err := engine.DocumentMetadata(nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("optional explicit nil treated as absent: %v", err)
	}
	rendered.Entries = append(rendered.Entries, Entry{Key: "1", Value: &Node{Kind: Dictionary}})
	if _, err := engine.DocumentMetadata(nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("optional nil duplicate: %v", err)
	}
	rendered.Entries = rendered.Entries[:len(rendered.Entries)-1]
	rendered.Entries[len(rendered.Entries)-1].Value = &Node{Kind: Dictionary}
	rendered.Entries = append(rendered.Entries, Entry{Key: "1", Value: &Node{Kind: Dictionary}})
	if _, err := engine.DocumentMetadata(nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("optional duplicate: %v", err)
	}
}
