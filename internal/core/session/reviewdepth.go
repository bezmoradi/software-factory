package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	keptSizes      = 64
	depthUnlisted  = "Needs reviewers from the list."
	reviewerSuffix = "-reviewer"
	depthRequired  = "Needs a reviewer that isn't already required."
	depthUnsized   = "Needs a big-change size above 0 first."
)

type changeSize struct {
	files   int
	lines   int
	clipped bool
	ok      bool
}

type DepthView struct {
	Depth     workflow.Depth
	Type      string
	Runs      int
	AboveRuns int
	Advisory  int
	Available []string
	Required  []string
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
	limit := sizeLimit(HoldSettings{MaxLines: d.MaxLines})
	if !listed {
		return true, "Required by this project above " + limit + ": the engine couldn't count this change"
	}
	n := w.sizeOf(ctx, repo, base, head)
	switch {
	case !n.ok:
		return true, "Required by this project above " + limit + ": the engine couldn't count this change"
	case n.clipped:
		return true, "Required by this project above " + limit + ": this change has " + strconv.Itoa(n.files) + " " + plural(n.files, "file", "files") + " and more changed lines than the engine counts"
	case n.lines > d.MaxLines:
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
	kind := workflow.TypeOr(setup.setup.Types, setup.setup.DefaultType)
	required := workflow.Required(setup.setup, kind, nil)
	base := workflow.Deepen(required, d, false, "")
	v := DepthView{Depth: d, Type: kind.Name, Runs: workflow.RunTotal(base), Advisory: len(workflow.Advisory(base, d)), Available: w.reviewersFor(r.Name), Required: w.requiredFor(r.Name)}
	if d.Sized() {
		v.AboveRuns = workflow.RunTotal(workflow.Deepen(required, d, true, ""))
	}
	return v, nil
}

func (w *Work) SetReviewDepth(path string, d workflow.Depth) (bool, error) {
	if field, why := d.Problem(); why != "" {
		return false, onField(field, invalid("depth", why))
	}
	r, err := w.repoByPath(path)
	if err != nil {
		return false, err
	}
	available, required := w.reviewersFor(r.Name), w.requiredFor(r.Name)
	clean := workflow.Depth{Always: unique(d.Always), Advisory: unique(d.Advisory), Extra: unique(d.Extra), MaxLines: d.MaxLines, Twice: d.Twice}
	err = w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		if err := unsized(clean, p.Depth); err != nil {
			return err
		}
		if err := w.unlisted(clean, p.Depth, available, required); err != nil {
			return err
		}
		if sameDepth(p.Depth, clean) {
			return errNoDepthChange
		}
		p.Depth = clean
		st.Projects[r.Name] = p
		return nil
	})
	if errors.Is(err, errNoDepthChange) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	w.cfg.Logger.Info("project setting", "op", "review-depth", "always", len(clean.Always), "big", len(clean.Extra), "warn", len(clean.Advisory), "sized", clean.Sized())
	return true, nil
}

var errNoDepthChange = errors.New("the review settings are already these")

func sameDepth(a, b workflow.Depth) bool {
	return slices.Equal(a.Always, b.Always) && slices.Equal(a.Advisory, b.Advisory) && slices.Equal(a.Extra, b.Extra) && a.MaxLines == b.MaxLines && a.Twice == b.Twice
}

func (w *Work) reviewersFor(project string) []string {
	out := []string{}
	for _, a := range w.installed(project) {
		if w.packKnown(project) || strings.HasSuffix(a, reviewerSuffix) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

func (w *Work) requiredFor(project string) []string {
	setup := w.setupFor(project).setup
	out := []string{}
	names := slices.Clone(setup.Agents)
	for _, role := range review.Roles {
		names = append(names, setup.Roles[role])
	}
	for _, a := range names {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

func unsized(d, saved workflow.Depth) error {
	if d.MaxLines > 0 {
		return nil
	}
	for _, a := range d.Extra {
		if !slices.Contains(saved.Extra, a) {
			return onField("reviewers", invalid("depth", depthUnsized))
		}
	}
	if d.Twice && !saved.Twice {
		return onField("twice", invalid("depth", depthUnsized))
	}
	return nil
}

func (w *Work) unlisted(d, saved workflow.Depth, available, required []string) error {
	kept := saved.Added()
	for _, a := range d.Added() {
		if why := w.roleRefusal(a, available, required, kept); why != "" {
			return onField("reviewers", invalid("agent", why))
		}
	}
	return nil
}

func (w *Work) roleRefusal(a string, available, required, saved []string) string {
	if slices.Contains(saved, a) {
		return ""
	}
	if !slices.Contains(available, a) {
		return depthUnlisted
	}
	if slices.Contains(required, a) {
		return depthRequired
	}
	offer := offerFor(w.cfg.RoleAgents, a)
	if review.RoleProblem(a, &offer) != "" {
		return withName("", a, " isn't from a trusted source.")
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
	SetReviewDepth(path string, d workflow.Depth) (bool, error)
}

var _ ReviewDepthPage = (*Work)(nil)
