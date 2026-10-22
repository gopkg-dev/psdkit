package psdkit

import (
	"io"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// SourceDocument indexes original PSD/PSB bytes and owns its source until Close.
// Decode methods are explicit; indexing does not allocate pixel images.
type SourceDocument = psdfile.Document

// LinkedFileRecord preserves an embedded, external or alias source record and its original spans.
type LinkedFileRecord = psdfile.LinkedFileRecord

// LinkedDocumentResolver supplies an externally linked PSD/PSB selected explicitly by the caller.
type LinkedDocumentResolver = psdfile.LinkedDocumentResolver

// SmartObjectRecord preserves modern placement metadata across source layer sets.
type SmartObjectRecord = psdfile.SmartObjectRecord

// DescriptorEncodeOptions bounds source-aware ordered descriptor encoding.
type DescriptorEncodeOptions = psdfile.DescriptorEncodeOptions

// LoadOptions configures metadata indexing and on-demand decoding limits.
type LoadOptions = psdfile.LoadOptions

// SourceLimits bounds files, metadata, raw reads and explicit pixel decoding.
type SourceLimits = psdfile.Limits

// MetadataOptions lowers the working budget for an owned placement or linked-content view.
type MetadataOptions = psdfile.MetadataOptions

// SmartObjectView reports owned placements and their measured metadata reservation.
type SmartObjectView = psdfile.SmartObjectView

// LinkedFileView reports owned source records and their measured metadata reservation.
type LinkedFileView = psdfile.LinkedFileView

// LinkedRasterOptions bounds embedded PNG/JPEG and flat RGB8 TIFF decoding and owned ICC storage.
type LinkedRasterOptions = psdfile.LinkedRasterOptions

// LinkedRaster contains owned RGB8 pixels in the original embedded ICC encoding.
type LinkedRaster = psdfile.LinkedRaster

// TIFFLayerSource borrows a Photoshop-private TIFF's saved layer channels and normalized metadata.
type TIFFLayerSource = psdfile.TIFFLayerSource

// TIFFLayerSourceInfo records private TIFF geometry, original spans and metadata reservation.
type TIFFLayerSourceInfo = psdfile.TIFFLayerSourceInfo

// TIFFNormalizationRange identifies typed byte-order reversals at original source offsets.
type TIFFNormalizationRange = psdfile.TIFFNormalizationRange

// TIFFOpaqueBlock retains a private block with unavailable normalized semantics.
type TIFFOpaqueBlock = psdfile.TIFFOpaqueBlock

// TIFFSavedPreview labels the original flattened main IFD image independently of private layer samples.
type TIFFSavedPreview = psdfile.TIFFSavedPreview

// FilterCacheArray indexes deferred samples or a retained filter mask in their original precision.
type FilterCacheArray = psdfile.FilterCacheArray

// FilterCacheRecord indexes preserved FEid/FXid data without supplying live filter output.
type FilterCacheRecord = psdfile.FilterCacheRecord

// FilterCacheView reports owned cache indexes and their metadata reservation.
type FilterCacheView = psdfile.FilterCacheView

// RegionOptions bounds selected-region storage and complete compressed-stream validation work.
type RegionOptions = psdfile.RegionOptions

// FileHeader preserves version, channels, source dimensions, depth and color mode.
type FileHeader = psdfile.Header

// RawSpan identifies an unchanged interval of original source bytes.
type RawSpan = psdfile.Span

// ResourceBlock describes an original image resource with its name, data and padding.
type ResourceBlock = psdfile.ResourceBlock

// AdditionalBlock describes an original layer or document extension block.
type AdditionalBlock = psdfile.AdditionalBlock

// LayerRecord describes a layer in physical file order, including all encoded channels.
type LayerRecord = psdfile.LayerRecord

// LayerMetadataView reports owned layer semantics and bounded parser storage without decoding pixels.
type LayerMetadataView = psdfile.LayerMetadataView

// ChannelRecord indexes encoded layer samples and their compression.
type ChannelRecord = psdfile.ChannelRecord

// LayerSet indexes the main or alternate layer-information payload.
type LayerSet = psdfile.LayerSet

// Plane contains owned source-precision channel samples.
type Plane = psdfile.Plane

// LayerPixels contains decoded channels in their original depth and geometry.
type LayerPixels = psdfile.LayerPixels

// CompositePixels contains the decoded saved-image planes without color conversion.
type CompositePixels = psdfile.CompositePixels

// ReadError describes a source feature, field path, byte offset and underlying failure.
type ReadError = psdfile.Error

// Descriptor preserves ordered typed Action descriptor entries and their original source spans.
type Descriptor = psdfile.Descriptor

// DescriptorEntry preserves one descriptor key, OSType and typed value without deduplicating keys.
type DescriptorEntry = psdfile.DescriptorEntry

// DescriptorValue represents a typed list or reference item.
type DescriptorValue = psdfile.DescriptorValue

// DescriptorBlob indexes a descriptor payload without copying its bytes.
type DescriptorBlob = psdfile.DescriptorBlob

// DescriptorClass retains the original class name and identifier encoding.
type DescriptorClass = psdfile.DescriptorClass

// DescriptorEnum retains an enumeration's type and value identifiers.
type DescriptorEnum = psdfile.DescriptorEnum

// DescriptorUnit retains a scalar unit number, width and exact IEEE bits.
type DescriptorUnit = psdfile.DescriptorUnit

// DescriptorReference contains ordered typed Action reference items.
type DescriptorReference = psdfile.DescriptorReference

// DescriptorProperty associates a property identifier with its class.
type DescriptorProperty = psdfile.DescriptorProperty

// DescriptorEnumeratedReference retains the class and enumeration of a reference.
type DescriptorEnumeratedReference = psdfile.DescriptorEnumeratedReference

// DescriptorOffset retains a class-relative reference offset.
type DescriptorOffset = psdfile.DescriptorOffset

// DescriptorNamedReference retains a class and its referenced object name.
type DescriptorNamedReference = psdfile.DescriptorNamedReference

// DescriptorObjectArray retains the original ObAr prefix and ordered columns.
type DescriptorObjectArray = psdfile.DescriptorObjectArray

// DescriptorUnitArray retains an ObAr column's units and source-precision values.
type DescriptorUnitArray = psdfile.DescriptorUnitArray

var (
	// ErrClosed identifies a closed SourceDocument.
	ErrClosed = psdfile.ErrClosed
	// ErrMalformed identifies invalid or truncated PSD/PSB data.
	ErrMalformed = psdfile.ErrMalformed
	// ErrLimit identifies a configured source or decode budget violation.
	ErrLimit = psdfile.ErrLimit
	// ErrUnsupportedDecode identifies unsupported source-plane or descriptor decoding.
	ErrUnsupportedDecode = psdfile.ErrUnsupported
)

// DefaultSourceLimits returns the default metadata and decode budgets.
func DefaultSourceLimits() SourceLimits { return psdfile.DefaultLimits() }

// OpenDocument indexes a PSD/PSB file and keeps it open until SourceDocument.Close.
// The optional LoadOptions configures resource limits; pixels remain encoded until requested.
func OpenDocument(filename string, options ...LoadOptions) (*SourceDocument, error) {
	return psdfile.Open(filename, options...)
}

// ParseDocument indexes and retains PSD/PSB bytes without copying their encoded payloads.
// Callers must keep the supplied bytes unchanged until SourceDocument.Close.
func ParseDocument(data []byte, options ...LoadOptions) (*SourceDocument, error) {
	return psdfile.Parse(data, options...)
}

// ReadDocument indexes a borrowed ReaderAt; closing the document does not close reader.
// The caller keeps reader alive and immutable until all document operations finish.
func ReadDocument(reader io.ReaderAt, size int64, options ...LoadOptions) (*SourceDocument, error) {
	return psdfile.Read(reader, size, options...)
}
