package psdcompose

import "github.com/gopkg-dev/psdkit/psdmodel"

func (plan *composePlan) clippingSource(
	layer *psdmodel.Layer, backdrop composePixel, x, y int,
) composePixel {
	if layer.BlendMode != "pass" {
		return plan.source(layer, composePixel{}, x, y)
	}
	if !plan.enterLayerSource() {
		return composePixel{}
	}
	tracked := backdrop
	tracked.residual = 1
	full := plan.stack(layer.Children, tracked, x, y)
	shape := composeClamp(1 - full.residual)
	if !plan.options.ApproximateClippedGroupBackdrop || shape == 0 || shape == 1 {
		return passGroupIntrinsic(full, backdrop, shape)
	}
	isolated := plan.stack(layer.Children, composePixel{}, x, y)
	return passClipGroupIntrinsic(full, backdrop, isolated, shape)
}

// passClipGroupIntrinsic changes only the RGB reconstruction of an external clipping base.
// Hidden or opacity-zero clipped members retain their structural clipping membership.
func passClipGroupIntrinsic(full, backdrop, isolated composePixel, shape float64) composePixel {
	intrinsic := passGroupIntrinsic(full, backdrop, shape)
	if shape == 0 || shape == 1 {
		return intrinsic
	}
	remaining := backdrop.alpha * (1 - shape)
	denominator := 1 + remaining
	for component := range 3 {
		intrinsic.color[component] = composeClamp(
			(intrinsic.color[component] + isolated.color[component]*remaining) / denominator,
		)
	}
	return intrinsic
}
