package session

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const firstStepRead = 10 * time.Second

var startedWords = map[string]string{AskDefine: "Started Define", AskTriage: "Started triage", ReviseRootCause: "Corrected the diagnosis"}

type FirstStep struct {
	Step    string
	Command string
	Fresh   bool
}

type FirstSteps interface {
	FirstStep(ctx context.Context, sid string) (FirstStep, bool)
	FirstCommands(sid string) map[string]string
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

func readDoc(d *WorkDoc) bool {
	return d != nil && d.Hash != ""
}

func firstStepOf(v GateView) (FirstStep, bool) {
	step := firstStepKind(v.Profile)
	switch {
	case step == "":
		return FirstStep{}, false
	case step == AskDefine && (v.Define || readDoc(v.Requirements) || v.Holds["define"] != ""):
		return FirstStep{}, false
	case step == AskTriage && readDoc(v.Plan) && CauseStatus(v.Plan.Text) != "":
		return FirstStep{}, false
	}
	command := stageCommands(v)[step]
	if command == "" {
		return FirstStep{}, false
	}
	return FirstStep{Step: step, Command: command}, true
}

func (w *Work) FirstStep(ctx context.Context, sid string) (FirstStep, bool) {
	ws, ok := w.session(sid)
	if !ok || firstStepKind(workflow.TypeOr(w.packSetup(sid, ws.repo).setup.Types, ws.typeID).Profile()) == "" {
		return FirstStep{}, false
	}
	v, err := w.View(ctx, sid)
	if err != nil {
		return FirstStep{}, false
	}
	f, ok := firstStepOf(v)
	if !ok {
		return FirstStep{}, false
	}
	f.Fresh = w.claimStep(sid, f.Step)
	return f, true
}

func (w *Work) FirstCommands(sid string) map[string]string {
	ws, ok := w.session(sid)
	if !ok {
		return nil
	}
	setup := w.packSetup(sid, ws.repo).setup
	all := stageCommands(GateView{Commands: setup.Workflow.Commands, Profile: workflow.TypeOr(setup.Types, ws.typeID).Profile()})
	out := map[string]string{}
	for _, step := range []string{AskDefine, AskTriage} {
		if c := all[step]; c != "" {
			out[step] = c
		}
	}
	return out
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
	return (text != "" || attached) && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!")
}

func (r *Registry) firstStepFor(sid, text string, attached bool) FirstStep {
	if !typedPlain(text, attached) {
		return FirstStep{}
	}
	r.mu.Lock()
	fs := r.firstSteps
	a := r.admitted[sid]
	r.mu.Unlock()
	if fs == nil || a == nil {
		return FirstStep{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), firstStepRead)
	defer cancel()
	f, ok := fs.FirstStep(ctx, sid)
	if !ok {
		return FirstStep{}
	}
	return f
}

func (f FirstStep) fresh(step string) string {
	if f.Fresh {
		return step
	}
	return ""
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

func (r *Registry) stepTextLocked(sid, text string, f FirstStep) (sent string, noted bool, step string, err error) {
	sent, noted = r.noted(sid, text)
	if f.Step == "" || refusedName(commandName(f.Command), r.catalogs[sid]) {
		return sent, noted, "", nil
	}
	wrap := func(s string) string { return strings.TrimSpace(f.Command + " " + s) }
	if w := wrap(sent); utf16Len(w) <= MaxText {
		return w, noted, f.Step, nil
	}
	if w := wrap(text); noted && utf16Len(w) <= MaxText {
		return w, false, f.Step, nil
	}
	return "", false, "", tooLongForStep(f.Step, utf16Len(wrap(text))-MaxText)
}

func (r *Registry) promptCommands(given string, b Batch) map[string]string {
	if !slices.ContainsFunc(b.Events, func(raw any) bool {
		ev, ok := raw.(map[string]any)
		return ok && ev["type"] == "prompt"
	}) {
		return nil
	}
	r.mu.Lock()
	c := r.conns[given]
	fs := r.firstSteps
	r.mu.Unlock()
	if c == nil || fs == nil {
		return nil
	}
	return fs.FirstCommands(c.sid)
}

func stepStarted(text string, commands map[string]string) (string, string, bool) {
	rest, notes := text, ""
	for strings.HasPrefix(rest, NoteMarker) {
		note, after, found := strings.Cut(rest, noteBreak)
		if !found {
			return "", "", false
		}
		notes += note + noteBreak
		rest = after
	}
	for _, step := range []string{AskDefine, AskTriage} {
		command := commands[step]
		after, ok := strings.CutPrefix(rest, command)
		next, _ := utf8.DecodeRuneInString(after)
		if command == "" || !ok || (after != "" && !unicode.IsSpace(next)) {
			continue
		}
		shown := strings.TrimSpace(after)
		if step == AskTriage && strings.HasPrefix(shown, correctDiagnosis) {
			step = ReviseRootCause
		}
		return strings.TrimSuffix(notes+shown, noteBreak), step, true
	}
	return "", "", false
}

func stepShown(kind string, ev, frame Fields, commands map[string]string) {
	if kind != "prompt" {
		return
	}
	delete(frame, "step")
	delete(frame, "started")
	text, _ := ev["text"].(string)
	if from, _ := ev["from"].(string); from != "page" && from != "history" {
		return
	}
	shown, step, ok := stepStarted(text, commands)
	_, images := ev["images"]
	_, files := ev["files"]
	if !ok || (shown == "" && !images && !files) {
		return
	}
	frame["text"] = shown
	frame["step"] = step
	frame["started"] = startedWords[step]
}

func tooLongForStep(step string, over int) error {
	return invalid("text", "This message is too long to send as "+stageNames[step]+"'s command, which goes in front of it. Shorten it by at least "+strconv.Itoa(over)+" "+pronoun(over, "character", "characters")+" and send it again, or put the long part in a file and attach it.")
}
