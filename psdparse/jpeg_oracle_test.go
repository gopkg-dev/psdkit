package psdparse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdrender"
)

func TestJPEGFrontendOracle(t *testing.T) {
	directory := os.Getenv("PSD_NATIVE_ORACLE_DIR")
	if directory == "" {
		t.Skip("set PSD_NATIVE_ORACLE_DIR for JPEG reference")
	}
	data, err := os.ReadFile(filepath.Join(directory, "psd-jpeg.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Request struct {
			Template struct {
				Source string
				Config psdrender.Config
			}
			Inputs  []struct{ Replacements map[string]string }
			Options psdrender.Options
		}
		Images []struct{ DataURL string }
	}
	if err := json.Unmarshal(data, &fixture, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Images) != len(fixture.Request.Inputs) {
		t.Fatal("JPEG fixture input and output counts differ")
	}
	_, encoded, _ := strings.Cut(fixture.Request.Template.Source, ",")
	source, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseBytes(source)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := psdrender.New(document)
	if err != nil {
		t.Fatal(err)
	}
	for group, input := range fixture.Request.Inputs {
		prints := make(map[string]*image.NRGBA)
		for tag, url := range input.Replacements {
			pixels, readErr := decodeOracleDataImage(url)
			if readErr != nil {
				t.Fatal(readErr)
			}
			prints[tag] = pixels
		}
		pixels, renderErr := engine.RenderWithOptions(context.Background(), fixture.Request.Template.Config, prints, nil,
			fixture.Request.Options)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		var actual bytes.Buffer
		if err := psdrender.Encode(&actual, pixels, "jpeg"); err != nil {
			t.Fatal(err)
		}
		if outputDir := os.Getenv("PSD_NATIVE_JPEG_OUTPUT"); outputDir != "" {
			filename := filepath.Join(outputDir, fmt.Sprintf("%02d-actual.jpg", group+1))
			if err := os.WriteFile(filename, actual.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, encoded, _ := strings.Cut(fixture.Images[group].DataURL, ",")
		expected, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		decode := func(encoded []byte) *image.NRGBA {
			decoded, decodeErr := jpeg.Decode(bytes.NewReader(encoded))
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			result := image.NewNRGBA(decoded.Bounds())
			draw.Draw(result, result.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
			return result
		}
		got, want := decode(actual.Bytes()), decode(expected)
		if got.Bounds() != want.Bounds() {
			t.Fatalf("group %d JPEG dimensions differ: %v versus %v", group+1, got.Bounds(), want.Bounds())
		}
		count, maximum, total := 0, 0, 0
		for index, value := range got.Pix {
			difference := int(value) - int(want.Pix[index])
			if difference < 0 {
				difference = -difference
			}
			if difference != 0 {
				count++
				maximum = max(maximum, difference)
				total += difference
			}
		}
		t.Logf("group %d JPEG channels differing=%d max=%d mean=%f",
			group+1, count, maximum, float64(total)/float64(len(got.Pix)))
		if count != 0 {
			t.Errorf("JPEG output differs from frontend in group %d", group+1)
		}
	}
}
