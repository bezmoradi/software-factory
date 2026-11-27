package session

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	StageDone    = "done"
	StageCurrent = "current"
	StageWaiting = "waiting"
)

type PathAction struct {
	Ask   string
	Label string
	Title string
}

type PathStage struct {
	ID      string
	Name    string
	State   string
	Status  string
	Active  bool
	Detail  string
	Actions []PathAction
}

type PathCard struct {
	Step   string
	Count  string
	Detail string
	Bar    []float64
}

type PathLine struct {
	Text    string
	Actions []PathAction
	Tone    string
}

type PathModel struct {
	Stages        []PathStage
	Line          *PathLine
	Notice        *PathLine
	Card          PathCard
	Missing       []string
	ReviewsActive bool
	Required      []string
	Asks          []PathAction
	Busy          bool
}

type mark struct {
	done   bool
	detail string
	card   string
}

func markDone(ok bool, detail string) mark { return mark{done: ok, detail: detail} }

func (m mark) complete() bool { return m.done }

func (m mark) shortly(card string) mark {
	m.card = card
	return m
}

func (m mark) cardText() string {
	if m.card != "" {
		return m.card
	}
	return m.detail
}

var reviewOK = []string{"pass", "overridden"}

var activeAgent = []string{"pending", "running", "waiting"}

type pathTurn struct {
	ran     map[string]bool
	running string
	busy    bool
	active  string
	off     bool
	dirty   bool
}

