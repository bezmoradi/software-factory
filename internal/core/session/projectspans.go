package session

import (
	"slices"
	"time"
)

const MaxProjectSpans = 20

type ProjectSpan struct {
	Name string
	From time.Time
	To   time.Time
}

func inSpans(spans []ProjectSpan, name string, at time.Time) bool {
	return slices.ContainsFunc(spans, func(s ProjectSpan) bool {
		return s.Name == name && !at.Before(s.From) && (s.To.IsZero() || at.Before(s.To))
	})
}

type projectLines struct {
	spans []ProjectSpan
	url   string
}

func (p projectLines) at(name string, t time.Time) bool {
	return inSpans(p.spans, name, t)
}

func (p projectLines) audit(e AuditEntry) bool {
	if e.ProjectURL != "" && p.url != "" {
		return sameRepository(e.ProjectURL, p.url)
	}
	return p.at(e.Repository, e.Time)
}

func openSpan(spans []ProjectSpan, r Repository) bool {
	n := len(spans)
	return n > 0 && spans[n-1].To.IsZero() && spans[n-1].Name == r.Name && spans[n-1].From.Equal(r.AddedAt)
}

func spansOf(st DecisionState, r Repository) []ProjectSpan {
	if list := st.Spans[r.Name]; openSpan(list, r) {
		return slices.Clone(list)
	}
	return []ProjectSpan{{Name: r.Name, From: r.AddedAt}}
}

func opened(spans []ProjectSpan, name string, from time.Time) []ProjectSpan {
	out := append(slices.Clone(spans), ProjectSpan{Name: name, From: from})
	if len(out) > MaxProjectSpans {
		out = out[len(out)-MaxProjectSpans:]
	}
	return out
}

func closed(spans []ProjectSpan, at time.Time) []ProjectSpan {
	out := slices.Clone(spans)
	if n := len(out); n > 0 && out[n-1].To.IsZero() {
		out[n-1].To = at
	}
	return out
}

func (l *DecisionLog) record(repo string) (Repository, bool) {
	if l.cfg.Repos == nil {
		return Repository{}, false
	}
	list, err := l.cfg.Repos.List()
	if err != nil {
		return Repository{}, false
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == repo })
	if i < 0 {
		return Repository{}, false
	}
	return list[i], true
}

func (l *DecisionLog) linesFor(st DecisionState, repo string) projectLines {
	r, ok := l.record(repo)
	if !ok {
		return projectLines{}
	}
	return projectLines{spans: spansOf(st, r), url: r.URL}
}

func (l *DecisionLog) Spans(repo string) []ProjectSpan {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, _ := l.state()
	return l.linesFor(st, repo).spans
}

func (l *DecisionLog) parkSpans(st *DecisionState, id ProjectID, at time.Time) bool {
	list, ok := st.Spans[id.Name]
	if !ok {
		return false
	}
	st.Spans[id.Name] = closed(list, at)
	var parked bool
	st.RemovedSpans, _, parked = parkProject(st.Spans, st.RemovedSpans, id)
	return parked
}

func (l *DecisionLog) restoreSpans(st *DecisionState, id ProjectID, at time.Time) {
	st.RemovedSpans, _ = restoreProject(st.Spans, st.RemovedSpans, id)
	from := at
	if r, ok := l.record(id.Name); ok && sameURL(ProjectID{URL: r.URL}, id) {
		from = r.AddedAt
	}
	st.Spans[id.Name] = opened(closed(st.Spans[id.Name], from), id.Name, from)
}

func (l *DecisionLog) keepSpans(st *DecisionState, names []string) bool {
	var n int
	st.RemovedSpans, n = keepProjects(st.Spans, st.RemovedSpans, names)
	changed := n > 0
	if l.cfg.Repos == nil {
		return changed
	}
	list, err := l.cfg.Repos.List()
	if err != nil {
		return changed
	}
	for _, r := range list {
		if !slices.Contains(names, r.Name) || openSpan(st.Spans[r.Name], r) {
			continue
		}
		st.RemovedSpans = setAside(st.Spans, st.RemovedSpans, r.Name)
		st.Spans[r.Name] = opened(nil, r.Name, r.AddedAt)
		changed = true
	}
	return changed
}
