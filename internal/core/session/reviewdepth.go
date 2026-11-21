package session

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const keptSizes = 64

type changeSize struct {
	files   int
	lines   int
	clipped bool
	ok      bool
}

type DepthView struct {
	Depth     workflow.Depth
	Agents    []string
	Type      string
	Runs      int
	AboveRuns int
	Advisory  int
}

func (w *Work) depthOf(name string) workflow.Depth {
	return w.load().Projects[name].Depth
}

func (w *Work) deepened(ctx context.Context, t PushTarget, required []workflow.Requirement, base, head string, listed bool) []workflow.Requirement {
	d := w.depthOf(t.Name)
	if !d.Sized() || head == "" {
		return workflow.Deepen(required, d, false, "")
	}
	above, why := w.above(ctx, t.Repo, base, head, listed, d)
	return workflow.Deepen(required, d, above, why)
}

func (w *Work) above(ctx context.Context, repo, base, head string, listed bool, d workflow.Depth) (bool, string) {
	limit := sizeLimit(HoldSettings{MaxFiles: d.MaxFiles, MaxLines: d.MaxLines})
	if !listed {
		return true, "Required by this project above " + limit + ": the engine couldn't count this change"
	}
	n := w.sizeOf(ctx, repo, base, head)
	switch {
	case !n.ok:
		return true, "Required by this project above " + limit + ": the engine couldn't count this change"
	case n.clipped && d.MaxLines > 0:
		return true, "Required by this project above " + limit + ": this change has " + strconv.Itoa(n.files) + " " + plural(n.files, "file", "files") + " and more changed lines than the engine counts"
	case (d.MaxFiles > 0 && n.files > d.MaxFiles) || (d.MaxLines > 0 && n.lines > d.MaxLines):
		return true, "Required by this project above " + limit + ": this change has " + strconv.Itoa(n.files) + " " + plural(n.files, "file", "files") + " and " + strconv.Itoa(n.lines) + " changed " + plural(n.lines, "line", "lines")
	}
	return false, ""
}

func (w *Work) sizeOf(ctx context.Context, repo, base, head string) changeSize {
	if w.cfg.Changes == nil {
		return changeSize{}
	}
	key := repo + "\x00" + base + "\x00" + head
	w.sizeMu.Lock()
	n, ok := w.sizes[key]
	w.sizeMu.Unlock()
	if ok {
		return n
	}
	cs, err := w.cfg.Changes.Changes(ctx, repo, base, head)
	if err != nil {
		return changeSize{}
	}
	n = changeSize{files: len(cs.Files), lines: changedLines(cs.Diff) + int(min(cs.UndiffedLines, maxCountedLines)), clipped: cs.Clipped || cs.Uncounted, ok: true}
	w.sizeMu.Lock()
	if len(w.sizes) >= keptSizes {
		clear(w.sizes)
	}
	w.sizes[key] = n
	w.sizeMu.Unlock()
	return n
}

func pooledEntry(entries map[review.Key]ReviewEntry, k review.Key, runs int) (ReviewEntry, bool, bool) {
	rank := func(e ReviewEntry) int {
		switch {
		case e.Record.State == review.Running:
			return 6
		case e.Record.State == review.Block && e.Record.Override == "":
			return 5
		case e.Record.State == review.Failed:
			return 4
		case e.Record.State == review.Unknown:
			return 3
		case e.Record.State == review.Block:
			return 2
		}
		return 1
	}
	var out ReviewEntry
	found, complete := false, true
	for _, rk := range k.Runs(runs) {
		e, ok := entries[rk]
		if !ok {
			complete = false
			continue
		}
		if !found || rank(e) > rank(out) {
			out = e
		}
		found = true
	}
	return out, complete, found
}

func (w *Work) ReviewDepth(path string) (DepthView, error) {
	r, err := w.repoByPath(path)
	if err != nil {
		return DepthView{}, err
	}
	d := w.depthOf(r.Name)
	setup := w.setupFor(r.Name)
	kind, ok := typeIn(setup.setup.Types, w.defaultType(setup.setup))
	if !ok {
		kind, _ = typeIn(setup.setup.Types, DefaultWorkType)
	}
	required := workflow.Required(setup.setup, kind, nil)
	base := workflow.Deepen(required, d, false, "")
	v := DepthView{Depth: d, Type: kind.Name, Runs: workflow.RunTotal(base), Advisory: len(workflow.Advisory(base, d))}
	if d.Sized() {
		v.AboveRuns = workflow.RunTotal(workflow.Deepen(required, d, true, ""))
	}
	for _, role := range review.Roles {
		if a := setup.setup.Roles[role]; a != "" && !slices.Contains(v.Agents, a) {
			v.Agents = append(v.Agents, a)
		}
	}
	for _, a := range append(append(slices.Clone(setup.setup.Agents), d.Extra...), slices.Sorted(maps.Keys(d.Runs))...) {
		if !slices.Contains(v.Agents, a) {
			v.Agents = append(v.Agents, a)
		}
	}
	return v, nil
}

func (w *Work) SetReviewDepth(path string, d workflow.Depth) error {
	if field, why := d.Problem(); why != "" {
		return onField(field, explained(invalid("depth"), "The engine didn't save it: "+why+"."))
	}
	r, err := w.repoByPath(path)
	if err != nil {
		return err
	}
	installed, known := w.installed(r.Name), w.packKnown(r.Name)
	for _, list := range []struct {
		field  string
		agents []string
	}{{"advisory", d.Advisory}, {"extra", d.Extra}} {
		for _, a := range list.agents {
			if why := w.roleRefusal(a, installed, known); why != "" {
				return onField(list.field, explained(invalid("agent"), clip("The engine didn't save it: "+why, maxOutput)))
			}
		}
	}
	clean := workflow.Depth{Advisory: unique(d.Advisory), Extra: unique(d.Extra), MaxFiles: d.MaxFiles, MaxLines: d.MaxLines, ExtraRuns: d.ExtraRuns}
	for a, n := range d.Runs {
		if n > 1 {
			if clean.Runs == nil {
				clean.Runs = map[string]int{}
			}
			clean.Runs[a] = n
		}
	}
	if clean.ExtraRuns < 2 {
		clean.ExtraRuns = 0
	}
	if err := w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		p.Depth = clean
		st.Projects[r.Name] = p
		return nil
	}); err != nil {
		return err
	}
	w.cfg.Logger.Info("project setting", "op", "review-depth", "advisory", len(clean.Advisory), "repeated", len(clean.Runs), "sized", clean.Sized())
	return nil
}

func (w *Work) roleRefusal(a string, installed []string, known bool) string {
	offer := offerFor(w.cfg.RoleAgents, a)
	if why := review.RoleProblem(a, &offer); why != "" {
		return why + "."
	}
	if strings.HasPrefix(a, "knackpack:") && !known && len(installed) > 0 && !slices.Contains(installed, a) {
		return a + " isn't an agent the knackpack installed in the reviewer image provides."
	}
	if strings.Contains(a, ":") && known && !slices.Contains(installed, a) {
		return a + " isn't an agent of the pack this project uses."
	}
	return ""
}

func unique(list []string) []string {
	var out []string
	for _, a := range list {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

type ReviewDepthPage interface {
	ReviewDepth(path string) (DepthView, error)
	SetReviewDepth(path string, d workflow.Depth) error
}

var _ ReviewDepthPage = (*Work)(nil)
