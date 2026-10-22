package psdscene

import (
	"context"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

var textCharacterNumbers = map[string]float64{
	"HorizontalScale": 1, "VerticalScale": 1, "Tracking": 0, "Kerning": 0, "BaselineShift": 0,
	"FontCaps": 0, "FontBaseline": 0, "BaselineDirection": 2, "Tsume": 0, "StyleRunAlignment": 2,
	"Language": 0, "YUnderline": 1, "Kashida": 1, "OutlineWidth": 1, "CharacterDirection": 0, "DiacriticPos": 2,
}

var textCharacterBooleans = map[string]bool{
	"FauxBold": false, "FauxItalic": false, "AutoLeading": false, "Underline": false,
	"Strikethrough": false, "Ligatures": false, "DLigatures": false, "NoBreak": false,
	"HindiNumbers": false, "FillFlag": true, "StrokeFlag": false, "FillFirst": true,
}

var textParagraphNumbers = map[string]float64{
	"Justification": 0, "FirstLineIndent": 0, "StartIndent": 0, "EndIndent": 0, "SpaceBefore": 0, "SpaceAfter": 0,
	"HyphenatedWordSize": 6, "PreHyphen": 2, "PostHyphen": 2, "ConsecutiveHyphens": 8, "Zone": 36,
	"AutoLeading": 1.75, "LeadingType": 1, "KinsokuOrder": 0, "Kinsoku": 0, "MojiKumi": 1,
}

var textParagraphBooleans = map[string]bool{
	"AutoHyphenate": false, "Hanging": false, "Burasagari": false, "EveryLineComposer": false,
}

var textParagraphArrays = map[string][]float64{
	"WordSpacing": {.8, 1, 1.33}, "LetterSpacing": {0, 0, 0}, "GlyphSpacing": {1, 1, 1},
}

func textStyle(metadata *psdtext.Metadata, descriptorText string) (psdfont.Style, [4]float64, error) {
	var result psdfont.Style
	var color [4]float64
	if metadata == nil || len(metadata.Styles) != 1 || metadata.Text != descriptorText+"\r" {
		return result, color, sceneError(psdfile.ErrUnsupported, "text.style", 0,
			"one complete character style and matching editor text are required")
	}
	style := metadata.Styles[0].Style
	if style.Data == nil || style.Font == nil || *style.Font < 0 || *style.Font >= int64(len(metadata.Fonts)) ||
		style.FontSize == nil || style.Leading == nil || *style.FontSize <= 0 || *style.Leading <= 0 ||
		style.AutoKerning == nil || style.FillColor == nil || style.FillColor.Type == nil ||
		*style.FillColor.Type != 1 || len(style.FillColor.Values) != 4 {
		return result, color, sceneError(psdfile.ErrUnsupported, "text.style", 0,
			"explicit font, size, leading, kerning and RGB fill metadata are required")
	}
	for _, required := range []string{"FauxBold", "FauxItalic", "AutoLeading", "HorizontalScale", "VerticalScale",
		"Tracking", "Kerning", "BaselineShift", "FontCaps", "FontBaseline", "Underline", "Strikethrough",
		"Ligatures", "DLigatures", "NoBreak"} {
		if style.Data.Lookup(required) == nil {
			return result, color, sceneError(psdfile.ErrUnsupported, "text.style", style.Data.Span.Offset,
				"inherited or incomplete character style is unsupported")
		}
	}
	for _, entry := range style.Data.Entries {
		if entry.Key == "Font" || entry.Key == "FontSize" || entry.Key == "Leading" || entry.Key == "AutoKerning" ||
			entry.Key == "FillColor" || entry.Key == "StrokeColor" {
			continue
		}
		if err := textDefault(entry.Key, entry.Value, textCharacterNumbers, textCharacterBooleans, nil); err != nil {
			return result, color, err
		}
	}
	font := metadata.Fonts[*style.Font]
	if font.Name == "" || font.Synthetic != nil && *font.Synthetic != 0 {
		return result, color, sceneError(psdfile.ErrUnsupported, "text.font", 0, "synthetic or unnamed font is unsupported")
	}
	if len(metadata.Paragraphs) == 0 {
		return result, color, sceneError(psdfile.ErrUnsupported, "text.paragraph", 0, "paragraph metadata is required")
	}
	for _, paragraph := range metadata.Paragraphs {
		properties := paragraph.Style.Data
		if properties == nil || properties.Kind != psdtext.Dictionary {
			return result, color, sceneError(psdfile.ErrUnsupported, "text.paragraph", 0,
				"explicit paragraph properties required")
		}
		required := []string{"Justification", "FirstLineIndent", "StartIndent", "EndIndent", "SpaceBefore", "SpaceAfter"}
		for _, key := range required {
			if properties.Lookup(key) == nil {
				return result, color, sceneError(psdfile.ErrUnsupported, "text.paragraph", properties.Span.Offset,
					"inherited or incomplete paragraph style is unsupported")
			}
		}
		for _, entry := range properties.Entries {
			err := textDefault(entry.Key, entry.Value, textParagraphNumbers, textParagraphBooleans, textParagraphArrays)
			if err != nil {
				return result, color, err
			}
		}
		adjustments := paragraph.Data.Lookup("Adjustments")
		if adjustments != nil {
			if !textArray(adjustments.Lookup("Axis"), []float64{1, 0, 1}) ||
				!textArray(adjustments.Lookup("XY"), []float64{0, 0}) || len(adjustments.Entries) != 2 {
				return result, color, sceneError(psdfile.ErrUnsupported, "text.paragraph.adjustments", adjustments.Span.Offset,
					"paragraph axis or position adjustments are unsupported")
			}
		}
	}
	for index, value := range style.FillColor.Values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return result, color, sceneError(psdfile.ErrUnsupported, "text.color", 0,
				"RGB fill components must be unit values")
		}
		color[index] = value
	}
	result = psdfont.Style{FontName: font.Name, Size: *style.FontSize,
		Leading: *style.Leading, Kerning: *style.AutoKerning}
	return result, color, nil
}

