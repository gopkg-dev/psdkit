package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"path/filepath"
	"strconv"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func multiStyleCloneNode(node *psdtext.Node) *psdtext.Node {
	if node == nil {
		return nil
	}
	result := *node
	result.Entries = make([]psdtext.Entry, len(node.Entries))
	for index, entry := range node.Entries {
		result.Entries[index] = entry
		result.Entries[index].Value = multiStyleCloneNode(entry.Value)
	}
	result.Items = make([]*psdtext.Node, len(node.Items))
	for index, item := range node.Items {
		result.Items[index] = multiStyleCloneNode(item)
	}
	return &result
}

func multiStyleEngine(t *testing.T, text, font string, split int64, secondSize float64) []byte {
	t.Helper()
	engine, err := psdtext.Decode(nil, openTypeTestEngine(text, font, 20))
	if err != nil {
		t.Fatal(err)
	}
	styles := engine.Root.Lookup("EngineDict").Lookup("StyleRun")
	array := styles.Lookup("RunArray")
	second := multiStyleCloneNode(array.Items[0])
	data := second.Lookup("StyleSheet").Lookup("StyleSheetData")
	data.Lookup("FontSize").Lexeme = strconv.FormatFloat(secondSize, 'g', -1, 64)
	colors := data.Lookup("FillColor").Lookup("Values").Items
	for index, value := range []string{"1", ".8", ".1", ".2"} {
		colors[index].Lexeme = value
	}
	array.Items = append(array.Items, second)
	lengths := styles.Lookup("RunLengthArray")
	_, err = engine.Metadata(nil)
	if err == nil {
		t.Fatal("edited runs without lengths unexpectedly valid")
	}
	units := int64(len(utf16.Encode([]rune(text)))) + 1
	lengths.Items = []*psdtext.Node{{Kind: psdtext.Number, Lexeme: strconv.FormatInt(split, 10)},
		{Kind: psdtext.Number, Lexeme: strconv.FormatInt(units-split, 10)}}
	encoded, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestOpenTypeMultiStyleExplicitPoliciesAndLiveSources(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	for _, modern := range []bool{false, true} {
		data := openTypeTestPSDExtra(t, "AVfi", face.PostScriptName, 20, modern, "",
			multiStyleEngine(t, "AVfi", face.PostScriptName, 1, 27.5))
		source, reader := openTypeSceneSource(t, data, nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone}
		if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy); pixels != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatal("default single-style scope changed")
		}
		policy.StyleBoundaries = OpenTypeIsolatedStyleRuns
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
		if pixels == nil || err != nil {
			t.Fatal(err)
		}
		positive := 0
		colours := map[[3]byte]bool{}
		for at := 0; at < len(pixels.Pix); at += 4 {
			if pixels.Pix[at+3] > 0 {
				positive++
				colours[[3]byte{pixels.Pix[at], pixels.Pix[at+1], pixels.Pix[at+2]}] = true
			}
		}
		if positive == 0 || len(colours) < 2 {
			t.Fatal("multi-style output did not paint adjacent colours")
		}
		full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
		if err != nil {
			t.Fatal(err)
		}
		for _, bounds := range []image.Rectangle{image.Rect(17, 23, 45, 54), image.Rect(30, 25, 85, 70)} {
			region, err := RenderTile(nil, source, bounds, TileOptions{
				ApproximateOpenTypeText: &policy, FontResolver: resolver})
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, region, full, bounds)
		}
		if reader.forbiddenHit != 0 {
			t.Fatal("multi-style selected saved glyph pixels")
		}
		policy.StyleBoundaries = OpenTypeCompatibleStyleRuns
		compatible, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
		if compatible == nil || err != nil || !bytes.Equal(compatible.Pix, pixels.Pix) {
			t.Fatalf("different-size styles merged typography: %v", err)
		}
	}
}

func TestOpenTypeMultiStyleCompatibleClusterColourIsUnqualified(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	data := openTypeTestPSDExtra(t, "ffi", face.PostScriptName, 20, true, "",
		multiStyleEngine(t, "ffi", face.PostScriptName, 1, 20))
	source, reader := openTypeSceneSource(t, data, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, StyleBoundaries: OpenTypeCompatibleStyleRuns,
		Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy); pixels != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("cross-colour ligature assignment guessed: %v", err)
	}
	policy.StyleBoundaries = OpenTypeIsolatedStyleRuns
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy); pixels == nil || err != nil {
		t.Fatal(err)
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("cross-colour policy selected saved glyph pixels")
	}
}
