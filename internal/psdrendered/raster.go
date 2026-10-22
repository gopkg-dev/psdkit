// Package psdrendered binds a scene-generated raster to its placement and absolute geometry.
package psdrendered

import (
	"image"
	"reflect"
)

// WorkingBytes conservatively covers one retained identity token and its metadata-fingerprint scratch.
// It excludes the borrowed pixel backing and placed metadata, which remain owned by the source layer.
const WorkingBytes int64 = 512

// Raster records runtime identity after source projection and filters have completed.
// It has no serialized form and does not authenticate pixel contents; callers must obey model read-only rules.
type Raster struct {
	pixels       *image.NRGBA
	bounds       image.Rectangle
	placement    any
	placementKey [32]byte
	data         *byte
	length       int
	capacity     int
	stride       int
	pixelRect    image.Rectangle
	qualified    bool
}

type fingerprintedPlacement interface {
	RasterFingerprint() [32]byte
}

// New binds an owned rendered raster to the unchanged placed-layer metadata.
func New(pixels *image.NRGBA, bounds image.Rectangle, placement any) *Raster {
	raster := &Raster{pixels: pixels, bounds: bounds, placement: placement}
	key, valid := placementFingerprint(placement)
	if pixels == nil || !valid {
		return raster
	}
	raster.placementKey, raster.qualified = key, true
	raster.data, raster.length, raster.capacity = pixelData(pixels), len(pixels.Pix), cap(pixels.Pix)
	raster.stride, raster.pixelRect = pixels.Stride, pixels.Rect
	return raster
}

// Valid checks that the same raster, bounds and placement still identify the rendered layer.
// Placement is supplied as a pointer by the scene engine; arbitrary noncomparable values are rejected safely.
func (raster *Raster) Valid(pixels *image.NRGBA, bounds image.Rectangle, placement any) bool {
	if raster == nil || !raster.qualified || raster.pixels != pixels || raster.bounds != bounds || pixels == nil ||
		raster.data != pixelData(pixels) || raster.length != len(pixels.Pix) || raster.capacity != cap(pixels.Pix) ||
		raster.stride != pixels.Stride || raster.pixelRect != pixels.Rect {
		return false
	}
	key, valid := placementFingerprint(placement)
	if !valid || raster.placement != placement {
		return false
	}
	return raster.placementKey == key
}

func pixelData(pixels *image.NRGBA) *byte {
	if len(pixels.Pix) == 0 {
		return nil
	}
	return &pixels.Pix[0]
}

func placementFingerprint(placement any) ([32]byte, bool) {
	if placement == nil || reflect.TypeOf(placement).Kind() != reflect.Pointer ||
		reflect.ValueOf(placement).IsNil() {
		return [32]byte{}, false
	}
	value, valid := placement.(fingerprintedPlacement)
	if !valid {
		return [32]byte{}, false
	}
	return value.RasterFingerprint(), true
}
