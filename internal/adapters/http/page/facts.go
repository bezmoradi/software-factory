package page

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	projectFactsRoute = "/api/project-facts"
	projectFactsFor   = 40 * time.Second

	saidLookupsSaved     = "Saved. The push card looks up new packages for this project."
	saidLookupsOffSaved  = "Saved. The push card doesn't look up new packages for this project."
	saidLookupsUnchanged = "No change: this setting is already saved."
)

type uncommittedJSON struct {
	Commit string   `json:"commit"`
	Count  int      `json:"count"`
	Paths  []string `json:"paths"`
	Cut    bool     `json:"cut"`
	At     int64    `json:"at"`
}

type overlapJSON struct {
	SID   string   `json:"sid"`
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	More  int      `json:"more"`
}

type agentCheckJSON struct {
	Kind     string `json:"kind"`
	Command  string `json:"command"`
	OK       bool   `json:"ok"`
	At       int64  `json:"at"`
	EditedAt int64  `json:"editedAt,omitempty"`
	Hidden   string `json:"hidden,omitempty"`
	Label    string `json:"label"`
	Result   string `json:"result"`
}

type changeNoteJSON struct {
	Path string `json:"path"`
	What string `json:"what"`
}

type fileKindJSON struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

type dependencyJSON struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	From      string `json:"from"`
	To        string `json:"to"`
	Change    string `json:"change"`
	File      string `json:"file"`
	Lockfile  bool   `json:"lockfile"`
	Looked    bool   `json:"looked"`
	Found     bool   `json:"found"`
	Published int64  `json:"published"`
	Young     bool   `json:"young"`
	Note      string `json:"note,omitempty"`
	Label     string `json:"ecosystemLabel"`
}

type workflowRunJSON struct {
	Path    string   `json:"path"`
	Name    string   `json:"name"`
	When    string   `json:"when"`
	Secrets []string `json:"secrets"`
	IDToken bool     `json:"idToken"`
	Trusted []string `json:"trusted"`
	New     bool     `json:"new"`
	Changed bool     `json:"changed"`
	Problem string   `json:"problem,omitempty"`
}

type pushExtraJSON struct {
	Commit           string            `json:"commit"`
	Problems         []string          `json:"problems"`
	Tests            []changeNoteJSON  `json:"tests"`
	Dependencies     []dependencyJSON  `json:"dependencies"`
	MoreDependencies int               `json:"moreDependencies"`
	Kinds            []fileKindJSON    `json:"kinds"`
	Workflows        []workflowRunJSON `json:"workflows"`
	WorkflowsProblem string            `json:"workflowsProblem,omitempty"`
	Lookup           string            `json:"lookup"`
	Locked           *lockedJSON       `json:"locked,omitempty"`
}

type protectionJSON struct {
	State      string   `json:"state"`
	Reviews    string   `json:"reviews"`
	Approvals  int      `json:"approvals"`
	AtLeast    bool     `json:"atLeast"`
	Checks     string   `json:"checks"`
	CheckNames []string `json:"checkNames"`

	Rows []protectionRowJSON `json:"rows"`
}

type lookupsJSON struct {
	Repo    string `json:"repo"`
	Lookups bool   `json:"lookups"`
	Message string `json:"message,omitempty"`
	Tone    string `json:"tone,omitempty"`
}

type projectFactsJSON struct {
	Repo             string            `json:"repo"`
	Branch           string            `json:"branch"`
	Commit           string            `json:"commit"`
	Workflows        []workflowRunJSON `json:"workflows"`
	WorkflowsProblem string            `json:"workflowsProblem,omitempty"`
	Protection       protectionJSON    `json:"protection"`
	Readiness        []readinessJSON   `json:"readiness"`
}

func uncommittedOf(u *session.Uncommitted) *uncommittedJSON {
	if u == nil {
		return nil
	}
	return &uncommittedJSON{Commit: u.Commit, Count: u.Count, Paths: nonNil(u.Paths), Cut: u.Cut, At: millis(u.At)}
}

func overlapsOf(list []session.Overlap) []overlapJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]overlapJSON, 0, len(list))
	for _, o := range list {
		out = append(out, overlapJSON{SID: o.SID, Name: o.Name, Paths: nonNil(o.Paths), More: o.More})
	}
	return out
}

func checksOf(list []session.AgentCheck) []agentCheckJSON {
	if len(list) == 0 {
		return nil
	}
	out := make([]agentCheckJSON, 0, len(list))
	for _, c := range list {
		out = append(out, agentCheckJSON{Kind: string(c.Kind), Command: c.Command, OK: c.OK, At: millis(c.At), EditedAt: millis(c.EditedAt), Hidden: string(c.Hidden),
			Label: session.CheckKindLabel(string(c.Kind)), Result: session.CheckResult(c.OK, string(c.Hidden))})
	}
	return out
}

