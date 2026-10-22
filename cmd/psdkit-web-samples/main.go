// Command psdkit-web-samples packages the pinned apparel template and two original print assets.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/gopkg-dev/psdkit"
)

type asset struct {
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Contents    *asset `json:"contents,omitempty"`
	Compression string `json:"compression,omitempty"`
}

type sampleManifest struct {
	Version  int `json:"version"`
	Document struct {
		asset            `json:",inline"`
		PreferredLayerID psdkit.LayerID `json:"preferredLayerID"`
		Width            int            `json:"width"`
		Height           int            `json:"height"`
		ContentWidth     int            `json:"contentWidth"`
		ContentHeight    int            `json:"contentHeight"`
		SharedInstances  int            `json:"sharedInstances"`
	} `json:"document"`
	Prints []asset                     `json:"prints"`
	Policy psdkit.SmartContentsOptions `json:"policy"`
}

func main() {
	out := flag.String("out", "web/demo/public/samples", "output directory")
	assets := flag.String("assets", os.Getenv("PSDKIT_SAMPLE_ASSETS"), "psdkit-assets checkout directory")
	flag.Parse()
	if err := run(context.Background(), *out, *assets); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, directory, assetRoot string) error {
	data, err := os.ReadFile("web/samples/manifest.json")
	if err != nil {
		return err
	}
	var manifest sampleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if assetRoot == "" {
		for _, candidate := range []string{".sample-assets", filepath.Join("..", "psdkit-assets")} {
			if info, err := os.Stat(filepath.Join(candidate, "garment", manifest.Document.File)); err == nil && !info.IsDir() {
				assetRoot = candidate
				break
			}
		}
	}
	if assetRoot == "" {
		return fmt.Errorf("apparel assets missing: clone gopkg-dev/psdkit-assets beside this repository or set PSDKIT_SAMPLE_ASSETS")
	}
	read := func(item asset) ([]byte, error) {
		if !filepath.IsLocal(item.File) {
			return nil, fmt.Errorf("invalid sample asset path")
		}
		data, err := os.ReadFile(filepath.Join(assetRoot, "garment", item.File))
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != item.SHA256 {
			return nil, fmt.Errorf("sample SHA-256 mismatch: %s", item.File)
		}
		return data, nil
	}
	template, err := read(manifest.Document.asset)
	if err != nil {
		return err
	}
	source, err := psdkit.ParseDocument(template)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	if int(source.Header.Width) != manifest.Document.Width || int(source.Header.Height) != manifest.Document.Height {
		return fmt.Errorf("sample template dimensions changed")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, manifest.Document.File), template, 0o644); err != nil {
		return err
	}
	manifest.Policy = psdkit.SmartContentsOptions{ApproximateCustomWarp: true, AssumeUntaggedSRGB: true}
	for i := range manifest.Prints {
		item := &manifest.Prints[i]
		encoded, err := read(*item)
		if err != nil {
			return err
		}
		configuration, err := png.DecodeConfig(bytes.NewReader(encoded))
		if err != nil {
			return err
		}
		if configuration.Width <= 0 || configuration.Height <= 0 || int64(configuration.Width)*int64(configuration.Height) > 16_000_000 {
			return fmt.Errorf("print dimensions exceed the sample budget")
		}
		input, err := png.Decode(bytes.NewReader(encoded))
		if err != nil {
			return err
		}
		printImage := fitPrint(input, manifest.Document.ContentWidth, manifest.Document.ContentHeight)
		var output bytes.Buffer
		if err := png.Encode(&output, printImage); err != nil {
			return err
		}
		digest := sha256.Sum256(output.Bytes())
		item.SHA256 = hex.EncodeToString(digest[:])
		if err := os.WriteFile(filepath.Join(directory, item.File), output.Bytes(), 0o644); err != nil {
			return err
		}
		contents, err := psdkit.BuildRasterContents(ctx, printImage)
		if err != nil {
			return err
		}
		var packed bytes.Buffer
		compressor := gzip.NewWriter(&packed)
		if _, err := compressor.Write(contents); err != nil {
			return err
		}
		if err := compressor.Close(); err != nil {
			return err
		}
		contentsHash := sha256.Sum256(packed.Bytes())
		item.Contents = &asset{File: strings.TrimSuffix(item.File, filepath.Ext(item.File)) + ".psd.gz",
			Name: item.Name + ".psd", SHA256: hex.EncodeToString(contentsHash[:]), Compression: "gzip"}
		if err := os.WriteFile(filepath.Join(directory, item.Contents.File), packed.Bytes(), 0o644); err != nil {
			return err
		}
		if err := verifyReplacement(ctx, template, contents, manifest); err != nil {
			return fmt.Errorf("%s: %w", item.Name, err)
		}
	}
	encoded, err := json.Marshal(manifest, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("Packaged muban.psd and two verified apparel prints in", directory)
	return nil
}

// fitPrint contains the complete artwork on a white source canvas using premultiplied bilinear filtering.
func fitPrint(input image.Image, width, height int) *image.NRGBA {
	output := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := input.Bounds()
	scale := math.Min(float64(width)*0.92/float64(bounds.Dx()), float64(height)*0.92/float64(bounds.Dy()))
	w, h := float64(bounds.Dx())*scale, float64(bounds.Dy())*scale
	left, top := (float64(width)-w)/2, (float64(height)-h)/2
	for y := range height {
		for x := range width {
			pixel := color.NRGBA{255, 255, 255, 255}
			if float64(x)+.5 >= left && float64(x)+.5 < left+w && float64(y)+.5 >= top && float64(y)+.5 < top+h {
				sx, sy := (float64(x)+.5-left)/scale-.5, (float64(y)+.5-top)/scale-.5
				x0, y0 := int(math.Floor(sx)), int(math.Floor(sy))
				fx, fy := sx-float64(x0), sy-float64(y0)
				var channels [4]float64
				for dy := range 2 {
					for dx := range 2 {
						weight := []float64{1 - fx, fx}[dx] * []float64{1 - fy, fy}[dy]
						c := input.At(bounds.Min.X+max(0, min(bounds.Dx()-1, x0+dx)), bounds.Min.Y+max(0, min(bounds.Dy()-1, y0+dy)))
						r, g, b, a := c.RGBA()
						for i, v := range [4]uint32{r, g, b, a} {
							channels[i] += float64(v) * weight
						}
					}
				}
				pixel.R = uint8(math.Round((channels[0] + 65535 - channels[3]) / 257))
				pixel.G = uint8(math.Round((channels[1] + 65535 - channels[3]) / 257))
				pixel.B = uint8(math.Round((channels[2] + 65535 - channels[3]) / 257))
			}
			output.SetNRGBA(x, y, pixel)
		}
	}
	return output
}

func verifyReplacement(ctx context.Context, template, contents []byte, manifest sampleManifest) error {
	source, err := psdkit.ParseDocument(template)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	editor, err := psdkit.NewEditor(source)
	if err != nil {
		return err
	}
	defer func() { _ = editor.Close() }()
	objects, err := editor.SmartObjects(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, object := range objects {
		if object.ID == manifest.Document.PreferredLayerID && len(object.SharedInstances) == manifest.Document.SharedInstances {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("preferred shared smart object is missing")
	}
	if err := editor.ReplaceContentsWithOptions(ctx, manifest.Document.PreferredLayerID, contents, manifest.Policy); err != nil {
		return err
	}
	_, err = editor.Composite(ctx)
	return err
}
