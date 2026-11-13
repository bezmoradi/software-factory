package session

import "context"

func (d *Dispatcher) AgentReview(ctx context.Context, sid string) error {
	return d.reviewAs(ctx, sid, false, true)
}

func (d *Dispatcher) SessionSignIn(sid string) (SignIn, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil || r.signIn.Method == "" {
		return SignIn{}, false
	}
	return r.signIn, true
}
