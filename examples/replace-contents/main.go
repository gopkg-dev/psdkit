// Command replace-contents updates an embedded smart-object PSD/PSB and all its shared instances.
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
	"github.com/gopkg-dev/psdkit/psdfile"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (resultErr error) {
	input := flag.String("psd", "", "Parent PSD/PSB")
	contents := flag.String("contents", "", "Embedded replacement RGB8 PSD/PSB with matching prototype dimensions")
	index := flag.Int("object", 0, "Modern smart-object's bottom-to-top index")
	output := flag.String("out", "output/replaced-contents.psd", "Source-format PSD/PSB destination")
	preview := flag.String("preview", "output/replaced-contents.png", "Straight-alpha PNG preview")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "-psd is required; generate an input with examples/create or supply your PSD/PSB")
		os.Exit(2)
	}
	if *contents == "" {
		return errors.New("provide a PSD/PSB replacement with -contents")
	}
	info, err := os.Stat(*contents)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > psdfile.DefaultLimits().MaxDecodeBytes {
		return errors.New("replacement must be a regular file within the owned-content budget")
	}
	data, err := os.ReadFile(*contents)
	if err != nil {
		return err
	}
	editor, err := psdkit.OpenEditor(*input)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, editor.Close()) }()
	ctx := context.Background()
	objects, err := editor.SmartObjects(ctx)
	if err != nil {
		return err
	}
	if *index < 0 || *index >= len(objects) {
		return errors.New("smart-object index is outside the editable collection")
	}
	if err := editor.ReplaceContents(ctx, objects[*index].ID, data); err != nil {
		return err
	}
	pixels, err := editor.Composite(ctx)
	if err != nil {
		return err
	}
	for _, name := range []string{*output, *preview} {
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
	}
	if err := editor.SavePSD(ctx, *output, psdkit.SaveOptions{Compression: 3}); err != nil {
		return err
	}
	file, err := os.Create(*preview)
	if err != nil {
		return err
	}
	if err := png.Encode(file, pixels); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Printf("updated %d shared instances: %s and %s\n", len(objects[*index].SharedInstances), *output, *preview)
	return nil
}
