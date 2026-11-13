package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const MaxApproveBody = 1 << 20

var (
	workflowRoutes = map[string]bool{"/api/gate": true, "/api/size": true, "/api/approve-plan": true, "/api/approve-requirements": true, "/api/team": true, "/api/project": true, allowFileRoute: true, flagRemoveRoute: true}
	approveRoutes  = map[string]bool{"/api/approve-plan": true, "/api/approve-requirements": true}
	hash256        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	workTypeID     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type workDocJSON struct {
	Path     string `json:"path"`
	Text     string `json:"text"`
	Hash     string `json:"hash"`
	Approved bool   `json:"approved"`
	Changed  bool   `json:"changed"`
	Clipped  bool   `json:"clipped"`
	Previous string `json:"previous,omitempty"`
}

type workTypeJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Steps   []string `json:"steps"`
	Edits   string   `json:"edits"`
	Push    bool     `json:"push"`
	Roles   []string `json:"roles"`
	Builtin bool     `json:"builtin"`
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
	Types        []workTypeJSON     `json:"types"`
	Commands     map[string]string  `json:"commands"`
	Team         map[string]any     `json:"team"`
	Requirements *workDocJSON       `json:"requirements"`
	Plan         *workDocJSON       `json:"planDoc"`
	PlanPath     string             `json:"plan"`
	PlanHash     string             `json:"planHash"`
	Approved     bool               `json:"approved"`
	Research     string             `json:"research"`
	Tasks        bool               `json:"tasks"`
	TasksWritten bool               `json:"tasksWritten"`
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
}

func docJSON(d *session.WorkDoc) *workDocJSON {
	if d == nil {
		return nil
	}
	return &workDocJSON{Path: d.Path, Text: d.Text, Hash: d.Hash, Approved: d.Approved, Changed: d.Changed, Clipped: d.Clipped, Previous: d.Previous}
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
		out = append(out, workTypeJSON{ID: t.ID, Name: t.Name, Steps: steps, Edits: string(t.Edits), Push: t.Push, Roles: roles, Builtin: t.Builtin})
	}
	return out
}

func gateOf(v session.GateView) gateJSON {
	out := gateJSON{Head: v.Head, Branch: v.Branch, Adapter: string(v.Adapter), Size: string(profileSize(v)), WorkType: v.Type, Chosen: v.Chosen,
		Effective: string(v.Effective), SizeNote: v.Note, Types: typesJSON(v.Types), Commands: map[string]string{},
		Team: map[string]any{"approved": v.TeamApproved, "note": v.TeamNote}, Requirements: docJSON(v.Requirements), Plan: docJSON(v.Plan),
		Research: v.Research, Tasks: v.TasksNeeded, TasksWritten: v.TasksWritten, Define: v.Define, Reviewers: []gateReviewerJSON{},
		Missing: nonNil(v.Missing), Allowed: v.Allowed, Waiting: v.Waiting, Implemented: v.Waiting == "" && v.Effective != workflow.Exploring,
		Holds: v.Holds, Rounds: v.Rounds, Committed: v.PlanApprovals, Flagged: flagsJSON(v.Flagged), FlagsRead: v.FlagsRead, LFS: nonNil(v.LFS), Handoff: v.Handoff}
	for step, c := range v.Commands {
		out.Commands[string(step)] = c
	}
	if v.Plan != nil {
		out.PlanPath, out.PlanHash, out.Approved = v.Plan.Path, v.Plan.Hash, v.Plan.Approved
	}
	for _, r := range v.Reviewers {
		out.Reviewers = append(out.Reviewers, gateReviewerJSON{Type: r.Agent, Role: string(r.Role), Name: r.Name, Why: r.Why, State: r.State, Verdict: r.Verdict,
			Round: r.Round, Limit: r.Limit, Capped: r.Capped, Override: r.Override, Since: r.Since, Incremental: r.Since != ""})
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
			rules = append(rules, map[string]string{"pattern": f.Pattern, "agent": f.Agent})
		}
		out.Setup = map[string]any{"adapter": string(s.Adapter), "types": typesJSON(s.Types), "defaultType": s.DefaultType, "roles": roles,
			"agents": nonNil(s.Agents), "fileRules": rules, "rounds": s.Rounds}
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
		s.flag(w, path, sid, body)
	case "/api/gate":
		if !keysWithin(body, "sid") || sid == "" {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err := s.cfg.Work.View(sid)
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, map[string]any{"gate": gateOf(v)})
	case "/api/size":
		size, ok := body["size"].(string)
		if !keysWithin(body, "sid", "size") || sid == "" || !ok || !workTypeID.MatchString(size) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Work.SetType(sid, size)
		s.logger.Info("work type", "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "/api/approve-plan", "/api/approve-requirements":
		hash, okHash := body["hash"].(string)
		text, okText := body["text"].(string)
		if !keysWithin(body, "sid", "hash", "text") || sid == "" || !okHash || !hash256.MatchString(hash) || !okText {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		err := s.cfg.Work.Approve(sid, path == "/api/approve-plan", hash, text)
		s.logger.Info("approval", "plan", path == "/api/approve-plan", "ok", err == nil)
		reply(w, err, http.StatusOK)
	case "/api/team":
		repo, okPath := body["path"].(string)
		op, okOp := body["op"].(string)
		if !keysWithin(body, "path", "op", "sha") || !okPath || len(repo) > session.MaxCwd || !okOp {
			refuse(w, http.StatusBadRequest, "fields")
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
				refuse(w, http.StatusBadRequest, "sha")
				return
			}
			v, err = s.cfg.Work.ApproveTeam(ctx, repo, sha)
		case "forget":
			v, err = s.cfg.Work.ForgetTeam(repo)
		default:
			refuse(w, http.StatusBadRequest, "op")
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
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		rounds, kind := 0, ""
		switch op {
		case session.ProjectPRComment:
			on, ok := body["on"].(bool)
			if !ok || !keysWithin(body, "path", "op", "on") {
				refuse(w, http.StatusBadRequest, "on")
				return
			}
			err := s.cfg.Work.SetProjectOption(repo, op, on)
			s.logger.Info("project setting", "op", op, "on", on, "ok", err == nil)
			reply(w, err, http.StatusOK)
			return
		case "rounds":
			n, ok := integer(body["rounds"])
			if !ok || n < 1 || n > 20 || !keysWithin(body, "path", "op", "rounds") {
				refuse(w, http.StatusBadRequest, "rounds")
				return
			}
			rounds = int(n)
		case "type":
			t, ok := body["type"].(string)
			if !ok || (t != "" && !workTypeID.MatchString(t)) || !keysWithin(body, "path", "op", "type") {
				refuse(w, http.StatusBadRequest, "type")
				return
			}
			kind = t
		case "findings":
			on, ok := body["findings"].(bool)
			if !ok || !keysWithin(body, "path", "op", "findings") {
				refuse(w, http.StatusBadRequest, "findings")
				return
			}
			err := s.cfg.Work.SetAutoFindings(repo, on)
			s.logger.Info("project setting", "op", op, "ok", err == nil)
			reply(w, err, http.StatusOK)
			return
		default:
			send(w, http.StatusBadRequest, problem{Error: "op", Message: "The engine keeps a project's review rounds, default work type, automatic findings, pull request comments and push hold only."})
			return
		}
		err := s.cfg.Work.SetProject(repo, op, rounds, kind)
		s.logger.Info("project setting", "op", op, "ok", err == nil)
		reply(w, err, http.StatusOK)
	}
}
