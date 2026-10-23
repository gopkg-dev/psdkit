package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func singlePixelPatternEntry(rgba [4]byte) []byte {
	var rectangle []byte
	for _, coordinate := range []uint32{0, 0, 1, 1} {
		rectangle = binary.BigEndian.AppendUint32(rectangle, coordinate)
	}
	vm := binary.BigEndian.AppendUint32(bytes.Clone(rectangle), 3)
	for component := range 5 {
		if component == 3 {
			vm = binary.BigEndian.AppendUint32(vm, 0)
			continue
		}
		array := binary.BigEndian.AppendUint32(nil, 8)
		array = append(array, rectangle...)
		array = append(array, 0, 8, 0, rgba[min(component, 3)])
		vm = binary.BigEndian.AppendUint32(vm, 1)
		vm = binary.BigEndian.AppendUint32(vm, uint32(len(array)))
		vm = append(vm, array...)
	}
	body := []byte{0, 0, 0, 1, 0, 0, 0, 3, 0, 1, 0, 1}
	body = append(body, writerLinkedUnicode("Original pattern")...)
	body = append(body, 5)
	body = append(body, "first"...)
	body = binary.BigEndian.AppendUint32(body, 3)
	body = binary.BigEndian.AppendUint32(body, uint32(len(vm)))
	body = append(body, vm...)
	result := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	result = append(result, body...)
	return append(result, make([]byte, (4-len(body)%4)%4)...)
}

func assertSinglePixelPatternReopened(t *testing.T, source *psdfile.Document, fill psdpattern.Fill, rgba [4]byte) {
	t.Helper()
	view := patternEditRead(t, source)
	if !reflect.DeepEqual(*view.Fill, fill) {
		t.Fatal("single-pixel save normalized original scale, angle or fractional phase")
	}
	record, err := source.ResolvePattern(nil, *view.Fill)
	if err != nil {
		t.Fatal(err)
	}
	tile, err := source.DecodePatternTileRGB8(nil, *record)
	if err != nil || tile.Rect != image.Rect(0, 0, 1, 1) || !bytes.Equal(tile.Pix, rgba[:]) {
		t.Fatalf("saved 1x1 tile lost original alpha or hidden RGB: %v", err)
	}
	expected := bytes.Repeat(rgba[:], 16)
	if rgba[3] == 0 {
		clear(expected)
	}
	live, err := psdscene.Render(nil, source)
	if err != nil || !bytes.Equal(live.Pix, expected) {
		t.Fatalf("saved single-pixel pattern did not rerender exact source color: %v", err)
	}
	region := image.Rect(1, 0, 3, 3)
	partial, err := psdscene.RenderTile(nil, source, region)
	if err != nil || partial.Rect != region || !bytes.Equal(partial.Pix, expected[:24]) {
		t.Fatalf("saved single-pixel pattern changed ROI pixels: %v", err)
	}
	var planes []byte
	for _, channel := range rgba[:3] {
		merged := byte((int(channel)*int(rgba[3]) + 255*(255-int(rgba[3])) + 127) / 255)
		planes = append(planes, bytes.Repeat([]byte{merged}, 16)...)
	}
	if rgba[3] != 255 {
		planes = append(planes, bytes.Repeat(rgba[3:], 16)...)
	}
	compositeExpect(t, source, planes)
}

