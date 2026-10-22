package psdadjust

func compileChannelMixer(transform *Transform, parameters *ChannelMixerParams) error {
	if parameters == nil {
		return adjustmentError(ErrInvalid, "adjustment.channelMixer", 0, "channel mixer parameters are absent")
	}
	for _, row := range parameters.Rows {
		for _, coefficient := range row.Inputs {
			if coefficient < -200 || coefficient > 200 {
				return adjustmentError(ErrInvalid, "adjustment.channelMixer", 0, "channel coefficient exceeds -200 to 200")
			}
		}
		if row.Constant < -200 || row.Constant > 200 {
			return adjustmentError(ErrInvalid, "adjustment.channelMixer", 0, "channel constant exceeds -200 to 200")
		}
	}
	if parameters.Monochrome {
		for _, row := range parameters.Rows {
			if row.Inputs[3] != 0 {
				return adjustmentError(ErrUnsupported, "adjustment.channelMixer", 0,
					"CMYK black-input coefficients are unsupported")
			}
		}
		row := parameters.Rows[0]
		transform.monochrome = &[4]int16{row.Inputs[0], row.Inputs[1], row.Inputs[2], row.Constant}
		return nil
	}
	if parameters.Rows[3] != (MixerChannel{}) {
		return adjustmentError(ErrUnsupported, "adjustment.channelMixer", 0,
			"fourth-output channel semantics require separate verification")
	}
	for _, row := range parameters.Rows[:3] {
		if row.Inputs[3] != 0 {
			return adjustmentError(ErrUnsupported, "adjustment.channelMixer", 0, "CMYK black-input coefficients are unsupported")
		}
	}
	transform.mixer = new([3][4]float64)
	for output, row := range parameters.Rows[:3] {
		for input := range 3 {
			transform.mixer[output][input] = float64(row.Inputs[input]) / 100
		}
		transform.mixer[output][3] = float64(row.Constant) / 100
	}
	return nil
}
