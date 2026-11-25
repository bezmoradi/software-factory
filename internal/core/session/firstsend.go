package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	firstSendFor   = 10 * time.Minute
	firstSendEvery = 3 * time.Second
)

func clipUnits(s string, n int) string {
	r := []rune(s)
	for len(r) > 0 && utf16Len(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}

type firstSender struct {
	base   context.Context
	tell   func(sid, text string) error
	now    func() time.Time
	logger shared.Logger
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newFirstSender(base context.Context, tell func(sid, text string) error, now func() time.Time, logger shared.Logger) *firstSender {
	return &firstSender{base: base, tell: tell, now: now, logger: logger}
}

func (f *firstSender) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func (f *firstSender) wait() { f.wg.Wait() }

func (f *firstSender) send(sid, text, what string) {
	f.mu.Lock()
	if f.closed || f.tell == nil || text == "" {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	f.wg.Go(func() {
		until := f.now().Add(firstSendFor)
		t := time.NewTicker(firstSendEvery)
		defer t.Stop()
		for {
			select {
			case <-f.base.Done():
				return
			case <-t.C:
			}
			f.mu.Lock()
			closed := f.closed
			f.mu.Unlock()
			if closed {
				return
			}
			err := f.tell(sid, text)
			if err == nil {
				f.logger.Info("first message", "event", "sent", "what", what)
				return
			}
			if e, ok := errors.AsType[*Error](err); ok && e.Kind != Conflict || !f.now().Before(until) {
				f.logger.Warn("first message", "event", "not-sent", "what", what)
				return
			}
		}
	})
}
