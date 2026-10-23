package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
)

func TestPassRangesDisabledEffectSetsPreserveAllCalibratedStages(t *testing.T) {
	for _, mode := range []string{"legacy-multiple", "norm", "scrn", "div "} {
		for _, globalHidden := range []bool{false, true} {
			for _, location := range []string{"group", "child", "both"} {
				t.Run(mode+"/"+location+map[bool]string{false: "/master-off", true: "/global-hidden"}[globalHidden],
					func(t *testing.T) {
						group := singlePassNativeGroup(color.NRGBA{R: 203, G: 224, B: 154, A: 230}, mode, 191, 161)
						if mode == "legacy-multiple" {
							group = passRangesTestGroup()
							group.Children = append(group.Children,
								composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 200, A: 64}))
						}
						bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 136, G: 4, B: 39, A: 74})
						document := composeTestDocument(bottom, group)
						baseline, err := Render(t.Context(), document)
						if err != nil {
							t.Fatal(err)
						}
						plan, err := preparePlan(document, nil)
						if err != nil {
							t.Fatal(err)
						}
						set := &psdeffects.Set{Enabled: globalHidden, EffectsVisible: !globalHidden,
							Items: []psdeffects.Effect{{
								Kind: "futureFx", Enabled: true, Opacity: 1,
								Unsupported: []string{"retained future effect controls"},
							}}}
						original := *set
						original.Items = append([]psdeffects.Effect(nil), set.Items...)
						original.Items[0].Unsupported = append([]string(nil), set.Items[0].Unsupported...)
						if location != "child" {
							group.Effects = set
						}
						if location != "group" {
							group.Children[0].Effects = set
						}
						inactivePlan, err := preparePlan(document, nil)
						if err != nil || inactivePlan.passRanges[group] != plan.passRanges[group] ||
							inactivePlan.workingBytes != plan.workingBytes {
							t.Fatalf("disabled set changed the source stage or retained budget: %v", err)
						}
						actual, err := Render(t.Context(), document, Options{MaxWorkingBytes: plan.workingBytes})
						if err != nil || !bytes.Equal(actual.Pix, baseline.Pix) || !reflect.DeepEqual(*set, original) {
							t.Fatalf("disabled effects changed pixels, metadata or exact budget: %v", err)
						}
						regional, err := RenderRegion(t.Context(), document, image.Rect(0, 0, 1, 1))
						if err != nil || !bytes.Equal(regional.Pix, baseline.Pix) {
							t.Fatalf("disabled effects changed regional pixels: %v", err)
						}
						ctx, cancel := context.WithCancel(t.Context())
						cancel()
						if pixels, err := Render(ctx, document); pixels != nil || !errors.Is(err, context.Canceled) {
							t.Fatalf("disabled metadata bypassed cancellation: %v", err)
						}
						set.Enabled, set.EffectsVisible = true, true
						if pixels, err := Render(t.Context(), document); pixels != nil || !errors.Is(err, ErrUnsupported) {
							t.Fatalf("reactivated unsupported effects were silently ignored: %v", err)
						}
					})
			}
		}
	}
}

func TestPassRangesActiveEffectSetsRemainRestrictedEvenWithoutPaint(t *testing.T) {
	for _, set := range []*psdeffects.Set{
		{Enabled: true, EffectsVisible: true},
		{Enabled: true, EffectsVisible: true, Unsupported: []string{"unknown whole-set semantics"}},
		{Enabled: false, EffectsVisible: true, Unsupported: []string{"ambiguous master effects switch"}},
		{Enabled: true, EffectsVisible: false, Unsupported: []string{"unknown whole-set semantics"}},
		{Enabled: true, EffectsVisible: true, Items: []psdeffects.Effect{{Kind: psdeffects.ColorOverlay, Enabled: false}}},
		{Enabled: true, EffectsVisible: true, Items: []psdeffects.Effect{{Kind: psdeffects.ColorOverlay,
			Enabled: true, Opacity: 0, BlendMode: "norm"}}},
	} {
		for _, mode := range []string{"norm", "scrn"} {
			for _, onGroup := range []bool{false, true} {
				group := singlePassNativeGroup(color.NRGBA{R: 203, A: 230}, mode, 191, 161)
				if onGroup {
					group.Effects = set
				} else {
					group.Children[0].Effects = set
				}
				if passGroupNormalRanges(group) || singlePassRangeSource(group) {
					t.Fatal("an active effect set escaped the conservative eligibility gate")
				}
				pixels, err := Render(t.Context(), composeTestDocument(group))
				if pixels != nil || !errors.Is(err, ErrUnsupported) {
					t.Fatalf("active effect set was accepted with pass-group ranges: %v", err)
				}
			}
		}
	}
}
