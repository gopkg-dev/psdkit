// Command placed-layer renders one original placed layer and its effects onto a transparent parent-sized image.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func main() {
	input := flag.String("psd", "", "original PSD/PSB source path")
	output := flag.String("out", "", "fresh PNG output path")
	index := flag.Int("layer", -1, "physical source layer index; -1 selects the first placed layer with a drop shadow")
	shadows := flag.Bool("approximate-drop-shadow", false, "explicitly select generic drop-shadow rendering")
	projection := flag.Bool("approximate-placed-projection", false,
		"explicitly project transparent-border contents with full-canvas bilinear sampling")
	budget := flag.Int64("max-working-bytes", 256<<20, "bounded source and effect working storage")
	list := flag.Bool("list-drop-shadows", false, "list original placed shadows; -psd may select a source directory")
	limit := flag.Int("list-limit", 3, "maximum metadata-only list matches")
	flag.Parse()
	if *list {
		if err := listShadows(*input, *limit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(*input, *output, *index, *shadows, *projection, *budget); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func listShadows(input string, limit int) error {
	if input == "" || limit < 1 {
		return errors.New("-psd and a positive -list-limit are required")
	}
	info, err := os.Stat(input)
	if err != nil {
		return err
	}
	paths := []string{input}
	if info.IsDir() {
		entries, err := os.ReadDir(input)
		if err != nil {
			return err
		}
		paths = nil
		sizes := make(map[string]int64)
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".psd") &&
				!strings.EqualFold(filepath.Ext(entry.Name()), ".psb") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			path := filepath.Join(input, entry.Name())
			paths, sizes[path] = append(paths, path), info.Size()
		}
		slices.SortFunc(paths, func(left, right string) int {
			if sizes[left] < sizes[right] {
				return -1
			}
			if sizes[left] > sizes[right] {
				return 1
			}
			return strings.Compare(left, right)
		})
	}
	found := 0
	for _, path := range paths {
		matches := limit - found
		if info.IsDir() {
			// A directory inventory samples one placed shadow per source before selecting more templates.
			matches = 1
		}
		count, err := listSourceShadows(path, matches)
		if err != nil {
			return err
		}
		found += count
		if found == limit {
			return nil
		}
	}
	if found == 0 {
		return errors.New("no original placed drop shadow was found")
	}
	return nil
}

func listSourceShadows(path string, limit int) (count int, err error) {
	source, err := psdfile.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	for index := range source.Layers {
		view, err := source.ReadLayerMetadata(context.Background(), index)
		if err != nil {
			return count, err
		}
		layer := view.Layer
		if layer == nil || layer.Hidden || layer.Placed == nil || layer.Effects == nil ||
			!layer.Effects.Enabled || !layer.Effects.EffectsVisible {
			continue
		}
		for _, effect := range layer.Effects.Items {
			if !effect.Enabled || effect.Opacity == 0 || effect.Kind != psdeffects.DropShadow {
				continue
			}
			angle := effect.Angle
			if effect.UseGlobalLight {
				angle = layer.Effects.GlobalAngle
			}
			fmt.Printf("%s\tlayer=%d\tname=%q\tcanvas=%dx%d\tsize=%g\tspread=%g\tdistance=%g\tangle=%g\n",
				path, index, layer.Name, source.Header.Width, source.Header.Height,
				effect.Size, effect.Spread, effect.Distance, math.Mod(angle, 360))
			if warp := layer.Placed.Warp; warp != nil {
				fmt.Printf("\twarp=%s\torders=%dx%d\tmesh=%d\tsplit=%t\tunsupported=%q\n",
					warp.Style, warp.UOrder, warp.VOrder, len(warp.MeshPoints), warp.Split, warp.Unsupported)
			}
			count++
			if count >= limit {
				return count, nil
			}
		}
	}
	return count, nil
}

func run(input, output string, index int, shadows, projection bool, budget int64) (err error) {
	if input == "" || output == "" {
		return errors.New("-psd and -out are required")
	}
	if budget < 2 {
		return errors.New("-max-working-bytes must cover source materialization and effect composition")
	}
	source, err := psdfile.Open(input)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	if index == -1 {
		for candidate := range source.Layers {
			view, err := source.ReadLayerMetadata(context.Background(), candidate)
			if err != nil {
				return err
			}
			if view.Layer == nil || view.Layer.Hidden || view.Layer.Placed == nil || view.Layer.Effects == nil {
				continue
			}
			for _, effect := range view.Layer.Effects.Items {
				if effect.Enabled && effect.Opacity != 0 && effect.Kind == psdeffects.DropShadow {
					index = candidate
					break
				}
			}
			if index >= 0 {
				break
			}
		}
	}
	if index < 0 {
		return errors.New("the source has no visible placed layer with an active drop shadow")
	}
	// Reserve half for owned source materialization and half for simultaneous compositor output and coverage.
	configuration := psdcompose.Options{ApproximateDropShadow: shadows, MaxWorkingBytes: budget / 2}
	layer, err := psdscene.RenderPlacedLayer(context.Background(), source, index,
		psdscene.Options{Compose: configuration, MaxWorkingBytes: budget / 2, ApproximatePlacedProjection: projection})
	if err != nil {
		return err
	}
	// The isolated preview excludes unrelated layers and parent group paint while preserving this layer's parameters.
	pixels, err := psdcompose.Render(context.Background(), &psdmodel.Document{
		Width: int(source.Header.Width), Height: int(source.Header.Height), Layers: []*psdmodel.Layer{layer},
	}, configuration)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	err = errors.Join(png.Encode(file, pixels), file.Close())
	if err != nil {
		return err
	}
	fmt.Printf("Rendered original placed layer %d %q with effects: %d x %d → %s\n",
		index, layer.Name, pixels.Rect.Dx(), pixels.Rect.Dy(), output)
	return nil
}
