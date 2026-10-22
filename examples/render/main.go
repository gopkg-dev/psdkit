// Command render replaces visible raster smart objects and exports one PNG or JPEG preview.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	filename := flag.String("psd", "", "PSD path, relative to the current directory")
	printPath := flag.String("print", "", "Optional replacement PNG/JPEG; otherwise use a generated pattern")
	outputPath := flag.String("out", "output/preview.png", "Output PNG/JPEG path")
	flag.Parse()
	if *filename == "" {
		fmt.Fprintln(os.Stderr, "-psd is required; generate an input with examples/create or supply your PSD/PSB")
		os.Exit(2)
	}
	if err := render(*filename, *printPath, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func render(filename, printPath, outputPath string) error {
	format := ""
	switch strings.ToLower(filepath.Ext(outputPath)) {
	case ".png":
		format = "png"
	case ".jpg", ".jpeg":
		format = "jpeg"
	default:
		return errors.New("output path must end in .png, .jpg or .jpeg")
	}
	document, err := psdkit.Open(filename)
	if err != nil {
		return err
	}
	renderer, err := psdkit.NewRenderer(document)
	if err != nil {
		return err
	}
	config := psdkit.DefaultConfig(renderer.Metadata())
	var replacement *image.NRGBA
	if printPath != "" {
		replacement, err = readPrint(printPath)
	} else {
		replacement, err = generatedPrint()
	}
	if err != nil {
		return err
	}
	replacements := make(map[string]*image.NRGBA, len(config.Objects))
	for _, object := range config.Objects {
		replacements[object.Tag] = replacement
	}
	pixels, err := renderer.Render(context.Background(), config, replacements, nil)
	if err != nil {
		return err
	}
	pixels, err = psdkit.PrepareOutput(pixels, psdkit.Options{Format: format})
	if err != nil {
		return err
	}
	if err := writeOutput(outputPath, pixels, format); err != nil {
		return err
	}
	fmt.Printf("Rendered %d x %d image: %s\n", pixels.Rect.Dx(), pixels.Rect.Dy(), outputPath)
	return nil
}

func readPrint(filename string) (pixels *image.NRGBA, err error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > psdkit.MaxFileBytes {
		return nil, errors.New("replacement file exceeds 500 MiB")
	}
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if format != "png" && format != "jpeg" {
		return nil, errors.New("replacement must be PNG or JPEG")
	}
	if err := psdkit.AssertImageSize(config.Width, config.Height); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	decoded, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	if decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return nil, errors.New("replacement dimensions changed during decoding")
	}
	pixels, err = psdkit.EmptyImage(config.Width, config.Height)
	if err != nil {
		return nil, err
	}
	draw.Draw(pixels, pixels.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	return pixels, nil
}

func generatedPrint() (*image.NRGBA, error) {
	pixels, err := psdkit.EmptyImage(512, 512)
	if err != nil {
		return nil, err
	}
	for y := 0; y < pixels.Rect.Dy(); y++ {
		for x := 0; x < pixels.Rect.Dx(); x++ {
			shade := color.NRGBA{R: 22, G: 117, B: 204, A: 255}
			if (x/64+y/64)%2 == 0 {
				shade = color.NRGBA{R: 255, G: 194, B: 66, A: 255}
			}
			pixels.SetNRGBA(x, y, shade)
		}
	}
	return pixels, nil
}

func writeOutput(filename string, pixels *image.NRGBA, format string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	return errors.Join(psdkit.Encode(file, pixels, format), file.Close())
}
