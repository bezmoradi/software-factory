package session

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	PushRecordFormat  = "software-factory-push-record/1"
	MaxPushRecord     = 256 << 10
	pushRecordLines   = AuditReadAll
	pushRecordLine    = 600
	pushRecordShort   = 120
	pushRecordNoTail  = "[left out: the record would be over 256 KiB]"
	pushRecordChecked = "Reported by the agent from inside its container; the engine didn't run these. They are the last test, lint, type-check and build commands it ran before this push."
	pushRecordNoCheck = "None held: the agent reported no test, lint, type-check or build command before this push, or the engine restarted since, as it keeps them in memory only."
	pushRecordNoRun   = "The engine holds no verification run of this commit from before the push."
	pushRecordHeader  = "Built by Software Factory from its own records: the audit file, the review records, the approvals and the verification runs. Text it didn't write itself: the person's reasons, the reviewers' verdict lines, the approved requirements, plan or root cause, the commands under agentReported, which the agent reported, and the check's output under verification.tail. Tokens and keys are masked."
)

type PushRecord struct {
	Name string
	Text []byte
}

type PushRecordPage interface {
	PushRecord(ctx context.Context, sid, commit string) (PushRecord, error)
}

type PushRecordWork interface {
	SavedApprovals(sid string) (req, plan *SavedApproval, workType string, ok bool)
}

type PushRecordsConfig struct {
	Targets    func(sid string) (PushTarget, bool)
	Audit      AuditReader
	Reviews    func() (map[review.Key]ReviewEntry, error)
	Work       PushRecordWork
	Checks     func(sid string) []AgentCheck
	CardChecks func(sid, commit string) ([]AgentCheck, bool)
	Verify     func(sid, commit string, at time.Time) (VerifyRun, string, bool)
	Version    string
	Now        func() time.Time
}

type PushRecords struct {
	cfg PushRecordsConfig
}

var _ PushRecordPage = (*PushRecords)(nil)

func NewPushRecords(cfg PushRecordsConfig) *PushRecords {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PushRecords{cfg: cfg}
}

type pushRecordJSON struct {
	Format        string                `json:"format"`
	About         string                `json:"about"`
	Made          time.Time             `json:"made"`
	Engine        string                `json:"engine"`
	Project       string                `json:"project"`
	Remote        string                `json:"remote"`
	Branch        string                `json:"branch"`
	Session       string                `json:"session"`
	Commit        string                `json:"commit"`
	Push          pushLineJSON          `json:"push"`
	Allowed       *pushLineJSON         `json:"allowed"`
	Reviews       []pushReviewJSON      `json:"reviews"`
	Approvals     pushApprovalsJSON     `json:"approvals"`
	Exceptions    []pushExceptionJSON   `json:"exceptions"`
	AgentReported pushAgentReportedJSON `json:"agentReported"`
	Verification  *pushVerificationJSON `json:"verification"`
	Problems      []string              `json:"problems"`
}

type pushLineJSON struct {
	Time         time.Time `json:"time"`
	By           string    `json:"by"`
	Result       string    `json:"result"`
	Request      string    `json:"request"`
	Reason       string    `json:"reason"`
	Missing      []string  `json:"missing"`
	PersonReason string    `json:"personReason"`
}

type pushReviewJSON struct {
	Agent          string    `json:"agent"`
	Role           string    `json:"role"`
	Run            int       `json:"run"`
	Provider       string    `json:"provider"`
	State          string    `json:"state"`
	Verdict        string    `json:"verdict"`
	Override       string    `json:"override"`
	Advisory       bool      `json:"advisory"`
	Base           string    `json:"base"`
	Since          string    `json:"since"`
	Covers         string    `json:"covers"`
	ApprovedDigest string    `json:"approvedDigest"`
	TeamDigest     string    `json:"teamDigest"`
	Pack           string    `json:"pack"`
	PackVersion    string    `json:"packVersion"`
	PackDigest     string    `json:"packDigest"`
	PluginCommit   string    `json:"pluginCommit"`
	ClaudeCode     string    `json:"claudeCode"`
	Models         []string  `json:"models"`
	RunModel       string    `json:"runModel"`
	RunEffort      string    `json:"runEffort"`
	Turns          int       `json:"turns"`
	StartedAt      time.Time `json:"startedAt"`
	EndedAt        time.Time `json:"endedAt"`
	CostUSD        float64   `json:"costUsd"`
}