func TestSinglePixelPatternRealtimeEditSaveReopenAllCompressions(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, alpha := range []byte{0, 128, 255} {
				t.Run(fmt.Sprintf("v%d/c%d/a%d", version, compression, alpha), func(t *testing.T) {
					rgba := [4]byte{31, 67, 193, alpha}
					patterns := singlePixelPatternEntry(rgba)
					originalPatterns := bytes.Clone(patterns)
					editor := patternEditSource(t, version, patternEditDescriptor(), nil, patterns)
					for _, controls := range [][4]float64{{75, 37, .5, -.25}, {.0001, -123.25, -1e200, 1e200},
						{1e200, 1e100, .1, -.1}} {
						fill := *patternEditRead(t, editor.source).Fill
						fill.Scale, fill.Angle = controls[0], controls[1]
						fill.Phase = psdpattern.Phase{X: controls[2], Y: controls[3]}
						if err := editor.SetPatternFill(nil, 1, fill); err != nil {
							t.Fatalf("single-pixel edit rejected inactive sampling controls: %v", err)
						}
						node, _, _, _ := editor.findNodeLocked(1)
						if !bytes.Equal(node.patternTile.Pix, rgba[:]) {
							t.Fatal("owned pattern tile lost hidden RGB or source alpha")
						}
						before, _ := editor.Changes()
						if err := editor.SetPatternFill(nil, 1, fill); err != nil {
							t.Fatal(err)
						}
						if after, _ := editor.Changes(); after != before {
							t.Fatal("repeated single-pixel edit changed revision")
						}
						preview, err := editor.Composite(nil)
						if err != nil {
							t.Fatal(err)
						}
						saved := compositeSave(t, editor, SaveOptions{Compression: compression})
						if saved.Header.Version != version {
							t.Fatal("single-pixel save changed PSD/PSB format")
						}
						assertSinglePixelPatternReopened(t, saved, fill, rgba)
						retained, err := saved.ReadSpan(saved.AdditionalInfo[0].Data)
						if err != nil || !bytes.Equal(retained, originalPatterns) ||
							!bytes.Equal(patterns, originalPatterns) {
							t.Fatalf("single-pixel edit changed encoded local tile blocks: %v", err)
						}
						reopened, err := New(saved)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						actual, err := reopened.Composite(nil)
						if err != nil || !bytes.Equal(actual.Pix, preview.Pix) {
							t.Fatalf("reopened editor changed constant source samples: %v", err)
						}
						editor = reopened
					}
				})
			}
		}
	}
}

func TestSinglePixelPatternPreflightNeverQualifiesMultiPixelSources(t *testing.T) {
	for _, original := range []bool{false, true} {
		descriptor := patternEditDescriptor()
		fill := psdpattern.Fill{ID: "first", Name: "Original pattern", Scale: 75, Angle: 37, Link: true}
		if original {
			patchPatternDescriptor(descriptor, fill)
		}
		editor := patternEditSource(t, 1, descriptor, nil, nil)
		if !original {
			if err := editor.SetPatternFillWithOptions(nil, 1, &fill,
				PatternFillEditOptions{Pixels: patternOptionsArtwork()}); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := editor.Changes()
		if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("identical unsupported multi-pixel original or overlay bypassed actual tile validation: %v", err)
		}
		if after, _ := editor.Changes(); after != before {
			t.Fatal("failed multi-pixel qualification changed state")
		}
	}
	patterns := singlePixelPatternEntry([4]byte{31, 67, 193, 128})
	patterns = append(patterns, patternEditFixture("second", "Second pattern", [3][4]byte{})...)
	editor := patternEditSource(t, 1, patternEditDescriptor(), nil, patterns)
	fill := *patternEditRead(t, editor.source).Fill
	fill.Scale, fill.Angle, fill.Phase = 75, 37, psdpattern.Phase{X: .5}
	if err := editor.SetPatternFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	previous, tile := node.pattern, node.patternTile
	fill.ID, fill.Name = "second", "Second pattern"
	if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("single-pixel qualification leaked to a different local tile: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fill.ID, fill.Name = "first", "Original pattern"
	fill.Phase.Y = .25
	if err := editor.SetPatternFill(ctx, 1, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("single-pixel edit ignored cancellation: %v", err)
	}
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("single-pixel edit bypassed peak memory budget: %v", err)
	}
	if after, _ := editor.Changes(); before != after || node.pattern != previous || node.patternTile != tile {
		t.Fatal("failed single-pixel replacement changed committed state")
	}
}

