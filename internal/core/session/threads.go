package session

import (
	"context"
	"errors"
	"regexp"
)

var ThreadPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var ErrSteerRefused = errors.New("the agent's turn ended before the message reached it")

type ThreadedSession interface {
	ThreadID() string
}

type SteeringSession interface {
	Steer(ctx context.Context, text string, parts []Attachment) error
}

func savedThread(id string) string {
	if ThreadPattern.MatchString(id) {
		return id
	}
	return ""
}

func (d *Dispatcher) threadOf(r *run) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return r.thread
}

func (d *Dispatcher) keepThread(r *run, sess any) {
	t, ok := sess.(ThreadedSession)
	if !ok {
		return
	}
	id := savedThread(t.ThreadID())
	d.mu.Lock()
	changed := id != "" && id != r.thread
	if changed {
		r.thread = id
	}
	d.mu.Unlock()
	if changed {
		d.saveRuns()
	}
}
