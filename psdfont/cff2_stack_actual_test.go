package psdfont

import (
	"encoding/json/v2"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestCFF2Stack513FullPathOperationsAndEOF(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/stack-inputs-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		File  string
		Cases []struct {
			Name          string
			ID            uint16
			ExpectedError string `json:"expected_error"`
			Commands      []Command
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	face, err := Open(nil, oracle.File)
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, row := range oracle.Cases {
		t.Run(row.Name, func(t *testing.T) {
			glyph, err := face.Glyph(nil, row.ID)
			if row.ExpectedError != "" {
				cause := ErrMalformed
				if row.ExpectedError == "limit" {
					cause = ErrLimit
				}
				if glyph != nil || !errors.Is(err, cause) {
					t.Fatalf("expected typed %s nilGlyph got %+v %v", row.ExpectedError, glyph, err)
				}
				return
			}
			if glyph == nil || err != nil || len(glyph.Path) != len(row.Commands) {
				t.Fatalf("full path: got%+v %v expected%d commands", glyph, err, len(row.Commands))
			}
			for index, command := range glyph.Path {
				if !reflect.DeepEqual(command, row.Commands[index]) {
					t.Fatalf("command%d %+v expected%+v", index, command, row.Commands[index])
				}
			}
			commands += len(glyph.Path)
			for _, delta := range []int64{-1, 0, 1} {
				bounded, err := face.Glyph(nil, row.ID,
					GlyphOptions{MaxWorkingBytes: glyph.WorkingBytes() + delta})
				if delta < 0 {
					if bounded != nil || !errors.Is(err, ErrLimit) {
						t.Fatalf("one-short glyph must fail closed: %v", err)
					}
				} else if bounded == nil || err != nil {
					t.Fatalf("exact glyph budget: %v", err)
				}
			}
			if row.Name == "Subr512" {
				bounded, err := face.Glyph(nil, row.ID, GlyphOptions{MaxComponents: 1})
				if bounded != nil || !errors.Is(err, ErrLimit) {
					t.Fatalf("shared root and subroutine visits: %v", err)
				}
			}
		})
	}
	if len(oracle.Cases) != 14 || commands != 1979 {
		t.Fatalf("nonempty 14 cases/1979 commands required, got %d/%d", len(oracle.Cases), commands)
	}
}
