package psdfile

import "context"

// MetadataOptions lowers the allocation budget for one owned metadata view.
// Zero uses the source document's configured limit; larger values do not raise that limit.
type MetadataOptions struct {
	MaxBytes    int64
	reservation *int64
}

// SmartObjectView reports owned placements and their conservative decoding reservation.
type SmartObjectView struct {
	Records       []SmartObjectRecord
	MetadataBytes int64
}

// LinkedFileView reports owned linked-file records and their conservative decoding reservation.
type LinkedFileView struct {
	Records       []LinkedFileRecord
	MetadataBytes int64
}

// ReadSmartObjectView returns placements with a measured reservation for combined-operation budgeting.
func (d *Document) ReadSmartObjectView(ctx context.Context, options ...MetadataOptions) (SmartObjectView, error) {
	var result SmartObjectView
	option, err := metadataViewOption(options, &result.MetadataBytes)
	if err != nil {
		return result, err
	}
	result.Records, err = d.SmartObjects(ctx, option)
	return result, err
}

// ReadLinkedFileView returns linked records with a measured reservation for combined-operation budgeting.
func (d *Document) ReadLinkedFileView(ctx context.Context, options ...MetadataOptions) (LinkedFileView, error) {
	var result LinkedFileView
	option, err := metadataViewOption(options, &result.MetadataBytes)
	if err != nil {
		return result, err
	}
	result.Records, err = d.LinkedFiles(ctx, option)
	return result, err
}

func metadataViewOption(options []MetadataOptions, reservation *int64) (MetadataOptions, error) {
	if len(options) > 1 {
		return MetadataOptions{}, fileError(ErrMalformed, "metadata", "options", 0, "provide at most one metadata option")
	}
	var option MetadataOptions
	if len(options) == 1 {
		option = options[0]
	}
	option.reservation = reservation
	return option, nil
}

// IndexMetadataBytes reports the conservative retained allocation reservation from original indexing.
// It excludes encoded payloads and later decoded views, and remains available after Close.
func (d *Document) IndexMetadataBytes() int64 { return d.metadataBytes }

func (d *Document) metadataLimits(options []MetadataOptions) (Limits, error) {
	limits := d.limits
	if len(options) > 1 || len(options) == 1 && options[0].MaxBytes < 0 {
		return Limits{}, fileError(ErrMalformed, "metadata", "options", 0,
			"provide at most one nonnegative metadata budget")
	}
	if len(options) == 1 && options[0].MaxBytes != 0 {
		limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, options[0].MaxBytes)
	}
	return limits, nil
}
