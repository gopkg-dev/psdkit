package psdeffects

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
)

func effectRGB() *Descriptor {
	return &Descriptor{ClassID: "RGBC", Entries: []Entry{
		{Key: "Rd  ", Type: "doub", Value: float64(255)},
		{Key: "Grn ", Type: "doub", Value: float64(0)},
		{Key: "Bl  ", Type: "doub", Value: float64(0)},
	}}
}

func shadowDescriptor(enabled bool) *Descriptor {
	return &Descriptor{ClassID: "DrSh", Entries: []Entry{
		{Key: "enab", Type: "bool", Value: enabled},
		{Key: "Clr ", Type: "Objc", Value: effectRGB()},
		{Key: "Opct", Type: "UntF", Value: Unit{Unit: "#Prc", Number: 75}},
		{Key: "Dstn", Type: "UntF", Value: Unit{Unit: "#Pxl", Number: 3}},
		{Key: "lagl", Type: "UntF", Value: Unit{Unit: "#Ang", Number: 120}},
		{Key: "uglg", Type: "bool", Value: true},
	}}
}

func TestDecodeEffectsOwnsOrderedUnknownDataAndParameters(t *testing.T) {
	descriptor := &Descriptor{Version: 16, Entries: []Entry{
		{Key: "masterFXSwitch", Type: "bool", Value: true},
		{Key: "opaque", Type: "tdta", Value: Raw{Data: Span{Offset: 91, Length: 24}}},
		{Key: "DrSh", Type: "Objc", Value: shadowDescriptor(true)},
		{Key: "opaque", Type: "long", Value: int32(7)},
	}}
	set, err := Decode(nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.OrderedEntries) != 4 || set.OrderedEntries[1].Key != "opaque" ||
		set.OrderedEntries[3].Key != "opaque" || set.OrderedEntries[1].Value.(Raw).Data.Offset != 91 {
		t.Fatal("unknown ordered duplicate entries or original blob span were lost")
	}
	effect := set.Items[0]
	if effect.Kind != DropShadow || effect.Color != [3]float64{1, 0, 0} || effect.Opacity != 0.75 ||
		effect.Distance != 3 || effect.Angle != 120 || !effect.UseGlobalLight || set.GlobalAngle != 30 {
		t.Fatal("known typed effect parameters differ from the descriptor")
	}
	original := descriptor.Entries[2].Value.(*Descriptor)
	original.Entries[1].Value.(*Descriptor).Entries[0].Value = float64(4)
	original.Entries[0].Value = false
	descriptor.Entries[1].Key = "changed"
	if !set.Items[0].Enabled || set.Items[0].Color[0] != 1 || set.OrderedEntries[1].Key != "opaque" ||
		set.Items[0].Descriptor.Entries[1].Value.(*Descriptor).Entries[0].Value != float64(255) {
		t.Fatal("typed or original descriptor views borrowed the caller's mutable entries")
	}
}

func TestDecodeDisabledUnknownContourAndMultipleEffects(t *testing.T) {
	shadow := shadowDescriptor(false)
	shadow.Entries = append(shadow.Entries, Entry{Key: "TrnS", Type: "Objc",
		Value: &Descriptor{ClassID: "ShpC", Entries: []Entry{{Key: "custom", Type: "long", Value: int32(1)}}}})
	descriptor := &Descriptor{Entries: []Entry{{Key: "dropShadowMulti", Type: "VlLs", Value: []Value{
		{Type: "Objc", Value: shadow}, {Type: "Objc", Value: shadowDescriptor(true)},
	}}}}
	set, err := Decode(nil, descriptor)
	if err != nil || len(set.Items) != 2 || len(set.Items[0].Unsupported) == 0 {
		t.Fatalf("disabled nonstandard contour metadata was not preserved: %v", err)
	}
	if err := Validate(set); err != nil {
		t.Fatalf("disabled unsupported contour blocked the active basic shadow: %v", err)
	}
	set.Items[0].Enabled = true
	if err := Validate(set); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("active unknown contour was silently ignored: %v", err)
	}
	set.Enabled = false
	if _, err := Rasterize(nil, rectangleAlpha(image.Rect(0, 0, 1, 1), 255), set); err != nil {
		t.Fatalf("master switch did not suppress disabled effect semantics: %v", err)
	}
}

func TestDecodeLimitsCyclesAndCancellation(t *testing.T) {
	descriptor := &Descriptor{Entries: []Entry{{Key: "DrSh", Type: "Objc", Value: shadowDescriptor(true)}}}
	for _, options := range []DecodeOptions{{MaxBytes: 100}, {MaxEntries: 1}, {MaxDepth: 1}} {
		if set, err := Decode(nil, descriptor, options); !errors.Is(err, ErrLimit) || set != nil {
			t.Fatalf("descriptor snapshot returned partial output or ignored its limits: %v", err)
		}
	}
	cycle := &Descriptor{}
	cycle.Entries = []Entry{{Key: "cycle", Type: "Objc", Value: cycle}}
	if _, err := Decode(nil, cycle); !errors.Is(err, ErrLimit) {
		t.Fatalf("cyclic caller descriptor was not depth bounded: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, descriptor); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled descriptor decode: %v", err)
	}
	largeKey := &Descriptor{Entries: []Entry{{Key: strings.Repeat("x", 10_000), Type: "long", Value: int32(1)}}}
	if _, err := Decode(nil, largeKey, DecodeOptions{MaxBytes: 1000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("retained unknown key and diagnostic string escaped the byte budget: %v", err)
	}
}
