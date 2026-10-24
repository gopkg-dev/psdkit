// Package psdplaced carries private candidate placement inputs between editing and source rendering.
package psdplaced

import (
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
)

// Prepared identifies a candidate modern placement while retaining its original parent sources.
// Source and Candidate remain caller-owned and open throughout rendering; neither is modified or closed.
// Candidate contains exactly one layer with the candidate SoLd/SoLE block in its current edited form.
// Non-nil Contents replaces this placement's original linked contents for rendering only, using FileType.
// Encoded input storage and document indexes are borrowed and must already be charged by the editing transaction.
// Contents must remain immutable until rendering returns; nil uses the original parent's linked source.
type Prepared struct {
	Source, Candidate *psdfile.Document
	LayerIndex        int
	Contents          []byte
	FileType          string
	// Effects supplies current parent effects solely to qualify the required source neighborhood.
	// Source rendering retains this metadata but does not apply its colors or alpha to artwork.
	Effects *psdeffects.Set
	// EffectsApproximateDropShadow selects only this parent's source-neighborhood calculation.
	// It never enables approximation in recursively rendered contents.
	EffectsApproximateDropShadow bool
}
