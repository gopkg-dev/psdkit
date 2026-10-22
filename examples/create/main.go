// Example create writes a new editable transparent RGB8 PSD or PSB.
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
	width := flag.Int("width", 640, "canvas width in pixels")
	height := flag.Int("height", 480, "canvas height in pixels")
	large := flag.Bool("psb", false, "write PSB version 2")
	output := flag.String("out", "", "destination PSD/PSB path")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "-out is required")
		os.Exit(2)
	}
	version := uint16(1)
	if *large {
		version = 2
	}
	if err := run(*width, *height, version, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(width, height int, version uint16, path string) (err error) {
	editor, err := psdkit.CreateDocument(context.Background(), width, height,
		psdkit.CreateOptions{Version: version})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, editor.Close()) }()
	return editor.SavePSD(context.Background(), path)
}
