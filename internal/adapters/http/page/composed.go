package page

import (
	"context"
	"errors"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const maxCommentFile = 4096

var errComposedShape = errors.New("composed shape")

func (s *Server) composedText(ctx context.Context, sid string, body map[string]any) (string, error) {
	_, hasText := body["text"]
	revise, hasRevise := body["revise"]
	comments, hasComments := body["comments"]
	ask, hasAsk := body["ask"]
	parts := 0
	for _, has := range []bool{hasRevise, hasComments, hasAsk} {
		if has {
			parts++
		}
	}
	if parts != 1 || hasText || present(body["images"]) || present(body["files"]) {
		return "", errComposedShape
	}
	if hasAsk {
		name, ok := ask.(string)
		if !ok || !session.ValidAsk(name) {
			return "", errComposedShape
		}
		return session.AskMessage(name, func() (session.GateView, error) {
			if s.cfg.Work == nil {
				return session.GateView{}, nil
			}
			return s.cfg.Work.View(ctx, sid)
		})
	}
	if hasComments {
		list, ok := commentsOf(comments)
		if !ok {
			return "", errComposedShape
		}
		return session.CommentsMessage(list)
	}
	m, ok := revise.(map[string]any)
	kind, okKind := m["kind"].(string)
	note, okNote := m["note"].(string)
	if !ok || !keysWithin(m, "kind", "note") || !okKind || !session.ValidReviseKind(kind) || !okNote {
		return "", errComposedShape
	}
	return session.ReviseMessage(kind, note, func() (string, error) {
		if s.cfg.Work == nil {
			return "", nil
		}
		v, err := s.cfg.Work.View(ctx, sid)
		if err != nil || v.Plan == nil {
			return "", err
		}
		return v.Plan.Path, nil
	})
}

func commentsOf(v any) ([]session.LineComment, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]session.LineComment, 0, len(list))
	for _, x := range list {
		m, ok := x.(map[string]any)
		file, okFile := m["file"].(string)
		line, okLine := integer(m["line"])
		removed, okRemoved := m["removed"].(bool)
		text, okText := m["text"].(string)
		if !ok || !keysWithin(m, "file", "line", "removed", "text") || !okFile || file == "" || len(file) > maxCommentFile || !okLine || line < 0 || line > 1<<31 || !okRemoved || !okText {
			return nil, false
		}
		out = append(out, session.LineComment{File: file, Line: int(line), Removed: removed, Text: text})
	}
	return out, true
}
