package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

type multiStyleCausalReader struct {
	io.ReaderAt
	cause error
}

func (reader multiStyleCausalReader) ReadAt([]byte, int64) (int, error) { return 0, reader.cause }

func TestOpenTypeMultiStyleGlobalSourceContracts(t *testing.T) {
	cases, resolver := multiStyleOracle(t)
	row := cases[0]
	for _, fixture := range []string{"unknown-late-style", "modern-surrogate-boundary", "saved-bounds"} {
		for _, candidate := range cases {
			if fixture == "modern-surrogate-boundary" && strings.HasPrefix(candidate.Label, "supplementary") {
				row = candidate
				break
			}
		}
		engineData := multiStyleCaseEngine(t, row)
		if fixture == "unknown-late-style" {
			engine, err := psdtext.Decode(nil, engineData)
			if err != nil {
				t.Fatal(err)
			}
			styles := engine.Root.Lookup("EngineDict").Lookup("StyleRun").Lookup("RunArray")
			data := styles.Items[len(styles.Items)-1].Lookup("StyleSheet").Lookup("StyleSheetData")
			data.Entries = append(data.Entries, psdtext.Entry{Key: "UnqualifiedLateStyle", Value: &psdtext.Node{
				Kind: psdtext.Number, Lexeme: "0"}})
			engineData, err = psdtext.Encode(nil, engine)
			if err != nil {
				t.Fatal(err)
			}
		}
		data := openTypeTestPSDExtra(t, row.Text, row.Fonts[0], row.Sizes[0], true, "", engineData)
		if fixture == "modern-surrogate-boundary" {
			data = bytes.ReplaceAll(data, []byte("/opaqueStyle 1 >> /1 2"), []byte("/opaqueStyle 1 >> /1 1"))
			data = bytes.ReplaceAll(data, []byte("/opaqueStyle 2 >> /1 2"), []byte("/opaqueStyle 2 >> /1 3"))
		}
		if fixture == "saved-bounds" {
			data = openTypeSceneWrongSavedBounds(t, data)
		}
		source, reader := openTypeSceneSource(t, data, nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
			calls++
			return resolver.ResolveFont(ctx, name)
		})
		policy := multiStyleCaseOptions(row)
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
		if fixture == "saved-bounds" {
			if pixels == nil || err != nil || calls == 0 {
				t.Fatal("saved bounds became live glyph eligibility")
			}
			full, err := Render(nil, source, Options{FontResolver: resolver, ApproximateOpenTypeText: &policy})
			if err != nil {
				t.Fatal(err)
			}
			window := image.Rect(20, 35, 67, 65)
			tile, err := RenderTile(nil, source, window, TileOptions{
				FontResolver: resolver, ApproximateOpenTypeText: &policy})
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, tile, full, window)
		} else if pixels != nil || err == nil || calls != 0 {
			t.Fatalf("global source qualification reached font callback: %s %v", fixture, err)
		}
		if reader.forbiddenHit != 0 {
			t.Fatal("source validation read saved artwork")
		}
	}
	row = cases[0]
	source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := multiStyleCaseOptions(row)
	cause := errors.New("source metadata ReaderAt causal failure")
	reader.reader = multiStyleCausalReader{ReaderAt: reader.reader, cause: cause}
	pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
	if pixels != nil || !errors.Is(err, cause) {
		t.Fatalf("source ReaderAt cause replaced: %v", err)
	}
	source.Close()
	pixels, err = RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
	if pixels != nil || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatal("closed source accepted multi-style metadata")
	}
}
