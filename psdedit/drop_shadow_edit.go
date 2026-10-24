package psdedit

import "github.com/gopkg-dev/psdkit/psdeffects"

// effectsPureDropShadow keeps approximation scoped to active DropShadow instances.
func effectsPureDropShadow(set psdeffects.Set) bool {
	active := false
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		if effect.Kind != psdeffects.DropShadow {
			return false
		}
		active = true
	}
	return active
}

// effectsShadowOverlay admits the explicitly approximate raster shadow/interior-overlay combination.
func effectsShadowOverlay(set psdeffects.Set) bool {
	shadow, overlay := false, false
	for _, effect := range set.Items {
		if !effect.Enabled || effect.Opacity == 0 {
			continue
		}
		switch effect.Kind {
		case psdeffects.DropShadow:
			shadow = true
		case psdeffects.ColorOverlay:
			overlay = true
		default:
			return false
		}
	}
	return shadow && overlay
}