func CauseStatus(text string) string {
	lines := strings.Split(text, "\n")
	at := slices.IndexFunc(lines, func(l string) bool { return strings.TrimRight(l, " \t") == "## Status" })
	if at < 0 {
		return ""
	}
	for _, l := range lines[at+1:] {
		if strings.HasPrefix(l, "#") {
			return ""
		}
		if f := strings.Fields(strings.ReplaceAll(l, "\t", " ")); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

func press(t pathTurn, step string) string {
	if t.ran[step] {
		return hint(t, ": press Continue")
	}
	return hint(t, ": press Start")
}

const (
	continueDefine = "Continue defining"
	continueTriage = "Continue triage"
)

func pressDefine(t pathTurn) string {
	if !t.ran["define"] {
		return ": send what you want built"
	}
	return hint(t, ": press "+continueDefine)
}

func hint(t pathTurn, text string) string {
	if t.off || t.busy {
		return ""
	}
	return text
}

func reviewCount(done, all int) string {
	return reviewsPass(done, all) + " on this commit"
}

func reviewsPass(done, all int) string {
	word := "reviews pass"
	if all == 1 {
		word = "review passes"
	}
	return strconv.Itoa(done) + " of " + strconv.Itoa(all) + " " + word
}

func reviewsCard(done, all, overridden int, verified bool) string {
	switch {
	case !verified:
		return reviewsPass(done, all)
	case overridden == 1:
		return "Reviews pass, one overridden"
	case overridden > 1:
		return "Reviews pass, " + strconv.Itoa(overridden) + " overridden"
	}
	return "Every review passes"
}

func busyCard(t pathTurn, card string) string {
	if !t.busy {
		return ""
	}
	return card
}

func headAt(v GateView) string {
	if v.Head == "" {
		return "Couldn't read the current commit"
	}
	if v.Branch != "" {
		return "On " + v.Branch + " at " + shortSHA7(v.Head)
	}
	return "At " + shortSHA7(v.Head)
}

func incidentMarks(v GateView, t pathTurn) map[string]mark {
	d := v.Plan
	status := ""
	if d != nil {
		status = CauseStatus(d.Text)
	}
	shown := firstRunes(status, 40)
	reproduced := status == "REPRODUCED" || status == "CONFIRMED" || status == "UNCONFIRMED"
	var triage mark
	switch {
	case status != "":
		triage = markDone(true, "Triaged · "+path.Base(d.Path)+" says "+shown)
	case !t.ran["triage"]:
		triage = markDone(false, "No triage yet: describe the incident in a message, with its evidence and the deployed commit or tag")
	case t.busy:
		triage = markDone(false, "Triaging the incident")
	default:
		triage = markDone(false, "No triage yet: give the evidence and the deployed commit or tag in a message"+hint(t, ", then press "+continueTriage))
	}
	var reproduce mark
	switch {
	case reproduced:
		reproduce = markDone(true, "Reproduced at the deployed commit")
	case status == "BLOCKED":
		reproduce = markDone(false, "Blocked: the file names the evidence or environment it needs. Give it in a message"+hint(t, ", then press Continue"))
	case status == "TRIAGED":
		reproduce = markDone(false, "Next: a reproduction at the deployed commit, run and shown to fail"+hint(t, ": press Continue")).shortly(busyCard(t, "Reproducing the failure"))
	case status != "":
		reproduce = markDone(false, "The file's Status is "+shown+", which knackpack doesn't use")
	}
	var cause mark
	switch {
	case status == "CONFIRMED" && d.Approved:
		cause = markDone(true, "Cause confirmed · approved")
	case status == "CONFIRMED" && t.busy:
		cause = markDone(false, "Still diagnosing the cause")
	case status == "CONFIRMED" && d.Changed:
		cause = markDone(false, "Cause confirmed, but the file changed after you approved it: read it and approve again")
	case status == "CONFIRMED":
		cause = markDone(false, "Cause confirmed · waiting for your approval")
	case status == "UNCONFIRMED":
		cause = markDone(false, "No cause confirmed yet: the file says what would decide. Give it in a message"+hint(t, ", then press Continue"))
	case status == "REPRODUCED":
		cause = markDone(false, "Next: ranked hypotheses, and a cause confirmed by a test that tells them apart"+hint(t, ": press Continue")).shortly(busyCard(t, "Testing the hypotheses"))
	}
	return map[string]mark{"triage": triage, "reproduce": reproduce, "cause": cause}
}

func pathMarks(v GateView, t pathTurn, pushed bool) map[string]mark {
	reviewers := v.Reviewers
	reqHash := v.Requirements != nil && v.Requirements.Hash != ""
	working := func(step string) bool { return t.active == step || t.running == step }
	var define mark
	switch {
	case v.Define:
		define = markDone(true, "Requirements approved")
	case working("define"):
		define = markDone(false, "Writing the requirements")
	case reqHash && t.busy && !working("plan"):
		define = markDone(false, "Still writing the requirements")
	case reqHash:
		define = markDone(false, "Agreed requirements wait for your approval")
	case v.Holds["define"] != "":
		define = markDone(false, "Start waits until the gate can check the working tree")
	default:
		define = markDone(false, "No agreed requirements yet"+pressDefine(t))
	}
	change := v.Profile == workflow.Change
	okCount, overridden := 0, 0
	for _, r := range reviewers {
		if slices.Contains(reviewOK, r.State) {
			okCount++
		}
		if r.State == "overridden" {
			overridden++
		}
	}
	verified := len(reviewers) > 0 && okCount == len(reviewers)
	at := headAt(v)
	wait := v.Waiting
	passed := "Every review passes on this commit"
	if slices.ContainsFunc(reviewers, func(r WorkReviewer) bool { return r.State == "overridden" }) {
		passed = "Reviews done on this commit, with an override"
	}
	running := slices.ContainsFunc(reviewers, func(r WorkReviewer) bool { return r.State == "running" })
	incident := v.Profile == workflow.Incident
	fixed := !incident || (v.Plan != nil && v.Plan.Approved)
	out := map[string]mark{}
	if incident {
		out = incidentMarks(v, t)
	}
	out["define"] = define
	if change {
		var plan mark
		name := ""
		if v.Plan != nil {
			name = path.Base(v.Plan.Path)
		}
		switch {
		case v.Plan == nil && working("plan"):
			plan = markDone(false, "Writing the plan")
		case v.Plan == nil && v.Holds["plan"] != "":
			plan = markDone(false, "Starts once you approve the requirements")
		case v.Plan == nil:
			plan = markDone(false, "Needs a plan"+press(t, "plan"))
		case !v.Plan.Approved && t.busy:
			plan = markDone(false, "Still writing the plan")
		case !v.Plan.Approved:
			plan = markDone(false, name+" · waiting for your approval")
		default:
			research := "skipped"
			if v.Research != "" {
				research = "done"
			}
			plan = markDone(true, name+" · approved · research "+research)
		}
		out["plan"] = plan
	}
	suffix := ""
	switch {
	case wait != "" && v.Head != "":
		suffix = " · " + wait
	case v.Started == StartedNoBase:
		suffix = " · no base branch, so the page can't tell this branch's commits"
	}
	if v.Profile == workflow.Direct && v.Type == workflow.DefaultType {
		suffix += " · this pack writes no requirements the engine can read, so nothing is approved before the change"
	}
	if fixed {
		out["implement"] = markDone(implementBuilt(v, t.busy, t.dirty), at+suffix+implementNote(wait, t)).shortly(at)
	} else {
		out["implement"] = markDone(false, at+" · Fix starts once you approve the root cause").shortly(at)
	}
	switch {
	case !verified && wait == "":
		out["verify"] = markDone(false, reviewCount(okCount, len(reviewers)))
	case !verified && running:
		out["verify"] = markDone(false, reviewCount(okCount, len(reviewers))+". Reviews are running before Implement is done: "+wait)
	case !verified:
		out["verify"] = markDone(false, reviewCount(okCount, len(reviewers))+". Reviews start once Implement is done: "+wait)
	case wait != "":
		out["verify"] = markDone(false, passed+", but Implement isn't done: "+wait)
	default:
		out["verify"] = markDone(true, passed)
	}
	out["verify"] = out["verify"].shortly(reviewsCard(okCount, len(reviewers), overridden, verified))
	switch {
	case pushed:
		to := ""
		if v.Branch != "" {
			to = " to " + v.Branch
		}
		out["push"] = markDone(true, "Pushed "+shortSHA7(v.Head)+to)
	case v.Allowed:
		out["push"] = markDone(true, "The gate has what it needs for this commit: Push sends it, once the sandbox check holds")
	default:
		out["push"] = markDone(false, "Push held until it has:")
	}
	return out
}

const (
	exploreLine   = "Changes files freely; nothing is pushed."
	exploreIssues = " Turn what you found into issues; you approve each one."
	issuesLabel   = "Draft issues"
	issuesTitle   = "The agent drafts issues from this session; you approve each one before it's opened"
	reviewLabel   = "Write the incident review"
	earlyText     = "The agent is changing files before the requirements are approved."
	earlyLabel    = "Ask for the requirements"
	outsideText   = "Working outside the project before your approval: nothing in the project has changed yet."
	toneWarn      = "warn"
	toneInfo      = "info"
)

var stageNames = map[string]string{"define": "Define", "plan": "Plan", "triage": "Triage", "reproduce": "Reproduce", "cause": "Root cause", "implement": "Implement", "verify": "Code review", "push": "Push"}

var incidentStages = []string{"triage", "reproduce", "cause"}

var stepStages = map[workflow.Step]string{workflow.Define: "define", workflow.Plan: "plan", workflow.Implement: "implement", workflow.Review: "verify", workflow.Push: "push"}

func pathStages(v GateView) []string {
	if v.Profile == workflow.Incident {
		return append(slices.Clone(incidentStages), "implement", "verify", "push")
	}
	steps := workflow.TypeOr(v.Types, v.Type).Steps
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, stepStages[s])
	}
	return out
}

