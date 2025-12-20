package grid

// releasePlan clears the current plan pointer. If allowRetry is true,
// it also releases the per-level dedupe key so the level can re-trigger.
func (s *GridStrategy) releasePlan(allowRetry bool) {
	if s.plan == nil {
		return
	}
	if allowRetry && s.plan.LevelKey != "" && s.processedGridLevels != nil {
		delete(s.processedGridLevels, s.plan.LevelKey)
	}
	s.plan = nil
}

func (s *GridStrategy) planDone() {
	if s.plan == nil {
		return
	}
	s.plan.State = PlanDone
	s.releasePlan(false)
}

func (s *GridStrategy) planFailed(reason string, allowRetry bool) {
	if s.plan == nil {
		return
	}
	s.plan.State = PlanFailed
	s.plan.LastError = reason
	s.releasePlan(allowRetry)
}
