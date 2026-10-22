package psdtext

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func textTestMetadata() []byte {
	data := []byte("<< /EngineDict << /Editor << /Text ")
	data = append(data, textTestString("中😀\rA\r")...)
	data = append(data, []byte(` >> /StyleRun <<
 /DefaultRunData << /StyleSheet << /StyleSheetData << >> >> >>
 /RunArray [
 << /StyleSheet << /StyleSheetData << /Font 0 /FontSize 24.0 /Leading 30 /Tracking -20
 /FauxBold true /AutoLeading false /FillColor << /Type 1 /Values [1 .1 .2 .3] >>
 /UnknownProperty (hwid) >> >> >>
 << /StyleSheet << /StyleSheetData << /Font 1 /FontSize 12.5 >> >> >> ]
 /RunLengthArray [4 2] >>
 /ParagraphRun << /RunArray [
 << /ParagraphSheet << /Properties << /Justification 0 /FirstLineIndent .5 >> >> >>
 << /ParagraphSheet << /Properties << /Justification 2 /StartIndent 10 >> >> >> ]
 /RunLengthArray [4 2] >> >>
 /ResourceDict << /FontSet [ << /Name `)...)
	data = append(data, textTestString("FixtureFont-Regular")...)
	data = append(data, []byte(" /Script 0 /FontType 1 /Synthetic 0 >> << /Name ")...)
	data = append(data, textTestString("MissingFont-DoNotSubstitute")...)
	return append(data, []byte(" /Script 3 /FontType 2 /Synthetic 1 >> ] >> >>")...)
}

func TestMetadataPreservesUnicodeRunCoordinatesAndRequestedFonts(t *testing.T) {
	engine, err := Decode(nil, textTestMetadata())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Text != "中😀\rA\r" || metadata.UTF16Units != 6 || len(metadata.Fonts) != 2 ||
		metadata.Fonts[1].Name != "MissingFont-DoNotSubstitute" || *metadata.Fonts[1].Script != 3 {
		t.Fatal("Unicode units or requested font metadata were changed")
	}
	if len(metadata.Styles) != 2 || metadata.Styles[1].Start != 4 || metadata.Styles[1].Length != 2 ||
		len(metadata.Paragraphs) != 2 || metadata.Paragraphs[1].Start != 4 {
		t.Fatal("UTF-16 run coordinates were indexed as UTF-8 bytes")
	}
	style := metadata.Styles[0].Style
	if style.Font == nil || *style.Font != 0 || *style.FontSize != 24 || *style.Leading != 30 ||
		*style.Tracking != -20 || !*style.FauxBold || *style.AutoLeading || style.FauxItalic != nil ||
		style.FillColor == nil || len(style.FillColor.Values) != 4 || style.FillColor.Values[0] != 1 {
		t.Fatal("character style presence, values or colors were lost")
	}
	if style.Data.Lookup("UnknownProperty").Kind != Bytes ||
		*metadata.Paragraphs[0].Style.FirstLineIndent != .5 || *metadata.Paragraphs[1].Style.Justification != 2 {
		t.Fatal("unrecognized style property or paragraph metadata was discarded")
	}
	metadata.Fonts[0].Name = "mutated view"
	*metadata.Styles[0].Style.Leading = 1
	metadata.Styles[0].Style.FillColor.Values[0] = 0
	again, err := engine.Metadata(nil)
	if err != nil || again.Fonts[0].Name != "FixtureFont-Regular" || *again.Styles[0].Style.Leading != 30 ||
		again.Styles[0].Style.FillColor.Values[0] != 1 {
		t.Fatalf("typed scalar or slice mutations changed the syntax tree: %v", err)
	}
}

func TestMetadataRejectsInvalidRunsAndBoundsOwnedView(t *testing.T) {
	for _, replacement := range [][2]string{
		{"/RunLengthArray [4 2]", "/RunLengthArray [4 1]"},
		{"/RunLengthArray [4 2]", "/RunLengthArray [4]"},
		{"/RunLengthArray [4 2]", "/RunLengthArray [-4 10]"},
		{"/Font 1 /FontSize", "/Font 2 /FontSize"},
		{"/FontSize 24.0", "/FontSize true"},
	} {
		data := strings.Replace(string(textTestMetadata()), replacement[0], replacement[1], 1)
		engine, err := Decode(nil, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Metadata(nil); !errors.Is(err, ErrMalformed) {
			t.Errorf("inconsistent run or style accepted: %v", err)
		}
	}
	engine, err := Decode(nil, textTestMetadata())
	if err != nil {
		t.Fatal(err)
	}
	for _, limits := range []MetadataOptions{{MaxBytes: 1}, {MaxBytes: 1000}, {MaxRuns: 1}, {MaxFonts: 1}} {
		if _, err := engine.Metadata(nil, limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("typed view ignored limits %+v: %v", limits, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.Metadata(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("typed view ignored pre-cancellation")
	}
	ctx, cancel = context.WithCancel(context.Background())
	counted := &textTestCancelContext{Context: ctx, cancel: cancel, after: 4}
	if _, err := engine.Metadata(counted); !errors.Is(err, context.Canceled) {
		t.Fatalf("typed view ignored cancellation during font traversal: %v", err)
	}
	text := engine.Root.Lookup("EngineDict").Lookup("Editor").Lookup("Text")
	text.Text = string([]byte{0xff})
	if _, err := engine.Metadata(nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid edited Unicode text was silently replaced: %v", err)
	}
	text.Text = strings.Repeat("x", 10000)
	if _, err := engine.Metadata(nil, MetadataOptions{MaxBytes: 1000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("large edited text bypassed metadata traversal budget: %v", err)
	}
}
