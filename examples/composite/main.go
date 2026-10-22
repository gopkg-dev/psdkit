// Command composite rebuilds an RGB8 PSD/PSB layer tree and exports straight-alpha PNG pixels.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	filename := flag.String("psd", "", "RGB8 PSD/PSB source")
	output := flag.String("out", "output/composite.png", "straight-alpha PNG destination")
	flag.Parse()
	if *filename == "" {
		fmt.Fprintln(os.Stderr, "-psd is required; generate an input with examples/create or supply your PSD/PSB")
		os.Exit(2)
	}
	source, err := psdkit.OpenDocument(*filename)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	document, err := source.Decode(context.Background())
	if err != nil {
		return err
	}
	pixels, err := psdkit.Composite(context.Background(), document)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		return err
	}
	file, err := os.Create(*output)
	if err != nil {
		return err
	}
	if err := png.Encode(file, pixels); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Printf("%s: %dx%d rebuilt from layers\n", *output, pixels.Rect.Dx(), pixels.Rect.Dy())
	return nil
}
