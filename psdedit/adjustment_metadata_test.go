package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type metadataEditCase struct {
	key              string
	source, expected []byte
	desired, neutral psdadjust.Adjustment
}

func metadataEditCases() []metadataEditCase {
	var balance []byte
	for _, value := range []int16{30, -20, 15, -25, 35, -10, 15, -30, 40} {
		balance = binary.BigEndian.AppendUint16(balance, uint16(value))
	}
	balance = append(balance, 1, 0, 0, 0)
	expected := bytes.Clone(balance)
	expected[0], expected[1], expected[18] = 0xff, 0x9c, 0
	return []metadataEditCase{
		{key: "blnc", source: balance, expected: expected,
			desired: psdadjust.Adjustment{Kind: psdadjust.ColorBalance, ColorBalance: &psdadjust.ColorBalanceParams{
				Shadows: [3]int16{-100, -20, 15}, Midtones: [3]int16{-25, 35, -10},
				Highlights: [3]int16{15, -30, 40}}},
			neutral: psdadjust.Adjustment{Kind: psdadjust.ColorBalance, ColorBalance: &psdadjust.ColorBalanceParams{}}},
		{key: "brit", source: []byte{0xff, 0xe2, 0, 20, 0, 128, 0, 0, 0, 0},
			expected: []byte{0, 100, 0xff, 0x9c, 0, 99, 0, 0, 0, 0},
			desired: psdadjust.Adjustment{Kind: psdadjust.BrightnessContrast,
				BrightnessContrast: &psdadjust.BrightnessContrastParams{
					Brightness: 100, Contrast: -100, Mean: 99, Legacy: true}},
			neutral: psdadjust.Adjustment{Kind: psdadjust.BrightnessContrast,
				BrightnessContrast: &psdadjust.BrightnessContrastParams{Mean: 128, Legacy: true}}},
	}
}

func TestMetadataAdjustmentEditPSDPSBAllCompressionsAndExplicitPreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, test := range metadataEditCases() {
			tags := compositeFixtureTag(test.key, test.source)
			tags = append(tags, compositeFixtureTag("zzZZ", []byte{0xf1, 7, 0x92, 3})...)
			tags = append(tags, compositeFixtureTag(test.key, test.source)...)
			input := adjustmentEditFixture(version, tags)
			original := bytes.Clone(input)
			editor := compositeEditor(t, input)
			id := adjustmentEditLayer(t, editor)
			current, err := psdadjust.Decode(nil, test.key, test.source)
			if err != nil {
				t.Fatal(err)
			}
			if err := editor.SetAdjustment(nil, id, *current); err != nil {
				t.Fatalf("unchanged non-neutral %s was rejected: %v", test.key, err)
			}
			state, _ := editor.Changes()
			if state.Dirty {
				t.Fatal("unchanged non-neutral controls advanced revision")
			}
			if err := editor.SetAdjustment(nil, id, test.desired); err != nil {
				t.Fatalf("known %s metadata could not be edited: %v", test.key, err)
			}
			if test.desired.ColorBalance != nil {
				test.desired.ColorBalance.Shadows[0] = 99
			} else {
				test.desired.BrightnessContrast.Brightness = -99
			}
			if pixels, err := editor.Composite(nil); pixels != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("%s metadata editing enabled unverified rendering: %v", test.key, err)
			}
			var automatic bytes.Buffer
			if err := editor.Write(nil, &automatic); !errors.Is(err, ErrUnsupported) || automatic.Len() != 0 {
				t.Fatalf("%s unsupported automatic preview wrote partial output: %v", test.key, err)
			}
			preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			copy(preview.Pix, []byte{20, 70, 130, 255})
			for compression := range uint16(4) {
				saved := compositeSave(t, editor, SaveOptions{Compression: compression, CompositeImage: preview})
				seen, opaque := 0, false
				for _, block := range saved.Layers[1].AdditionalInfo {
					data, err := saved.ReadSpan(block.Data)
					if err != nil {
						t.Fatal(err)
					}
					switch block.Key {
					case test.key:
						if !bytes.Equal(data, test.expected) {
							t.Fatalf("edited %s controls, padding or ownership differ: %x", test.key, data)
						}
						seen++
					case "zzZZ":
						opaque = bytes.Equal(data, []byte{0xf1, 7, 0x92, 3})
					}
				}
				if seen != 2 || !opaque || saved.Header.Version != version || !bytes.Equal(input, original) {
					t.Fatal("matching blocks, opaque bytes, source bytes or PSD/PSB version changed")
				}
				compositeExpect(t, saved, []byte{20, 70, 130})
				decoded, err := saved.Decode(nil)
				want, decodeErr := psdadjust.Decode(nil, test.key, test.expected)
				if err != nil || decodeErr != nil || decoded.Layers[1].Adjustment == nil ||
					!reflect.DeepEqual(decoded.Layers[1].Adjustment.ColorBalance, want.ColorBalance) ||
					!reflect.DeepEqual(decoded.Layers[1].Adjustment.BrightnessContrast, want.BrightnessContrast) {
					t.Fatalf("saved %s metadata did not reopen: %v / %v", test.key, err, decodeErr)
				}
				if _, err := psdscene.Render(nil, saved); !errors.Is(err, psdcompose.ErrUnsupported) {
					t.Fatalf("saved %s used the supplied preview as a live result: %v", test.key, err)
				}
				reopened, err := New(saved)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				if err := reopened.SetAdjustment(nil, adjustmentEditLayer(t, reopened), test.neutral); err != nil {
					t.Fatalf("saved %s could not be edited again: %v", test.key, err)
				}
				adjustmentEditPreview(t, reopened, 150)
				neutral := compositeSave(t, reopened)
				compositeExpect(t, neutral, []byte{150, 150, 150})
			}
		}
	}
}

