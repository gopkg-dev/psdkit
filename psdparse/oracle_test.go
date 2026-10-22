package psdparse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"image"
	"os"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
)

type oraclePixels struct {
	Width, Height, Bytes int
	SHA256               string
}
type oracleMask struct {
	Left, Top, Right, Bottom int
	Disabled                 bool
	DefaultColor             uint8
	Pixels                   oraclePixels
}
type oracleWarp struct {
	Style, Rotate                        string
	Value, Perspective, PerspectiveOther float64
	UOrder, VOrder                       int
	Bounds                               map[string]struct {
		Value float64
		Units string
	}
}
type oraclePlaced struct {
	IDSHA256, Type                string
	Width, Height                 int
	Transform, NonAffineTransform []float64
	Warp                          oracleWarp
}
type oracleLayer struct {
	Name, BlendMode          string
	Left, Top, Right, Bottom int
	Hidden                   bool
	Opacity                  float64
	FillOpacity              *float64
	Pixels                   oraclePixels
	Mask                     *oracleMask
	Placed                   *oraclePlaced
}
type sampleOracle struct {
	Width, Height int
	Composite     oraclePixels
	Layers        []oracleLayer
	LinkedFiles   []struct{ IDSHA256, Name string }
}

func pixelHash(pixels *image.NRGBA) string {
	if pixels == nil {
		return ""
	}
	sum := sha256.Sum256(pixels.Pix)
	return hex.EncodeToString(sum[:])
}

func TestSampleMatchesFrontendPixelOracle(t *testing.T) {
	filename := testfixture.Sample(t)
	encoded, err := os.ReadFile("testdata/sample-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle sampleOracle
	if err := json.Unmarshal(encoded, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	document, err := ParseFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if pixelHash(document.Composite) != oracle.Composite.SHA256 {
		t.Errorf("saved composite pixels differ from ag-psd: %s", pixelHash(document.Composite))
		if reference, readError := os.ReadFile(os.Getenv("PSD_COMPOSITE_ORACLE")); readError == nil {
			count := 0
			for index, value := range document.Composite.Pix {
				if index < len(reference) && value != reference[index] {
					if count < 12 {
						t.Logf("difference offset=%d pixel=%v expected=%v", index,
							document.Composite.Pix[index/4*4:index/4*4+4], reference[index/4*4:index/4*4+4])
					}
					count++
				}
			}
			t.Logf("composite differing channels=%d", count)
		}
	}
	if len(document.Layers) != len(oracle.Layers) {
		t.Fatal("layer count differs from ag-psd")
	}
	for index, expected := range oracle.Layers {
		layer := document.Layers[index]
		if layer.Name != expected.Name || layer.BlendMode != expected.BlendMode || layer.Hidden != expected.Hidden ||
			layer.Bounds != image.Rect(expected.Left, expected.Top, expected.Right, expected.Bottom) ||
			*layer.Opacity != expected.Opacity {
			t.Errorf("layer %d metadata differs from ag-psd", index)
		}
		if pixelHash(layer.Pixels) != expected.Pixels.SHA256 {
			t.Errorf("layer %d saved pixels differ from ag-psd", index)
		}
		if expected.Mask != nil {
			if layer.Mask == nil {
				t.Errorf("layer %d lost its mask", index)
				continue
			}
			if pixelHash(layer.Mask.Pixels) != expected.Mask.Pixels.SHA256 {
				t.Errorf("layer %d mask pixels differ from ag-psd", index)
			}
			maskBounds := image.Rect(expected.Mask.Left, expected.Mask.Top, expected.Mask.Right, expected.Mask.Bottom)
			if layer.Mask.Bounds != maskBounds ||
				layer.Mask.Disabled != expected.Mask.Disabled || *layer.Mask.DefaultColor != expected.Mask.DefaultColor {
				t.Errorf("layer %d mask metadata differs from ag-psd", index)
			}
		}
		if expected.Placed != nil {
			placed := layer.Placed
			if placed == nil {
				t.Errorf("layer %d lost its smart object", index)
				continue
			}
			digest := sha256.Sum256([]byte(placed.ID))
			if hex.EncodeToString(digest[:]) != expected.Placed.IDSHA256 || placed.Type != expected.Placed.Type ||
				placed.Width != expected.Placed.Width || placed.Height != expected.Placed.Height ||
				!reflect.DeepEqual(placed.Transform, expected.Placed.Transform) ||
				!reflect.DeepEqual(placed.NonAffineTransform, expected.Placed.NonAffineTransform) {
				t.Errorf("layer %d placed identity, dimensions or corner transforms differ", index)
			}
			warp := placed.Warp
			reference := expected.Placed.Warp
			if warp == nil || warp.Style != reference.Style || warp.Rotate != reference.Rotate ||
				warp.UOrder != reference.UOrder ||
				warp.VOrder != reference.VOrder || warp.Value != reference.Value || warp.Perspective != reference.Perspective ||
				warp.PerspectiveOther != reference.PerspectiveOther {
				t.Errorf("layer %d warp metadata differs", index)
			}
		}
	}
	for _, reference := range oracle.LinkedFiles {
		found := false
		for id, name := range document.LinkedNames {
			digest := sha256.Sum256([]byte(id))
			if hex.EncodeToString(digest[:]) == reference.IDSHA256 && name == reference.Name {
				found = true
			}
		}
		if !found {
			t.Error("linked smart-object source filename differs from frontend")
		}
	}
}
