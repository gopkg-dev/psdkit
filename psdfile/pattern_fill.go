package psdfile

import (
	"context"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

// PatternFill retains an owned PtFl descriptor and its opaque local pattern reference.
// Source spans borrow the document lifetime; missing local patterns never trigger preset or filesystem lookups.
type PatternFill struct {
	Fill       *psdpattern.Fill
	Descriptor *Descriptor
	Block      AdditionalBlock
	Tail       Span
}

// DecodePatternFill explicitly reads ordered PtFl metadata under source and allocation budgets.
// Unknown fields remain in Descriptor and return a retained view plus ErrUnsupported.
func (d *Document) DecodePatternFill(
	ctx context.Context, block AdditionalBlock, options ...MetadataOptions,
) (*PatternFill, error) {
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
	if block.Key != "PtFl" || !d.validSpan(block.Data) {
		return nil, fileError(ErrMalformed, "patternFill", "patternFill.span", block.Data.Offset,
			"block key or span does not identify a source pattern fill")
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: limits}
	cursor := descriptorCursor{document: view, offset: block.Data.Offset, end: block.Data.Offset + block.Data.Length}
	if !cursor.reserve(int64(unsafe.Sizeof(PatternFill{}))+int64(unsafe.Sizeof(psdpattern.Fill{}))+256, "patternFill") {
		return nil, cursor.err
	}
	version := cursor.u32("patternFill.version")
	if cursor.err != nil {
		return nil, patternReadError(ctx, cursor.err)
	}
	if version != 16 {
		return nil, fileError(ErrUnsupported, "patternFill", "patternFill.version", block.Data.Offset,
			"unknown pattern fill descriptor version")
	}
	descriptor := cursor.descriptor(1, "patternFill", false)
	if cursor.err != nil {
		return nil, patternReadError(ctx, cursor.err)
	}
	descriptor.Version, descriptor.VersionData = version, Span{Offset: block.Data.Offset, Length: 4}
	descriptor.Span = Span{Offset: block.Data.Offset, Length: cursor.offset - block.Data.Offset}
	result := &PatternFill{Descriptor: descriptor, Block: block,
		Tail: Span{Offset: cursor.offset, Length: cursor.end - cursor.offset},
		Fill: &psdpattern.Fill{Scale: 100, Link: true}}
	conversion := gradientConverter{cursor: &cursor, fill: &psdgradient.Fill{}}
	linkPresent := false
	if descriptor.ClassID != "null" && descriptor.ClassID != "patternLayer" {
		conversion.unsupported("patternFill.class")
	}
	conversion.entries(descriptor, "patternFill", func(entry DescriptorEntry) {
		switch entry.Key {
		case "Ptrn":
			pattern := conversion.object(entry, "patternFill.pattern")
			if pattern == nil {
				return
			}
			if pattern.ClassID != "Ptrn" {
				conversion.unsupported("patternFill.pattern.class")
			}
			conversion.entries(pattern, "patternFill.pattern", func(item DescriptorEntry) {
				if item.Key != "Idnt" && item.Key != "Nm  " {
					conversion.unsupported("patternFill.pattern.unknown")
					return
				}
				value, valid := item.Value.(string)
				if item.Type != "TEXT" || !valid {
					conversion.invalid("patternFill.pattern.text")
					return
				}
				switch item.Key {
				case "Idnt":
					result.Fill.ID = value
				case "Nm  ":
					result.Fill.Name = value
				default:
					conversion.unsupported("patternFill.pattern.unknown")
				}
			})
		case "Scl ":
			result.Fill.Scale = conversion.unit(entry, "#Prc", "patternFill.scale")
		case "Angl":
			result.Fill.Angle = conversion.unit(entry, "#Ang", "patternFill.angle")
		case "Lnkd", "Algn":
			if linkPresent {
				conversion.unsupported("patternFill.link.duplicate")
			}
			linkPresent = true
			result.Fill.Link = conversion.boolean(entry, "patternFill.link")
		case "phase":
			phase := conversion.object(entry, "patternFill.phase")
			if phase == nil {
				return
			}
			if phase.ClassID != "Pnt " {
				conversion.unsupported("patternFill.phase.class")
			}
			conversion.entries(phase, "patternFill.phase", func(item DescriptorEntry) {
				if item.Key != "Hrzn" && item.Key != "Vrtc" {
					conversion.unsupported("patternFill.phase.unknown")
					return
				}
				var value float64
				if item.Type == "doub" {
					value = conversion.number(item, "patternFill.phase")
				} else {
					value = conversion.unit(item, "#Pxl", "patternFill.phase")
				}
				switch item.Key {
				case "Hrzn":
					result.Fill.Phase.X = value
				case "Vrtc":
					result.Fill.Phase.Y = value
				default:
					conversion.unsupported("patternFill.phase.unknown")
				}
			})
		default:
			conversion.unsupported("patternFill.unknown")
		}
	})
	if cursor.err != nil {
		return result, patternReadError(ctx, cursor.err)
	}
	if result.Tail.Length > 3 {
		conversion.unsupported("patternFill.tail")
	} else if result.Tail.Length != 0 {
		padding := cursor.bytes(result.Tail.Length, "patternFill.tail")
		for _, value := range padding {
			if value != 0 {
				conversion.unsupported("patternFill.tail")
				break
			}
		}
	}
	if cursor.err != nil {
		return result, patternReadError(ctx, cursor.err)
	}
	if result.Fill.Scale <= 0 || math.IsNaN(result.Fill.Scale) || math.IsInf(result.Fill.Scale, 0) {
		return result, fileError(ErrMalformed, "patternFill", "patternFill.scale", block.Data.Offset,
			"pattern scale is not positive and finite")
	}
	result.Fill.Unsupported = conversion.fill.Unsupported
	if result.Fill.ID == "" {
		result.Fill.Unsupported = append(result.Fill.Unsupported, "patternFill.reference")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(result.Fill.Unsupported) != 0 {
		return result, fileError(ErrUnsupported, "patternFill", "patternFill", block.Data.Offset,
			"pattern fill semantics are preserved but unresolved or unknown")
	}
	return result, nil
}

// ResolvePattern resolves one opaque PtFl reference only against retained local pattern records.
// Missing and duplicate references return ErrUnsupported without searching external pattern libraries.
func (d *Document) ResolvePattern(
	ctx context.Context, fill psdpattern.Fill, options ...MetadataOptions,
) (*PatternRecord, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if fill.ID == "" || len(fill.Unsupported) != 0 {
		return nil, fileError(ErrUnsupported, "patterns", "patterns.reference", 0, "pattern reference is unresolved")
	}
	records, err := d.Patterns(ctx, options...)
	if err != nil {
		return nil, err
	}
	var result *PatternRecord
	for index := range records {
		if records[index].ID == fill.ID {
			if result != nil {
				return nil, fileError(ErrUnsupported, "patterns", "patterns.reference", 0,
					"duplicate pattern reference is ambiguous")
			}
			result = &records[index]
		}
	}
	if result == nil {
		return nil, fileError(ErrUnsupported, "patterns", "patterns.reference", 0,
			"referenced pattern is absent from retained local source blocks")
	}
	return result, nil
}
