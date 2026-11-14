package session

import (
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

type TeamRule struct {
	Pattern     string
	Instruction string
}

type TeamRules struct {
	Main  string
	Rules []TeamRule
}

func instructionProblems(team *workflow.Team) []string {
	if team == nil {
		return nil
	}
	var out []string
	for i, r := range team.FileRules {
		if r.Instruction != "" && secretText(r.Instruction) {
			out = append(out, "fileRules["+strconv.Itoa(i)+"]: the \"instruction\" looks like it holds a secret, so the file can't be used")
		}
	}
	return out
}

func (w *Work) sessionSetup(sid, name string) workflow.Setup {
	ws, ok := w.session(sid)
	if !ok {
		ws = workSession{repo: name, claude: true}
	}
	return w.setupFor(ws.repo, ws.claude).setup
}

func (w *Work) TeamRulesFor(sid string, t PushTarget, base, head string) TeamRules {
	setup := w.sessionSetup(sid, t.Name)
	var withText []workflow.FileRule
	for _, r := range setup.FileRules {
		if r.Instruction != "" {
			withText = append(withText, r)
		}
	}
	if len(withText) == 0 {
		return TeamRules{}
	}
	changes, err := w.cfg.Files.ChangedFiles(t.Repo, base, head)
	if err != nil {
		return TeamRules{}
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	out := TeamRules{Main: main}
	for _, r := range withText {
		for _, c := range changes {
			if workflow.PathMatches(r.Pattern, c.Path, false) {
				out.Rules = append(out.Rules, TeamRule{Pattern: r.Pattern, Instruction: r.Instruction})
				break
			}
		}
	}
	return out
}

func (w *Work) Guarded(sid, top string, paths []string) string {
	if top == "" || len(paths) == 0 {
		return ""
	}
	if _, ok := w.session(sid); !ok {
		return ""
	}
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return ""
	}
	protect := w.sessionSetup(sid, t.Name).Protect
	for _, p := range paths {
		if why := gate.GuardedWrite(top, p, protect); why != "" {
			return why
		}
	}
	return ""
}

func teamRulesAsk(t TeamRules) string {
	if len(t.Rules) == 0 {
		return ""
	}
	at := ""
	if t.Main != "" {
		at = " at commit " + t.Main
	}
	var b strings.Builder
	b.WriteString(" The team file " + TeamFile + " on the remote's main branch" + at + ", which the person approved on the page, gives rules for paths this change touches." +
		" Each is the team's rule from main, not something the agent wrote: apply it when you review the files its pattern matches, and count a file that breaks it like any other finding.")
	for _, r := range t.Rules {
		b.WriteString(" The team's rule from main for files matching " + strconv.Quote(r.Pattern) + ": " + strconv.Quote(r.Instruction) + ".")
	}
	return b.String()
}
