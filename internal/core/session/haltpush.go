package session

func (p *Pushes) UseHalt(h *Halt) {
	p.halt.Store(h)
}

func (p *Pushes) pageHalted() error {
	if p.halted(ByPage) == "" {
		return nil
	}
	return explained(conflict("halted"), "Stop everything is on, so nothing is pushed until you press Release at the top of the page.")
}

func (p *Pushes) halted(by string) string {
	h := p.halt.Load()
	if h == nil {
		return ""
	}
	return h.pushes(by)
}

func (p *Pushes) holdForRelease(sid, head string) (string, bool, string) {
	p.wait(sid, head)
	p.cfg.Logger.Info("push", "event", "review-held-by-halt")
	if p.halted(ByReviews) == "" {
		p.queueReview(sid, head)
	}
	return "", false, "the person turned on Pause all or Stop everything on the page, so the engine starts the reviews of commit " + shortSHA(head) + " when they press Release, and pushes it itself if every review passes"
}