type pushApprovalJSON struct {
	Hash        string    `json:"hash"`
	Commit      string    `json:"commit"`
	Time        time.Time `json:"time"`
	Text        string    `json:"text,omitempty"`
	TextLeftOut bool      `json:"textLeftOut,omitempty"`
	TextNotKept bool      `json:"textNotKept,omitempty"`
}

type pushApprovalsJSON struct {
	WorkType     string            `json:"workType"`
	Requirements *pushApprovalJSON `json:"requirements"`
	Plan         *pushApprovalJSON `json:"plan"`
}

type pushExceptionJSON struct {
	Kind         string    `json:"kind"`
	Time         time.Time `json:"time"`
	Commit       string    `json:"commit"`
	Reviewer     string    `json:"reviewer,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	Path         string    `json:"path,omitempty"`
	PersonReason string    `json:"personReason"`
}

type pushCheckJSON struct {
	Kind     string     `json:"kind"`
	Command  string     `json:"command"`
	OK       bool       `json:"ok"`
	Done     bool       `json:"done"`
	Hidden   string     `json:"hidden,omitempty"`
	At       time.Time  `json:"at"`
	EditedAt *time.Time `json:"editedAt,omitempty"`
}

type pushAgentReportedJSON struct {
	Note   string          `json:"note"`
	Held   bool            `json:"held"`
	Checks []pushCheckJSON `json:"checks"`
}

type pushVerificationJSON struct {
	Run         string    `json:"run"`
	Commit      string    `json:"commit"`
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	Code        int       `json:"code"`
	Reason      string    `json:"reason"`
	By          string    `json:"by"`
	Command     string    `json:"command"`
	CommandHash string    `json:"commandHash"`
	Image       string    `json:"image"`
	ImageID     string    `json:"imageId"`
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended"`
	Tail        string    `json:"tail"`
	Cut         bool      `json:"cut"`
	Withheld    bool      `json:"withheld"`
}

func (p *PushRecords) PushRecord(ctx context.Context, sid, commit string) (PushRecord, error) {
	if p.cfg.Targets == nil || p.cfg.Audit == nil {
		return PushRecord{}, conflict("not started here", "Only a session this engine started has push records here.")
	}
	t, ok := p.cfg.Targets(sid)
	if !ok {
		return PushRecord{}, conflict("not started here", "Only a session this engine started has push records here.")
	}
	lines, err := p.cfg.Audit.Read(t.Name, t.Branch, pushRecordLines)
	if err != nil {
		return PushRecord{}, err
	}
	pushed, found := pushedLine(lines, sid, commit)
	if !found {
		return PushRecord{}, notFound("push", "The engine's audit has no push of that commit on this branch.")
	}
	if err := ctx.Err(); err != nil {
		return PushRecord{}, err
	}
	out := pushRecordJSON{Format: PushRecordFormat, About: pushRecordHeader, Made: p.cfg.Now().UTC(), Engine: CleanLine(p.cfg.Version, pushRecordShort), Project: t.Name,
		Remote: maskLines(pushed.Remote, true), Branch: t.Branch, Session: sid, Commit: commit, Push: pushLineOf(pushed),
		Reviews: []pushReviewJSON{}, Exceptions: []pushExceptionJSON{}, Problems: []string{}}
	if allowed, ok := allowedLine(lines, pushed); ok {
		a := pushLineOf(allowed)
		out.Allowed = &a
	}
	out.Reviews, out.Problems = p.reviews(t, commit, out.Problems)
	out.Approvals = p.approvals(lines, sid, pushed.Time)
	out.Exceptions = exceptionsOf(lines, pushed.Time)
	out.AgentReported = p.agentReported(sid, commit, pushed.Time)
	if v, ok := p.verification(sid, commit, pushed.Time); ok {
		out.Verification = &v
	} else {
		out.Problems = append(out.Problems, pushRecordNoRun)
	}
	text, err := fitRecord(&out)
	if err != nil {
		return PushRecord{}, err
	}
	return PushRecord{Name: "software-factory-push-" + shortSHA(commit) + ".json", Text: text}, nil
}

