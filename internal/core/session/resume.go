package session

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	ResumeEvery = 15 * time.Second
	ResumeFor   = 10 * time.Minute

	stoppingReason  = "the engine is stopping, so it decides no push now; end your turn: the person can push from the page once the engine is back"
	stoppingMessage = "The engine is stopping. Try again once it's back."
)

type WaitingPlan struct {
	SID    string
	Commit string
	Since  time.Time
}

type WaitingStore interface {
	Load() ([]WaitingPlan, error)
	Save([]WaitingPlan) error
}

type queuedReview struct {
	sid    string
	commit string
	next   time.Time
	until  time.Time
}

type resumeState struct {
	since   map[string]time.Time
	mu      sync.Mutex
	queue   []queuedReview
	running bool
}

var transientStart = []string{"sign-in check", "fetch", "no plugin", "branch", "records", "merge base"}

func (p *Pushes) Close() {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
}

func (p *Pushes) stopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closing
}

type waitedFor struct {
	head string
	req  string
}

func (p *Pushes) waitedReq(sid, head string) string {
	if w, ok := p.waitReq[sid]; ok && w.head == head {
		return w.req
	}
	return ""
}

func (p *Pushes) wait(sid, head string) {
	p.waitAs(sid, head, "")
}

func (p *Pushes) waitAs(sid, head, req string) {
	p.mu.Lock()
	if shared.ValidRequest(req) {
		p.waitReq[sid] = waitedFor{head: head, req: req}
	} else if w, ok := p.waitReq[sid]; ok && w.head != head {
		delete(p.waitReq, sid)
	}
	if p.resume.since == nil {
		p.resume.since = map[string]time.Time{}
	}
	if p.waiting[sid] != head || p.resume.since[sid].IsZero() {
		p.resume.since[sid] = p.cfg.Now()
	}
	p.waiting[sid] = head
	p.mu.Unlock()
	p.saveWaiting()
}

func (p *Pushes) planned(sid, commit string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return commit != "" && p.waiting[sid] == commit
}

func (p *Pushes) decided(sid, head string, reply push.Reply) {
	p.mu.Lock()
	if p.closing && reply.Decision == push.DecisionRefused && p.waiting[sid] == "" {
		p.waiting[sid] = head
	}
	p.mu.Unlock()
	p.saveWaiting()
}

func (p *Pushes) loadWaiting() {
	if p.cfg.Waiting == nil {
		return
	}
	saved, err := p.cfg.Waiting.Load()
	if err != nil {
		p.cfg.Logger.Warn("push", "event", "waiting-not-read")
		return
	}
	p.resume.since = map[string]time.Time{}
	for _, w := range saved {
		if sidPattern.MatchString(w.SID) && w.Commit != "" {
			p.waiting[w.SID] = w.Commit
			p.resume.since[w.SID] = w.Since
		}
	}
}

func (p *Pushes) saveWaiting() {
	if p.cfg.Waiting == nil {
		return
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	out := make([]WaitingPlan, 0, len(p.waiting))
	for sid, commit := range p.waiting {
		out = append(out, WaitingPlan{SID: sid, Commit: commit, Since: p.resume.since[sid]})
	}
	for sid := range p.resume.since {
		if p.waiting[sid] == "" {
			delete(p.resume.since, sid)
		}
	}
	p.mu.Unlock()
	slices.SortFunc(out, func(a, b WaitingPlan) int { return strings.Compare(a.SID, b.SID) })
	if err := p.cfg.Waiting.Save(out); err != nil {
		p.cfg.Logger.Warn("push", "event", "waiting-not-saved")
	}
}

func (p *Pushes) Resume(_ context.Context) int {
	if p.halted(ByReviews) != "" {
		p.cfg.Logger.Info("push resume", "event", "held-by-halt")
		return 0
	}
	p.mu.Lock()
	plans := maps.Clone(p.waiting)
	p.mu.Unlock()
	queued := 0
	for _, sid := range slices.Sorted(maps.Keys(plans)) {
		commit := plans[sid]
		if why := p.stale(sid, commit); why != "" {
			p.drop(sid, commit)
			p.cfg.Logger.Info("push resume", "event", "dropped", "why", why)
			continue
		}
		p.queueReview(sid, commit)
		queued++
	}
	p.saveWaiting()
	if queued > 0 || len(plans) > 0 {
		p.cfg.Logger.Info("push resume", "event", "queued", "count", queued, "dropped", len(plans)-queued)
	}
	return queued
}

func (p *Pushes) stale(sid, commit string) string {
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return "session"
	}
	head, err := p.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return "branch"
	}
	if head != commit {
		return "moved"
	}
	p.mu.Lock()
	denied := p.refusedLocked(sid, commit)
	p.mu.Unlock()
	if denied {
		return "denied"
	}
	return ""
}

func (p *Pushes) drop(sid, commit string) {
	p.mu.Lock()
	if p.waiting[sid] == commit {
		delete(p.waiting, sid)
	}
	p.mu.Unlock()
}

func (p *Pushes) queueReview(sid, commit string) {
	p.wait(sid, commit)
	now := p.cfg.Now()
	q := &p.resume
	q.mu.Lock()
	q.queue = slices.DeleteFunc(q.queue, func(x queuedReview) bool { return x.sid == sid })
	q.queue = append(q.queue, queuedReview{sid: sid, commit: commit, next: now, until: now.Add(ResumeFor)})
	start := !q.running
	q.running = true
	q.mu.Unlock()
	if start {
		go p.work()
	}
}

