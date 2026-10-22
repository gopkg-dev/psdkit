package psdfont

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"unsafe"
)

func ownedRunBytes(run *Run) int64 {
	retained := int64(unsafe.Sizeof(Run{})) + int64(cap(run.Glyphs))*int64(unsafe.Sizeof(PositionedGlyph{}))
	seen := make(map[*Glyph]bool)
	for _, positioned := range run.Glyphs {
		glyph := positioned.Glyph
		if glyph == nil || seen[glyph] {
			continue
		}
		seen[glyph] = true
		retained += int64(unsafe.Sizeof(Glyph{})) + int64(cap(glyph.Contours))*int64(unsafe.Sizeof([]Point{}))
		for _, contour := range glyph.Contours {
			retained += int64(cap(contour)) * int64(unsafe.Sizeof(Point{}))
		}
		retained += int64(cap(glyph.Path))*int64(unsafe.Sizeof(Command{})) +
			int64(cap(glyph.Instructions))*int64(unsafe.Sizeof(Span{}))
	}
	return retained
}

func TestRunWorkingBytesRecordsConstructionPeak(t *testing.T) {
	if (*Run)(nil).WorkingBytes() != 0 || (&Run{}).WorkingBytes() != 0 {
		t.Fatal("unproduced run has a reservation")
	}
	for _, name := range []string{"base.ttf", "geometry-y-position.ttf"} {
		face, err := Open(nil, filepath.Join("testdata", "opentype", "fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
		for _, text := range []string{"A", "AVfiAffl", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
			style := Style{FontName: face.PostScriptName, Size: 96, Leading: 120}
			for _, openType := range []bool{false, true} {
				var run *Run
				if openType {
					run, err = LayoutOpenType(nil, text, style, resolver, ShapeOptions{})
				} else {
					run, err = Layout(nil, text, style, resolver)
				}
				if err != nil {
					t.Fatal(err)
				}
				if run.WorkingBytes() < ownedRunBytes(run) || run.WorkingBytes() <= 0 {
					t.Fatalf("name=%s opentype=%t recorded=%d owned=%d", name, openType,
						run.WorkingBytes(), ownedRunBytes(run))
				}
				if !openType {
					continue
				}
				limits := LayoutOptions{MaxWorkingBytes: run.WorkingBytes()}
				bounded, boundedErr := LayoutOpenType(nil, text, style, resolver, ShapeOptions{}, limits)
				if boundedErr != nil || bounded == nil || bounded.WorkingBytes() != run.WorkingBytes() {
					t.Fatalf("exact recorded peak rejected: %v", boundedErr)
				}
				limits.MaxWorkingBytes--
				partial, boundedErr := LayoutOpenType(nil, text, style, resolver, ShapeOptions{}, limits)
				if partial != nil || !errors.Is(boundedErr, ErrLimit) {
					t.Fatalf("peak minus one unexpectedly succeeded: %v", boundedErr)
				}
			}
		}
	}
}
