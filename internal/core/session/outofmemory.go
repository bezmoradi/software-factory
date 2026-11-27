package session

import (
	"context"
	"strconv"
	"time"
)

const (
	memoryFor      = 5 * time.Second
	memoryLargest  = 1 << 50
	memorySetting  = "SF_BOX_MEMORY"
	outOfMemoryTag = "stopped: out of memory"
)

type MemoryEvents struct {
	OOMKills int64
	Limit    int64
}

type MemoryHost interface {
	MemoryEvents(ctx context.Context, box string) (MemoryEvents, error)
}

func (d *Dispatcher) ranOutOfMemory(ctx context.Context, host Host, box string) (MemoryEvents, bool) {
	h, ok := host.(MemoryHost)
	if !ok {
		return MemoryEvents{}, false
	}
	mctx, cancel := context.WithTimeout(ctx, memoryFor)
	defer cancel()
	m, err := h.MemoryEvents(mctx, box)
	if err != nil || m.OOMKills <= 0 {
		return MemoryEvents{}, false
	}
	if m.Limit <= 0 || m.Limit > memoryLargest {
		m.Limit = 0
	}
	return m, true
}

func (r *Registry) EndedOutOfMemory(sid string, limit int64) {
	r.mu.Lock()
	if a := r.admitted[sid]; a != nil {
		a.outOfMemory, a.memoryLimit = true, limit
	}
	r.mu.Unlock()
	r.Ended(sid, false)
}

func OutOfMemoryNote(limit int64) string {
	size := memorySetting + " sets its limit"
	if limit > 0 {
		size = memorySize(limit) + "; " + memorySetting + " sets it"
	}
	return "Claude Code was stopped because the session's container ran out of memory (" + size + "). Wake it to go on."
}

func memorySize(n int64) string {
	const mib, gib = 1 << 20, 1 << 30
	switch {
	case n >= gib && n%gib == 0:
		return strconv.FormatInt(n/gib, 10) + " GiB"
	case n >= gib:
		return strconv.FormatFloat(float64(n)/gib, 'f', 1, 64) + " GiB"
	}
	return strconv.FormatInt((n+mib/2)/mib, 10) + " MiB"
}
