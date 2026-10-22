package psdcompose

func (plan *composePlan) renderLive() bool {
	if plan.renderFailure != nil {
		return false
	}
	if plan.renderContext != nil {
		// Ordinary samples can observe cancellation without locking cancelCtx.Err for every layer.
		// Recursive clipping keeps its existing per-evaluation error polling and work accounting.
		if !plan.options.ApproximateClippedGroupBackdrop {
			select {
			case <-plan.renderContext.Done():
			default:
				return true
			}
		}
		if err := plan.renderContext.Err(); err != nil {
			plan.renderFailure = err
			return false
		}
	}
	return true
}

func (plan *composePlan) enterLayerSource() bool {
	if !plan.renderLive() {
		return false
	}
	if !plan.options.ApproximateClippedGroupBackdrop {
		return true
	}
	if plan.pixelEvaluations >= plan.options.MaxClippedGroupEvaluations {
		plan.renderFailure = composeError(ErrLimit, "document.clippedBackdropWork",
			"per-pixel source evaluations exceed MaxClippedGroupEvaluations")
		return false
	}
	plan.pixelEvaluations++
	return true
}
