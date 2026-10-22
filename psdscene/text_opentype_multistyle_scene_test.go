package psdscene

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func TestOpenTypeMultiStyleSceneAndRecursiveOriginalContents(t *testing.T) {
	cases, resolver := multiStyleOracle(t)
	for _, row := range cases {
		if !strings.HasPrefix(row.Label, "mixed-font-size") {
			continue
		}
		childData := multiStyleCasePSD(t, row, true)
		policy := multiStyleCaseOptions(row)
		child, childReader := openTypeSceneSource(t, childData, nil)
		prototype, err := Render(nil, child, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
		if err != nil || prototype == nil {
			t.Fatal(err)
		}
		for _, region := range []image.Rectangle{image.Rect(0, 0, 33, 44), image.Rect(29, 31, 81, 75)} {
			pixels, err := RenderTile(nil, child, region, TileOptions{ApproximateOpenTypeText: &policy,
				FontResolver: resolver, CachedSpecialLayers: true})
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, pixels, prototype, region)
		}
		visits := 0
		err = VisitTiles(nil, child, prototype.Rect, image.Pt(40, 35), func(pixels *image.NRGBA) error {
			visits++
			openTypeSceneEqualWindow(t, pixels, prototype, pixels.Rect)
			return nil
		}, TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver})
		if err != nil || visits != 12 || childReader.forbiddenHit != 0 {
			t.Fatalf("multi-style visitor: visits=%d err=%v", visits, err)
		}
		canvas := image.Pt(300, 180)
		quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
		for _, psb := range []bool{false, true} {
			contents := childData
			if psb {
				contents = recursiveTextPSB(t, contents)
			}
			for _, external := range []bool{false, true} {
				parent, parentReader := recursiveTextOriginal(t,
					recursiveTextParentFixture(t, contents, external, canvas, quad))
				settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedPlacedLayers: true, CachedSpecialLayers: true}
				var linkedReader *openTypeSceneReader
				if external {
					settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
						linked, reader := openTypeSceneSource(t, contents, nil)
						linkedReader = reader
						return linked, nil
					}
				}
				metadata, err := parent.ReadLayerMetadata(nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := psdrender.CompileProjectionRegion(metadata.Layer,
					image.Rectangle{Max: canvas}, image.Rectangle{Max: canvas})
				if err != nil {
					t.Fatal(err)
				}
				projected, origin, err := psdrender.ProjectPlacedContentsContext(nil, metadata.Layer, prototype, plan)
				if err != nil {
					t.Fatal(err)
				}
				expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: canvas.X, Height: canvas.Y,
					Layers: []*psdmodel.Layer{{Bounds: image.Rectangle{Min: origin, Max: origin.Add(projected.Rect.Size())},
						Pixels: projected, BlendMode: "normal"}}})
				if err != nil {
					t.Fatal(err)
				}
				parentBounds := image.Rect(1, 2, 3, 4)
				parentPolicy := policy
				parentPolicy.Bounds = &parentBounds
				settings.ApproximateOpenTypeText = &parentPolicy
				for _, region := range []image.Rectangle{expected.Rect, image.Rect(23, 19, 87, 95),
					image.Rect(80, 41, 150, 126), image.Rect(250, 120, 297, 175)} {
					pixels, err := RenderTile(nil, parent, region, settings)
					if err != nil {
						t.Fatalf("policy=%d psb=%t linked=%t: %v", row.Policy, psb, external, err)
					}
					openTypeSceneEqualWindow(t, pixels, expected, region)
				}
				cause := errors.New("multi-style recursive visitor cause")
				err = VisitTiles(nil, parent, expected.Rect, image.Pt(100, 60), func(*image.NRGBA) error {
					return cause
				}, settings)
				if !errors.Is(err, cause) || parentReader.forbiddenHit != 0 ||
					linkedReader != nil && linkedReader.forbiddenHit != 0 {
					t.Fatal("recursive policy read cache or lost visitor cause")
				}
			}
		}
	}
}
