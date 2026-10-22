package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/gopkg-dev/psdkit"
)

func main() {
	expected := flag.String("expected", "", "expected published dependency version")
	flag.Parse()
	if err := run(*expected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(expected string) (err error) {
	info, ok := debug.ReadBuildInfo()
	if !ok || expected == "" {
		return errors.New("build information and expected version are required")
	}
	matched := false
	for _, dependency := range info.Deps {
		if dependency.Path != "github.com/gopkg-dev/psdkit" {
			continue
		}
		if dependency.Version != expected || dependency.Replace != nil {
			return fmt.Errorf("unexpected dependency version or local replacement: %+v", dependency)
		}
		matched = true
		fmt.Printf("dependency: %s %s; local replace: none\n", dependency.Path, dependency.Version)
	}
	if !matched {
		return errors.New("published module is missing from executable build information")
	}
	directory, err := os.MkdirTemp("", "psdkit-consumer-output-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, version := range []uint16{1, 2} {
		if err := checkFormat(ctx, directory, version); err != nil {
			return err
		}
	}
	return nil
}

func checkFormat(ctx context.Context, directory string, version uint16) (err error) {
	editor, err := psdkit.CreateDocument(ctx, 64, 48, psdkit.CreateOptions{Version: version})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, editor.Close()) }()
	group, err := editor.AddGroup(0, 1, "印花组", true)
	if err != nil {
		return err
	}
	art := image.NewNRGBA(image.Rect(0, 0, 16, 12))
	ink := color.NRGBA{R: 34, G: 228, B: 255, A: 255}
	for y := range art.Rect.Dy() {
		for x := range art.Rect.Dx() {
			art.SetNRGBA(x, y, ink)
		}
	}
	layer, err := editor.AddRaster(group, 0, "印花", art, image.Pt(7, 9))
	if err != nil {
		return err
	}
	if err := editor.Rename(layer, "published module"); err != nil {
		return err
	}
	pixels, err := editor.Composite(ctx, psdkit.CompositeOptions{Workers: 2})
	if err != nil {
		return err
	}
	if pixels.Rect != image.Rect(0, 0, 64, 48) ||
		pixels.NRGBAAt(7, 9) != ink || pixels.NRGBAAt(0, 0).A != 0 {
		return errors.New("full composition dimensions or pixels changed")
	}
	isolated, err := editor.CompositeGroup(ctx, group)
	if err != nil {
		return err
	}
	if isolated.Rect != image.Rect(7, 9, 23, 21) || isolated.NRGBAAt(7, 9) != ink {
		return errors.New("isolated group composition changed bounds or pixels")
	}
	filename := filepath.Join(directory, fmt.Sprintf("version-%d.psd", version))
	if err := editor.SavePSD(ctx, filename, psdkit.SaveOptions{Compression: 1}); err != nil {
		return err
	}
	reopened, err := psdkit.OpenEditor(filename)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, reopened.Close()) }()
	layers, err := reopened.Layers()
	if err != nil {
		return err
	}
	var imported psdkit.LayerID
	for _, item := range layers {
		if item.Name == "published module" {
			imported = item.ID
		}
	}
	if imported == 0 {
		return errors.New("saved layer metadata did not survive reopening")
	}
	for _, name := range []string{"same-target first save", "same-target second save"} {
		if err := reopened.Rename(imported, name); err != nil {
			return err
		}
		if err := reopened.SavePSD(ctx, filename); err != nil {
			return err
		}
	}
	source, err := psdkit.OpenDocument(filename)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	if source.Header.Version != version || source.Header.Width != 64 || source.Header.Height != 48 {
		return errors.New("saved PSD/PSB header changed")
	}
	final, err := reopened.Composite(ctx)
	if err != nil {
		return err
	}
	if final.NRGBAAt(7, 9) != ink {
		return errors.New("reopened composition changed pixels")
	}
	fmt.Printf("format %d: PSD/PSB round-trip, repeated save and group composition OK (64x48)\n", version)
	return nil
}