func workflowRunsOf(list []session.WorkflowRun) []workflowRunJSON {
	out := make([]workflowRunJSON, 0, len(list))
	for _, w := range list {
		out = append(out, workflowRunJSON{Path: w.Path, Name: w.Name, When: w.When, Secrets: nonNil(w.Secrets), IDToken: w.IDToken, Trusted: nonNil(w.Trusted), New: w.New, Changed: w.Changed, Problem: w.Problem})
	}
	return out
}

func extraOf(x *session.PushExtra) *pushExtraJSON {
	if x == nil {
		return nil
	}
	out := &pushExtraJSON{Commit: x.Commit, Problems: nonNil(x.Problems), Tests: make([]changeNoteJSON, 0, len(x.Tests)), Dependencies: make([]dependencyJSON, 0, len(x.Dependencies)),
		MoreDependencies: x.MoreDependencies, Kinds: make([]fileKindJSON, 0, len(x.Kinds)), Workflows: workflowRunsOf(x.Workflows), WorkflowsProblem: x.WorkflowsProblem, Lookup: x.Lookup, Locked: lockedOf(x.Locked)}
	for _, n := range x.Tests {
		out.Tests = append(out.Tests, changeNoteJSON{Path: n.Path, What: n.What})
	}
	for _, d := range x.Dependencies {
		out.Dependencies = append(out.Dependencies, dependencyJSON{Ecosystem: d.Ecosystem, Name: d.Name, From: d.From, To: d.To, Change: d.Change, File: d.File, Lockfile: d.Lockfile,
			Looked: d.Looked, Found: d.Found, Published: millis(d.Published), Young: d.Young, Note: d.Note, Label: session.EcosystemLabel(d.Ecosystem)})
	}
	for _, k := range x.Kinds {
		out.Kinds = append(out.Kinds, fileKindJSON{Path: k.Path, Kind: k.Kind, Target: k.Target})
	}
	return out
}

func projectFactsOf(v session.ProjectFactsView) projectFactsJSON {
	p := v.Protection
	return projectFactsJSON{Repo: v.Repo, Branch: v.Branch, Commit: v.Commit, Workflows: workflowRunsOf(v.Workflows), WorkflowsProblem: v.WorkflowsProblem, Readiness: readinessOf(v.Readiness),
		Protection: protectionJSON{State: p.State, Reviews: p.Reviews, Approvals: p.Approvals, AtLeast: p.AtLeast, Checks: p.Checks, CheckNames: nonNil(p.CheckNames), Rows: protectionRowsOf(p)}}
}

func (s *Server) UseProjectFacts(facts session.ProjectFactsPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts = facts
}

func (s *Server) projectFacts(w http.ResponseWriter, r *http.Request, body map[string]any) {
	s.mu.Lock()
	facts := s.facts
	s.mu.Unlock()
	if facts == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no repositories, so it reads no workflows or branch protection."})
		return
	}
	op, okOp := body["op"].(string)
	repo, okRepo := body["repo"].(string)
	if !okOp || !okRepo || len(repo) > maxRulesRepo || !rulesRepoName.MatchString(repo) {
		refuseFields(w)
		return
	}
	switch op {
	case "read":
		if !keysWithin(body, "op", "repo") {
			refuseFields(w)
			return
		}
	case "read-lookups":
		if !keysWithin(body, "op", "repo") {
			refuseFields(w)
			return
		}
		on, err := facts.Lookups(repo)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, lookupsJSON{Repo: repo, Lookups: on})
		return
	case "lookups":
		on, okOn := body["on"].(bool)
		if !keysWithin(body, "op", "repo", "on") || !okOn {
			refuseFields(w)
			return
		}
		changed, err := facts.SetLookups(repo, on)
		shared.For(r.Context(), s.logger).Info("package lookup", "on", on, "changed", changed, "ok", err == nil)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		saved := saidLookupsOffSaved
		if on {
			saved = saidLookupsSaved
		}
		said := saveSaid(changed, saved, saidLookupsUnchanged)
		send(w, http.StatusOK, lookupsJSON{Repo: repo, Lookups: on, Message: said.Message, Tone: said.Tone})
		return
	default:
		send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine reads a project's workflows and branch protection, and reads or switches the package lookup, nothing else."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), projectFactsFor)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(projectFactsFor + writeFor))
	v, err := facts.ProjectFacts(ctx, repo)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, projectFactsOf(v))
}
