// Command inspect reports a PSD's dimensions, feature counts and rendering capabilities.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	filename := flag.String("psd", "", "PSD path, relative to the current directory")
	flag.Parse()
	if *filename == "" {
		fmt.Fprintln(os.Stderr, "-psd is required; generate an input with examples/create or supply your PSD/PSB")
		os.Exit(2)
	}
	if err := inspect(*filename); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspect(filename string) error {
	document, err := psdkit.Open(filename)
	if err != nil {
		return err
	}
	metadata, err := psdkit.Inspect(document)
	if err != nil {
		return err
	}
	instances := 0
	for _, object := range metadata.Objects {
		instances += len(object.Instances)
	}
	fmt.Printf("Canvas: %d x %d\n", document.Width, document.Height)
	fmt.Printf("Layers: %d\n", countLayers(document.Layers))
	fmt.Printf("Smart objects: %d\nInstances: %d\nSlices: %d\n",
		len(metadata.Objects), instances, len(metadata.Slices))
	fmt.Printf("Rendering supported: %t\n", len(metadata.BlockingIssues) == 0)
	for _, warning := range metadata.Warnings {
		fmt.Printf("Warning: %s\n", redactIDs(warning, metadata))
	}
	for _, issue := range metadata.BlockingIssues {
		fmt.Printf("Blocking issue: %s\n", redactIDs(issue, metadata))
	}
	return nil
}

func countLayers(layers []*psdkit.Layer) int {
	count := len(layers)
	for _, layer := range layers {
		if layer != nil {
			count += countLayers(layer.Children)
		}
	}
	return count
}

func redactIDs(message string, metadata psdkit.Metadata) string {
	for _, object := range metadata.Objects {
		if object.ID != "" {
			message = strings.ReplaceAll(message, object.ID, "[REDACTED_ID]")
		}
	}
	return message
}
