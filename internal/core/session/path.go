package session

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	StageDone     = "done"
	StageSkipped  = "skipped"
	StageOutdated = "outdated"
	StageCurrent  = "current"
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
	Active  bool
	Detail  string
	Actions []PathAction
}

type PathCard struct {
	Step   string
	Count  string
	Detail string
	Done   float64
}

type PathLine struct {
	Text    string
	Actions []PathAction
}

type PathModel struct {
	Stages        []PathStage
	Line          *PathLine
	Card          PathCard
	Missing       []string
	ReviewsActive bool
	Required      []string
	Asks          []PathAction
}

type mark struct {
	done   bool
	state  string
	detail string
}

func markDone(ok bool, detail string) mark { return mark{done: ok, detail: detail} }
func markAs(state, detail string) mark     { return mark{done: true, state: state, detail: detail} }

func (m mark) complete() bool { return m.done && m.state != StageOutdated }

var reviewOK = []string{"pass", "overridden"}

var activeAgent = []string{"pending", "running", "waiting"}

type pathTurn struct {
	ran     map[string]bool
	running string
	busy    bool
	active  string
	off     bool
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

func press(off, ran bool) string {
	if off {
		return ""
	}
	if ran {
		return ": press Continue"
	}
	return ": press Start"
}

const (
	continueDefine = "Continue defining"
	continueTriage = "Continue triage"
)

func pressDefine(t pathTurn) string {
	switch {
	case !t.ran["define"]:
		return ": send what you want built"
	case t.off || t.busy:
		return ""
	}
	return ": press " + continueDefine
}

func reviewCount(done, all int) string {
	word := "reviews pass"
	if all == 1 {
		word = "review passes"
	}
	return strconv.Itoa(done) + " of " + strconv.Itoa(all) + " " + word + " on this commit"
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
		triage = markDone(false, "The agent is working on the triage")
	default:
		triage = markDone(false, "No triage yet: give the evidence and the deployed commit or tag in a message, then press "+continueTriage)
	}
	var reproduce mark
	switch {
	case reproduced:
		reproduce = markDone(true, "Reproduced at the deployed commit")
	case status == "BLOCKED":
		reproduce = markDone(false, "Blocked: the file names the evidence or environment it needs. Give it in a message, then press Continue")
	case status == "TRIAGED":
		reproduce = markDone(false, "Next: a reproduction at the deployed commit, run and shown to fail: press Continue")
	case status != "":
		reproduce = markDone(false, "The file's Status is "+shown+", which knackpack doesn't use")
	}
	var cause mark
	switch {
	case status == "CONFIRMED" && d.Approved:
		cause = markDone(true, "Cause confirmed · approved")
	case status == "CONFIRMED" && t.busy:
		cause = markDone(false, "The agent is still working on the diagnosis")
	case status == "CONFIRMED" && d.Changed:
		cause = markDone(false, "Cause confirmed, but the file changed after you approved it: read it and approve again")
	case status == "CONFIRMED":
		cause = markDone(false, "Cause confirmed · waiting for your approval")
	case status == "UNCONFIRMED":
		cause = markDone(false, "No cause confirmed yet: the file says what would decide. Give it in a message, then press Continue")
	case status == "REPRODUCED":
		cause = markDone(false, "Next: ranked hypotheses, and a cause confirmed by a test that tells them apart: press Continue")
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
		define = markDone(false, "The agent is working on the requirements")
	case reqHash && t.busy:
		define = markDone(false, "The agent is still working on the requirements")
	case reqHash:
		define = markDone(false, "Agreed requirements wait for your approval")
	case v.Holds["define"] != "":
		define = markDone(false, "Start waits until the gate can check the working tree")
	default:
		define = markDone(false, "No agreed requirements yet"+pressDefine(t))
	}
	change := v.Profile == workflow.Change
	okCount := 0
	for _, r := range reviewers {
		if slices.Contains(reviewOK, r.State) {
			okCount++
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
		case v.NoPlan:
			plan = markAs(StageSkipped, "Skipped: you approved the requirements without a plan")
		case v.Plan == nil && working("plan"):
			plan = markDone(false, "The agent is making the plan")
		case v.Plan == nil && v.Holds["plan"] != "":
			plan = markDone(false, "Starts once you approve the requirements")
		case v.Plan == nil:
			plan = markDone(false, "Needs a plan"+press(t.off, t.ran["plan"]))
		case !v.Plan.Approved && t.busy:
			plan = markDone(false, "The agent is still working on the plan")
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
		out["implement"] = markDone(wait == "", at+suffix)
	} else {
		out["implement"] = markDone(false, at+" · Fix starts once you approve the root cause")
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
	switch {
	case pushed:
		to := ""
		if v.Branch != "" {
			to = " to " + v.Branch
		}
		out["deliver"] = markDone(true, "Pushed "+shortSHA7(v.Head)+to)
	case v.Allowed:
		out["deliver"] = markDone(true, "The gate has what it needs for this commit: Push sends it, once the sandbox check holds")
	default:
		out["deliver"] = markDone(false, "Push held until it has:")
	}
	return out
}

const (
	exploreLine   = "Changes files freely; nothing is pushed."
	exploreIssues = " Turn what you found into issues; you approve each one."
	issuesLabel   = "Draft issues"
	issuesTitle   = "The agent drafts issues from this session; you approve each one before it's opened"
	reviewLabel   = "Write the incident review"
)

var stageNames = map[string]string{"define": "Define", "plan": "Plan", "triage": "Triage", "reproduce": "Reproduce", "cause": "Root cause", "implement": "Implement", "verify": "Code review", "deliver": "Deliver"}

var incidentStages = []string{"triage", "reproduce", "cause"}

var stepStages = map[workflow.Step]string{workflow.Define: "define", workflow.Plan: "plan", workflow.Implement: "implement", workflow.Review: "verify", workflow.Deliver: "deliver"}

func pathStages(v GateView) []string {
	if v.Profile == workflow.Incident {
		return append(slices.Clone(incidentStages), "implement", "verify", "deliver")
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
	case v.Profile == workflow.Change && !v.NoPlan && v.Plan != nil && !v.Plan.Approved:
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
	}
	return "Sends " + command + " to this session"
}

func Path(i Info, v GateView, pushed bool) PathModel {
	commands := stageCommands(v)
	reviewsActive := ReviewsActive(i, v)
	asks := sessionAsks(commands)
	if v.Profile == workflow.Explore {
		line := &PathLine{Text: exploreLine, Actions: asks}
		if len(asks) > 0 {
			line.Text += exploreIssues
		}
		return PathModel{Stages: []PathStage{}, Line: line, Card: PathCard{Step: workflow.TypeOr(v.Types, v.Type).Name, Detail: headAt(v)}, ReviewsActive: reviewsActive, Required: []string{}, Missing: []string{}, Asks: asks}
	}
	waiting := i.Live && i.Needs > 0
	t := pathTurn{ran: map[string]bool{}, busy: i.Busy, off: SessionActions(i)[ActionSteps] != ""}
	for _, s := range slices.Concat(i.StepsRan, v.Ran) {
		t.ran[s] = true
	}
	if t.busy {
		t.running = i.Step
		if t.running == "" {
			t.running = awaitingStep(v)
		}
	}
	working := i.Busy || i.Compacting || i.Subagents
	switch {
	case reviewsActive:
		t.active = "verify"
	case working && !waiting:
		t.active = cmpOr(t.running, i.Step, "work")
	}
	marks := pathMarks(v, t, pushed)
	incident := v.Profile == workflow.Incident
	status := ""
	if incident && v.Plan != nil {
		status = CauseStatus(v.Plan.Text)
	}
	out := PathModel{ReviewsActive: reviewsActive, Required: []string{}, Missing: []string{}, Asks: asks}
	for _, r := range v.Reviewers {
		out.Required = append(out.Required, r.Agent)
	}
	current := false
	finished := 0
	var card *PathCard
	stages := pathStages(v)
	for _, id := range stages {
		m := marks[id]
		now := !m.done && !current
		st := PathStage{ID: id, Name: stageNames[id], Detail: m.detail, Actions: []PathAction{}}
		if id == "implement" && incident {
			st.Name = "Fix"
		}
		if now {
			current = true
			card = &PathCard{Step: st.Name, Count: strconv.Itoa(len(out.Stages) + 1), Detail: m.detail}
		}
		if m.done && m.state != StageOutdated {
			finished++
		}
		switch {
		case m.state != "":
			st.State = m.state
		case m.done:
			st.State = StageDone
		case now:
			st.State = StageCurrent
		}
		switch t.active {
		case "verify":
			st.Active = id == "verify"
		case "define", "plan":
			st.Active = id == t.active && !m.done
		case "work":
			st.Active = now && id != "define" && id != "plan"
		}
		st.Actions = stageActions(id, m, now, status, t, v, commands)
		out.Stages = append(out.Stages, st)
	}
	n := len(out.Stages)
	if card != nil {
		card.Count = "Step " + card.Count + " of " + strconv.Itoa(n)
		card.Done = float64(finished) / float64(n)
		out.Card = *card
	} else {
		out.Card = PathCard{Step: "Every step done", Detail: marks[stages[len(stages)-1]].detail, Done: 1}
	}
	if !v.Allowed && !pushed {
		out.Missing = append(out.Missing, v.Missing...)
	}
	return out
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
	switch {
	case id == "triage":
		if now && status == "" && t.ran[id] && !t.busy && commands[id] != "" {
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
		if !m.complete() && t.ran[id] && !t.busy && !produced && commands[id] != "" && v.Holds[id] == "" {
			out = append(out, offer(id, continueDefine))
		}
	case id == "plan":
		if !m.complete() && t.running != id && v.Plan == nil && commands[id] != "" && v.Holds[id] == "" {
			label := "Start"
			if t.ran[id] {
				label = "Continue"
			}
			out = append(out, offer(id, label))
		}
	case id == "implement":
		if !m.complete() && commands[AskFix] != "" {
			out = append(out, offer(AskFix, "Start"))
		}
	case id == "deliver":
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