func ReviewsActive(i Info, v GateView) bool {
	return slices.ContainsFunc(v.Reviewers, func(r WorkReviewer) bool {
		return r.State == "running" || slices.Contains(activeAgent, i.AgentStates[r.Agent])
	})
}

func awaitingStep(v GateView) string {
	switch {
	case v.Profile == workflow.Change && v.Requirements != nil && v.Requirements.Hash != "" && !v.Requirements.Approved:
		return "define"
	case v.Profile == workflow.Change && v.Plan != nil && !v.Plan.Approved:
		return "plan"
	}
	return ""
}

func stageTitle(ask, command string) string {
	switch ask {
	case AskFix:
		return "Sends /knackpack:fix-incident to this session, with the SHA-256 of the root cause you approved"
	case AskCommit:
		return "Asks this session to commit every uncommitted change, and to ask you about any file that shouldn't be committed"
	case AskIssues:
		return issuesTitle
	case AskIncidentReview:
		return "Sends " + command + " to this session, which commits a draft incident review"
	case AskRequirements:
		return "Sends " + strings.Fields(command)[0] + " to this session, asking the agent to agree the requirements with you before it changes more files"
	}
	return "Sends " + command + " to this session"
}

func Path(i Info, v GateView, pushed bool) PathModel {
	commands := stageCommands(v)
	reviewsActive := ReviewsActive(i, v)
	asks := sessionAsks(commands)
	busy := agentWorking(i)
	if v.Profile == workflow.Explore {
		line := &PathLine{Text: exploreLine, Actions: asks}
		if len(asks) > 0 {
			line.Text += exploreIssues
		}
		return PathModel{Stages: []PathStage{}, Line: line, Card: PathCard{Step: workflow.TypeOr(v.Types, v.Type).Name, Detail: headAt(v), Bar: []float64{}}, ReviewsActive: reviewsActive, Required: []string{}, Missing: []string{}, Asks: asks, Busy: busy}
	}
	waiting := i.Live && i.Needs > 0
	t := pathTurn{ran: map[string]bool{}, busy: busy, off: SessionActions(i)[ActionSteps] != "", dirty: uncommittedAny(i.Uncommitted)}
	for _, s := range slices.Concat(i.StepsRan, v.Ran) {
		t.ran[s] = true
	}
	if t.busy {
		t.running = i.Step
		if t.running == "" {
			t.running = awaitingStep(v)
		}
	}
	switch {
	case reviewsActive:
		t.active = "verify"
	case t.busy && !waiting:
		t.active = cmpOr(t.running, i.Step, "work")
	}
	marks := pathMarks(v, t, pushed)
	incident := v.Profile == workflow.Incident
	status := ""
	if incident && v.Plan != nil {
		status = CauseStatus(v.Plan.Text)
	}
	out := PathModel{ReviewsActive: reviewsActive, Required: []string{}, Missing: []string{}, Asks: asks, Busy: t.busy}
	for _, r := range v.Reviewers {
		out.Required = append(out.Required, r.Agent)
	}
	first, focus := -1, -1
	stages := pathStages(v)
	for at, id := range stages {
		m := marks[id]
		now := !m.done && first < 0
		if now {
			first = at
		}
		st := PathStage{ID: id, Name: stageNames[id], Detail: m.detail, Actions: []PathAction{}}
		if id == "implement" && incident {
			st.Name = "Fix"
		}
		switch t.active {
		case "verify":
			st.Active = id == "verify"
		case "define", "plan":
			st.Active = id == t.active && !m.done
		case "work":
			st.Active = now && id != "define" && id != "plan" && id != "verify"
		}
		if st.Active && focus < 0 {
			focus = at
		}
		st.Actions = stageActions(id, m, now, status, t, v, commands)
		out.Stages = append(out.Stages, st)
	}
	if focus < 0 {
		focus = first
	}
	bar := make([]float64, len(stages))
	for at, id := range stages {
		m, st := marks[id], &out.Stages[at]
		switch {
		case m.done:
			st.State = StageDone
		case at == focus:
			st.State = StageCurrent
		case at == first && at < focus:
			st.State = StageWaiting
		}
		st.Status = stageStatus(*st, awaitsApproval(id, v, status))
		switch {
		case m.complete():
			bar[at] = 1
		case at == focus:
			bar[at] = .5
		}
	}
	if focus >= 0 {
		st := out.Stages[focus]
		out.Card = PathCard{Step: st.Name, Count: "Step " + strconv.Itoa(focus+1) + " of " + strconv.Itoa(len(stages)), Detail: marks[st.ID].cardText(), Bar: bar}
	} else {
		out.Card = PathCard{Step: "Every step done", Detail: marks[stages[len(stages)-1]].cardText(), Bar: bar}
	}
	if !v.Allowed && !pushed {
		out.Missing = append(out.Missing, v.Missing...)
	}
	out.Notice = earlyNotice(i, v, commands)
	if out.Notice == nil {
		out.Notice = outsideNotice(i, v, t)
	}
	return out
}

