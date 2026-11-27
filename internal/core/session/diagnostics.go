package session

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	DiagnosticsFormat = "software-factory-diagnostics/1"
	diagLeftOut       = "[left out]"
	maxDiagFacts      = 200
)

type DiagFact struct {
	Name  string
	Value string
}

type DiagnosticsPage interface {
	Diagnostics(ctx context.Context) (string, error)
}

type DiagnosticsConfig struct {
	Version  string
	Versions func(ctx context.Context) []DiagFact
	Settings func() []DiagFact
	Projects func() ([]Repository, error)
	Project  func(name string) ProjectInfo
	Sessions func() []Info
	Reviews  func() (map[review.Key]ReviewEntry, error)
	Events   func() []shared.Event
	Usage    func() []DiagFact
	Now      func() time.Time
}

type Diagnostics struct {
	cfg DiagnosticsConfig
}

var _ DiagnosticsPage = (*Diagnostics)(nil)

func NewDiagnostics(cfg DiagnosticsConfig) *Diagnostics {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Diagnostics{cfg: cfg}
}

var (
	diagName  = regexp.MustCompile(`^[a-z][a-z0-9 ._-]{0,63}$`)
	diagValue = regexp.MustCompile(`^[A-Za-z0-9 ._:+()-]{0,120}$`)
	diagWord  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	diagPart  = regexp.MustCompile(`^[a-z][a-z0-9 -]{0,47}$`)
	diagOS    = regexp.MustCompile(`^[A-Za-z0-9 ._:+()/-]{0,120}$`)
)

func diagFact(f DiagFact) (string, bool) {
	if !diagName.MatchString(f.Name) {
		return "", false
	}
	v := f.Value
	ok := diagValue.MatchString(v)
	if !ok && strings.HasPrefix(f.Name, "os") || !ok && strings.HasSuffix(f.Name, " os") {
		ok = diagOS.MatchString(v) && !strings.HasPrefix(v, "/") && !strings.Contains(v, "//")
	}
	if !ok {
		v = diagLeftOut
	}
	if v == "" {
		v = "unknown"
	}
	return f.Name + ": " + v, true
}

func diagCut(s string, pattern *regexp.Regexp) string {
	if s == "" {
		return "-"
	}
	if pattern.MatchString(s) {
		return s
	}
	return "other"
}

func (x *Diagnostics) Diagnostics(ctx context.Context) (string, error) {
	var b strings.Builder
	now := x.cfg.Now().UTC()
	line := func(s string) {
		if secretText(s) || flagURL.MatchString(s) {
			s = diagLeftOut
		}
		b.WriteString(s)
		b.WriteByte('\n')
	}
	facts := func(title string, list []DiagFact) {
		line("")
		line("[" + title + "]")
		if len(list) > maxDiagFacts {
			list = list[:maxDiagFacts]
		}
		for _, f := range list {
			if s, ok := diagFact(f); ok {
				line(s)
			}
		}
	}
	line(DiagnosticsFormat)
	line("made: " + now.Format(time.RFC3339))
	line("product: " + diagCut(x.cfg.Version, diagValue))
	if x.cfg.Versions != nil {
		facts("versions", x.cfg.Versions(ctx))
	}
	if x.cfg.Settings != nil {
		facts("settings", x.cfg.Settings())
	}
	facts("counts", x.counts())
	if x.cfg.Usage != nil {
		facts("usage", x.cfg.Usage())
	}
	line("")
	events := []shared.Event{}
	if x.cfg.Events != nil {
		events = x.cfg.Events()
	}
	if len(events) > shared.MaxEvents {
		events = events[len(events)-shared.MaxEvents:]
	}
	line(fmt.Sprintf("[events] %d, oldest first: time level part kind error request", len(events)))
	for _, e := range events {
		line(strings.Join([]string{e.Time.UTC().Format(time.RFC3339), diagCut(e.Level, diagWord), strings.ReplaceAll(diagCut(e.Part, diagPart), " ", "_"),
			diagCut(e.Kind, diagWord), diagCut(e.Error, diagWord), reqOrDash(e.Req)}, " "))
	}
	return b.String(), nil
}

func reqOrDash(id string) string {
	if shared.ValidRequest(id) {
		return id
	}
	return "-"
}

func (x *Diagnostics) counts() []DiagFact {
	var out []DiagFact
	add := func(name string, n int) {
		out = append(out, DiagFact{Name: name, Value: fmt.Sprint(n)})
	}
	if x.cfg.Projects != nil {
		if list, err := x.cfg.Projects(); err == nil {
			add("projects", len(list))
			if x.cfg.Project != nil {
				findings, comments, held := 0, 0, 0
				for _, r := range list {
					p := x.cfg.Project(r.Name)
					if p.AutoFindings {
						findings++
					}
					if p.PRComment {
						comments++
					}
					if p.Hold.On() {
						held++
					}
				}
				add("projects sending findings on their own", findings)
				add("projects commenting on pull requests", comments)
				add("projects with a push hold", held)
			}
		} else {
			out = append(out, DiagFact{Name: "projects", Value: "unreadable"})
		}
	}
	if x.cfg.Sessions != nil {
		all := x.cfg.Sessions()
		live, verified := 0, 0
		for _, s := range all {
			if s.Live {
				live++
			}
			if s.Verified {
				verified++
			}
		}
		add("sessions", len(all))
		add("sessions connected", live)
		add("sessions started here", verified)
	}
	if x.cfg.Reviews != nil {
		entries, err := x.cfg.Reviews()
		if err != nil {
			out = append(out, DiagFact{Name: "reviews", Value: "unreadable"})
		} else {
			by := map[string]int{}
			for _, e := range entries {
				by[string(e.Record.State)]++
			}
			add("reviews", len(entries))
			states := make([]string, 0, len(by))
			for s := range by {
				states = append(states, s)
			}
			slices.Sort(states)
			for _, s := range states {
				if diagWord.MatchString(s) {
					add("reviews "+s, by[s])
				}
			}
		}
	}
	return out
}