func pushedLine(lines []AuditEntry, sid, commit string) (AuditEntry, bool) {
	for _, e := range lines {
		if (e.Kind == "" || e.Kind == AuditPush) && e.Result == string(gate.Pushed) && e.Commit == commit && e.Session == sid {
			return e, true
		}
	}
	return AuditEntry{}, false
}

func allowedLine(lines []AuditEntry, pushed AuditEntry) (AuditEntry, bool) {
	if pushed.Request == "" {
		return AuditEntry{}, false
	}
	for _, e := range lines {
		if (e.Kind == "" || e.Kind == AuditPush) && e.Result == string(gate.Allowed) && e.Request == pushed.Request && e.Session == pushed.Session {
			return e, true
		}
	}
	return AuditEntry{}, false
}

func pushLineOf(e AuditEntry) pushLineJSON {
	e = e.masked()
	missing := make([]string, 0, len(e.Missing))
	for _, m := range e.Missing {
		missing = append(missing, CleanLine(m, pushRecordLine))
	}
	return pushLineJSON{Time: e.Time.UTC(), By: e.By, Result: e.Result, Request: e.Request, Reason: CleanLine(e.Reason, maxPushReason), Missing: missing, PersonReason: CleanLine(e.PersonReason, maxPushReason)}
}

func (p *PushRecords) reviews(t PushTarget, commit string, problems []string) ([]pushReviewJSON, []string) {
	out := []pushReviewJSON{}
	if p.cfg.Reviews == nil {
		return out, append(problems, "The engine has no review records here.")
	}
	entries, err := p.cfg.Reviews()
	if err != nil {
		return out, append(problems, "The review records couldn't be read.")
	}
	for k, e := range entries {
		if k.Commit != commit || k.Branch != t.Branch {
			continue
		}
		out = append(out, reviewOf(k, e))
	}
	slices.SortFunc(out, func(a, b pushReviewJSON) int {
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.Run, b.Run))
	})
	if len(out) == 0 {
		problems = append(problems, "The review records hold no review of this commit on this branch: none ran, or the records kept only the latest 500.")
	}
	return out, problems
}

func reviewOf(k review.Key, e ReviewEntry) pushReviewJSON {
	models := make([]string, 0, len(e.Models))
	for _, m := range e.Models {
		models = append(models, CleanLine(maskSecrets(m), pushRecordShort))
	}
	return pushReviewJSON{Agent: CleanLine(k.Agent, pushRecordShort), Role: CleanLine(string(e.Role), pushRecordShort), Run: k.Number(), Provider: CleanLine(e.Record.Provider, pushRecordShort),
		State: string(e.Record.State), Verdict: CleanLine(maskSecrets(e.Record.Verdict), pushRecordLine), Override: CleanLine(maskCut(e.Record.Override), maxPushReason), Advisory: e.Record.Advisory,
		Base: e.Base, Since: e.Since, Covers: e.Covers, ApprovedDigest: e.Approved, TeamDigest: e.Team,
		Pack: CleanLine(maskSecrets(e.Pack), pushRecordShort), PackVersion: CleanLine(maskSecrets(e.PackVersion), pushRecordShort), PackDigest: CleanLine(e.PackDigest, pushRecordShort),
		PluginCommit: e.PluginCommit, ClaudeCode: CleanLine(e.ClaudeCode, pushRecordShort), Models: models, RunModel: CleanLine(maskSecrets(e.RunModel), pushRecordShort),
		RunEffort: CleanLine(e.RunEffort, pushRecordShort), Turns: e.Turns, StartedAt: e.StartedAt.UTC(), EndedAt: e.EndedAt.UTC(), CostUSD: e.CostUSD}
}

