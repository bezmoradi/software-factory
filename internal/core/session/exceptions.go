package session

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
)

const (
	MaxExceptions      = 500
	MaxExceptionRepos  = 50
	exceptionRulesFor  = 30 * time.Second
	ExceptionAllowPush = "allow-push"
)

type ExceptionItem struct {
	Kind     string
	Time     time.Time
	SID      string
	Repo     string
	Branch   string
	Commit   string
	Reviewer string
	Path     string
	Missing  int
	Result   string
	Verdict  string
	Reason   string
}

type ExceptionRules struct {
	Repo    string
	Rules   []AllowRule
	Problem string
}

type ExceptionsView struct {
	Items        []ExceptionItem
	More         bool
	AuditProblem string
	Hosts        []AddedHost
	Gateway      *Gateway
	HostsKnown   bool
	Rules        []ExceptionRules
	RulesKnown   bool
	Now          time.Time
}

type ExceptionsPage interface {
	Exceptions(ctx context.Context) (ExceptionsView, error)
}

type ExceptionsConfig struct {
	Audit  AuditAll
	Egress func() (EgressView, error)
	Rules  AllowRulesPage
	Repos  Repositories
	Now    func() time.Time
}

type Exceptions struct {
	cfg ExceptionsConfig
}

var _ ExceptionsPage = (*Exceptions)(nil)

func NewExceptions(cfg ExceptionsConfig) *Exceptions {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Exceptions{cfg: cfg}
}

func exceptionOf(e AuditEntry) (ExceptionItem, bool) {
	item := ExceptionItem{Kind: e.Kind, Time: e.Time, SID: e.Session, Repo: e.Repository, Branch: e.Branch, Commit: e.Commit, Reviewer: e.Reviewer, Path: e.Path, Result: e.Result, Reason: e.PersonReason}
	switch e.Kind {
	case AuditPush:
		if e.Result != string(gate.Allowed) || e.PersonReason == "" {
			return item, false
		}
		item.Kind, item.Missing = ExceptionAllowPush, len(e.Missing)
	case AuditOverride, AuditAllowFile, AuditAllowCommit:
	case AuditCapReached, AuditCapAnswer:
		item.Result, item.Verdict, item.Path = capKind(e), capAnswer(e), ""
	case AuditUsageAnswer:
		item.Result, item.Path = "", ""
	default:
		return item, false
	}
	return item, true
}

func (x *Exceptions) Exceptions(ctx context.Context) (ExceptionsView, error) {
	v := ExceptionsView{Now: x.cfg.Now()}
	if x.cfg.Audit != nil {
		entries, err := x.cfg.Audit.ReadAll(AuditReadAll)
		if err != nil {
			v.AuditProblem = "The engine couldn't read its audit file."
		}
		for _, e := range entries {
			item, ok := exceptionOf(e)
			if !ok {
				continue
			}
			if len(v.Items) >= MaxExceptions {
				v.More = true
				break
			}
			v.Items = append(v.Items, item)
		}
	}
	if x.cfg.Egress != nil {
		if ev, err := x.cfg.Egress(); err == nil {
			v.HostsKnown = true
			v.Hosts = slices.Clone(ev.Added)
			slices.SortFunc(v.Hosts, func(a, b AddedHost) int { return b.AddedAt.Compare(a.AddedAt) })
			v.Gateway = ev.Gateway
		}
	}
	if x.cfg.Rules != nil && x.cfg.Repos != nil {
		v.Rules, v.RulesKnown = x.rules(ctx)
	}
	return v, nil
}

func (x *Exceptions) rules(ctx context.Context) ([]ExceptionRules, bool) {
	repos, err := x.cfg.Repos.List()
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, exceptionRulesFor)
	defer cancel()
	var out []ExceptionRules
	for i, r := range repos {
		if i >= MaxExceptionRepos {
			break
		}
		got := ExceptionRules{Repo: r.Name}
		view, err := x.cfg.Rules.AllowRules(ctx, r.Name)
		switch {
		case err != nil:
			got.Problem = "The engine couldn't list this project's saved rules."
			if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
				got.Problem = clip(e.Message, maxRuleProblem)
			}
		default:
			got.Rules = view.Rules
		}
		out = append(out, got)
	}
	return out, true
}
