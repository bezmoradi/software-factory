package session

import (
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxTypeReason   = 200
	MaxTypeID       = 32
	WorkTypeHeld    = "held"
	WorkTypeRefused = "refused"
)

type typeAsk struct {
	held   bool
	typeID string
	reason string
}

type TypeAnswer struct {
	Result string
	Reason string
}

type TypeAsk struct {
	Held   bool
	Type   string
	Name   string
	Reason string
}

type TypeCard struct {
	Text     string
	Suggests string
	Type     string
	Reason   string
	Use      string
}

func (w *Work) unusable(sid string) error {
	if err := heldBranch(w.cfg.Doubled, sid); err != nil {
		return err
	}
	if w.cfg.HistoryOnly != nil {
		return w.cfg.HistoryOnly(sid)
	}
	return nil
}

func (w *Work) SuggestType(sid, id, reason string) TypeAnswer {
	refuse := func(why string) TypeAnswer { return TypeAnswer{Result: WorkTypeRefused, Reason: why} }
	if w.unusable(sid) != nil {
		return refuse("this session is read-only on this engine, so it takes no work type")
	}
	ws, ok := w.session(sid)
	if !ok {
		return refuse("only a session this engine started can use this tool")
	}
	setup := w.packSetup(sid, ws.repo)
	if ws.chosen {
		name := ws.typeID
		if t, ok := typeIn(setup.setup.Types, ws.typeID); ok {
			name = t.Name
		}
		return refuse("this session's work type is already " + CleanLine(name, 80) + ", so there is nothing to suggest; go on with the work")
	}
	t, ok := typeIn(setup.setup.Types, id)
	if !ok {
		return refuse(CleanLine(clipUnits(id, MaxTypeID), MaxTypeID) + " isn't one of this project's work types, which are " + typeIDs(setup.setup.Types) + "; suggest one of them")
	}
	clean := strings.TrimSpace(CleanLine(reason, MaxTypeReason+1))
	switch {
	case strings.ContainsAny(reason, "\r\n  ") || !utf8.ValidString(reason):
		return refuse("the reason must be one line of plain text")
	case clean == "":
		return refuse("give a reason: one line saying why the work type fits")
	case utf8.RuneCountInString(reason) > MaxTypeReason:
		return refuse("the reason must be at most 200 characters")
	}
	w.mu.Lock()
	if s := w.sessions[sid]; s != nil && !s.chosen {
		held := s.ask != nil && s.ask.held
		s.ask = &typeAsk{held: held, typeID: t.ID, reason: clean}
	}
	w.mu.Unlock()
	w.cfg.Logger.Info("work type", "event", "suggested", "type", t.ID)
	return TypeAnswer{Result: WorkTypeHeld, Reason: "Software Factory's page shows your suggestion of " + CleanLine(t.Name, 80) + " to the developer, who picks the work type; end your turn now, and your next message says when you can go on"}
}

func typeIDs(types []workflow.WorkType) string {
	ids := make([]string, 0, len(types))
	for _, t := range types {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

func (w *Work) heldForType(sid string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil || s.chosen {
		return
	}
	if s.ask == nil {
		s.ask = &typeAsk{}
	}
	if !s.ask.held {
		next := *s.ask
		next.held = true
		s.ask = &next
	}
}

func (w *Work) typeAskOf(g gathered) *TypeAsk {
	a := g.ws.ask
	if g.ws.chosen || a == nil || !a.held && a.typeID == "" {
		return nil
	}
	out := &TypeAsk{Held: a.held}
	if t, ok := typeIn(g.setup.setup.Types, a.typeID); ok {
		out.Type, out.Name, out.Reason = t.ID, t.Name, a.reason
	}
	return out
}

func WorkTypeCard(i Info, v GateView) *TypeCard {
	a := v.Ask
	if a == nil || v.Chosen {
		return nil
	}
	who := AgentWord(i.Agent)
	c := &TypeCard{Text: who + " suggested a work type for this session. The work type decides which steps and reviews apply."}
	if a.Held {
		c.Text = who + " wants to change a file, and this session has no work type yet. The work type decides which steps and reviews apply, so the change waits until you pick one."
	}
	if a.Type != "" {
		c.Suggests, c.Type, c.Reason, c.Use = who+" suggests "+a.Name+", and says why:", a.Type, a.Reason, "Use "+a.Name
	}
	return c
}

func typeTold(name string) string {
	return "The developer picked the work type " + CleanLine(name, 80) + " on the software factory page, so you can go on and change files now. Its steps and reviews apply from here."
}
