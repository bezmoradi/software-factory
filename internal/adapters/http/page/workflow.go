package page

import (
	"context"
	"maps"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxApproveBody = 1 << 20
	lockTestsRoute = "/api/lock-tests"
)

var (
	workflowRoutes = map[string]bool{"/api/gate": true, "/api/size": true, "/api/approve-plan": true, "/api/approve-requirements": true, "/api/team": true, "/api/project": true, allowFileRoute: true, flagRemoveRoute: true, allowCommitRoute: true, commitRewriteRoute: true, lockTestsRoute: true}
	approveRoutes  = map[string]bool{"/api/approve-plan": true, "/api/approve-requirements": true}
	hash256        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	workTypeID     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type workDocJSON struct {
	Path       string `json:"path"`
	Text       string `json:"text"`
	Hash       string `json:"hash"`
	Approved   bool   `json:"approved"`
	Changed    bool   `json:"changed"`
	Clipped    bool   `json:"clipped"`
	Previous   string `json:"previous,omitempty"`
	Commit     string `json:"commit,omitempty"`
	ApprovedAt string `json:"approvedAt,omitempty"`
	Reported   bool   `json:"reported,omitempty"`
	OnBase     bool   `json:"onBase,omitempty"`
	Note       string `json:"note"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
}

type workTypeJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Steps   []string `json:"steps"`
	Edits   string   `json:"edits"`
	Push    bool     `json:"push"`
	Roles   []string `json:"roles"`
	Builtin bool     `json:"builtin"`
	Warning string   `json:"startWarning,omitempty"`
}

type gateReviewerJSON struct {
	Type        string `json:"type"`
	Role        string `json:"role"`
	Name        string `json:"name"`
	Why         string `json:"why"`
	State       string `json:"state"`
	Verdict     string `json:"verdict"`
	Round       int    `json:"round"`
	Limit       int    `json:"limit"`
	Capped      bool   `json:"capped"`
	Override    string `json:"override"`
	Since       string `json:"since"`
	Incremental bool   `json:"incremental"`
	SetAside    string `json:"setAside,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

type gateJSON struct {
	Head         string             `json:"head"`
	Branch       string             `json:"branch"`
	Adapter      string             `json:"adapter"`
	Size         string             `json:"size"`
	WorkType     string             `json:"workType"`
	Chosen       bool               `json:"chosen"`
	Effective    string             `json:"effective"`
	SizeNote     string             `json:"sizeNote"`
	Upload       string             `json:"uploadWarning,omitempty"`
	Types        []workTypeJSON     `json:"types"`
	Commands     map[string]string  `json:"commands"`
	Team         map[string]any     `json:"team"`
	Requirements *workDocJSON       `json:"requirements"`
	Plan         *workDocJSON       `json:"planDoc"`
	PlanPath     string             `json:"plan"`
	PlanHash     string             `json:"planHash"`
	Approved     bool               `json:"approved"`
	Research     string             `json:"research"`
	Define       bool               `json:"define"`
	Reviewers    []gateReviewerJSON `json:"reviewers"`
	Missing      []string           `json:"missing"`
	Allowed      bool               `json:"allowed"`
	Waiting      string             `json:"waiting"`
	Implemented  bool               `json:"implemented"`
	Holds        map[string]string  `json:"holds"`
	Rounds       int                `json:"rounds"`
	Committed    bool               `json:"committed"`
	Flagged      []flagJSON         `json:"flagged"`
	FlagsRead    bool               `json:"flagsRead"`
	LFS          []string           `json:"lfs"`
	Handoff      bool               `json:"handoff"`
	Catalog      string             `json:"catalog"`
	CatalogWhy   string             `json:"catalogWhy,omitempty"`
	TestLock     testLockJSON       `json:"testLock"`
	TypeChange   *typeChangeJSON    `json:"typeChange,omitempty"`
	Started      any                `json:"started"`
	Review       reviewOfferJSON    `json:"review"`
	Pushed       bool               `json:"pushed"`
	Path         pathJSON           `json:"path"`
	Flow         *flowJSON          `json:"flow"`
	TypeAsk      *typeCardJSON      `json:"typeAsk,omitempty"`
}

type pathActionJSON struct {
	Ask   string `json:"ask"`
	Label string `json:"label"`
	Title string `json:"title"`
	Hold  string `json:"hold,omitempty"`
}

type pathStageJSON struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	State   string           `json:"state"`
	Active  bool             `json:"active"`
	Detail  string           `json:"detail"`
	Actions []pathActionJSON `json:"actions"`
}

type pathCardJSON struct {
	Step   string  `json:"step"`
	Count  string  `json:"count"`
	Detail string  `json:"detail"`
	Done   float64 `json:"done"`
}

type pathJSON struct {
	Stages        []pathStageJSON `json:"stages"`
	Card          pathCardJSON    `json:"card"`
	Missing       []string        `json:"missing"`
	ReviewsActive bool            `json:"reviewsActive"`
	Required      []string        `json:"required"`
}

func pathOf(m session.PathModel) pathJSON {
	out := pathJSON{Stages: make([]pathStageJSON, 0, len(m.Stages)), Card: pathCardJSON{Step: m.Card.Step, Count: m.Card.Count, Detail: m.Card.Detail, Done: m.Card.Done},
		Missing: nonNil(m.Missing), ReviewsActive: m.ReviewsActive, Required: nonNil(m.Required)}
	for _, s := range m.Stages {
		st := pathStageJSON{ID: s.ID, Name: s.Name, State: s.State, Active: s.Active, Detail: s.Detail, Actions: make([]pathActionJSON, 0, len(s.Actions))}
		for _, a := range s.Actions {
			st.Actions = append(st.Actions, pathActionJSON{Ask: a.Ask, Label: a.Label, Title: a.Title, Hold: a.Hold})
		}
		out.Stages = append(out.Stages, st)
	}
	return out
}

type plannedJSON struct {
	Name    string `json:"name"`
	Agent   string `json:"agent"`
	Why     string `json:"why"`
	Summary string `json:"summary"`
}

type reviewOfferJSON struct {
	Available bool          `json:"available"`
	Reason    string        `json:"reason"`
	Planned   []plannedJSON `json:"planned"`
}

func startedJSON(s session.BranchStart) any {
	switch s {
	case session.StartedCommits:
		return true
	case session.StartedNone:
		return false
	case session.StartedNoBase:
		return "nobase"
	}
	return nil
}

func offerOf(i session.Info, v session.GateView) reviewOfferJSON {
	o := session.OfferReview(i, v)
	out := reviewOfferJSON{Available: o.Reason == "", Reason: o.Reason, Planned: []plannedJSON{}}
	for _, r := range o.Planned {
		out.Planned = append(out.Planned, plannedJSON{Name: r.Name, Agent: r.Agent, Why: r.Why, Summary: r.Summary})
	}
	return out
}

type typeFileJSON struct {
	Path string `json:"path"`
	What string `json:"what,omitempty"`
}

type typeLineJSON struct {
	What  string         `json:"what"`
	Files []typeFileJSON `json:"files"`
	More  int            `json:"more"`
}

type typeChangeJSON struct {
	Type  string         `json:"type"`
	Lines []typeLineJSON `json:"lines"`
}

func typeChangeOf(c *session.TypeChange) *typeChangeJSON {
	if c == nil {
		return nil
	}
	out := &typeChangeJSON{Type: c.Type, Lines: make([]typeLineJSON, 0, len(c.Lines))}
	for _, l := range c.Lines {
		line := typeLineJSON{What: l.What, Files: make([]typeFileJSON, 0, len(l.Files)), More: l.More}
		for _, f := range l.Files {
			line.Files = append(line.Files, typeFileJSON{Path: f.Path, What: f.What})
		}
		out.Lines = append(out.Lines, line)
	}
	return out
}

type lockedJSON struct {
	Commit  string   `json:"commit"`
	Count   int      `json:"count"`
	Paths   []string `json:"paths"`
	Problem string   `json:"problem,omitempty"`
}

type testLockJSON struct {
	Offered    bool        `json:"offered"`
	Why        string      `json:"why,omitempty"`
	Commit     string      `json:"commit,omitempty"`
	At         int64       `json:"at,omitempty"`
	Files      int         `json:"files"`
	Paths      []string    `json:"paths"`
	Cut        bool        `json:"cut"`
	BranchLeft int         `json:"branchLeft"`
	Changed    *lockedJSON `json:"changed"`
}

func lockedOf(c *session.LockedChange) *lockedJSON {
	if c == nil {
		return nil
	}
	return &lockedJSON{Commit: c.Commit, Count: c.Count, Paths: nonNil(c.Paths), Problem: c.Problem}
}

func testLockOf(v session.LockView) testLockJSON {
	out := testLockJSON{Offered: v.Offered, Why: v.Why, Commit: v.Commit, Files: v.Files, Paths: nonNil(v.Paths), Cut: v.Cut, BranchLeft: v.BranchLeft, Changed: lockedOf(v.Changed)}
	if v.Commit != "" {
		out.At = millis(v.At)
	}
	return out
}

func docJSON(d *session.WorkDoc, clipped string, v session.GateView) *workDocJSON {
	if d == nil {
		return nil
	}
	kind := session.DocKind(d == v.Requirements, v.Effective)
	return &workDocJSON{Kind: kind, Status: session.DocStatus(kind, *d), Path: d.Path, Text: d.Text, Hash: d.Hash, Approved: d.Approved, Changed: d.Changed, Clipped: d.Clipped, Previous: d.Previous,
		Commit: d.Commit, ApprovedAt: d.ApprovedAt, Reported: d.Reported, OnBase: d.OnBase, Note: session.DocNote(*d, clipped, v.PlanApprovals, v.Head)}
}

func typesJSON(list []workflow.WorkType) []workTypeJSON {
	out := make([]workTypeJSON, 0, len(list))
	for _, t := range list {
		steps := make([]string, 0, len(t.Steps))
		for _, s := range t.Steps {
			steps = append(steps, string(s))
		}
		roles := make([]string, 0, len(t.Roles))
		for _, r := range t.Roles {
			roles = append(roles, string(r))
		}
		out = append(out, workTypeJSON{ID: t.ID, Name: t.Name, Steps: steps, Edits: string(t.Edits), Push: t.Push, Roles: roles, Builtin: t.Builtin, Warning: t.Profile().StartWarning()})
	}
	return out
}

func gateOf(v session.GateView) gateJSON {
	out := gateJSON{Head: v.Head, Branch: v.Branch, Adapter: string(v.Adapter), Size: string(profileSize(v)), WorkType: v.Type, Chosen: v.Chosen,
		Effective: string(v.Effective), SizeNote: v.Note, Upload: v.Effective.UploadWarning(), Types: typesJSON(v.Types), Commands: map[string]string{},
		Team: map[string]any{"approved": v.TeamApproved, "note": v.TeamNote}, Requirements: docJSON(v.Requirements, "", v), Plan: docJSON(v.Plan, session.PlanDocWord(v.Effective), v),
		Research: v.Research, Define: v.Define, Reviewers: []gateReviewerJSON{},
		Missing: nonNil(v.Missing), Allowed: v.Allowed, Waiting: v.Waiting, Implemented: v.Waiting == "" && v.Effective != workflow.Exploring,
		Holds: v.Holds, Rounds: v.Rounds, Committed: v.PlanApprovals, Flagged: flagsJSON(v.Flagged), FlagsRead: v.FlagsRead, LFS: nonNil(v.LFS), Handoff: v.Handoff,
		Catalog: string(v.Catalog), CatalogWhy: v.CatalogWhy, TestLock: testLockOf(v.Lock), TypeChange: typeChangeOf(v.Change), Started: startedJSON(v.Started)}
	for step, c := range v.Commands {
		out.Commands[string(step)] = c
	}
	maps.Copy(out.Commands, session.StageCommands(v))
	if v.Plan != nil {
		out.PlanPath, out.PlanHash, out.Approved = v.Plan.Path, v.Plan.Hash, v.Plan.Approved
	}
	for _, r := range v.Reviewers {
		out.Reviewers = append(out.Reviewers, gateReviewerJSON{Type: r.Agent, Role: string(r.Role), Name: r.Name, Why: r.Why, State: r.State, Verdict: r.Verdict,
			Round: r.Round, Limit: r.Limit, Capped: r.Capped, Override: r.Override, Since: r.Since, Incremental: r.Since != "", SetAside: r.SetAside, Summary: r.Summary})
	}
	return out
}

func profileSize(v session.GateView) workflow.Profile {
	for _, t := range v.Types {
		if t.ID == v.Type {
			return t.Profile()
		}
	}
	return workflow.Fix
}

type teamViewJSON struct {
	Path          string           `json:"path"`
	Head          string           `json:"head"`
	Text          *string          `json:"text"`
	SHA           string           `json:"sha"`
	Problems      []string         `json:"problems"`
	Approved      map[string]any   `json:"approved"`
	Note          string           `json:"note"`
	Setup         map[string]any   `json:"setup"`
	SetupProblems []string         `json:"setupProblems"`
	Remote        []map[string]any `json:"remote"`
}

func teamOf(v session.TeamView) teamViewJSON {
	out := teamViewJSON{Path: session.TeamFile, Head: v.Main, SHA: v.SHA, Problems: nonNil(v.Problems), Note: v.Note, SetupProblems: nonNil(v.SetupProblems), Remote: []map[string]any{}}
	if v.Main != "" {
		out.Remote = append(out.Remote, map[string]any{"branch": v.Branch, "present": v.Present, "same": v.Present})
	}
	if v.Present {
		text := v.Text
		out.Text = &text
	}
	if v.Approved != nil {
		out.Approved = map[string]any{"sha": v.Approved.SHA, "at": millis(v.Approved.At), "same": v.Same}
	}
	if s := v.Setup; s != nil {
		roles := map[string]string{}
		for r, a := range s.Roles {
			roles[string(r)] = a
		}
		rules := make([]map[string]string, 0, len(s.FileRules))
		for _, f := range s.FileRules {
			rules = append(rules, map[string]string{"pattern": f.Pattern, "agent": f.Agent, "instruction": f.Instruction})
		}
		out.Setup = map[string]any{"adapter": string(s.Adapter), "types": typesJSON(s.Types), "defaultType": s.DefaultType, "roles": roles,
			"agents": nonNil(s.Agents), "fileRules": rules, "rounds": s.Rounds, "protect": nonNil(s.Protect)}
	}
	return out
}

func (s *Server) workflowAPI(w http.ResponseWriter, r *http.Request, path string, body map[string]any) {
	if s.cfg.Work == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no repositories or agent-host, so it has no workflow."})
		return
	}
	sid := str(body["sid"], 80)
	switch path {
	case allowFileRoute, flagRemoveRoute:
		s.flag(r.Context(), w, path, sid, body)
	case allowCommitRoute, commitRewriteRoute:
		s.commitFlag(r.Context(), w, path, sid, body)
	case "/api/gate":
		if !keysWithin(body, "sid") || sid == "" {
			refuseFields(w)
			return
		}
		v, err := s.cfg.Work.View(r.Context(), sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		g := gateOf(v)
		info, _ := s.reg.Session(sid)
		g.Review, g.Pushed = offerOf(info, v), session.HeadPushed(info, v.Head)
		g.Path = pathOf(session.Path(info, v, g.Pushed))
		g.Flow = flowOf(session.LiveFlow(info, v, g.Pushed))
		g.TypeAsk = typeCardOf(session.WorkTypeCard(info, v))
		send(w, http.StatusOK, map[string]any{"gate": g})
	case "/api/size":
		size, ok := body["size"].(string)
		if !keysWithin(body, "sid", "size") || sid == "" || !ok || !workTypeID.MatchString(size) {
			refuseFields(w)
			return
		}
		err := s.cfg.Work.SetType(r.Context(), sid, size)
		shared.For(r.Context(), s.logger).Info("work type", "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "/api/approve-plan", "/api/approve-requirements":
		hash, okHash := body["hash"].(string)
		text, okText := body["text"].(string)
		if !keysWithin(body, "sid", "hash", "text") || sid == "" || !okHash || !hash256.MatchString(hash) || !okText {
			refuseFields(w)
			return
		}
		err := s.cfg.Work.Approve(r.Context(), sid, path == "/api/approve-plan", hash, text)
		shared.For(r.Context(), s.logger).Info("approval", "plan", path == "/api/approve-plan", "ok", err == nil)
		reply(w, err, http.StatusOK)
	case lockTestsRoute:
		op, okOp := body["op"].(string)
		if !okOp || sid == "" {
			refuseFields(w)
			return
		}
		var err error
		switch op {
		case "lock":
			head, okHead := body["head"].(string)
			if !keysWithin(body, "sid", "op", "head") || !okHead || !flagCommit.MatchString(head) {
				refuseFields(w)
				return
			}
			err = s.cfg.Work.LockTests(r.Context(), sid, head)
		case "unlock":
			if !keysWithin(body, "sid", "op") {
				refuseFields(w)
				return
			}
			err = s.cfg.Work.UnlockTests(sid)
		default:
			refuse(w, http.StatusBadRequest, "op", saidOp)
			return
		}
		shared.For(r.Context(), s.logger).Info("test lock", "op", op, "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "/api/team":
		repo, okPath := body["path"].(string)
		op, okOp := body["op"].(string)
		if !keysWithin(body, "path", "op", "sha") || !okPath || len(repo) > session.MaxCwd || !okOp {
			refuseFields(w)
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(manageFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), manageFor)
		defer cancel()
		var v session.TeamView
		var err error
		switch op {
		case "read":
			v, err = s.cfg.Work.Team(ctx, repo)
		case "approve":
			sha, ok := body["sha"].(string)
			if !ok || !hash256.MatchString(sha) {
				refuse(w, http.StatusBadRequest, "sha", "That isn't the SHA-256 of a team file. Reload the team file and approve it again.")
				return
			}
			v, err = s.cfg.Work.ApproveTeam(ctx, repo, sha)
		case "forget":
			v, err = s.cfg.Work.ForgetTeam(repo)
		default:
			refuse(w, http.StatusBadRequest, "op", saidOp)
			return
		}
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, teamOf(v))
	case "/api/project":
		repo, okPath := body["path"].(string)
		op, okOp := body["op"].(string)
		if okOp && op == "hold" {
			s.projectHold(w, body)
			return
		}
		if !keysWithin(body, "path", "op", "rounds", "type", "findings", "on") || !okPath || len(repo) > session.MaxCwd || !okOp {
			refuseFields(w)
			return
		}
		rounds, kind := 0, ""
		switch op {
		case session.ProjectPRComment:
			on, ok := body["on"].(bool)
			if !ok || !keysWithin(body, "path", "op", "on") {
				refuse(w, http.StatusBadRequest, "on", saidFields)
				return
			}
			err := s.cfg.Work.SetProjectOption(repo, op, on)
			shared.For(r.Context(), s.logger).Info("project setting", "op", op, "on", on, "ok", err == nil)
			reply(w, err, http.StatusOK)
			return
		case "rounds":
			n, ok := integer(body["rounds"])
			if !ok || n < 1 || n > 20 || !keysWithin(body, "path", "op", "rounds") {
				refuse(w, http.StatusBadRequest, "rounds", "Review rounds are a whole number from 1 to 20.")
				return
			}
			rounds = int(n)
		case "type":
			t, ok := body["type"].(string)
			if !ok || (t != "" && !workTypeID.MatchString(t)) || !keysWithin(body, "path", "op", "type") {
				refuse(w, http.StatusBadRequest, "type", "That isn't a work type the engine knows. Reload the page and pick one from the list.")
				return
			}
			kind = t
		case "findings":
			on, ok := body["findings"].(bool)
			if !ok || !keysWithin(body, "path", "op", "findings") {
				refuse(w, http.StatusBadRequest, "findings", saidFields)
				return
			}
			err := s.cfg.Work.SetAutoFindings(repo, on)
			shared.For(r.Context(), s.logger).Info("project setting", "op", op, "ok", err == nil)
			reply(w, err, http.StatusOK)
			return
		default:
			send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine keeps a project's review rounds, default work type, automatic findings, pull request comments and push hold only."})
			return
		}
		err := s.cfg.Work.SetProject(r.Context(), repo, op, rounds, kind)
		shared.For(r.Context(), s.logger).Info("project setting", "op", op, "ok", err == nil)
		reply(w, err, http.StatusOK)
	}
}