var stateWords = map[string]string{StageDone: "Done", StageCurrent: "Now"}

func stageStatus(st PathStage, approval bool) string {
	switch {
	case st.Active:
		return "Working"
	case st.State == StageWaiting && approval:
		return "Waiting for your approval"
	case st.State == StageWaiting:
		return "Not done"
	}
	return stateWords[st.State]
}

func awaitsApproval(id string, v GateView, status string) bool {
	switch id {
	case "define":
		return v.Requirements != nil && v.Requirements.Hash != "" && !v.Define
	case "plan":
		return v.Plan != nil && !v.Plan.Approved
	case "cause":
		return status == "CONFIRMED" && v.Plan != nil && !v.Plan.Approved
	}
	return false
}

func earlyNotice(i Info, v GateView, commands map[string]string) *PathLine {
	e := v.Early
	if e == nil || commands[AskRequirements] == "" || !e.Commits && !uncommittedAny(i.Uncommitted) {
		return nil
	}
	return &PathLine{Text: earlyText, Actions: []PathAction{{Ask: AskRequirements, Label: earlyLabel, Title: stageTitle(AskRequirements, commands[AskRequirements])}}, Tone: toneWarn}
}

func implementBuilt(v GateView, busy, dirty bool) bool {
	return v.Waiting == "" && !busy && !dirty
}

