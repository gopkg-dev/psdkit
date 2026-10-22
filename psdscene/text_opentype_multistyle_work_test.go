package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func TestOpenTypeMultiStyleWholeLayerRasterAndPaintCeilings(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("testdata", "opentype-multistyle", "repeated-contours.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{2, 50, 260} {
		row := multiStyleOracleCase{Text: strings.Repeat("A", count), Policy: OpenTypeIsolatedStyleRuns,
			Samples: 8, Transform: [6]float64{1, 0, 0, 1, 20, 900}}
		for index := range count {
			end := int64(index + 1)
			if index == count-1 {
				end++
			}
			row.Ends = append(row.Ends, end)
			row.Fonts = append(row.Fonts, face.PostScriptName)
			row.Sizes = append(row.Sizes, 1000)
			colors := [][4]float64{{1, .4, .2, .8}, {1, .8, .1, .2}}
			row.Colors = append(row.Colors, colors[index%2])
		}
		data := multiStyleCasePSD(t, row, true)
		prototype, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Clone(data[:prototype.Composite.Span.Offset])
		prototype.Close()
		binary.BigEndian.PutUint32(data[14:], 1024)
		binary.BigEndian.PutUint32(data[18:], 1024)
		data = append(data, 0, 0)
		data = append(data, make([]byte, 3*1024*1024)...)
		source, reader := openTypeSceneSource(t, data, nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
			calls++
			return face, nil
		})
		policy := multiStyleCaseOptions(row)
		policy.MaxWorkingBytes = 128 << 20
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
		switch count {
		case 2:
			if pixels == nil || err != nil {
				t.Fatal(err)
			}
			for _, point := range []image.Point{{40, 200}, {589, 899}, {39, 200}, {40, 199}, {590, 900}} {
				pixel := pixels.NRGBAAt(point.X, point.Y)
				if point.In(image.Rect(40, 200, 590, 900)) {
					if pixel.R != 204 || pixel.G != 26 || pixel.B != 51 || pixel.A != 255 {
						t.Fatal("owned overlapping rectangle positive control differs")
					}
				} else if pixel.A != 0 {
					t.Fatal("paint extended beyond affine control hull")
				}
			}
		case 50:
			// 700 rows x 8 samples x 400 lines x 50 paints exceeds 100 million aggregate scanline work.
			if pixels != nil || !errors.Is(err, psdfont.ErrLimit) {
				t.Fatalf("each paint reset the whole-layer raster budget: %v", err)
			}
		default:
			// 550 x 700 pixels x 260 paints exceeds the independent 100 million painted-pixel ceiling.
			var detail *psdfile.Error
			if pixels != nil || !errors.As(err, &detail) || detail.Path != "textOpenType.stylePaint" {
				t.Fatalf("repeated full-canvas scans exceeded cumulative paint budget: %v", err)
			}
		}
		if calls != count || reader.forbiddenHit != 0 {
			t.Fatal("work qualification skipped source fonts or used saved artwork")
		}
	}
}
