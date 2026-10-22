package psdscene

import (
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestOpenTypeTypographyPolicyRejectsAmbiguousModels(t *testing.T) {
	for _, control := range []struct {
		name    string
		options OpenTypeTextRenderOptions
		want    error
	}{
		{name: "legacy", options: OpenTypeTextRenderOptions{}},
		{name: "expanded", options: OpenTypeTextRenderOptions{
			ApproximateTypography: true, FauxBold: OpenTypeFauxBoldExpandedAdvance}},
		{name: "area", options: OpenTypeTextRenderOptions{ApproximateTypography: true, AreaCoverage: true}},
		{name: "pixelCenter", options: OpenTypeTextRenderOptions{ApproximateTypography: true, NoAntialias: true}},
		{name: "unknownWeight", options: OpenTypeTextRenderOptions{
			ApproximateTypography: true, FauxBold: 255}, want: psdfile.ErrUnsupported},
		{name: "weightWithoutTypography", options: OpenTypeTextRenderOptions{
			FauxBold: OpenTypeFauxBoldExpandedAdvance}, want: psdfile.ErrUnsupported},
		{name: "coverageWithoutTypography", options: OpenTypeTextRenderOptions{
			AreaCoverage: true}, want: psdfile.ErrUnsupported},
		{name: "mixedStyles", options: OpenTypeTextRenderOptions{
			ApproximateTypography: true, NoAntialias: true,
			StyleBoundaries: OpenTypeIsolatedStyleRuns}, want: psdfile.ErrUnsupported},
		{name: "twoCoverageModels", options: OpenTypeTextRenderOptions{
			ApproximateTypography: true, AreaCoverage: true, NoAntialias: true}, want: psdfile.ErrMalformed},
	} {
		t.Run(control.name, func(t *testing.T) {
			control.options.Kerning = OpenTypeTextNone
			err := validateOpenTypeTypographyPolicy(control.options)
			if !errors.Is(err, control.want) {
				t.Fatalf("direct policy qualification: %v; want %v", err, control.want)
			}
			owned, _, err := ownOpenTypeTextOptions(&control.options, 1<<20)
			if !errors.Is(err, control.want) || (owned == nil) != (control.want != nil) {
				t.Fatalf("scene policy ownership: %v; want %v", err, control.want)
			}
		})
	}
}