func TestSinglePixelPatternRejectsUnrepresentableFractionalPhase(t *testing.T) {
	descriptor := patternEditDescriptor()
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
		Key: "phase", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "Pnt ", Entries: []psdfile.DescriptorEntry{
			{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Width: 32, Number: float32(0)}},
			{Key: "Vrtc", Type: "doub", Value: float64(0)},
		}},
	})
	editor := patternEditSource(t, 1, descriptor, nil, singlePixelPatternEntry([4]byte{31, 67, 193, 0}))
	fill := *patternEditRead(t, editor.source).Fill
	fill.Phase.X = .1
	if err := editor.SetPatternFill(nil, 1, fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("inactive geometry silently rounded the requested source representation: %v", err)
	}
	assertPatternOptionsUnchanged(t, editor)
	fill.Phase.X = .5
	if err := editor.SetPatternFill(nil, 1, fill); err != nil {
		t.Fatal(err)
	}
	assertSinglePixelPatternReopened(t, compositeSave(t, editor), fill, [4]byte{31, 67, 193, 0})
}

func singlePixelFloat32Descriptor() *psdfile.Descriptor {
	descriptor := patternEditDescriptor()
	for _, parameter := range []struct {
		key, unit string
		value     float32
	}{{"Scl ", "#Prc", 100}, {"Angl", "#Ang", 0}} {
		descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
			Key: parameter.key, Type: "UnFl", Value: psdfile.DescriptorUnit{
				Unit: parameter.unit, Width: 32, Number: parameter.value,
			},
		})
	}
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{
		Key: "phase", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "Pnt ", Entries: []psdfile.DescriptorEntry{
			{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Width: 32, Number: float32(0)}},
			{Key: "Vrtc", Type: "UnFl", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Width: 32, Number: float32(0)}},
		}},
	})
	return descriptor
}

func TestSinglePixelPatternRoundedNoOpCannotHideUnrepresentableControls(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited%t", edited), func(t *testing.T) {
			editor := patternEditSource(t, 1, singlePixelFloat32Descriptor(), nil,
				singlePixelPatternEntry([4]byte{31, 67, 193, 128}))
			fill := *patternEditRead(t, editor.source).Fill
			if edited {
				fill.Scale, fill.Angle, fill.Phase = 75.5, 37.5, psdpattern.Phase{X: .5, Y: -.25}
				if err := editor.SetPatternFill(nil, 1, fill); err != nil {
					t.Fatal(err)
				}
			}
			var beforeSave bytes.Buffer
			if err := editor.Write(nil, &beforeSave); err != nil {
				t.Fatal(err)
			}
			before, _ := editor.Changes()
			node, _, _, _ := editor.findNodeLocked(1)
			oldPattern, oldTile := node.pattern, node.patternTile
			owned := editor.ownedBytesLocked(nil)
			prior := make(map[int64][]byte, len(node.additional))
			for offset, data := range node.additional {
				prior[offset] = bytes.Clone(data)
			}
			for _, modify := range []func(*psdpattern.Fill){
				func(value *psdpattern.Fill) { value.Scale = math.Nextafter(value.Scale, math.Inf(1)) },
				func(value *psdpattern.Fill) { value.Angle = math.Nextafter(value.Angle, math.Inf(1)) },
				func(value *psdpattern.Fill) { value.Phase.X = math.Nextafter(value.Phase.X, math.Inf(1)) },
				func(value *psdpattern.Fill) { value.Phase.Y = math.Nextafter(value.Phase.Y, math.Inf(-1)) },
			} {
				requested := fill
				modify(&requested)
				if err := editor.SetPatternFill(nil, 1, requested); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("identical rounded bytes concealed an unrepresentable request: %v", err)
				}
				if after, _ := editor.Changes(); before != after || node.pattern != oldPattern ||
					node.patternTile != oldTile || editor.ownedBytesLocked(nil) != owned ||
					len(node.additional) != len(prior) {
					t.Fatal("failed exact-representation check changed revision or retained state")
				}
				for offset, data := range prior {
					if !bytes.Equal(node.additional[offset], data) {
						t.Fatal("failed exact-representation check changed descriptor overlay bytes")
					}
				}
				var afterSave bytes.Buffer
				if err := editor.Write(nil, &afterSave); err != nil || !bytes.Equal(beforeSave.Bytes(), afterSave.Bytes()) {
					t.Fatalf("failed exact-representation check changed saved data: %v", err)
				}
			}
		})
	}
}
