package session

import (
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	MetricsAuditLines = 50000
	maxMetricRoles    = 50
	maxMetricPulls    = 20
	maxMetricCost     = 1e9
)

var metricRole = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,99}$`)

type MetricsConfig struct {
	Repos    Repositories
	Audit    AuditAll
	Reviews  func() ([]ReviewEntry, error)
	Imported func() ([]ReviewEntry, error)
	Runs     func() ([]SavedRun, error)
	Caps     func() (CapsState, error)
	Follows  func() (FollowRecord, error)
	Spans    func(repo string) []ProjectSpan
	Now      func() time.Time
}

type RoleMetrics struct {
	Role       string
	Reviews    int
	Passed     int
	Blocked    int
	NoVerdict  int
	Failed     int
	Overridden int
}

type MetricPull struct {
	Number int
	State  string
	At     time.Time
}

type MetricsView struct {
	Repo              string
	Now               time.Time
	AuditSince        time.Time
	AuditLines        int
	AuditCut          bool
	Pushes            int
	Timed             int
	StartToPushMedian time.Duration
	StartToPushMean   time.Duration
	StartToPushLong   time.Duration
	Rounds            int
	Roles             []RoleMetrics
	Allows            int
	Denied            int
	Overrides         int
	ReviewsSince      time.Time
	ReviewRuns        int
	ReviewCost        float64
	ImportedRuns      int
	ImportedCost      float64
	AgentSessions     int
	AgentCost         float64
	PullsSince        time.Time
	PullsOpened       int
	PullsMerged       int
	PullsClosed       int
	Pulls             []MetricPull
	Problems          []string
}

type MetricsPage interface {
	Metrics(repo string) (MetricsView, error)
}

type Metrics struct {
	cfg MetricsConfig
}

var _ MetricsPage = (*Metrics)(nil)

func NewMetrics(cfg MetricsConfig) *Metrics {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Metrics{cfg: cfg}
}

func knownRepo(repos Repositories, name string) error {
	if repos == nil {
		return explained(conflict("not-yet"), "This engine has no repositories.")
	}
	list, err := repos.List()
	if err != nil {
		return explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	if !slices.ContainsFunc(list, func(r Repository) bool { return r.Name == name }) {
		return explained(notFound("project"), "That project is no longer in the list.")
	}
	return nil
}

func costOf(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxMetricCost {
		return 0
	}
	return v
}

func (m *Metrics) Metrics(repo string) (MetricsView, error) {
	if err := knownRepo(m.cfg.Repos, repo); err != nil {
		return MetricsView{}, err
	}
	v := MetricsView{Repo: repo, Now: m.cfg.Now()}
	var entries []AuditEntry
	if m.cfg.Audit == nil {
		v.Problems = append(v.Problems, "This engine keeps no audit file.")
	} else if all, err := m.cfg.Audit.ReadAll(MetricsAuditLines); err != nil {
		v.Problems = append(v.Problems, "The engine couldn't read its audit file.")
	} else {
		entries = all
	}
	v.AuditLines, v.AuditCut = len(entries), len(entries) >= MetricsAuditLines
	slices.SortStableFunc(entries, func(a, b AuditEntry) int { return a.Time.Compare(b.Time) })
	if len(entries) > 0 {
		v.AuditSince = entries[0].Time
	}
	owned := func(e AuditEntry) bool { return e.Repository == repo }
	if m.cfg.Spans != nil {
		spans := m.cfg.Spans(repo)
		owned = func(e AuditEntry) bool { return inSpans(spans, e.Repository, e.Time) }
	}
	entries = slices.DeleteFunc(entries, func(e AuditEntry) bool { return !owned(e) })
	branches := map[string]bool{}
	for _, e := range entries {
		if e.Branch != "" {
			branches[e.Branch] = true
		}
	}
	var runs []SavedRun
	runsRead := false
	if m.cfg.Runs != nil {
		if list, err := m.cfg.Runs(); err == nil {
			runs, runsRead = list, true
		} else {
			v.Problems = append(v.Problems, "The engine couldn't read its sessions record, so it shows no time to the first push.")
		}
	}
	for _, r := range runs {
		if r.Repo == repo {
			branches[r.Branch] = true
		}
	}
	firstPush := m.fromAudit(&v, entries)
	if runsRead {
		startToPush(&v, runs, firstPush)
	}
	m.fromReviews(&v, branches)
	m.fromCaps(&v)
	m.fromFollows(&v)
	return v, nil
}

func (m *Metrics) fromAudit(v *MetricsView, entries []AuditEntry) map[string]time.Time {
	roles := map[string]*RoleMetrics{}
	role := func(name string) *RoleMetrics {
		if !metricRole.MatchString(name) {
			name = "another reviewer"
		}
		r := roles[name]
		if r == nil && len(roles) < maxMetricRoles {
			r = &RoleMetrics{Role: name}
			roles[name] = r
		}
		return r
	}
	firstPush := map[string]time.Time{}
	reviewed := map[string]map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case "", AuditPush:
			switch e.Result {
			case string(gate.Pushed):
				v.Pushes++
				v.Rounds += len(reviewed[e.Branch])
				delete(reviewed, e.Branch)
				if _, seen := firstPush[e.Session]; !seen && e.Session != "" {
					firstPush[e.Session] = e.Time
				}
			case string(gate.Allowed):
				if e.PersonReason != "" {
					v.Allows++
				}
			case string(gate.Denied):
				v.Denied++
			}
		case AuditReview:
			r := role(e.Reviewer)
			if r == nil {
				continue
			}
			switch review.State(e.Result) {
			case review.Pass:
				r.Passed++
			case review.Block:
				r.Blocked++
			case review.Unknown:
				r.NoVerdict++
			case review.Failed:
				r.Failed++
			default:
				continue
			}
			r.Reviews++
			if e.Branch != "" && e.Commit != "" {
				if reviewed[e.Branch] == nil {
					reviewed[e.Branch] = map[string]bool{}
				}
				reviewed[e.Branch][e.Commit] = true
			}
		case AuditOverride:
			v.Overrides++
			if r := role(e.Reviewer); r != nil {
				r.Overridden++
			}
		}
	}
	for _, r := range roles {
		v.Roles = append(v.Roles, *r)
	}
	slices.SortFunc(v.Roles, func(a, b RoleMetrics) int {
		if a.Reviews != b.Reviews {
			return b.Reviews - a.Reviews
		}
		if a.Role < b.Role {
			return -1
		}
		return 1
	})
	return firstPush
}

func startToPush(v *MetricsView, runs []SavedRun, firstPush map[string]time.Time) {
	var spans []time.Duration
	for _, r := range runs {
		at, ok := firstPush[r.SID]
		if !ok || r.Repo != v.Repo || r.StartedAt.IsZero() || at.Before(r.StartedAt) {
			continue
		}
		spans = append(spans, at.Sub(r.StartedAt))
	}
	if len(spans) == 0 {
		return
	}
	slices.Sort(spans)
	var sum time.Duration
	for _, d := range spans {
		sum += d
	}
	v.Timed = len(spans)
	v.StartToPushMean = sum / time.Duration(len(spans))
	v.StartToPushMedian = spans[len(spans)/2]
	if len(spans)%2 == 0 {
		v.StartToPushMedian = (spans[len(spans)/2-1] + spans[len(spans)/2]) / 2
	}
	v.StartToPushLong = spans[len(spans)-1]
}

func (m *Metrics) fromReviews(v *MetricsView, branches map[string]bool) {
	if m.cfg.Reviews != nil {
		if list, err := m.cfg.Reviews(); err != nil {
			v.Problems = append(v.Problems, "The engine couldn't read its review records, so it shows no reviewer cost.")
		} else {
			reviewCost(v, list, branches, false)
		}
	}
	if m.cfg.Imported != nil {
		if list, err := m.cfg.Imported(); err != nil {
			v.Problems = append(v.Problems, "The engine couldn't read the review records an import brought, so it leaves their cost out.")
		} else {
			reviewCost(v, list, branches, true)
		}
	}
}

func reviewCost(v *MetricsView, list []ReviewEntry, branches map[string]bool, imported bool) {
	for _, e := range list {
		if !branches[e.Key.Branch] || e.Record.State == review.Running {
			continue
		}
		if !e.StartedAt.IsZero() && (v.ReviewsSince.IsZero() || e.StartedAt.Before(v.ReviewsSince)) {
			v.ReviewsSince = e.StartedAt
		}
		c := costOf(e.CostUSD)
		if c <= 0 {
			continue
		}
		v.ReviewRuns++
		v.ReviewCost += c
		if imported {
			v.ImportedRuns++
			v.ImportedCost += c
		}
	}
}

func (m *Metrics) fromCaps(v *MetricsView) {
	if m.cfg.Caps == nil {
		return
	}
	st, err := m.cfg.Caps()
	if err != nil {
		v.Problems = append(v.Problems, "The engine couldn't read its caps record, so it shows no agent cost.")
		return
	}
	for _, s := range st.Sessions {
		if s.Project != v.Repo {
			continue
		}
		if c := costOf(s.Used.CostUSD); c > 0 {
			v.AgentSessions++
			v.AgentCost += c
		}
	}
}

func (m *Metrics) fromFollows(v *MetricsView) {
	if m.cfg.Follows == nil {
		return
	}
	rec, err := m.cfg.Follows()
	if err != nil {
		v.Problems = append(v.Problems, "The engine couldn't read the pull requests it follows.")
		return
	}
	for _, f := range rec.Follows {
		if f.Project != v.Repo {
			continue
		}
		if !f.Pushed.IsZero() && (v.PullsSince.IsZero() || f.Pushed.Before(v.PullsSince)) {
			v.PullsSince = f.Pushed
		}
		if f.Number > 0 {
			v.PullsOpened++
		}
		switch f.Done {
		case FollowMerged:
			v.PullsMerged++
		case FollowClosed:
			v.PullsClosed++
		default:
			continue
		}
		v.Pulls = append(v.Pulls, MetricPull{Number: f.Number, State: f.Done, At: f.DoneAt})
	}
	slices.SortFunc(v.Pulls, func(a, b MetricPull) int { return b.At.Compare(a.At) })
	if len(v.Pulls) > maxMetricPulls {
		v.Pulls = v.Pulls[:maxMetricPulls]
	}
}
