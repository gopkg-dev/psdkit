package psdfile

import (
	"context"
	"errors"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdgradient"
)

// GradientFill retains an owned live gradient view and its complete ordered source descriptor.
// Descriptor entries and spans retain unknown metadata without normalizing their encoding.
// Block and Tail spans borrow the source document's lifetime; no source pixels are decoded.
type GradientFill struct {
	Fill       *psdgradient.Fill
	Descriptor *Descriptor
	Block      AdditionalBlock
	Tail       Span
}

// DecodeGradientFill reads one GdFl descriptor under owned metadata and source lifecycle budgets.
// Unsupported interpolation, colors and unrecognized semantic fields return a retained view plus ErrUnsupported.
// It does not convert source RGB through ICC, open any external file or mutate source bytes.
func (d *Document) DecodeGradientFill(
	ctx context.Context, block AdditionalBlock, options ...MetadataOptions,
) (*GradientFill, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if block.Key != "GdFl" {
		return nil, fileError(ErrUnsupported, "gradient", "gradient.key", block.Data.Offset,
			"additional block is not a gradient fill")
	}
	if !d.validSpan(block.Data) {
		return nil, fileError(ErrMalformed, "gradient", "gradient.span", block.Data.Offset,
			"gradient descriptor is outside the source")
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: limits}
	cursor := descriptorCursor{document: view, offset: block.Data.Offset, end: block.Data.Offset + block.Data.Length}
	if !cursor.reserve(int64(unsafe.Sizeof(GradientFill{}))+int64(unsafe.Sizeof(psdgradient.Fill{})), "gradient") {
		return nil, cursor.err
	}
	version := cursor.u32("gradient.version")
	if cursor.err != nil {
		return nil, cursor.err
	}
	if version != 16 {
		return nil, fileError(ErrUnsupported, "gradient", "gradient.version", block.Data.Offset,
			"gradient descriptor version is not supported")
	}
	descriptor := cursor.descriptor(1, "gradient.descriptor", false)
	if cursor.err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, cursor.err
	}
	descriptor.Version, descriptor.VersionData = version, Span{Offset: block.Data.Offset, Length: 4}
	descriptor.Span = Span{Offset: block.Data.Offset, Length: cursor.offset - block.Data.Offset}
	result := &GradientFill{Descriptor: descriptor, Block: block,
		Tail: Span{Offset: cursor.offset, Length: cursor.end - cursor.offset}}
	converter := gradientConverter{cursor: &cursor, fill: &psdgradient.Fill{
		Kind: psdgradient.Linear, Scale: 100, Align: true, Smoothness: 4096, Interpolation: "classic", Form: "custom-stops",
	}}
	converter.decode(descriptor)
	result.Fill = converter.fill
	if cursor.err != nil {
		return result, cursor.err
	}
	if result.Tail.Length > 3 {
		converter.unsupported("gradient.tail")
	} else if result.Tail.Length != 0 {
		padding := cursor.bytes(result.Tail.Length, "gradient.tail")
		if cursor.err != nil {
			return result, cursor.err
		}
		for _, value := range padding {
			if value != 0 {
				converter.unsupported("gradient.tail")
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(result.Fill.Unsupported) != 0 {
		return result, fileError(ErrUnsupported, "gradient", result.Fill.Unsupported[0], block.Data.Offset,
			"gradient semantics are retained but not supported for rendering")
	}
	if err := psdgradient.Validate(*result.Fill); err != nil {
		kind := ErrMalformed
		if errors.Is(err, psdgradient.ErrUnsupported) {
			kind = ErrUnsupported
		} else if errors.Is(err, psdgradient.ErrLimit) {
			kind = ErrLimit
		}
		return result, &Error{Kind: kind, Feature: "gradient", Path: "gradient.fill", Offset: block.Data.Offset,
			Detail: "typed gradient parameters are invalid or outside the renderer subset", Cause: err}
	}
	return result, nil
}

type gradientConverter struct {
	cursor *descriptorCursor
	fill   *psdgradient.Fill
}

func (c *gradientConverter) unsupported(path string) {
	if c.cursor.reserve(int64(len(path))+16, path) {
		c.fill.Unsupported = append(c.fill.Unsupported, path)
	}
}

func (c *gradientConverter) invalid(path string) {
	c.cursor.fail(ErrMalformed, path, c.cursor.offset, "gradient descriptor field has an invalid encoding or range")
}

func (c *gradientConverter) entries(descriptor *Descriptor, path string, visit func(DescriptorEntry)) {
	if !c.cursor.reserve(64+int64(len(descriptor.Entries))*64, path) {
		return
	}
	seen := make(map[string]bool)
	for _, entry := range descriptor.Entries {
		if c.cursor.err != nil {
			return
		}
		logicalKey := entry.Key
		if logicalKey == "gradientsInterpolationMethod" {
			logicalKey = "gradientInterpolationMethod"
		}
		if seen[logicalKey] {
			c.unsupported(path + ".duplicate")
		}
		seen[logicalKey] = true
		visit(entry)
	}
}

func (c *gradientConverter) decode(descriptor *Descriptor) {
	if descriptor.ClassID != "gradientLayer" && descriptor.ClassID != "GdFl" && descriptor.ClassID != "null" {
		c.unsupported("gradient.class")
	}
	foundGradient := false
	c.entries(descriptor, "gradient", func(entry DescriptorEntry) {
		switch entry.Key {
		case "Grad":
			gradient := c.object(entry, "gradient.stops")
			if gradient != nil {
				foundGradient = true
				c.gradient(gradient)
			}
		case "Angl":
			c.fill.Angle = c.unit(entry, "#Ang", "gradient.angle")
		case "Scl ":
			c.fill.Scale = c.unit(entry, "#Prc", "gradient.scale")
		case "Type":
			style := c.enumeration(entry, "GrdT", "gradient.type")
			switch style {
			case "Lnr ":
				c.fill.Kind = psdgradient.Linear
			case "Rdl ":
				c.fill.Kind = psdgradient.Radial
			default:
				c.fill.Kind = psdgradient.Kind(style)
				c.unsupported("gradient.type")
			}
		case "Rvrs":
			c.fill.Reverse = c.boolean(entry, "gradient.reverse")
		case "Algn":
			c.fill.Align = c.boolean(entry, "gradient.align")
		case "Dthr":
			c.fill.Dither = c.boolean(entry, "gradient.dither")
			if c.fill.Dither {
				c.unsupported("gradient.dither")
			}
		case "Ofst":
			c.offset(c.object(entry, "gradient.offset"))
		case "gradientInterpolationMethod", "gradientsInterpolationMethod":
			c.fill.Interpolation = c.enumeration(entry, "gradientInterpolationMethodType", "gradient.interpolation")
			if c.fill.Interpolation == "Gcls" {
				c.fill.Interpolation = "classic"
			}
			if c.fill.Interpolation != "classic" {
				c.unsupported("gradient.interpolation")
			}
		default:
			c.unsupported("gradient.unknown")
		}
	})
	if !foundGradient {
		c.invalid("gradient.stops")
	}
}

func (c *gradientConverter) gradient(descriptor *Descriptor) {
	if descriptor.ClassID != "Grdn" {
		c.unsupported("gradient.stops.class")
	}
	c.entries(descriptor, "gradient.stops", func(entry DescriptorEntry) {
		switch entry.Key {
		case "Nm  ":
			value, valid := entry.Value.(string)
			if entry.Type != "TEXT" || !valid {
				c.invalid("gradient.name")
				return
			}
			c.fill.Name = value
		case "GrdF":
			form := c.enumeration(entry, "GrdF", "gradient.form")
			if form != "CstS" {
				c.fill.Form = form
				c.unsupported("gradient.form")
			}
		case "Intr":
			c.fill.Smoothness = c.number(entry, "gradient.smoothness")
		case "Clrs":
			c.colors(c.list(entry, "gradient.colors"))
		case "Trns":
			c.opacities(c.list(entry, "gradient.opacities"))
		default:
			c.unsupported("gradient.stops.unknown")
		}
	})
	if c.fill.Form == "custom-stops" && len(c.fill.ColorStops) == 0 {
		c.invalid("gradient.colors")
	}
}

func (c *gradientConverter) colors(values []DescriptorValue) {
	if !c.cursor.reserve(int64(len(values))*int64(unsafe.Sizeof(psdgradient.ColorStop{})), "gradient.colors") {
		return
	}
	c.fill.ColorStops = make([]psdgradient.ColorStop, 0, len(values))
	for _, value := range values {
		descriptor, valid := value.Value.(*Descriptor)
		if value.Type != "Objc" || !valid || descriptor == nil {
			c.invalid("gradient.colors.stop")
			return
		}
		if descriptor.ClassID != "Clrt" {
			c.unsupported("gradient.colors.class")
		}
		stop := psdgradient.ColorStop{Midpoint: 50}
		foundColor := false
		c.entries(descriptor, "gradient.colors.stop", func(entry DescriptorEntry) {
			switch entry.Key {
			case "Clr ":
				foundColor = true
				stop.RGB = c.rgb(c.object(entry, "gradient.colors.rgb"))
			case "Lctn":
				stop.Location = uint32(c.integer(entry, 4096, "gradient.colors.location"))
			case "Mdpn":
				stop.Midpoint = uint16(c.integer(entry, 100, "gradient.colors.midpoint"))
			case "Type":
				if c.enumeration(entry, "Clry", "gradient.colors.type") != "UsrS" {
					c.unsupported("gradient.colors.type")
				}
			default:
				c.unsupported("gradient.colors.unknown")
			}
		})
		if !foundColor {
			c.invalid("gradient.colors.rgb")
		}
		c.fill.ColorStops = append(c.fill.ColorStops, stop)
	}
}

func (c *gradientConverter) opacities(values []DescriptorValue) {
	if !c.cursor.reserve(int64(len(values))*int64(unsafe.Sizeof(psdgradient.OpacityStop{})), "gradient.opacities") {
		return
	}
	c.fill.OpacityStops = make([]psdgradient.OpacityStop, 0, len(values))
	for _, value := range values {
		descriptor, valid := value.Value.(*Descriptor)
		if value.Type != "Objc" || !valid || descriptor == nil {
			c.invalid("gradient.opacities.stop")
			return
		}
		if descriptor.ClassID != "TrnS" {
			c.unsupported("gradient.opacities.class")
		}
		stop := psdgradient.OpacityStop{Midpoint: 50, Opacity: 1}
		c.entries(descriptor, "gradient.opacities.stop", func(entry DescriptorEntry) {
			switch entry.Key {
			case "Opct":
				stop.Opacity = c.unit(entry, "#Prc", "gradient.opacities.opacity") / 100
			case "Lctn":
				stop.Location = uint32(c.integer(entry, 4096, "gradient.opacities.location"))
			case "Mdpn":
				stop.Midpoint = uint16(c.integer(entry, 100, "gradient.opacities.midpoint"))
			default:
				c.unsupported("gradient.opacities.unknown")
			}
		})
		c.fill.OpacityStops = append(c.fill.OpacityStops, stop)
	}
}

func (c *gradientConverter) rgb(descriptor *Descriptor) (rgb [3]float64) {
	if descriptor == nil {
		return rgb
	}
	if descriptor.ClassID != "RGBC" {
		c.unsupported("gradient.colors.space")
		return rgb
	}
	seen := [3]bool{}
	c.entries(descriptor, "gradient.colors.rgb", func(entry DescriptorEntry) {
		for component, key := range []string{"Rd  ", "Grn ", "Bl  "} {
			if entry.Key == key {
				rgb[component], seen[component] = c.number(entry, "gradient.colors.component")/255, true
				return
			}
		}
		c.unsupported("gradient.colors.rgb.unknown")
	})
	if !seen[0] || !seen[1] || !seen[2] {
		c.invalid("gradient.colors.component")
	}
	return rgb
}

func (c *gradientConverter) offset(descriptor *Descriptor) {
	if descriptor == nil {
		return
	}
	if descriptor.ClassID != "Pnt " {
		c.unsupported("gradient.offset.class")
	}
	c.entries(descriptor, "gradient.offset", func(entry DescriptorEntry) {
		switch entry.Key {
		case "Hrzn":
			c.fill.Offset.X = c.unit(entry, "#Prc", "gradient.offset.x")
		case "Vrtc":
			c.fill.Offset.Y = c.unit(entry, "#Prc", "gradient.offset.y")
		default:
			c.unsupported("gradient.offset.unknown")
		}
	})
}

func (c *gradientConverter) object(entry DescriptorEntry, path string) *Descriptor {
	value, valid := entry.Value.(*Descriptor)
	if entry.Type != "Objc" || !valid || value == nil {
		c.invalid(path)
		return nil
	}
	return value
}

func (c *gradientConverter) list(entry DescriptorEntry, path string) []DescriptorValue {
	value, valid := entry.Value.([]DescriptorValue)
	if entry.Type != "VlLs" || !valid {
		c.invalid(path)
		return nil
	}
	if len(value) > 4096 {
		c.cursor.fail(ErrLimit, path, c.cursor.offset, "gradient stop count exceeds the typed metadata limit")
		return nil
	}
	return value
}

func (c *gradientConverter) boolean(entry DescriptorEntry, path string) bool {
	value, valid := entry.Value.(bool)
	if entry.Type != "bool" || !valid {
		c.invalid(path)
	}
	return value
}

func (c *gradientConverter) enumeration(entry DescriptorEntry, typeID, path string) string {
	value, valid := entry.Value.(DescriptorEnum)
	if entry.Type != "enum" || !valid || value.TypeID != typeID {
		c.invalid(path)
		return ""
	}
	return value.ValueID
}

func (c *gradientConverter) unit(entry DescriptorEntry, unit, path string) float64 {
	value, valid := entry.Value.(DescriptorUnit)
	if entry.Type != "UntF" && entry.Type != "UnFl" || !valid || value.Unit != unit {
		c.invalid(path)
		return 0
	}
	var result float64
	switch number := value.Number.(type) {
	case float32:
		result = float64(number)
	case float64:
		result = number
	default:
		c.invalid(path)
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		c.invalid(path)
	}
	return result
}

func (c *gradientConverter) number(entry DescriptorEntry, path string) float64 {
	value, valid := entry.Value.(float64)
	if entry.Type != "doub" || !valid || math.IsInf(value, 0) || math.IsNaN(value) {
		c.invalid(path)
		return 0
	}
	return value
}

func (c *gradientConverter) integer(entry DescriptorEntry, maximum int32, path string) int32 {
	value, valid := entry.Value.(int32)
	if entry.Type != "long" || !valid || value < 0 || value > maximum {
		c.invalid(path)
		return 0
	}
	return value
}
