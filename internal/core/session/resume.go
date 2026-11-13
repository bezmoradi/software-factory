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

func (p *Pushes) wait(sid, head string) {
	p.mu.Lock()
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
	denied := p.refused[sid] == commit
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
		if len(q.queue) == 0 || ctx.Err() != nil || p.stopping() {
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
	if why := p.stale(sid, commit); why != "" {
		p.drop(sid, commit)
		p.saveWaiting()
		p.cfg.Logger.Info("push resume", "event", "dropped", "why", why)
		return false
	}
	p.mu.Lock()
	still := p.waiting[sid] == commit
	p.mu.Unlock()
	if !still || p.cfg.Reviews == nil {
		return false
	}
	t, _ := p.cfg.Targets(sid)
	entries, err := p.cfg.Reviews.Entries()
	if err != nil {
		return p.cfg.Now().Before(item.until)
	}
	needs := false
	for _, agent := range p.requiredAgents(sid, t, commit) {
		e, ok := entries[review.Key{Commit: commit, Branch: t.Branch, Agent: agent}]
		switch {
		case !ok:
			needs = true
		case e.Record.State == review.Running:
			return false
		case Retryable(e):
			needs = true
		}
	}
	if !needs {
		p.cfg.Logger.Info("push resume", "event", "deciding")
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
		p.cfg.Logger.Info("push resume", "event", "review-started")
		return false
	}
	e, ok := errors.AsType[*Error](err)
	switch {
	case ok && e.Reason == "reviewing":
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
	p.cfg.Logger.Info("push resume", "event", "review-not-started", "why", why)
	p.ReviewDone(sid, commit)
	return false
}

func unfinishedReviews(head string, ended []ReviewEntry) string {
	if len(ended) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ended))
	for _, e := range ended {
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
	return strings.Join(parts, "; ") + "; the engine won't start " + plural(len(ended), "it", "them") + " again on your request, but the person can press Review on the page"
}

func (p *Pushes) afterReviews(by, sid string, t PushTarget, head string) string {
	if by != ByReviews || p.cfg.Reviews == nil {
		return ""
	}
	entries, err := p.cfg.Reviews.Entries()
	if err != nil {
		return ""
	}
	var ended []ReviewEntry
	for _, agent := range p.requiredAgents(sid, t, head) {
		if e, ok := entries[review.Key{Commit: head, Branch: t.Branch, Agent: agent}]; ok && (e.Record.State == review.Failed || e.Record.State == review.Unknown) {
			ended = append(ended, e)
		}
	}
	if why := unfinishedReviews(head, ended); why != "" {
		return why + "; "
	}
	return ""
}
