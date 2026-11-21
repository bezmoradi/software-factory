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
	Hold  string
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

type PathModel struct {
	Stages        []PathStage
	Card          PathCard
	Missing       []string
	ReviewsActive bool
	Required      []string
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
	triage := markDone(false, "No triage yet: give the evidence and the deployed commit or tag in a message, then press Start")
	if status != "" {
		triage = markDone(true, "Triaged · "+path.Base(d.Path)+" says "+shown)
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

func pathMarks(v GateView, started BranchStart, t pathTurn, pushed bool) map[string]mark {
	reviewers := v.Reviewers
	begun := started == StartedCommits || v.Plan != nil || slices.ContainsFunc(reviewers, func(r WorkReviewer) bool { return r.State != "none" || r.Round != 0 })
	strict := profileOf(v) == workflow.Feature
	reqHash := v.Requirements != nil && v.Requirements.Hash != ""
	working := func(step string) bool { return t.active == step || t.running == step }
	var define mark
	switch {
	case strict && v.Define:
		define = markDone(true, "Requirements approved")
	case !strict && v.Define:
		define = markDone(true, "Requirements agreed")
	case working("define"):
		define = markDone(false, "The agent is working on the requirements")
	case strict && reqHash && t.busy:
		define = markDone(false, "The agent is still working on the requirements")
	case strict && reqHash:
		define = markDone(false, "Agreed requirements wait for your approval")
	case strict && v.Holds["define"] != "":
		define = markDone(false, "Start waits until the gate can check the working tree")
	case !strict && begun:
		define = markAs(StageSkipped, "Skipped: no agreed requirements")
	default:
		define = markDone(false, "No agreed requirements yet"+press(t.off, t.ran["define"]))
	}
	feature := v.Effective == workflow.Feature
	planned := !feature || (v.Plan != nil && v.Plan.Approved)
	okCount := 0
	for _, r := range reviewers {
		if slices.Contains(reviewOK, r.State) {
			okCount++
		}
	}
	verified := len(reviewers) > 0 && okCount == len(reviewers)
	at := headAt(v)
	if v.Effective == workflow.Exploring {
		return map[string]mark{
			"define":    define,
			"plan":      markAs(StageSkipped, "Not needed while Exploring"),
			"implement": markDone(false, at),
			"verify":    markDone(false, "No reviews while Exploring"),
			"deliver":   markDone(false, "Exploring can't push. Pick another work type to push:"),
		}
	}
	wait := v.Waiting
	passed := "Every review passes on this commit"
	if slices.ContainsFunc(reviewers, func(r WorkReviewer) bool { return r.State == "overridden" }) {
		passed = "Reviews done on this commit, with an override"
	}
	running := slices.ContainsFunc(reviewers, func(r WorkReviewer) bool { return r.State == "running" })
	incident := v.Effective == workflow.Incident
	fixed := !incident || (v.Plan != nil && v.Plan.Approved)
	out := map[string]mark{}
	if incident {
		out = incidentMarks(v, t)
	}
	out["define"] = define
	var plan mark
	name := ""
	if v.Plan != nil {
		name = path.Base(v.Plan.Path)
	}
	switch {
	case !feature:
		plan = markAs(StageSkipped, "Not needed for "+sizedWord(v.Effective))
	case v.Plan == nil && working("plan"):
		plan = markDone(false, "The agent is making the plan")
	case v.Plan == nil && strict && v.Holds["plan"] != "":
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
		plan = markDone(planned, name+" · approved · research "+research)
	}
	out["plan"] = plan
	suffix := ""
	switch {
	case wait != "" && v.Head != "":
		suffix = " · " + wait
	case started == StartedNoBase:
		suffix = " · no base branch, so the page can't tell this branch's commits"
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

func sizedWord(p workflow.Profile) string {
	switch p {
	case workflow.Fix:
		return "a Fix"
	case workflow.Incident:
		return "an Incident"
	case workflow.Exploring:
		return "Exploring"
	}
	return "Docs only"
}

func profileOf(v GateView) workflow.Profile {
	for _, t := range v.Types {
		if t.ID == v.Type {
			return t.Profile()
		}
	}
	return workflow.Fix
}

var stageNames = map[string]string{"define": "Define", "plan": "Plan", "triage": "Triage", "reproduce": "Reproduce", "cause": "Root cause", "implement": "Implement", "verify": "Code review", "deliver": "Deliver"}

var stageOrder = []string{"define", "plan", "triage", "reproduce", "cause", "implement", "verify", "deliver"}

var incidentStages = []string{"triage", "reproduce", "cause"}

var customKinds = map[string]workflow.Step{"define": workflow.Define, "plan": workflow.Plan, "implement": workflow.Implement, "verify": workflow.Review, "deliver": workflow.Deliver}

func ReviewsActive(i Info, v GateView) bool {
	return slices.ContainsFunc(v.Reviewers, func(r WorkReviewer) bool {
		return r.State == "running" || slices.Contains(activeAgent, i.AgentStates[r.Agent])
	})
}

func awaitingStep(v GateView) string {
	switch {
	case profileOf(v) == workflow.Feature && v.Requirements != nil && v.Requirements.Hash != "" && !v.Requirements.Approved:
		return "define"
	case v.Effective == workflow.Feature && v.Plan != nil && !v.Plan.Approved:
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
	case AskIncidentReview:
		return "Sends " + command + " to this session, which commits a draft incident review"
	}
	return "Sends " + command + " to this session"
}

func Path(i Info, v GateView, pushed bool) PathModel {
	commands := stageCommands(v)
	reviewsActive := ReviewsActive(i, v)
	waiting := i.Live && i.Needs > 0
	t := pathTurn{ran: map[string]bool{}, busy: i.Busy, off: SessionActions(i)[ActionSteps] != ""}
	for _, s := range i.StepsRan {
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
	marks := pathMarks(v, v.Started, t, pushed)
	if typed := customType(v); typed != nil {
		for stage, want := range customKinds {
			got, ok := marks[stage]
			if !slices.Contains(typed.Steps, want) || ok && got.state == StageSkipped {
				marks[stage] = markAs(StageSkipped, "Not part of "+typed.Name)
			}
		}
	}
	incident := v.Effective == workflow.Incident
	status := ""
	if incident && v.Plan != nil {
		status = CauseStatus(v.Plan.Text)
	}
	out := PathModel{ReviewsActive: reviewsActive, Required: []string{}, Missing: []string{}}
	if v.Effective != workflow.Exploring {
		for _, r := range v.Reviewers {
			out.Required = append(out.Required, r.Agent)
		}
	}
	current := false
	finished := 0
	var card *PathCard
	for _, id := range stageOrder {
		if incident && (id == "define" || id == "plan") || !incident && slices.Contains(incidentStages, id) {
			continue
		}
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
		out.Card = PathCard{Step: "Every step done", Detail: marks["deliver"].detail, Done: 1}
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

func customType(v GateView) *workflow.WorkType {
	for _, t := range v.Types {
		if t.ID == v.Type && !t.Builtin {
			return &t
		}
	}
	return nil
}

func stageActions(id string, m mark, now bool, status string, t pathTurn, v GateView, commands map[string]string) []PathAction {
	offer := func(ask, label string) PathAction {
		return PathAction{Ask: ask, Label: label, Title: stageTitle(ask, commands[ask])}
	}
	out := []PathAction{}
	switch {
	case slices.Contains(incidentStages, id):
		if now && status != "CONFIRMED" && commands[id] != "" {
			label := "Start"
			if status != "" {
				label = "Continue"
			}
			out = append(out, offer(id, label))
		}
	case id == "define" || id == "plan":
		produced := id == "define" && v.Requirements != nil && v.Requirements.Hash != "" || id == "plan" && v.Plan != nil
		if !m.complete() && t.running != id && !produced && commands[id] != "" {
			label := "Start"
			if t.ran[id] {
				label = "Continue"
			}
			a := offer(id, label)
			a.Hold = v.Holds[id]
			out = append(out, a)
		}
	case id == "implement":
		if !m.complete() && commands[AskFix] != "" {
			out = append(out, offer(AskFix, "Start"))
		}
	case id == "deliver":
		for _, d := range []struct{ ask, label string }{{AskIssues, "Issues"}, {AskHandoff, "Handoff"}, {AskIncidentReview, "Incident review"}} {
			if commands[d.ask] != "" {
				out = append(out, offer(d.ask, d.label))
			}
		}
	}
	return out
}