func TestMetadataAdjustmentRejectionsAndCancellationAreTransactional(t *testing.T) {
	for _, test := range metadataEditCases() {
		for _, cause := range []string{
			"canceled", "budget", "CgEd", "unknownTail", "invalidSource", "invalidDesired", "unknownDesired",
		} {
			t.Run(test.key+"/"+cause, func(t *testing.T) {
				payload := bytes.Clone(test.source)
				if cause == "unknownTail" {
					payload[len(payload)-1] = 77
				}
				if cause == "invalidSource" {
					payload[0], payload[1] = 0, 101
				}
				tags := compositeFixtureTag(test.key, payload)
				if cause == "CgEd" {
					tags = append(tags, compositeFixtureTag("CgEd", nil)...)
				}
				options := psdfile.LoadOptions{}
				if cause == "budget" {
					options.Limits = psdfile.DefaultLimits()
					options.Limits.MaxDecodeBytes = 16 << 10
				}
				editor := compositeEditor(t, adjustmentEditFixture(1, tags), options)
				id := adjustmentEditLayer(t, editor)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				want := ErrUnsupported
				switch cause {
				case "canceled":
					cancel()
					want = context.Canceled
				case "budget":
					want = psdfile.ErrLimit
				case "invalidSource", "invalidDesired":
					want = ErrInvalidEdit
				}
				desired := test.desired
				if cause == "unknownDesired" {
					desired.Unsupported = []string{"unknown semantic control"}
				}
				if cause == "invalidDesired" {
					if desired.ColorBalance != nil {
						parameters := *desired.ColorBalance
						parameters.Highlights[2] = 101
						desired.ColorBalance = &parameters
					} else {
						parameters := *desired.BrightnessContrast
						parameters.Contrast = -101
						desired.BrightnessContrast = &parameters
					}
				}
				if err := editor.SetAdjustment(ctx, id, desired); !errors.Is(err, want) {
					t.Fatalf("rejection differs: %v, want %v", err, want)
				}
				state, _ := editor.Changes()
				node, _, _, _ := editor.findNodeLocked(id)
				if state.Dirty || len(node.additional) != 0 || node.adjustment != nil || node.semanticBytes != 0 {
					t.Fatal("rejected metadata edit committed partial state")
				}
			})
		}
	}
}

func TestNonmonotoneMappedCurveEditorSavesAndReopensPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		payload := []byte{1, 0, 1, 0, 0, 0, 1}
		for value := range 256 {
			payload = append(payload, byte(value))
		}
		lut := make([]byte, 256)
		for value := range lut {
			lut[value] = byte(value*73 + 41)
		}
		editor := compositeEditor(t, adjustmentEditFixture(version, compositeFixtureTag("curv", payload)))
		id := adjustmentEditLayer(t, editor)
		if err := editor.SetAdjustment(nil, id, psdadjust.Adjustment{Kind: psdadjust.Curves,
			Curves: []psdadjust.CurveChannel{{LUT: lut}}}); err != nil {
			t.Fatal(err)
		}
		adjustmentEditPreview(t, editor, lut[150])
		saved := compositeSave(t, editor)
		compositeExpect(t, saved, []byte{lut[150], lut[150], lut[150]})
		decoded, err := saved.Decode(nil)
		if err != nil || !bytes.Equal(decoded.Layers[1].Adjustment.Curves[0].LUT, lut) {
			t.Fatalf("saved nonmonotone LUT changed: %v", err)
		}
		reopened, err := New(saved)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		adjustmentEditPreview(t, reopened, lut[150])
	}
}
