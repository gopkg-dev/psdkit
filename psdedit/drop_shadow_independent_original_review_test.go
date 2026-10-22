package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestIndependentApproximateShadowCannotRepairInvalidOriginalSpread(t *testing.T) {
	data := fractionalShadowEditFixture(t, 1, false, false)
	encoded := binary.BigEndian.AppendUint64(nil, math.Float64bits(4))
	invalid := binary.BigEndian.AppendUint64(nil, math.Float64bits(140))
	// Independently identify the unique Ckmt unit scalar, preserving all other descriptor fields.
	needle := append([]byte("CkmtUntF#Pxl"), encoded...)
	if bytes.Count(data, needle) != 1 {
		t.Fatalf("choke scalar fixture is not unique: %d", bytes.Count(data, needle))
	}
	data = bytes.Replace(data, needle, append([]byte("CkmtUntF#Pxl"), invalid...), 1)
	editor := compositeEditor(t, data)
	id, desired, _ := maskedOverlaySourceSet(t, editor)
	if desired.Items[0].Spread != 1.4 {
		t.Fatalf("invalid original spread was normalized: %g", desired.Items[0].Spread)
	}
	desired.Items[0].Spread = .07
	err := editor.SetEffectsWithOptions(nil, id, *desired, EffectsEditOptions{ApproximateDropShadow: true})
	if !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("valid desired spread erased invalid original source: %v", err)
	}
	changes, _ := editor.Changes()
	if changes.Dirty {
		t.Fatal("invalid source effect qualification committed desired parameters")
	}
}
