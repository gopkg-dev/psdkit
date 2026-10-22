package psdcompose

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestKnockoutNativeMatrix(t *testing.T) {
	if os.Getenv("PSDKIT_KNOCKOUT_NATIVE") != "1" {
		t.Skip("optional Photoshop Knockout fixtures are required")
	}
	nativeAdvancedMatrix(t, "photoshop-p3-knockout-fixtures.json", "p3-knockout-native-evidence.json")
}

func TestChannelRestrictionsNativeMatrix(t *testing.T) {
	if os.Getenv("PSDKIT_CHANNELS_NATIVE") != "1" {
		t.Skip("optional Photoshop channel-restriction fixtures are required")
	}
	nativeAdvancedMatrix(t, "photoshop-p3-channels-fixtures.json", "p3-channels-native-evidence.json")
}

func TestKnockoutGroupNativeMatrix(t *testing.T) {
	if os.Getenv("PSDKIT_KNOCKOUT_GROUP_NATIVE") != "1" {
		t.Skip("optional Photoshop group-self Knockout fixtures are required")
	}
	nativeAdvancedMatrix(t, "photoshop-p3-knockout-group-fixtures.json", "p3-knockout-group-native-evidence.json")
}

func nativeAdvancedMatrix(t *testing.T, manifestName, evidenceName string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "verification", manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string
		Cases   []struct {
			ID, PSD, PNG string
			Expected     struct {
				Kind          string
				Knockout      string
				Restrictions  []uint32
				Fill, Opacity int
				TSLY          bool `json:"tsly"`
			}
		}
		Errors []any
	}
	if err := json.Unmarshal(data, &manifest, json.MatchCaseInsensitiveNames(true)); err != nil || len(manifest.Cases) == 0 {
		t.Fatalf("native Knockout manifest: err=%v cases=%d errors=%v", err, len(manifest.Cases), manifest.Errors)
	}
	if len(manifest.Errors) != 0 {
		t.Errorf("native fixture generation errors: %v", manifest.Errors)
	}
	var records []map[string]any
	for _, item := range manifest.Cases {
		t.Run(item.ID, func(t *testing.T) {
			source, err := psdfile.Open(filepath.Join("..", item.PSD))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			document, err := source.Decode(nil)
			if err != nil {
				t.Fatal(err)
			}
			document.Composite = nil
			actual, err := Render(nil, document)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(filepath.Join("..", item.PNG))
			if err != nil {
				t.Fatal(err)
			}
			native, decodeErr := png.Decode(file)
			closeErr := file.Close()
			if decodeErr != nil || closeErr != nil {
				t.Fatalf("native PNG: %v %v", decodeErr, closeErr)
			}
			if actual.Bounds() != native.Bounds() {
				t.Fatal("native and rendered bounds differ")
			}
			maximum, total := 0, 0
			for y := range actual.Rect.Dy() {
				for x := range actual.Rect.Dx() {
					got := actual.NRGBAAt(x, y)
					want := color.NRGBAModel.Convert(native.At(x, y)).(color.NRGBA)
					if os.Getenv("PSDKIT_ADVANCED_DEBUG") == "1" && x == 1 {
						t.Logf("row=%d actual=%v native=%v", y, got, want)
					}
					for index, value := range []uint8{got.R, got.G, got.B, got.A} {
						expected := []uint8{want.R, want.G, want.B, want.A}[index]
						// Completely transparent PNG pixels may preserve arbitrary hidden source RGB.
						if index < 3 && got.A == 0 && want.A == 0 {
							continue
						}
						difference := int(value) - int(expected)
						if difference < 0 {
							difference = -difference
						}
						maximum, total = max(maximum, difference), total+difference
					}
				}
			}
			output := item.PSD[:len(item.PSD)-4] + "-go.png"
			file, err = os.Create(filepath.Join("..", output))
			if err != nil {
				t.Fatal(err)
			}
			encodeErr, closeErr := png.Encode(file, actual), file.Close()
			if encodeErr != nil || closeErr != nil {
				t.Fatalf("rendered PNG: %v %v", encodeErr, closeErr)
			}
			sourceBytes, err := os.ReadFile(filepath.Join("..", item.PSD))
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(sourceBytes)
			var blocks []map[string]any
			knockoutSeen, restrictionsSeen := false, len(item.Expected.Restrictions) == 0
			for _, layer := range source.Layers {
				targetName := "Knockout"
				if strings.HasPrefix(item.Expected.Kind, "group-self-") {
					targetName = "Group"
				}
				if item.Expected.Knockout != "" && layer.Name == targetName {
					fill, transparencyShapes := byte(255), byte(1)
					for _, block := range layer.AdditionalInfo {
						if block.Key != "iOpa" && block.Key != "tsly" {
							continue
						}
						payload, err := source.ReadSpan(block.Data)
						if err != nil || len(payload) == 0 {
							t.Fatalf("actual native advanced scalar: %v", err)
						}
						if block.Key == "iOpa" {
							fill = payload[0]
						} else {
							transparencyShapes = payload[0]
						}
					}
					expectedFill := byte(math.Round(float64(item.Expected.Fill) * 255 / 100))
					expectedOpacity := byte(math.Round(float64(item.Expected.Opacity) * 255 / 100))
					if fill != expectedFill || layer.Opacity != expectedOpacity ||
						(transparencyShapes != 0) != item.Expected.TSLY {
						t.Errorf("actual native Fill/Opacity/tsly=%d/%d/%d expected=%d/%d/%t", fill,
							layer.Opacity, transparencyShapes, expectedFill, expectedOpacity, item.Expected.TSLY)
					}
				}
				if item.Expected.Kind == "clipped" && layer.Name == "Knockout" && layer.Clipping != 1 {
					t.Error("owned native Knockout fixture did not retain its requested clipping relation")
				}
				for _, block := range layer.AdditionalInfo {
					if block.Key != "knko" && block.Key != "brst" && block.Key != "tsly" && block.Key != "iOpa" {
						continue
					}
					payload, err := source.ReadSpan(block.Data)
					if err != nil {
						t.Fatal(err)
					}
					if block.Key == "knko" && len(payload) == 4 {
						expected := byte(1)
						if item.Expected.Knockout == "deep" {
							expected = 2
						}
						knockoutSeen = knockoutSeen || payload[0] == expected
					}
					if block.Key == "brst" && len(payload) == 4*len(item.Expected.Restrictions) {
						restrictionsSeen = true
						for index, channel := range item.Expected.Restrictions {
							if binary.BigEndian.Uint32(payload[index*4:]) != channel {
								restrictionsSeen = false
							}
						}
					}
					if block.Key == "brst" && len(payload) != 0 && len(item.Expected.Restrictions) == 0 {
						restrictionsSeen = false
					}
					blocks = append(blocks, map[string]any{"layer": layer.Name, "key": block.Key,
						"data_hex": hex.EncodeToString(payload), "offset": block.Data.Offset})
				}
			}
			if item.Expected.Knockout != "" && !knockoutSeen || !restrictionsSeen {
				t.Error("actual native additional blocks do not contain the intended advanced setting")
			}
			records = append(records, map[string]any{"id": item.ID, "source": item.PSD,
				"source_sha256": hex.EncodeToString(hash[:]), "native_png": item.PNG, "rendered_png": output,
				"saved_composite_used": false, "max_rgba_error": maximum,
				"both_fully_transparent_hidden_rgb_ignored": true,
				"mean_rgba_error": float64(total) / float64(actual.Rect.Dx()*actual.Rect.Dy()*4),
				"actual_blocks":   blocks})
			if maximum > 1 {
				t.Errorf("native Knockout RGBA maximum error=%d total=%d", maximum, total)
			}
		})
	}
	encoded, err := json.Marshal(map[string]any{"cases": records, "application_version": manifest.Version,
		"saved_composite_used": false, "production_changed": true}, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("..", "verification", evidenceName),
		append(encoded, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