func textDefault(
	key string, value *psdtext.Node, numbers map[string]float64,
	booleans map[string]bool, arrays map[string][]float64,
) error {
	if expected, found := numbers[key]; found {
		actual, ok := value.Float64()
		if ok && actual == expected {
			return nil
		}
	} else if expected, found := booleans[key]; found {
		if value != nil && value.Kind == psdtext.Boolean && value.Bool == expected {
			return nil
		}
	} else if expected, found := arrays[key]; found && textArray(value, expected) {
		return nil
	}
	offset := int64(0)
	if value != nil {
		offset = value.Span.Offset
	}
	return sceneError(psdfile.ErrUnsupported, "text.properties", offset, "nondefault or unknown typography property")
}

func textArray(node *psdtext.Node, expected []float64) bool {
	if node == nil || node.Kind != psdtext.List || len(node.Items) != len(expected) {
		return false
	}
	for index, value := range node.Items {
		actual, ok := value.Float64()
		if !ok || actual != expected[index] {
			return false
		}
	}
	return true
}

func textPointShape(engine *psdtext.Node) error {
	shapes := engine.Lookup("Rendered").Lookup("Shapes")
	children := shapes.Lookup("Children")
	if children == nil || children.Kind != psdtext.List || len(children.Items) != 1 {
		return sceneError(psdfile.ErrUnsupported, "text.shape", 0, "one explicit point-text shape is required")
	}
	shape := children.Items[0]
	ph := shape.Lookup("Cookie").Lookup("Photoshop")
	base := ph.Lookup("Base")
	for _, node := range []*psdtext.Node{shapes.Lookup("WritingDirection"), shape.Lookup("ShapeType"),
		shape.Lookup("Procession"), ph.Lookup("ShapeType"), base.Lookup("ShapeType")} {
		value, ok := node.Int64()
		if !ok || value != 0 {
			return sceneError(psdfile.ErrUnsupported, "text.shape", 0, "paragraph, vertical or non-point text is unsupported")
		}
	}
	if !textArray(ph.Lookup("PointBase"), []float64{0, 0}) ||
		!textArray(base.Lookup("TransformPoint0"), []float64{1, 0}) ||
		!textArray(base.Lookup("TransformPoint1"), []float64{0, 1}) ||
		!textArray(base.Lookup("TransformPoint2"), []float64{0, 0}) {
		return sceneError(psdfile.ErrUnsupported, "text.shape", ph.Span.Offset,
			"additional point-shape transforms are unsupported")
	}
	return nil
}

