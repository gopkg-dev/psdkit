package psdedit

func (node *editNode) setSemanticCost(feature string, cost int64) {
	if node.semanticCosts == nil {
		node.semanticCosts = make(map[string]int64)
	}
	node.semanticBytes += cost - node.semanticCosts[feature]
	node.semanticCosts[feature] = cost
}