func (p *Pushes) work() {
	ctx := p.cfg.Base
	q := &p.resume
	for {
		q.mu.Lock()
		if len(q.queue) == 0 || ctx.Err() != nil || p.stopping() || p.halted(ByReviews) != "" {
			q.running = false
			q.mu.Unlock()
			return
		}
		now := p.cfg.Now()
		i := 0
		for j, x := range q.queue {
			if x.next.Before(q.queue[i].next) {
				i = j
			}
		}
		item := q.queue[i]
		if wait := item.next.Sub(now); wait > 0 {
			q.mu.Unlock()
			select {
			case <-ctx.Done():
			case <-time.After(min(wait, ResumeEvery)):
			}
			continue
		}
		q.queue = slices.Delete(q.queue, i, i+1)
		q.mu.Unlock()
		if !p.resumeOne(ctx, item) {
			continue
		}
		item.next = p.cfg.Now().Add(ResumeEvery)
		q.mu.Lock()
		if !slices.ContainsFunc(q.queue, func(x queuedReview) bool { return x.sid == item.sid }) {
			q.queue = append(q.queue, item)
		}
		q.mu.Unlock()
	}
}

func (p *Pushes) resumeOne(ctx context.Context, item queuedReview) bool {
	sid, commit := item.sid, item.commit
	p.mu.Lock()
	ctx = shared.WithRequest(ctx, p.waitedReq(sid, commit))
	p.mu.Unlock()
	if why := p.stale(sid, commit); why != "" {
		p.drop(sid, commit)
		p.saveWaiting()
		shared.For(ctx, p.cfg.Logger).Info("push resume", "event", "dropped", "why", why)
		return false
	}
	p.mu.Lock()
	still := p.waiting[sid] == commit
	p.mu.Unlock()
	if !still || p.cfg.Reviews == nil {
		return false
	}
	t, _ := p.cfg.Targets(sid)
	entries, required, err := p.entriesAt(sid, t, commit)
	if err != nil {
		return p.cfg.Now().Before(item.until)
	}
	needs := false
	for _, q := range required {
		for _, k := range (review.Key{Commit: commit, Branch: t.Branch, Agent: q.Agent}).Runs(q.RunCount()) {
			e, ok := entries[k]
			switch {
			case !ok:
				needs = true
			case e.Record.State == review.Running:
				return false
			case Retryable(e), Rerunnable(e):
				needs = true
			}
		}
	}
	if !needs {
		shared.For(ctx, p.cfg.Logger).Info("push resume", "event", "deciding")
		p.ReviewDone(sid, commit)
		return false
	}
	start := p.cfg.RetryReview
	if start == nil {
		start = p.cfg.StartReview
	}
	if start == nil {
		p.ReviewDone(sid, commit)
		return false
	}
	err = start(ctx, sid)
	if err == nil {
		shared.For(ctx, p.cfg.Logger).Info("push resume", "event", "review-started")
		return false
	}
	e, ok := errors.AsType[*Error](err)
	switch {
	case ok && (e.Reason == "reviewing" || e.Reason == "halted"):
		return false
	case ok && e.Reason == "reviews":
		return true
	case (!ok || slices.Contains(transientStart, e.Reason)) && p.cfg.Now().Before(item.until):
		return true
	}
	why := "other"
	if ok {
		why = e.Reason
	}
	shared.For(ctx, p.cfg.Logger).Info("push resume", "event", "review-not-started", "why", why)
	p.ReviewDone(sid, commit)
	return false
}

func unfinishedReviews(head string, ended []ReviewEntry) string {
	if len(ended) == 0 {
		return ""
	}
	var parts, held []string
	for _, e := range ended {
		if e.Record.Held != "" {
			held = append(held, "the review of commit "+shortSHA(head)+" by "+e.Key.Agent+" passed, but "+clip(e.Record.Held, 400))
			continue
		}
		what := "failed"
		if e.Record.State == review.Unknown {
			what = "gave no verdict the engine reads"
		}
		part := "the review of commit " + shortSHA(head) + " by " + e.Key.Agent + " " + what
		if e.Reason != "" {
			part += " (" + clip(e.Reason, 300) + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) > 0 {
		parts = []string{strings.Join(parts, "; ") + "; the engine won't start " + plural(len(parts), "it", "them") + " again on your request, but the person can press Review on the page"}
	}
	return strings.Join(append(parts, held...), "; ")
}

func (p *Pushes) afterReviews(by, sid string, t PushTarget, head string) string {
	if by != ByReviews || p.cfg.Reviews == nil {
		return ""
	}
	entries, required, err := p.entriesAt(sid, t, head)
	if err != nil {
		return ""
	}
	var ended []ReviewEntry
	for _, q := range required {
		for _, k := range (review.Key{Commit: head, Branch: t.Branch, Agent: q.Agent}).Runs(q.RunCount()) {
			if e, ok := entries[k]; ok && (e.Record.State == review.Failed || e.Record.State == review.Unknown) {
				ended = append(ended, e)
			}
		}
	}
	if why := unfinishedReviews(head, ended); why != "" {
		return why + "; "
	}
	return ""
}
