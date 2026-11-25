package session

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const firstStepRead = 10 * time.Second

type FirstStep struct {
	Step    string
	Command string
}

type FirstSteps interface {
	FirstStep(ctx context.Context, sid string, ran []string) (FirstStep, bool)
	UnclaimStep(sid, step string)
}

func firstStepKind(p workflow.Profile) string {
	switch p {
	case workflow.Change:
		return AskDefine
	case workflow.Incident:
		return AskTriage
	}
	return ""
}

func firstStepsRan(list []string) []string {
	var out []string
	for _, s := range list {
		if (s == AskDefine || s == AskTriage) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func withStep(list []string, step string) []string {
	if step == "" || slices.Contains(list, step) {
		return list
	}
	return append(list, step)
}

func firstStepOf(v GateView) (FirstStep, bool) {
	step := firstStepKind(v.Profile)
	switch {
	case !v.Chosen || step == "" || slices.Contains(v.Ran, step):
		return FirstStep{}, false
	case step == AskDefine && (v.Define || v.Requirements != nil && v.Requirements.Hash != "" || v.Holds["define"] != ""):
		return FirstStep{}, false
	case step == AskTriage && v.Plan != nil && CauseStatus(v.Plan.Text) != "":
		return FirstStep{}, false
	}
	command := stageCommands(v)[step]
	if command == "" {
		return FirstStep{}, false
	}
	return FirstStep{Step: step, Command: command}, true
}

func (w *Work) FirstStep(ctx context.Context, sid string, ran []string) (FirstStep, bool) {
	ws, ok := w.session(sid)
	if !ok || !ws.chosen {
		return FirstStep{}, false
	}
	step := firstStepKind(workflow.TypeOr(w.packSetup(sid, ws.repo).setup.Types, ws.typeID).Profile())
	if step == "" || slices.Contains(ran, step) || slices.Contains(ws.ran, step) {
		return FirstStep{}, false
	}
	v, err := w.View(ctx, sid)
	if err != nil {
		return FirstStep{}, false
	}
	f, ok := firstStepOf(v)
	if !ok || !w.claimStep(sid, f.Step) {
		return FirstStep{}, false
	}
	return f, true
}

func (w *Work) claimStep(sid, step string) bool {
	w.mu.Lock()
	s := w.sessions[sid]
	claimed := s != nil && !slices.Contains(s.ran, step)
	if claimed {
		s.ran = append(s.ran, step)
	}
	w.mu.Unlock()
	if claimed {
		w.persist(sid)
	}
	return claimed
}

func (w *Work) UnclaimStep(sid, step string) {
	w.mu.Lock()
	s := w.sessions[sid]
	had := s != nil && slices.Contains(s.ran, step)
	if had {
		s.ran = slices.DeleteFunc(s.ran, func(r string) bool { return r == step })
	}
	w.mu.Unlock()
	if had {
		w.persist(sid)
	}
}

func (r *Registry) UseFirstSteps(f FirstSteps) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.firstSteps = f
}

func typedPlain(text string, attached bool) bool {
	return !attached && text != "" && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!")
}

func (r *Registry) firstStepFor(sid, text string, attached bool) FirstStep {
	if !typedPlain(text, attached) {
		return FirstStep{}
	}
	r.mu.Lock()
	fs := r.firstSteps
	a := r.admitted[sid]
	var ran []string
	if a != nil {
		ran = slices.Clone(a.stepsRan)
	}
	r.mu.Unlock()
	if fs == nil || a == nil {
		return FirstStep{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), firstStepRead)
	defer cancel()
	f, ok := fs.FirstStep(ctx, sid, ran)
	if !ok {
		return FirstStep{}
	}
	return f
}

func (r *Registry) unclaimStep(sid, step string) {
	r.mu.Lock()
	fs := r.firstSteps
	r.mu.Unlock()
	if fs != nil {
		fs.UnclaimStep(sid, step)
	}
}

func (r *Registry) unclaimLocked(sid string, steps []string) {
	fs := r.firstSteps
	if fs == nil || len(steps) == 0 {
		return
	}
	go func() {
		for _, s := range steps {
			fs.UnclaimStep(sid, s)
		}
	}()
}

func (r *Registry) stepTextLocked(sid, text string, f FirstStep) (sent string, noted bool, step string) {
	sent, noted = r.noted(sid, text)
	if f.Step == "" || refusedName(commandName(f.Command), r.catalogs[sid]) {
		return sent, noted, ""
	}
	wrapped := f.Command + " " + sent
	if utf16Len(wrapped) > MaxText {
		return sent, noted, ""
	}
	return wrapped, noted, f.Step
}
