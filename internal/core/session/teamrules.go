package session

import (
	"context"
	"slices"
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

func (w *Work) TeamRulesFor(ctx context.Context, sid string, t PushTarget, base, head string) (TeamRules, error) {
	setup := w.sessionSetup(sid, t.Name)
	if !slices.ContainsFunc(setup.FileRules, func(r workflow.FileRule) bool { return r.Instruction != "" }) {
		return TeamRules{}, ctx.Err()
	}
	changes, err := w.cfg.Files.ChangedFiles(ctx, t.Repo, base, head)
	if cerr := ctx.Err(); cerr != nil {
		return TeamRules{}, cerr
	}
	if err != nil {
		return TeamRules{}, nil
	}
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	return teamRulesOf(setup.FileRules, paths, main), nil
}

func teamRulesOf(rules []workflow.FileRule, paths []string, main string) TeamRules {
	out := TeamRules{Main: main}
	for _, r := range rules {
		if r.Instruction == "" {
			continue
		}
		if slices.ContainsFunc(paths, func(p string) bool { return workflow.PathMatches(r.Pattern, p, false) }) {
			out.Rules = append(out.Rules, TeamRule{Pattern: r.Pattern, Instruction: r.Instruction})
		}
	}
	if len(out.Rules) == 0 {
		return TeamRules{}
	}
	return out
}

func (t TeamRules) Digest() string {
	if len(t.Rules) == 0 {
		return ""
	}
	lines := make([]string, 0, len(t.Rules))
	for _, r := range t.Rules {
		lines = append(lines, strconv.Quote(r.Pattern)+" "+strconv.Quote(r.Instruction))
	}
	slices.Sort(lines)
	return textHash(strings.Join(lines, "\n"))
}

func (w *Work) Guarded(sid, top string, paths []string) string {
	if top == "" || len(paths) == 0 {
		return ""
	}
	ws, ok := w.session(sid)
	if !ok {
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
	if why := w.specWrite(t, top, paths); why != "" {
		return why
	}
	return w.lockedWrite(ws, top, paths)
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