func (p *PushRecords) approvals(lines []AuditEntry, sid string, at time.Time) pushApprovalsJSON {
	var out pushApprovalsJSON
	var req, plan *SavedApproval
	if p.cfg.Work != nil {
		r, pl, kind, ok := p.cfg.Work.SavedApprovals(sid)
		if ok {
			out.WorkType, req, plan = CleanLine(kind, pushRecordShort), r, pl
		}
	}
	textOf := func(a *pushApprovalJSON, saved *SavedApproval, hash string) {
		if saved != nil && saved.Hash != "" && saved.Hash == hash && saved.Text != "" {
			a.Text = maskSecrets(saved.Text)
		} else {
			a.TextNotKept = true
		}
	}
	for _, e := range lines {
		if e.Session != sid || e.Time.After(at) {
			continue
		}
		a := &pushApprovalJSON{Hash: e.Hash, Commit: e.Commit, Time: e.Time.UTC()}
		switch {
		case e.Kind == AuditApproveRequirements && out.Requirements == nil:
			textOf(a, req, e.Hash)
			out.Requirements = a
		case e.Kind == AuditApprovePlan && out.Plan == nil:
			textOf(a, plan, e.Hash)
			out.Plan = a
		}
	}
	return out
}

func exceptionsOf(lines []AuditEntry, at time.Time) []pushExceptionJSON {
	out := []pushExceptionJSON{}
	for _, e := range slices.Backward(lines) {
		if e.Time.After(at) {
			continue
		}
		switch e.Kind {
		case AuditOverride, AuditAllowFile, AuditAllowCommit:
		default:
			continue
		}
		e = e.masked()
		out = append(out, pushExceptionJSON{Kind: e.Kind, Time: e.Time.UTC(), Commit: e.Commit, Reviewer: CleanLine(e.Reviewer, pushRecordShort), Verdict: CleanLine(maskSecrets(e.Verdict), pushRecordLine),
			Path: CleanLine(e.Path, pushRecordLine), PersonReason: CleanLine(e.PersonReason, maxPushReason)})
	}
	return out
}

func (p *PushRecords) agentReported(sid, commit string, at time.Time) pushAgentReportedJSON {
	var checks []AgentCheck
	held := false
	if p.cfg.CardChecks != nil {
		checks, held = p.cfg.CardChecks(sid, commit)
	}
	if !held && p.cfg.Checks != nil {
		for _, c := range p.cfg.Checks(sid) {
			if !c.At.After(at) {
				checks = append(checks, c)
			}
		}
		held = len(checks) > 0
	}
	if !held {
		return pushAgentReportedJSON{Note: pushRecordNoCheck, Checks: []pushCheckJSON{}}
	}
	out := pushAgentReportedJSON{Note: pushRecordChecked, Held: true, Checks: make([]pushCheckJSON, 0, len(checks))}
	for _, c := range checks {
		j := pushCheckJSON{Kind: string(c.Kind), Command: CleanLine(maskSecrets(c.Command), maxCheckCommand), OK: c.OK, Done: c.Done, Hidden: string(c.Hidden), At: c.At.UTC()}
		if !c.EditedAt.IsZero() {
			edited := c.EditedAt.UTC()
			j.EditedAt = &edited
		}
		out.Checks = append(out.Checks, j)
	}
	return out
}

