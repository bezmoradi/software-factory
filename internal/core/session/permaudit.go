package session

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

const (
	AuditPermission = "permission-answer"
	ChoiceOnce      = "allow once"
	ChoiceSave      = "allow and save"
	ChoiceAuto      = "allow and switch to auto mode"
	ChoiceDeny      = "deny"
	otherTool       = "other"
)

var toolKindPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,79}$`)

func toolKindOf(ev map[string]any) string {
	key := "tool"
	if _, ok := ev["toolKind"]; ok {
		key = "toolKind"
	}
	if s, ok := ev[key].(string); ok && toolKindPattern.MatchString(s) {
		return s
	}
	return otherTool
}

func shownInputHash(ev map[string]any) string {
	shown, _ := ev["detail"].(string)
	if shown == "" {
		shown, _ = ev["summary"].(string)
	}
	sum := sha256.Sum256([]byte(shown))
	return hex.EncodeToString(sum[:])
}

func choiceWord(value any) string {
	m, _ := value.(map[string]any)
	switch m["choice"] {
	case "yes":
		return ChoiceOnce
	case "always":
		return ChoiceSave
	case "auto":
		return ChoiceAuto
	}
	return ChoiceDeny
}

func (h *Halt) answered(sid string, k *card, value any, auto bool) {
	choice := choiceWord(value)
	e := AuditEntry{Session: sid, Kind: AuditPermission, Result: choice, Hash: k.hash, By: ByPage,
		Reason: "the developer answered a permission card for a " + k.tool + " call on the page: " + choice}
	if auto {
		e.By = ByEngine
		e.Reason = "away mode denied a permission card for a " + k.tool + " call automatically, with the note to the agent: " + AwayNote
	}
	go h.audit(e)
}
