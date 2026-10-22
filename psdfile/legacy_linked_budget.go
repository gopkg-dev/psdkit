package psdfile

import "context"

// rgb8LinkedMetadataBytesLocked reads only linked-name structure lengths, never names or content.
func (d *Document) rgb8LinkedMetadataBytesLocked(ctx context.Context, budget int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if budget < 0 {
		return 0, fileError(ErrLimit, "rgba8", "linkedNames.metadata", 0, "no linked-name budget remains")
	}
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: d.limits}
	var total int64
	visit := func(blocks []AdditionalBlock) error {
		for _, block := range blocks {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch block.Key {
			case "lnkD", "lnk2", "lnk3", "lnkE":
			default:
				continue
			}
			if !d.validSpan(block.Data) {
				return fileError(ErrMalformed, "rgba8", "linkedNames.block", block.Data.Offset,
					"linked-name block lies outside the source")
			}
			cursor := indexCursor{document: view, position: block.Data.Offset,
				end: block.Data.Offset + block.Data.Length, path: "linkedNames"}
			// The compatibility parser accepts at most eight unconsumed trailing bytes.
			for cursor.remaining() > 8 && cursor.err == nil {
				length := cursor.length(8, "record.length")
				body := cursor.take(length, "record")
				if cursor.err != nil {
					break
				}
				entry := cursor.child(body, "linkedNames.record")
				kind, version := entry.signature("kind"), entry.u32("version")
				if entry.err == nil && ((kind != "liFD" && kind != "liFE" && kind != "liFA") ||
					version < 1 || version > 7) {
					return fileError(ErrUnsupported, "rgba8", "linkedNames.version", body.Offset,
						"unknown linked-name kind or schema version")
				}
				idLength := int64(entry.u8("id.length"))
				entry.take(idLength, "id")
				units := int64(entry.u32("name.units"))
				if entry.err == nil && units > 1<<20 {
					return fileError(ErrMalformed, "rgba8", "linkedNames.name", entry.position-4,
						"Unicode linked name exceeds the compatibility parser's unit limit")
				}
				entry.take(units*2, "name")
				if entry.err != nil {
					cursor.err = entry.err
					break
				}
				// Charge encoded UTF-16, uint16/rune decoding arrays, worst-case UTF-8 and a map entry.
				reservation := units*12 + idLength*2 + 256
				if reservation > budget-total {
					return fileError(ErrLimit, "rgba8", "linkedNames.metadata", body.Offset,
						"owned linked names exceed the snapshot metadata budget")
				}
				total += reservation
				padding := (4 - length%4) % 4
				if padding <= cursor.remaining() {
					cursor.take(padding, "record.padding")
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if cursor.err != nil {
				return cursor.err
			}
		}
		return nil
	}
	if err := visit(d.AdditionalInfo); err != nil {
		return 0, err
	}
	for _, layer := range d.Layers {
		if err := visit(layer.AdditionalInfo); err != nil {
			return 0, err
		}
	}
	return total, nil
}
