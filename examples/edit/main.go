// Command edit renames or edits a layer and atomically saves an editable PSD/PSB.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	input := flag.String("psd", "", "Source PSD/PSB path")
	output := flag.String("out", "output/edited.psd", "Destination path; source format is preserved")
	index := flag.Int("layer", 0, "Editable layer's bottom-to-top index")
	name := flag.String("name", "psdkit edited layer", "New layer name")
	opacity := flag.Int("opacity", -1, "Opacity from 0 to 255; -1 preserves the source")
	hide := flag.Bool("hide", false, "Hide the selected layer")
	pixels := flag.String("image", "", "Replacement PNG/JPEG for an ordinary RGB8 raster layer")
	group := flag.String("group", "", "Move the selected layer into a new top-level pass-through group")
	compression := flag.String("compression", "zip", "Changed-data compression: raw, rle, zip, prediction")
	flag.Parse()
	if *input == "" {
		fmt.Fprintln(os.Stderr, "-psd is required; generate an input with examples/create or supply your PSD/PSB")
		os.Exit(2)
	}
	if err := edit(*input, *output, *index, *name, *opacity, *hide, *pixels, *group, *compression); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func edit(
	input, output string, index int, name string, opacity int, hide bool, pixels, group, compression string,
) (err error) {
	methods := map[string]uint16{"raw": 0, "rle": 1, "zip": 2, "prediction": 3}
	method, known := methods[strings.ToLower(compression)]
	if !known || opacity < -1 || opacity > 255 {
		return errors.New("invalid compression or opacity")
	}
	editor, err := psdkit.OpenEditor(input)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, editor.Close()) }()
	layers, err := editor.Layers()
	if err != nil {
		return err
	}
	if index < 0 || index >= len(layers) {
		return errors.New("layer index is outside the editable layer list")
	}
	id := layers[index].ID
	if err := editor.Rename(id, name); err != nil {
		return err
	}
	if opacity >= 0 {
		if err := editor.SetOpacity(id, uint8(opacity)); err != nil {
			return err
		}
	}
	if hide {
		if err := editor.SetVisible(id, false); err != nil {
			return err
		}
	}
	if pixels != "" {
		replacement, imageErr := readPixels(pixels)
		if imageErr != nil {
			return imageErr
		}
		if err := editor.ReplacePixels(id, replacement); err != nil {
			return err
		}
	}
	if group != "" {
		tree, treeErr := editor.Tree()
		if treeErr != nil {
			return treeErr
		}
		parent, groupErr := editor.AddGroup(0, len(tree), group, true)
		if groupErr != nil {
			return groupErr
		}
		if err := editor.Move(id, parent, 0); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	if err := editor.SavePSD(context.Background(), output, psdkit.SaveOptions{Compression: method}); err != nil {
		return err
	}
	changes, err := editor.Changes()
	if err != nil {
		return err
	}
	fmt.Printf("Saved editable document at revision %d: %s\n", changes.Revision, output)
	return nil
}

func readPixels(filename string) (pixels *image.NRGBA, err error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > psdkit.MaxFileBytes {
		return nil, errors.New("replacement must be a regular image within the encoded-size limit")
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
	if _, err := file.Seek(0, 0); err != nil {
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
