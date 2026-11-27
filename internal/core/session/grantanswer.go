package session

import (
	"context"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	carryOnAllowed = "Carry on with your task."
	carryOnRefused = "Carry on without it where you can."
)

func (g *Grants) answerAgent(ctx context.Context, sid, text string) {
	if text == "" {
		return
	}
	if g.cfg.Idle(sid) && g.cfg.Tell(sid, NoteMarker+text) == nil {
		shared.For(ctx, g.cfg.Logger).Info("grants", "event", "answer-sent")
		return
	}
	g.cfg.Note(sid, text)
	shared.For(ctx, g.cfg.Logger).Info("grants", "event", "answer-noted")
}

func (g *Grants) allowedWords(gr Grant) string {
	lasts := " for " + scopeWords(gr.Scope, gr.Project) + ", " + untilWords(gr.Expires) + ", on Software Factory's page, so you can reach it "
	switch {
	case gr.Credential != "":
		g.mu.Lock()
		c, ok := g.credentialByID(gr.Credential)
		g.mu.Unlock()
		if !ok {
			return ""
		}
		return madeNote(c, gr) + " " + carryOnAllowed
	case gr.Port > 0:
		return "The person allowed " + gr.Target() + lasts + "now: " + PortReachWords(gr.Host, gr.Port) + ". " + carryOnAllowed
	}
	return "The person allowed " + gr.Host + lasts + "over HTTPS through the proxy now. " + carryOnAllowed
}

func refusedNote(what string) string {
	return "The person didn't allow " + what + " on Software Factory's page. Don't ask for it again for 10 minutes; if you still need it, say why in your reply. " + carryOnRefused
}
