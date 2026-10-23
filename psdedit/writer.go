package psdedit

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// Write writes a complete PSD/PSB while keeping edits stable for the whole operation.
// Successful output marks the current revision saved; a partial or failed write does not.
// Changed 32-bit layer channels requested as ZIP use ZIP prediction for Photoshop compatibility.
func (e *Editor) Write(ctx context.Context, writer io.Writer, options ...SaveOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	option, err := e.saveOptionsLocked(options)
	if err != nil {
		return err
	}
	if writer == nil {
		return editError(ErrInvalidEdit, "save.writer", "missing output writer")
	}
	if err := e.writeLocked(ctx, writer, option); err != nil {
		return err
	}
	e.savedRevision = e.revision
	return nil
}

// WritePSD writes a PSD/PSB document using the same preservation and revision rules as Write.
func (e *Editor) WritePSD(ctx context.Context, writer io.Writer, options ...SaveOptions) error {
	return e.Write(ctx, writer, options...)
}

// Save writes, syncs and closes a same-directory temporary file before atomic replacement.
// Any failure before replacement leaves an existing destination intact and removes the temporary file.
func (e *Editor) Save(ctx context.Context, filename string, options ...SaveOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	option, err := e.saveOptionsLocked(options)
	if err != nil {
		return err
	}
	if filename == "" {
		return editError(ErrInvalidEdit, "save.filename", "missing destination filename")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	permission := os.FileMode(0644)
	info, err := os.Lstat(filename)
	if err == nil {
		if !info.Mode().IsRegular() {
			return editError(ErrInvalidEdit, "save.filename", "destination must be a regular file")
		}
		permission = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".psdkit-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(permission); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := e.writeLocked(ctx, file, option); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceSavedFile(temporary, filename); err != nil {
		return err
	}
	e.savedRevision = e.revision
	return nil
}

// SavePSD atomically saves a PSD/PSB document using the same rules as Save.
func (e *Editor) SavePSD(ctx context.Context, filename string, options ...SaveOptions) error {
	return e.Save(ctx, filename, options...)
}

func (e *Editor) saveOptionsLocked(options []SaveOptions) (SaveOptions, error) {
	if err := e.checkOpenLocked(); err != nil {
		return SaveOptions{}, err
	}
	if len(options) > 1 {
		return SaveOptions{}, editError(ErrInvalidEdit, "save.options", "provide at most one SaveOptions")
	}
	option := SaveOptions{}
	if len(options) == 1 {
		option = options[0]
	}
	if option.MaxOutputBytes < 0 {
		return SaveOptions{}, editError(psdfile.ErrLimit, "save.MaxOutputBytes", "limit must not be negative")
	}
	if option.MaxOutputBytes == 0 {
		option.MaxOutputBytes = e.source.Limits().MaxFileBytes
	}
	if option.Compression > 3 || (option.Composite != nil && option.CompositeImage != nil) {
		return SaveOptions{}, editError(ErrInvalidEdit, "save.options", "invalid compression or conflicting previews")
	}
	return option, nil
}

func (e *Editor) writeLocked(ctx context.Context, writer io.Writer, option SaveOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.revision == 0 && option.Composite == nil && option.CompositeImage == nil {
		if e.source.Source.Length > option.MaxOutputBytes {
			return editError(psdfile.ErrLimit, "save.output", "document exceeds MaxOutputBytes")
		}
		_, err := e.source.CopySpan(ctx, writer, e.source.Source)
		if err != nil {
			return err
		}
		return ctx.Err()
	}
	if err := e.validateArtboardStructureLocked(ctx); err != nil {
		return err
	}
	plan, err := e.prepareWriteLocked(ctx, option)
	if err != nil {
		return err
	}
	var scratch []byte
	for _, part := range plan.parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if part.data != nil {
			if err := writeBytes(ctx, writer, part.data); err != nil {
				return err
			}
			continue
		}
		if scratch == nil {
			scratch = make([]byte, 64<<10)
		}
		if _, err := e.source.CopySpanBuffer(ctx, writer, part.span, scratch); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type writePart struct {
	data    []byte
	span    psdfile.Span
	content bool
}

// writePlan represents owned changed bytes and borrowed unchanged spans, never a whole-file buffer.
type writePlan struct {
	parts []writePart
	size  int64
	limit int64
	err   error
}

func (p *writePlan) addPart(part writePart) {
	if p.err != nil {
		return
	}
	length := part.span.Length
	if part.data != nil {
		length = int64(len(part.data))
	}
	if length < 0 || length > p.limit-p.size {
		p.err = editError(psdfile.ErrLimit, "save.output", "document exceeds MaxOutputBytes")
		return
	}
	if length != 0 {
		p.parts = append(p.parts, part)
		p.size += length
	}
}

func (p *writePlan) bytes(data []byte)          { p.addPart(writePart{data: data}) }
func (p *writePlan) original(span psdfile.Span) { p.addPart(writePart{span: span}) }

func (p *writePlan) append(child writePlan) {
	if child.err != nil {
		p.err = child.err
		return
	}
	for _, part := range child.parts {
		p.addPart(part)
	}
}

func (p *writePlan) length(width uint8, length int64, path string) {
	if length < 0 || (width == 4 && uint64(length) > math.MaxUint32) || (width != 4 && width != 8) {
		p.err = editError(psdfile.ErrLimit, path, "section length exceeds its format width")
		return
	}
	if width == 8 {
		p.bytes(binary.BigEndian.AppendUint64(nil, uint64(length)))
		return
	}
	p.bytes(binary.BigEndian.AppendUint32(nil, uint32(length)))
}

func writeBytes(ctx context.Context, writer io.Writer, data []byte) error {
	for len(data) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		length := min(len(data), 64<<10)
		count, err := writer.Write(data[:length])
		if err != nil {
			return err
		}
		if count != length {
			return io.ErrShortWrite
		}
		data = data[length:]
	}
	return nil
}

func versionLengthWidth(version uint16) uint8 {
	if version == 2 {
		return 8
	}
	return 4
}
