// Command precision rebuilds ordinary high-depth PSD layers without eight-bit conversion.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"os"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type output struct {
	Width, Height int
	Depth         uint16
	Encoding      string
	ICCBytes      int
	ICCSHA256     string
	Pixels        []float64
}

func main() {
	inputPath := flag.String("psd", "", "source PSD/PSB")
	outputPath := flag.String("out", "", "straight high-precision RGBA JSON")
	maxBytes := flag.Int64("max-bytes", 0, "total working-memory budget")
	maxInflated := flag.Int64("max-inflated", 0, "explicit full ZIP output work allowance")
	nonFinite := flag.String("nonfinite", "reject", "reject, zero or clamp non-finite samples")
	flag.Parse()
	if err := run(*inputPath, *outputPath, *maxBytes, *maxInflated, *nonFinite); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(inputPath, outputPath string, maxBytes, maxInflated int64, nonFinite string) error {
	if inputPath == "" || outputPath == "" {
		return fmt.Errorf("provide -psd and -out")
	}
	options := psdscene.PrecisionOptions{MaxWorkingBytes: maxBytes,
		Region: psdfile.RegionOptions{MaxInflatedBytes: maxInflated}}
	switch nonFinite {
	case "reject":
		options.NonFinite = psdprecision.NonFiniteReject
	case "zero":
		options.NonFinite = psdprecision.NonFiniteZero
	case "clamp":
		options.NonFinite = psdprecision.NonFiniteClamp
	default:
		return fmt.Errorf("invalid -nonfinite policy")
	}
	document, err := psdfile.Open(inputPath)
	if err != nil {
		return err
	}
	defer func() { _ = document.Close() }()
	image, err := psdscene.RenderPrecision(context.Background(), document, options)
	if err != nil {
		return err
	}
	encoding := "encoded"
	if image.Encoding == psdprecision.LinearRGB {
		encoding = "linear"
	}
	profileHash := sha256.Sum256(image.ICCProfile)
	result := output{Width: image.Width, Height: image.Height, Depth: image.SourceDepth,
		Encoding: encoding, ICCBytes: len(image.ICCProfile), ICCSHA256: hex.EncodeToString(profileHash[:]), Pixels: image.Pix}
	data, err := json.Marshal(result, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, append(data, '\n'), 0600)
}