func (p *PushRecords) verification(sid, commit string, at time.Time) (pushVerificationJSON, bool) {
	if p.cfg.Verify == nil {
		return pushVerificationJSON{}, false
	}
	r, command, ok := p.cfg.Verify(sid, commit, at)
	if !ok || r.Commit != commit {
		return pushVerificationJSON{}, false
	}
	tail := ""
	if !r.Withheld && !secretText(r.Tail) {
		tail = maskSecrets(r.Tail)
	}
	return pushVerificationJSON{Run: r.ID, Commit: r.Commit, Kind: string(r.Kind), State: r.State, Code: r.Code, Reason: CleanLine(maskSecrets(r.Reason), maxVerifyReason), By: r.By,
		Command: command, CommandHash: r.CommandHash, Image: CleanLine(r.Image, MaxVerifyImage), ImageID: CleanLine(r.ImageID, pushRecordShort), Started: r.Started.UTC(), Ended: r.Ended.UTC(),
		Tail: tail, Cut: r.Cut, Withheld: r.Withheld || tail == "" && r.Tail != ""}, true
}

func fitRecord(out *pushRecordJSON) ([]byte, error) {
	text, err := json.MarshalIndent(out, "", "  ")
	if err != nil || len(text) <= MaxPushRecord {
		return text, err
	}
	steps := []func() bool{
		func() bool {
			if out.Approvals.Plan == nil || out.Approvals.Plan.Text == "" {
				return false
			}
			out.Approvals.Plan.Text, out.Approvals.Plan.TextLeftOut = "", true
			return true
		},
		func() bool {
			if out.Approvals.Requirements == nil || out.Approvals.Requirements.Text == "" {
				return false
			}
			out.Approvals.Requirements.Text, out.Approvals.Requirements.TextLeftOut = "", true
			return true
		},
		func() bool {
			if out.Verification == nil || out.Verification.Tail == "" {
				return false
			}
			out.Verification.Tail = pushRecordNoTail
			return true
		},
		func() bool {
			if len(out.AgentReported.Checks) == 0 {
				return false
			}
			out.AgentReported.Checks = out.AgentReported.Checks[:len(out.AgentReported.Checks)/2]
			return true
		},
		func() bool {
			if len(out.Exceptions) == 0 {
				return false
			}
			out.Exceptions = out.Exceptions[len(out.Exceptions)/2:]
			return true
		},
		func() bool {
			if len(out.Reviews) == 0 {
				return false
			}
			out.Reviews = out.Reviews[:len(out.Reviews)/2]
			return true
		},
	}
	out.Problems = append(out.Problems, "Parts of this record are left out, since the whole would be over 256 KiB; the fields that say so mark where.")
	for _, step := range steps {
		for step() {
			text, err = json.MarshalIndent(out, "", "  ")
			if err != nil || len(text) <= MaxPushRecord {
				return text, err
			}
		}
	}
	return text, nil
}

func (w *Work) SavedApprovals(sid string) (req, plan *SavedApproval, workType string, ok bool) {
	v := w.saved(sid)
	if v == nil {
		return nil, nil, "", false
	}
	return v.Requirements, v.Plan, v.Type, true
}

func (p *Pushes) CardChecks(sid, commit string) ([]AgentCheck, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.views[sid]
	if !ok || v.Result != string(gate.Pushed) || v.Commit != commit {
		return nil, false
	}
	return slices.Clone(v.Checks), len(v.Checks) > 0
}

func (v *Verifier) PushedRun(sid, commit string, at time.Time) (VerifyRun, string, bool) {
	t, ok := v.targetOf(sid)
	if !ok {
		return VerifyRun{}, "", false
	}
	id, cmds, known := v.project(t.Name)
	if !known {
		return VerifyRun{}, "", false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range slices.Backward(v.runs) {
		if r.Project != id || r.Commit != commit || r.Kind != VerifyCheck || r.Started.After(at) {
			continue
		}
		command := ""
		if r.CommandHash == commandHash(cmds.Check) {
			command = shownCommand(cmds.Check)
		}
		return r, command, true
	}
	return VerifyRun{}, "", false
}
