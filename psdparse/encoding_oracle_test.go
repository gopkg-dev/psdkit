package psdparse

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdrender"
)

func readOracleImage(t *testing.T, filename string) *image.NRGBA {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	output := image.NewNRGBA(image.Rect(0, 0, decoded.Bounds().Dx(), decoded.Bounds().Dy()))
	draw.Draw(output, output.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return output
}

func TestCanvasPNGQuantizationOracle(t *testing.T) {
	directory := os.Getenv("PSD_ENCODING_ORACLE_DIR")
	if directory == "" {
		t.Skip("set PSD_ENCODING_ORACLE_DIR for encoded HTTP results")
	}
	for group := 1; group <= 3; group++ {
		name := fmt.Sprintf("%02d-expected.png", group)
		expected := readOracleImage(t, filepath.Join(directory, "psd-random-background", name))
		name = fmt.Sprintf("%02d-actual.png", group)
		actual := readOracleImage(t, filepath.Join(directory, "psd-random-background", name))
		normalized, err := psdrender.CanvasPixels(actual)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(normalized.Pix, expected.Pix) {
			t.Errorf("group %d final PNG pixels differ from the independent frontend output", group)
		}
	}
}
