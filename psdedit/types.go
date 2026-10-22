// Package psdedit applies controlled layer edits and saves PSD/PSB documents while preserving source blocks.
package psdedit

import (
	"errors"
	"image"
	"sync"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// LayerID is a stable editor handle, distinct from a file's lyid metadata and physical index.
type LayerID uint64

// LayerInfo is an owned metadata view; Children and siblings are ordered bottom to top.
type LayerInfo struct {
	ID, Parent      LayerID
	Name            string
	Visible         bool
	Opacity         uint8
	FillOpacity     uint8
	Bounds          image.Rectangle
	Group, Expanded bool
	SourceIndex     int
	PSDID           uint32
	Children        []LayerInfo
}

// Changes reports the current revision and its relation to the last successful save.
// CompositeChanged remains true for visual edits relative to the immutable original source.
type Changes struct {
	Revision, SavedRevision                   uint64
	Dirty, CompositeChanged, StructureChanged bool
}

// SaveOptions selects compression for changed layer channels. Zero Compression selects Raw.
// Unchanged channels and composites retain their original encoding.
// Changed 32-bit layer channels requested as ZIP use ZIP prediction for Photoshop compatibility.
// Rebuilt composites use Raw or RLE: ZIP requests use RLE at depths 1/8 and Raw at depths 16/32.
// Composite and CompositeImage are mutually exclusive explicit replacement previews.
// Supplied previews must represent the edited document; the library validates their shape, not their artwork.
type SaveOptions struct {
	Compression    uint16
	Composite      *psdfile.CompositePixels
	CompositeImage *image.NRGBA
	MaxOutputBytes int64
	// ApproximateHueSaturation explicitly permits approximate Hue/Saturation for an automatically rebuilt preview.
	// An accurately supplied Composite or CompositeImage bypasses live rendering and needs no approximation flag.
	ApproximateHueSaturation bool
	// ApproximateDropShadow explicitly selects fractional DropShadow rendering for automatic previews.
	// Accurate supplied composites bypass this approximation and remain the caller's responsibility.
	ApproximateDropShadow bool
	// ApproximateClippedGroupBackdrop selects the explicit RGB-only clipping-base approximation.
	// It affects automatically rendered previews; supplied accurate previews bypass live composition.
	// The zero value preserves legacy RGB, and either policy preserves the legacy alpha calculation.
	ApproximateClippedGroupBackdrop bool
	// MaxClippedGroupEvaluations bounds selected automatic-preview source entries per output pixel.
	// Zero selects 4096; accurate supplied/native previews do not execute this rendering work.
	MaxClippedGroupEvaluations int64
	reservedForRender          int64
	writeFixedBytes            int64
}

var (
	// ErrInvalidEdit identifies invalid handles, unsupported tree operations or malformed image inputs.
	ErrInvalidEdit = errors.New("invalid PSD edit")
	// ErrUnsupported identifies an edit or automatic composite operation not supported in this phase.
	ErrUnsupported = errors.New("unsupported PSD edit or composite reconstruction")
)

// Editor owns mutable edit overlays; its source document remains immutable.
// Edits and saves are synchronized, and caller-supplied replacement pixels are copied.
type Editor struct {
	source                          *psdfile.Document
	ownsSource                      bool
	mu                              sync.RWMutex
	roots                           []*editNode
	nextID                          LayerID
	nextPSDID                       uint32
	revision, savedRevision         uint64
	visualChanged, structureChanged bool
	closed                          bool
	linkedRecords                   []psdfile.LinkedFileRecord
	linkedRecordsBytes              int64
	linkedOverlays                  map[string]*linkedReplacement
	globalAdditional                map[int64][]byte
	nativeTextPreview               *image.NRGBA
	nativeTextPreviewRevision       uint64
}

type linkedReplacement struct {
	data           []byte
	fileType       string
	embedExternal  bool
	retainExternal bool
	timestamp      *psdfile.LinkedFileTimestamp
}

type editNode struct {
	id                                          LayerID
	sourceIndex, endSourceIndex                 int
	psdID, endPSDID                             uint32
	name                                        string
	visible                                     bool
	opacity, fillOpacity                        uint8
	bounds                                      image.Rectangle
	group, expanded                             bool
	children                                    []*editNode
	pixels                                      *image.NRGBA
	planes                                      map[int16]psdfile.Plane
	additional                                  map[int64][]byte
	fillColor                                   *[3]float64
	adjustment                                  *psdadjust.Adjustment
	gradient                                    *psdgradient.Fill
	pattern                                     *psdpattern.Fill
	patternTile                                 *image.NRGBA
	effects                                     *psdeffects.Set
	vectorMask                                  *psdvector.Mask
	placed                                      *psdmodel.Placed
	semanticBytes                               int64
	semanticCosts                               map[string]int64
	nameChanged, visibleChanged, opacityChanged bool
	fillOpacityChanged                          bool
	fillOpacityAdded                            bool
	virtualComposite                            bool
}

type writeLayer struct {
	node        *editNode
	sourceIndex int
	divider     uint32
	end         bool
}

type preparedComposite struct {
	header      psdfile.Header
	globalAlpha bool
	data        []byte
}

func editError(kind error, path string, detail string) *psdfile.Error {
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: path, Detail: detail}
}