func implementNote(wait string, t pathTurn) string {
	switch {
	case wait != "":
		return ""
	case t.busy:
		return " · still working"
	case t.dirty:
		return " · uncommitted changes"
	}
	return ""
}

func outsideNotice(i Info, v GateView, t pathTurn) *PathLine {
	if !t.busy || i.Step != "" || t.dirty || v.Early != nil && v.Early.Commits || !beforeApproval(v) {
		return nil
	}
	return &PathLine{Text: outsideText, Actions: []PathAction{}, Tone: toneInfo}
}

func beforeApproval(v GateView) bool {
	return v.Profile == workflow.Change && (!v.Define || v.Plan == nil || !v.Plan.Approved)
}

func uncommittedAny(u *Uncommitted) bool {
	return u != nil && u.Count > 0
}

func cmpOr(list ...string) string {
	for _, s := range list {
		if s != "" {
			return s
		}
	}
	return ""
}

func stageActions(id string, m mark, now bool, status string, t pathTurn, v GateView, commands map[string]string) []PathAction {
	offer := func(ask, label string) PathAction {
		return PathAction{Ask: ask, Label: label, Title: stageTitle(ask, commands[ask])}
	}
	out := []PathAction{}
	if t.busy {
		return out
	}
	switch {
	case id == "triage":
		if now && status == "" && t.ran[id] && commands[id] != "" {
			out = append(out, offer(id, continueTriage))
		}
	case slices.Contains(incidentStages, id):
		if now && status != "CONFIRMED" && commands[id] != "" {
			label := "Start"
			if status != "" {
				label = "Continue"
			}
			out = append(out, offer(id, label))
		}
	case id == "define":
		produced := v.Requirements != nil && v.Requirements.Hash != ""
		if !m.complete() && t.ran[id] && !produced && commands[id] != "" && v.Holds[id] == "" {
			out = append(out, offer(id, continueDefine))
		}
	case id == "plan":
		if !m.complete() && v.Plan == nil && commands[id] != "" && v.Holds[id] == "" {
			label := "Start"
			if t.ran[id] {
				label = "Continue"
			}
			out = append(out, offer(id, label))
		}
	case id == "implement":
		if v.Waiting != "" && commands[AskFix] != "" {
			out = append(out, offer(AskFix, "Start"))
		}
	case id == "push":
		if commands[AskIncidentReview] != "" {
			out = append(out, offer(AskIncidentReview, reviewLabel))
		}
	}
	return out
}

func sessionAsks(commands map[string]string) []PathAction {
	if commands[AskIssues] == "" {
		return []PathAction{}
	}
	return []PathAction{{Ask: AskIssues, Label: issuesLabel, Title: stageTitle(AskIssues, commands[AskIssues])}}
}
