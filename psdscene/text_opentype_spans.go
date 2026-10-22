package psdscene

import (
	"context"
	"fmt"
	"unicode/utf8"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

type openTypeSourceSpan struct {
	text               string
	byteStart, byteEnd int
	start, length      int64
	style              psdfont.Style
	color              [4]float64
}

// Source ranges retain the terminal paragraph marker, while glyph substrings exclude that marker.
func openTypeTextSpans(
	ctx context.Context, metadata *psdtext.Metadata, text string, budget int64,
) ([]openTypeSourceSpan, int64, error) {
	if metadata == nil || metadata.Text != text+"\r" || metadata.UTF16Units <= 0 || len(metadata.Styles) == 0 ||
		len(metadata.Paragraphs) != 1 || metadata.Paragraphs[0].Start != 0 ||
		metadata.Paragraphs[0].Length != metadata.UTF16Units {
		return nil, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.spans", 0,
			"complete contiguous character styles and one default paragraph are required")
	}
	spanBytes := int64(unsafe.Sizeof(openTypeSourceSpan{}))
	if budget < 256 || int64(len(metadata.Styles)) > (budget-256)/spanBytes {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spans", 0, "style span ownership exceeds budget")
	}
	reserved := 256 + int64(len(metadata.Styles))*spanBytes
	result := make([]openTypeSourceSpan, len(metadata.Styles))
	position, bytePosition := int64(0), 0
	glyphUnits := metadata.UTF16Units - 1
	for index, sourceRun := range metadata.Styles {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if sourceRun.Start != position || sourceRun.Length <= 0 || sourceRun.Start < 0 ||
			sourceRun.Start > metadata.UTF16Units || sourceRun.Length > metadata.UTF16Units-sourceRun.Start {
			return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.spans", 0,
				"character style ranges must partition the complete UTF16 editor text")
		}
		end := sourceRun.Start + sourceRun.Length
		beginByte := bytePosition
		for position < min(end, glyphUnits) {
			if bytePosition >= len(text) {
				return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.spans", 0, "UTF16 range exceeds text")
			}
			if bytePosition%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, 0, err
				}
			}
			character, width := utf8.DecodeRuneInString(text[bytePosition:])
			if character == utf8.RuneError && width == 1 {
				return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.spans", 0, "invalid UTF8 source text")
			}
			units := int64(1)
			if character > 0xffff {
				units = 2
			}
			if units > min(end, glyphUnits)-position {
				return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.spans", 0,
					"character style boundary splits a UTF16 surrogate pair")
			}
			bytePosition += width
			position += units
		}
		position = end
		// Reuse the complete-property validator against one projected style without editing borrowed source nodes.
		projected := *metadata
		projectedRun := sourceRun
		projectedRun.Start, projectedRun.Length = 0, metadata.UTF16Units
		projected.Styles = []psdtext.StyleRun{projectedRun}
		style, color, copyBytes, err := openTypeTextStyle(&projected, text, budget-reserved)
		if err != nil {
			return nil, 0, fmt.Errorf("style span%d: %w", index, err)
		}
		// Projected validation storage is transient, but its construction peak remains conservative for the caller.
		reserved += copyBytes
		result[index] = openTypeSourceSpan{text: text[beginByte:bytePosition], start: sourceRun.Start,
			length: sourceRun.Length, style: style, color: color, byteStart: beginByte, byteEnd: bytePosition}
	}
	if position != metadata.UTF16Units || bytePosition != len(text) {
		return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.spans", 0,
			"style coverage does not match source text")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return result, reserved, nil
}
