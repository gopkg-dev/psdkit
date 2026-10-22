package psdadjust

func validatePhotoFilter(adjustment Adjustment) error {
	if adjustment.Version != 2 && adjustment.Version != 3 {
		return adjustmentError(ErrUnsupported, "adjustment.photoFilter.version", adjustment.Span.Offset,
			"photo filter requires an explicit version two or three")
	}
	if adjustment.ExtraVersion != 0 || adjustment.PhotoFilter == nil {
		return adjustmentError(ErrInvalid, "adjustment.photoFilter", adjustment.Span.Offset,
			"photo filter parameters must be present and extra version absent")
	}
	parameters := adjustment.PhotoFilter
	if parameters.Density > 100 {
		return adjustmentError(ErrInvalid, "adjustment.photoFilter.density", adjustment.Span.Offset,
			"photo filter density must be zero through 100")
	}
	if adjustment.Version == 2 && parameters.XYZWords != ([3]uint32{}) ||
		adjustment.Version == 3 && (parameters.ColorSpace != 0 || parameters.Components != ([4]uint16{})) {
		return adjustmentError(ErrInvalid, "adjustment.photoFilter.color", adjustment.Span.Offset,
			"photo filter color words must match the selected wire representation")
	}
	return nil
}
