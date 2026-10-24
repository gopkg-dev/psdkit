package psdedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestPlacedEffectsExplicitExternalEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				data, originalContents := placedEffectsRoundtripFixture(t, version, true, false, true)
				editor := compositeEditor(t, data)
				for round := range 2 {
					artwork := smartEditImage([4]color.NRGBA{
						{R: 180, G: byte(20 + round*110), B: 90, A: 255},
						{R: 10, G: 80, B: 200, A: 128}, {R: 40, G: 190, B: 60, A: 64},
						{R: 230, G: 140, B: 30, A: 255},
					})
					contents := smartEditPrototype(3-version, artwork)
					calls := 0
					resolver := func(_ context.Context, record psdfile.LinkedFileRecord) (*psdfile.Document, error) {
						calls++
						if record.Kind != "liFE" || record.ID != "fixture-id" {
							t.Fatal("external placed effect resolver received an unrelated source")
						}
						if _, err := editor.Layers(); err != nil {
							return nil, err
						}
						return psdfile.Parse(contents)
					}
					var desired [2]*psdeffects.Set
					for index := range 2 {
						desired[index] = placedEffectsRoundtripSet(t, editor.source, index)
						placedEffectsRoundtripDesired(desired[index], round, index)
						unchanged := smartContentsFilterState(t, editor)
						if err := editor.SetEffectsWithOptions(t.Context(), LayerID(index+1), *desired[index],
							EffectsEditOptions{ApproximateDropShadow: true}); !errors.Is(err, ErrUnsupported) {
							t.Fatalf("external effect edit silently used original cached source bytes: %v", err)
						}
						unchanged()
						if err := editor.SetEffectsWithOptions(t.Context(), LayerID(index+1), *desired[index], EffectsEditOptions{
							ApproximateDropShadow: true, Render: &psdscene.Options{ExternalResolver: resolver},
						}); err != nil {
							t.Fatalf("explicit mapped external effects failed: %v", err)
						}
					}
					if calls != 2 {
						t.Fatalf("external mapping called %d times across two independent edits, want 2", calls)
					}
					expected := placedEffectsRoundtripExpected(t, artwork, desired, false, 128, 173)
					var rejected bytes.Buffer
					if err := editor.WritePSD(t.Context(), &rejected,
						SaveOptions{ApproximateDropShadow: true}); !errors.Is(err, ErrUnsupported) || rejected.Len() != 0 {
						t.Fatalf("external save retained a prior callback or silently used its cached raster: %v", err)
					}
					path := filepath.Join(t.TempDir(), "explicit-external-placed.psd")
					if err := editor.Save(t.Context(), path,
						SaveOptions{Compression: compression, CompositeImage: expected}); err != nil {
						t.Fatal(err)
					}
					saved, err := psdfile.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = saved.Close() })
					placedEffectsRoundtripRaw(t, saved, artwork)
					placedEffectsRoundtripPreview(t, saved, expected)
					if saved.Header.Version != version {
						t.Fatal("external effects changed original PSD/PSB version")
					}
					links, err := saved.LinkedFiles(t.Context())
					if err != nil || len(links) != 1 || links[0].Kind != "liFE" {
						t.Fatalf("explicit external effect edit embedded or changed the source record: %v", err)
					}
					retained, err := saved.ReadSpan(links[0].Data)
					if err != nil || !bytes.Equal(retained, originalContents) {
						t.Fatal("external mapping rewrote the original external link's retained content bytes")
					}
					editor = lifecycleReopen(t, saved)
				}
			})
		}
	}
}

