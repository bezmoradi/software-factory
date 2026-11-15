package session

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	MaxCitedIDs = 20
	citedFor    = 45 * time.Second
	keptCited   = 200
)

var (
	cveID  = regexp.MustCompile(`(?i)\bCVE-(\d{4})-(\d{4,7})\b`)
	ghsaID = regexp.MustCompile(`(?i)\bGHSA((?:-[23456789cfghjmpqrvwx]{4}){3})\b`)
)

type CitedIDs struct {
	Looked  []string
	Missing []string
	Failed  string
	More    int
}

func (c CitedIDs) clone() CitedIDs {
	c.Looked, c.Missing = slices.Clone(c.Looked), slices.Clone(c.Missing)
	return c
}

type CitedLookup interface {
	Held(ctx context.Context, ids []string) (map[string]bool, error)
}

func (r *Reviews) UseCitedLookup(l CitedLookup) {
	r.mu.Lock()
	r.cited = l
	r.mu.Unlock()
}

func CitedIn(text string) ([]string, int) {
	var out []string
	more := 0
	add := func(id string) {
		if slices.Contains(out, id) {
			return
		}
		if len(out) >= MaxCitedIDs {
			more++
			return
		}
		out = append(out, id)
	}
	type at struct {
		i  int
		id string
	}
	var found []at
	for _, m := range cveID.FindAllStringSubmatchIndex(text, -1) {
		found = append(found, at{m[0], "CVE-" + text[m[2]:m[3]] + "-" + text[m[4]:m[5]]})
	}
	for _, m := range ghsaID.FindAllStringSubmatchIndex(text, -1) {
		found = append(found, at{m[0], "GHSA" + strings.ToLower(text[m[2]:m[3]])})
	}
	slices.SortFunc(found, func(a, b at) int { return a.i - b.i })
	for _, f := range found {
		add(f.id)
	}
	return out, more
}

func (r *Reviews) lookUpCited(ctx context.Context, e ReviewEntry) (CitedIDs, bool) {
	r.mu.Lock()
	l := r.cited
	r.mu.Unlock()
	if l == nil || e.Message == "" || e.Record.State == review.Failed || e.Record.Channel == review.Withheld {
		return CitedIDs{}, false
	}
	ids, more := CitedIn(e.Message)
	if len(ids) == 0 {
		return CitedIDs{}, false
	}
	out := CitedIDs{Looked: ids, More: more}
	lctx, cancel := context.WithTimeout(ctx, citedFor)
	defer cancel()
	held, err := l.Held(lctx, ids)
	if err != nil {
		out.Failed = clip(err.Error(), 200)
		r.cfg.Logger.Warn("cited ids", "event", "lookup-failed", "ids", len(ids))
		return out, true
	}
	for _, id := range ids {
		if known, ok := held[id]; ok && !known {
			out.Missing = append(out.Missing, id)
		}
	}
	r.cfg.Logger.Info("cited ids", "event", "looked-up", "ids", len(ids), "missing", len(out.Missing))
	return out, true
}

func (r *Reviews) citedOnto(v *ReviewView) {
	if len(r.citedNow) == 0 {
		return
	}
	v.Roles = slices.Clone(v.Roles)
	for i, role := range v.Roles {
		k := review.Key{Commit: v.Commit, Branch: v.Branch, Agent: role.Agent}.AtRun(role.Run)
		if c, ok := r.citedNow[k]; ok && c.started.Equal(role.started) && len(role.Cited.Looked) == 0 && role.State != review.Running {
			v.Roles[i].Cited = c.ids.clone()
		}
	}
}

type citedRun struct {
	started time.Time
	ids     CitedIDs
}

func (r *Reviews) noteCited(sid string, k review.Key, started time.Time, c CitedIDs) {
	r.mu.Lock()
	if r.citedNow == nil || len(r.citedNow) >= keptCited {
		r.citedNow = map[review.Key]citedRun{}
	}
	r.citedNow[k] = citedRun{started: started, ids: c.clone()}
	v, ok := r.views[sid]
	r.mu.Unlock()
	if ok && v.Commit == k.Commit && v.Branch == k.Branch {
		r.show(sid, v.clone())
	}
}

func (r *Reviews) markCited(k review.Key, started time.Time, c CitedIDs) {
	r.store.Lock()
	defer r.store.Unlock()
	list, err := r.cfg.Records.Load()
	if err != nil {
		r.cfg.Logger.Warn("review", "event", "records-unreadable")
		return
	}
	i := slices.IndexFunc(list, func(x ReviewEntry) bool { return x.Key == k && x.StartedAt.Equal(started) })
	if i < 0 {
		return
	}
	list[i].Cited = c
	if err := r.cfg.Records.Save(list); err != nil {
		r.cfg.Logger.Warn("review", "event", "records-not-saved")
	}
}
