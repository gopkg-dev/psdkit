package psdedit

import "github.com/gopkg-dev/psdkit/psdfile"

type linkedScope struct {
	blocks          []psdfile.AdditionalBlock
	layerSet, layer int
}

func sameLinkedBlock(first, second psdfile.AdditionalBlock) bool {
	return first.Span == second.Span && first.Data == second.Data
}

func (scope linkedScope) contains(block psdfile.AdditionalBlock) bool {
	for _, candidate := range scope.blocks {
		if sameLinkedBlock(candidate, block) {
			return true
		}
	}
	return false
}

func linkedContainerScope(source *psdfile.Document, block psdfile.AdditionalBlock) (linkedScope, bool) {
	scope := linkedScope{blocks: source.AdditionalInfo, layerSet: -1, layer: -1}
	if scope.contains(block) {
		return scope, true
	}
	for setIndex, set := range source.LayerSets {
		for layerIndex, layer := range set.Layers {
			scope = linkedScope{blocks: layer.AdditionalInfo, layerSet: setIndex, layer: layerIndex}
			if scope.contains(block) {
				return scope, true
			}
		}
	}
	return linkedScope{}, false
}

func (e *Editor) linkedRecordMigratesLocked(record psdfile.LinkedFileRecord) bool {
	overlay := e.linkedOverlays[record.ID]
	return record.Block.Key == "lnkE" && record.Kind == "liFE" && overlay != nil && overlay.embedExternal
}

// linkedMigrationTargetLocked chooses an existing lnk2 in the same scope, or the first moving lnkE.
// The latter anchors a new lnk2, so several sources never generate duplicate destination containers.
func (e *Editor) linkedMigrationTargetLocked(scope linkedScope) (psdfile.AdditionalBlock, bool) {
	for _, block := range scope.blocks {
		if block.Key == "lnk2" {
			return block, true
		}
	}
	for _, block := range scope.blocks {
		if block.Key != "lnkE" {
			continue
		}
		for _, record := range e.linkedRecords {
			if sameLinkedBlock(record.Block, block) && e.linkedRecordMigratesLocked(record) {
				return block, true
			}
		}
	}
	return psdfile.AdditionalBlock{}, false
}

func (e *Editor) linkedBlockReceivesMovedLocked(block psdfile.AdditionalBlock) bool {
	if block.Key != "lnk2" && block.Key != "lnkE" {
		return false
	}
	scope, ok := linkedContainerScope(e.source, block)
	if !ok {
		return false
	}
	target, ok := e.linkedMigrationTargetLocked(scope)
	if !ok || !sameLinkedBlock(target, block) {
		return false
	}
	for _, record := range e.linkedRecords {
		if e.linkedRecordMigratesLocked(record) && scope.contains(record.Block) {
			return true
		}
	}
	return false
}

func linkedLayerRetained(nodes []*editNode, index int) bool {
	for _, node := range nodes {
		if node.sourceIndex == index || node.endSourceIndex == index || linkedLayerRetained(node.children, index) {
			return true
		}
	}
	return false
}

// writeOrphanedLinkedBlocksLocked retains sources owned by deleted layers in document-level containers.
// Other surviving instances can still reference them, even when no contents replacement was requested.
func (e *Editor) writeOrphanedLinkedBlocksLocked(limit int64) (writePlan, error) {
	plan := writePlan{limit: limit}
	if !e.structureChanged {
		return plan, nil
	}
	for index, layer := range e.source.Layers {
		for _, block := range layer.AdditionalInfo {
			switch block.Key {
			case "lnkD", "lnk2", "lnk3", "lnkE":
				if linkedLayerRetained(e.roots, index) {
					continue
				}
				part, err := e.writeLinkedBlockLocked(block, true, limit)
				if err != nil {
					return plan, err
				}
				plan.append(part)
			}
		}
	}
	return plan, plan.err
}
