// Command index inspects original PSD/PSB blocks and optionally decodes one layer or the composite.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	filename := flag.String("psd", "", "PSD/PSB source path")
	layerIndex := flag.Int("layer", -1, "Physical layer index to decode; -1 indexes metadata only")
	composite := flag.Bool("composite", false, "Decode the saved composite planes")
	flag.Parse()
	if *filename == "" {
		fmt.Fprintln(os.Stderr, "-psd is required; generate an input with examples/create or supply your PSD/PSB")
		os.Exit(2)
	}
	if err := inspect(*filename, *layerIndex, *composite); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspect(filename string, layerIndex int, composite bool) (err error) {
	document, err := psdkit.OpenDocument(filename)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, document.Close()) }()
	header := document.Header
	format := "PSD"
	if header.Version == 2 {
		format = "PSB"
	}
	fmt.Printf("Format: %s\nCanvas: %d x %d\nDepth: %d\nColor mode: %d\nChannels: %d\n",
		format, header.Width, header.Height, header.Depth, header.ColorMode, header.Channels)
	fmt.Printf("Resources: %d\nLayers: %d\nLayer sets: %d\nDocument extensions: %d\n",
		len(document.Resources), len(document.Layers), len(document.LayerSets), len(document.AdditionalInfo))
	if layerIndex >= 0 {
		pixels, decodeErr := document.DecodeLayer(context.Background(), layerIndex)
		if decodeErr != nil {
			return decodeErr
		}
		fmt.Printf("Decoded layer %d: %d channels\n", layerIndex, len(pixels.Channels))
		for _, plane := range pixels.Channels {
			fmt.Printf("  Channel %d: %d x %d, %d-bit, %d bytes\n",
				plane.ID, plane.Width, plane.Height, plane.Depth, len(plane.Data))
		}
	}
	if composite {
		pixels, decodeErr := document.DecodeComposite(context.Background())
		if decodeErr != nil {
			return decodeErr
		}
		fmt.Printf("Decoded composite: %d x %d, %d-bit, %d channels\n",
			pixels.Width, pixels.Height, pixels.Depth, len(pixels.Channels))
	}
	return nil
}
