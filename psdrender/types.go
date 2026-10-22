// Package psdrender renders the editor's supported PSD subset using straight-alpha Go pixels.
package psdrender

import "github.com/gopkg-dev/psdkit/psdmodel"

// Allocation limits match the frontend PSD engine.
const (
	MaxPixels        = psdmodel.MaxPixels
	MaxDimension     = psdmodel.MaxDimension
	MaxDecodedPixels = psdmodel.MaxDecodedPixels
	MaxInputPixels   = 40_000_000
	MaxLayers        = psdmodel.MaxLayers
	MaxFileBytes     = psdmodel.MaxFileBytes
)

// Document aliases the shared model for compatibility with existing integrations.
type Document = psdmodel.Document

// Layer aliases the shared layer model.
type Layer = psdmodel.Layer

// Mask aliases the shared mask model.
type Mask = psdmodel.Mask

// BlendSlider aliases the split endpoints of one Blend If slider.
type BlendSlider = psdmodel.BlendSlider

// BlendRange aliases source and underlying-color Blend If controls.
type BlendRange = psdmodel.BlendRange

// Placed aliases the shared smart-object instance model.
type Placed = psdmodel.Placed

// Point aliases the shared canvas coordinate model.
type Point = psdmodel.Point

// WarpBounds aliases the shared warp bounds model.
type WarpBounds = psdmodel.WarpBounds

// Warp aliases the shared warp metadata model.
type Warp = psdmodel.Warp

// Slice aliases the shared document crop model.
type Slice = psdmodel.Slice

// Instance identifies a visible occurrence of an object in a document.
type Instance struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Left   int    `json:"left"`
	Top    int    `json:"top"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Object groups every instance of a linked smart object.
type Object struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	Instances []Instance `json:"instances"`
}

// BackgroundLayer describes a replaceable saved raster, including disabled smart objects.
type BackgroundLayer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ObjectID string `json:"objectId,omitempty"`
	Left     int    `json:"left"`
	Top      int    `json:"top"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// Metadata uses the frontend's JSON field names.
type Metadata struct {
	Width              int               `json:"width"`
	Height             int               `json:"height"`
	Objects            []Object          `json:"objects"`
	Slices             []Slice           `json:"slices"`
	Warnings           []string          `json:"warnings"`
	BlockingIssues     []string          `json:"blockingIssues"`
	BackgroundLayers   []BackgroundLayer `json:"backgroundLayers"`
	WorkingBytes       int64             `json:"workingBytes"`
	ObjectNamesVersion int               `json:"objectNamesVersion"`
}

// ObjectConfig maps one enabled smart object to an input tag.
type ObjectConfig struct {
	ObjectID string `json:"objectId"`
	Tag      string `json:"tag"`
	Enabled  bool   `json:"enabled"`
}

// Candidate references encoded input pixels held outside the renderer.
type Candidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// RandomRow controls independent background selection for one saved raster.
type RandomRow struct {
	LayerID                   string      `json:"layerId"`
	LayerName                 string      `json:"layerName"`
	LayerPreview              string      `json:"layerPreview,omitempty"`
	RandomImages              []Candidate `json:"randomImages"`
	Enabled                   bool        `json:"enabled"`
	ExcludeOriginalFromRandom bool        `json:"excludeOriginalFromRandom"`
}

// Config has the same fitting, alignment and replacement semantics as the editor.
type Config struct {
	Fit             string         `json:"fit"`
	Align           string         `json:"align"`
	Objects         []ObjectConfig `json:"objects"`
	RandomImageRows []RandomRow    `json:"randomImageRows,omitempty"`
}

// Options controls output crops, encoding and input transparency trimming.
type Options struct {
	Slices          bool   `json:"slices"`
	Format          string `json:"format"`
	MaxSize         int    `json:"maxSize,omitempty"`
	TrimTransparent bool   `json:"trimTransparent,omitempty"`
}

// RenderOptions is an alias for integrations using the frontend's terminology.
type RenderOptions = Options
