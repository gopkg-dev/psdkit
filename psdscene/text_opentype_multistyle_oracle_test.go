package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

type multiStyleOracleCase struct {
	Label, Text             string
	Ends                    []int64
	Fonts                   []string
	Sizes                   []float64
	Colors                  [][4]float64
	Policy                  OpenTypeStyleBoundaryPolicy
	Liga, Kern              bool
	Transform               [6]float64
	Samples                 int
	Advance                 float64
	RGBAHash                string `json:"rgba_sha256"`
	UnsupportedColorCluster bool   `json:"unsupported_color_cluster"`
	Glyphs                  []multiStyleOracleGlyph
}

type multiStyleOracleGlyph struct {
	Font                 string
	ID                   uint16
	Start, Length        int64
	X, Y, Advance, Scale float64
}

func multiStyleOracle(t *testing.T) ([]multiStyleOracleCase, psdfont.FontResolver) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "opentype-multistyle", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Fonts []struct{ File, Name, SHA256 string }
		Cases []multiStyleOracleCase
	}
	if err := json.Unmarshal(data, &fixture, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	faces := make(map[string]*psdfont.Face)
	for _, font := range fixture.Fonts {
		path := filepath.Join("..", font.File)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != font.SHA256 {
			t.Fatal("independent font fixture bytes changed")
		}
		face, err := psdfont.Open(nil, path)
		if err != nil || face.PostScriptName != font.Name {
			t.Fatalf("independent font identity: %v", err)
		}
		faces[font.Name] = face
	}
	return fixture.Cases, psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		face := faces[name]
		if face == nil {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
}

func multiStyleCaseEngine(t *testing.T, row multiStyleOracleCase) []byte {
	t.Helper()
	engine, err := psdtext.Decode(nil, openTypeTestEngine(row.Text, row.Fonts[0], row.Sizes[0]))
	if err != nil {
		t.Fatal(err)
	}
	styles := engine.Root.Lookup("EngineDict").Lookup("StyleRun")
	array, lengths := styles.Lookup("RunArray"), styles.Lookup("RunLengthArray")
	styleTemplate := multiStyleCloneNode(array.Items[0])
	fontset := engine.Root.Lookup("ResourceDict").Lookup("FontSet")
	fontTemplate := multiStyleCloneNode(fontset.Items[0])
	array.Items, lengths.Items, fontset.Items = nil, nil, nil
	start := int64(0)
	for index, end := range row.Ends {
		font := multiStyleCloneNode(fontTemplate)
		font.Lookup("Name").Text = row.Fonts[index]
		fontset.Items = append(fontset.Items, font)
		style := multiStyleCloneNode(styleTemplate)
		data := style.Lookup("StyleSheet").Lookup("StyleSheetData")
		data.Lookup("Font").Lexeme = strconv.Itoa(index)
		data.Lookup("FontSize").Lexeme = strconv.FormatFloat(row.Sizes[index], 'g', -1, 64)
		for channel, value := range row.Colors[index] {
			data.Lookup("FillColor").Lookup("Values").Items[channel].Lexeme =
				strconv.FormatFloat(value, 'g', -1, 64)
		}
		array.Items = append(array.Items, style)
		lengths.Items = append(lengths.Items, &psdtext.Node{Kind: psdtext.Number,
			Lexeme: strconv.FormatInt(end-start, 10)})
		start = end
	}
	result, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func multiStyleCasePSD(t *testing.T, row multiStyleOracleCase, modern bool) []byte {
	t.Helper()
	data := openTypeTestPSDExtra(t, row.Text, row.Fonts[0], row.Sizes[0], modern, "", multiStyleCaseEngine(t, row))
	at := bytes.Index(data, []byte("8BIMTySh")) + 14
	if at < 14 {
		t.Fatal("fixture TySh not found")
	}
	for index, value := range row.Transform {
		binary.BigEndian.PutUint64(data[at+index*8:], math.Float64bits(value))
	}
	return data
}

func multiStyleCaseOptions(row multiStyleOracleCase) OpenTypeTextRenderOptions {
	kerning := OpenTypeTextNone
	if row.Kern {
		kerning = OpenTypeTextMetrics
	}
	return OpenTypeTextRenderOptions{Kerning: kerning, Samples: row.Samples, StyleBoundaries: row.Policy,
		Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: row.Liga}}}}
}

func TestOpenTypeMultiStyleIndependentHarfBuzzGeometryAndPixels(t *testing.T) {
	cases, resolver := multiStyleOracle(t)
	for _, row := range cases {
		t.Run(row.Label, func(t *testing.T) {
			engine, err := psdtext.Decode(nil, multiStyleCaseEngine(t, row))
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := engine.Metadata(nil)
			if err != nil {
				t.Fatal(err)
			}
			spans, _, err := openTypeTextSpans(context.Background(), metadata, row.Text, 32<<20)
			if err != nil {
				t.Fatal(err)
			}
			groups, _, err := openTypeStyleGroups(context.Background(), spans, row.Text, row.Policy, 32<<20)
			if err != nil {
				t.Fatal(err)
			}
			shape := psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{
				{Tag: "liga", Enabled: row.Liga}, {Tag: "kern", Enabled: row.Kern},
			}}
			laidOut, _, err := layoutOpenTypeIndependentSpans(nil, groups, resolver, shape, 10000, 32<<20)
			if err != nil {
				t.Fatal(err)
			}
			observed := 0
			for _, span := range laidOut {
				if span.run == nil {
					continue
				}
				for _, glyph := range span.run.Glyphs {
					if observed >= len(row.Glyphs) {
						t.Fatal("more positioned glyphs than independent HB result")
					}
					want := row.Glyphs[observed]
					if glyph.Glyph.ID != want.ID || glyph.ClusterStart != want.Start ||
						glyph.ClusterLength != want.Length || span.run.FontName != want.Font ||
						math.Abs(glyph.X-want.X) > 1e-12 || math.Abs(glyph.Y-want.Y) > 1e-12 ||
						math.Abs(glyph.Advance-want.Advance) > 1e-12 || math.Abs(span.run.Scale-want.Scale) > 1e-12 {
						t.Fatalf("HB glyph %d differs: %#v, want %#v", observed, glyph, want)
					}
					observed++
				}
			}
			if observed != len(row.Glyphs) {
				t.Fatal("fewer positioned glyphs than independent HB result")
			}
			for _, modern := range []bool{false, true} {
				source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, modern), nil)
				records, err := source.TextLayers(nil)
				if err != nil {
					t.Fatal(err)
				}
				pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, multiStyleCaseOptions(row))
				if row.UnsupportedColorCluster {
					if pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) {
						t.Fatalf("cross-color native assignment was guessed: %v", err)
					}
				} else {
					if err != nil || pixels == nil {
						t.Fatal(err)
					}
					hash := sha256.Sum256(pixels.Pix)
					if hex.EncodeToString(hash[:]) != row.RGBAHash {
						t.Fatalf("independent polygon sample hash differs: got %x, want %s", hash, row.RGBAHash)
					}
				}
				if reader.forbiddenHit != 0 {
					t.Fatal("saved glyph or composite pixels were read")
				}
				source.Close()
			}
		})
	}
}