func textAffineRun(
	ctx context.Context, run *psdfont.Run, transform [6]float64, budget int64,
) (*psdfont.Run, int64, error) {
	if run == nil || !textFinite(run.Scale) || run.Scale <= 0 {
		return nil, 0, sceneError(psdfile.ErrMalformed, "text.affine", 0, "invalid glyph run scale")
	}
	for _, value := range transform {
		if !textFinite(value) {
			return nil, 0, sceneError(psdfile.ErrMalformed, "text.affine", 0, "nonfinite source transform")
		}
	}
	glyphBytes := int64(unsafe.Sizeof(psdfont.PositionedGlyph{}) + unsafe.Sizeof(psdfont.Glyph{}))
	reservation := int64(len(run.Glyphs))*glyphBytes + 256
	if reservation < 0 || reservation > budget {
		return nil, 0, sceneError(psdfile.ErrLimit, "text.affine", 0, "transformed glyph metadata exceeds the work budget")
	}
	result := &psdfont.Run{FontName: run.FontName, Scale: 1, Approximate: true,
		UTF16Units: run.UTF16Units, Glyphs: make([]psdfont.PositionedGlyph, len(run.Glyphs))}
	for index, positioned := range run.Glyphs {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		result.Glyphs[index] = positioned
		if positioned.LineBreak {
			continue
		}
		if positioned.Glyph == nil {
			return nil, 0, sceneError(psdfile.ErrMalformed, "text.affine", 0, "nil glyph in positioned run")
		}
		commands := positioned.Glyph.Path
		commandBytes := int64(len(commands)) * int64(unsafe.Sizeof(psdfont.Command{}))
		if commandBytes < 0 || commandBytes > budget-reservation {
			return nil, 0, sceneError(psdfile.ErrLimit, "text.affine", 0, "transformed paths exceed the work budget")
		}
		reservation += commandBytes
		glyph := &psdfont.Glyph{Path: make([]psdfont.Command, len(commands))}
		convert := func(px, py float64) (float64, float64) {
			x, y := positioned.X+px*run.Scale, positioned.Y-py*run.Scale
			return transform[0]*x + transform[2]*y + transform[4], -(transform[1]*x + transform[3]*y + transform[5])
		}
		for commandIndex, command := range commands {
			if commandIndex%256 == 0 && ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			glyph.Path[commandIndex] = command
			if command.Op == psdfont.Move || command.Op == psdfont.Line || command.Op == psdfont.Quad ||
				command.Op == psdfont.Cubic {
				glyph.Path[commandIndex].X, glyph.Path[commandIndex].Y = convert(command.X, command.Y)
			}
			if command.Op == psdfont.Quad || command.Op == psdfont.Cubic {
				controlX, controlY := convert(command.ControlX, command.ControlY)
				glyph.Path[commandIndex].ControlX, glyph.Path[commandIndex].ControlY = controlX, controlY
			}
			if command.Op == psdfont.Cubic {
				controlX, controlY := convert(command.Control2X, command.Control2Y)
				glyph.Path[commandIndex].Control2X, glyph.Path[commandIndex].Control2Y = controlX, controlY
			}
		}
		result.Glyphs[index].X, result.Glyphs[index].Y, result.Glyphs[index].Glyph = 0, 0, glyph
	}
	return result, reservation, nil
}

func textFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
