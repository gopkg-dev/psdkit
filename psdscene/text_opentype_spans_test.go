package psdscene

import (
	"context"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func multiSpanMetadata(t *testing.T, text string) *psdtext.Metadata {
	t.Helper()
	engine, err := psdtext.Decode(nil, openTypeTestEngine(text, "ExplicitFixture", 20))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestOpenTypeSourceSpanUTF16AndTerminalMarker(t *testing.T) {
	text := "A\U0001df00V"
	metadata := multiSpanMetadata(t, text)
	base := metadata.Styles[0]
	first, second, terminal := base, base, base
	first.Start, first.Length = 0, 1
	second.Start, second.Length = 1, 3
	terminal.Start, terminal.Length = 4, 1
	metadata.Styles = []psdtext.StyleRun{first, second, terminal}
	spans, bytes, err := openTypeTextSpans(context.Background(), metadata, text, 1<<20)
	if err != nil || len(spans) != 3 || bytes <= 0 {
		t.Fatal(err)
	}
	if spans[0].text != "A" || spans[1].text != "\U0001df00V" || spans[2].text != "" || spans[2].length != 1 {
		t.Fatal("UTF16 positions or terminal-only style changed")
	}
	second.Length = 1
	third := base
	third.Start, third.Length = 2, 3
	metadata.Styles = []psdtext.StyleRun{first, second, third}
	if spans, _, err := openTypeTextSpans(context.Background(), metadata, text, 1<<20); spans != nil ||
		!errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("surrogate-splitting boundary accepted: %v", err)
	}
}

func TestOpenTypeSourceSpanUnknownPropertyAndBudget(t *testing.T) {
	metadata := multiSpanMetadata(t, "AV")
	first, second := metadata.Styles[0], metadata.Styles[0]
	first.Length = 1
	second.Start, second.Length = 1, 2
	metadata.Styles = []psdtext.StyleRun{first, second}
	spans, _, err := openTypeTextSpans(context.Background(), metadata, "AV", 1<<20)
	if spans == nil || err != nil {
		t.Fatal(err)
	}
	node := *second.Style.Data
	node.Entries = append(append([]psdtext.Entry{}, node.Entries...), psdtext.Entry{Key: "UnknownTypography",
		Value: &psdtext.Node{Kind: psdtext.Number, Lexeme: "0"}})
	metadata.Styles[1].Style.Data = &node
	if spans, _, err := openTypeTextSpans(context.Background(), metadata, "AV", 1<<20); spans != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("unknown later style property ignored: %v", err)
	}
	metadata.Styles[1] = second
	if spans, _, err := openTypeTextSpans(context.Background(), metadata, "AV", 255); spans != nil ||
		!errors.Is(err, psdfile.ErrLimit) {
		t.Fatal("span ownership budget not checked")
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if spans, _, err := openTypeTextSpans(parent, metadata, "AV", 1<<20); spans != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatal("span parsing ignored canceled context")
	}
}
