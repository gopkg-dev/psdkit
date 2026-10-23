package psdedit

import "github.com/gopkg-dev/psdkit/psdfile"

// appendNewVectorMask puts new geometry before opaque original blocks whose trailing padding must stay last.
func appendNewVectorMask(plan *writePlan, node *editNode, original psdfile.LayerRecord) {
	change := node.vectorMaskChange
	if change == nil || change.removed {
		return
	}
	for _, block := range original.AdditionalInfo {
		if block.Key == "vmsk" || block.Key == "vsms" {
			return
		}
	}
	plan.bytes(writerAdditional("8BIM", "vmsk", 4, change.data))
}