func TestPlacedEffectsExternalResolverCannotCommitStaleOrFailedCandidates(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "error", "cancel", "stale", "source closed", "editor closed"} {
		t.Run(scenario, func(t *testing.T) {
			data, contents := placedEffectsRoundtripFixture(t, 1, true, false, true)
			editor := compositeEditor(t, data)
			desired, _ := effectSourceView(t, editor.source)
			placedEffectsRoundtripDesired(desired, 0, 0)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			before, _ := editor.Changes()
			owned := editor.ownedBytesLocked(nil)
			calls := 0
			resolver := func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				calls++
				if _, err := editor.Layers(); err != nil {
					return nil, err
				}
				switch scenario {
				case "missing":
					return nil, nil
				case "error":
					return nil, io.ErrClosedPipe
				case "cancel":
					cancel()
				case "stale":
					if err := editor.SetOpacity(2, 193); err != nil {
						return nil, err
					}
				case "source closed":
					if err := editor.source.Close(); err != nil {
						return nil, err
					}
				case "editor closed":
					if err := editor.Close(); err != nil {
						return nil, err
					}
				}
				return psdfile.Parse(contents)
			}
			err := editor.SetEffectsWithOptions(ctx, 1, *desired, EffectsEditOptions{
				ApproximateDropShadow: true, Render: &psdscene.Options{ExternalResolver: resolver},
			})
			if calls != 1 {
				t.Fatalf("explicit external effect resolver calls = %d, want 1", calls)
			}
			if scenario == "success" {
				if err != nil || editor.roots[0].effects == nil || editor.roots[0].pixels == nil {
					t.Fatalf("external resolver could not reenter editor reads outside the lock: %v", err)
				}
				unchanged := smartContentsFilterState(t, editor)
				if err := editor.SetEffectsWithOptions(t.Context(), 1, *desired,
					EffectsEditOptions{ApproximateDropShadow: true}); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("unchanged effect parameters skipped current external source verification: %v", err)
				}
				unchanged()
				return
			}
			kind := map[string]error{"missing": psdfile.ErrMalformed, "error": io.ErrClosedPipe,
				"cancel": context.Canceled, "stale": ErrStaleLinkedEdit,
				"source closed": psdfile.ErrClosed, "editor closed": psdfile.ErrClosed}[scenario]
			if !errors.Is(err, kind) {
				t.Fatalf("external effect candidate error = %v, want %v", err, kind)
			}
			if scenario == "editor closed" {
				if !editor.closed || len(editor.roots) != 0 {
					t.Fatal("external candidate committed to a closed editor")
				}
				return
			}
			if scenario == "stale" {
				before.Revision++
				before.Dirty = true
				before.CompositeChanged = true
				if editor.roots[1].opacity != 193 {
					t.Fatal("external effect candidate overwrote an independent callback edit")
				}
			}
			if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
				editor.roots[0].effects != nil || editor.roots[0].pixels != nil || len(editor.roots[0].additional) != 0 {
				t.Fatal("external effect failure committed parameters, pixels, budget or revision")
			}
		})
	}
}

func TestPlacedEffectsResolverCannotChangeCapturedFilterPolicyOrEffects(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowed%t", allowed), func(t *testing.T) {
			editor := compositeEditor(t, placedEffectsRoundtripFilterFixture(t, 1, true))
			stack := smartFilterAutoExactStack(t, editor.source)
			stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 15
			artwork := smartEditImage(smartEditColors())
			filterPolicy := psdfilter.Options{ApproximateBrightnessContrast: allowed}
			accurate := *artwork
			accurate.Rect = accurate.Rect.Add(image.Pt(2, 0))
			if err := editor.SetSmartFilterParameters(t.Context(), 1, stack,
				SmartFilterEditOptions{Pixels: &accurate}); err != nil {
				t.Fatal(err)
			}
			desired, _ := effectSourceView(t, editor.source)
			placedEffectsRoundtripDesired(desired, 0, 0)
			captured := *desired
			captured.Items = append([]psdeffects.Effect(nil), desired.Items...)
			contents := smartEditPrototype(1, artwork)
			options := EffectsEditOptions{ApproximateDropShadow: true,
				Render: &psdscene.Options{SmartFilters: &filterPolicy}}
			calls := 0
			options.Render.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				calls++
				if _, err := editor.Layers(); err != nil {
					return nil, err
				}
				filterPolicy.ApproximateBrightnessContrast = !allowed
				options.Render.SmartFilters = nil
				options.Render.ExternalResolver = nil
				options.Render.MaxWorkingBytes = 1
				desired.Items[0].Color = [3]float64{1, 1, 1}
				desired.Items[0].Distance = 45.25
				return psdfile.Parse(contents)
			}
			before, _ := editor.Changes()
			owned, pixels := editor.ownedBytesLocked(nil), editor.roots[0].pixels
			err := editor.SetEffectsWithOptions(t.Context(), 1, *desired, options)
			expectedCalls := 0
			if allowed {
				expectedCalls = 1
			}
			if calls != expectedCalls {
				t.Fatalf("captured source policy resolver calls = %d, want %d", calls, expectedCalls)
			}
			if !allowed {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("strict source preflight accepted absent approximation consent: %v", err)
				}
				if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned ||
					editor.roots[0].pixels != pixels || editor.roots[0].effects != nil {
					t.Fatal("captured strict source policy failure committed fresh effects or artwork")
				}
				return
			}
			if err != nil || editor.roots[0].effects == nil ||
				!effectsParametersEqual(*editor.roots[0].effects, captured) {
				t.Fatalf("external callback changed captured effects or disabled selected source approximation: %v", err)
			}
		})
	}
}
