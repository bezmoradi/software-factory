package session

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

var cardID = regexp.MustCompile(`^[0-9a-f]{32}$`)

var cardTypes = map[string]bool{"permission": true, "ask": true, "plan": true}

const (
	maxCards        = 1000
	maxSessionCards = 100
	maxQuestions    = 20
	maxAnswer       = 2000
)

type card struct {
	id        string
	sid       string
	kind      string
	complete  bool
	always    bool
	auto      bool
	questions []string
	waiter    *waiter
	answer    any
	answered  bool
	at        time.Time
}

func (r *Registry) indexCard(sid string, ev map[string]any) bool {
	id, _ := ev["id"].(string)
	kind, _ := ev["type"].(string)
	if !cardID.MatchString(id) || !cardTypes[kind] {
		return false
	}
	if known, ok := r.cards[id]; ok {
		return known.sid == sid && known.kind == kind
	}
	own, strangers := 0, 0
	for _, c := range r.cards {
		if c.sid == sid {
			own++
		}
		if r.admitted[c.sid] == nil {
			strangers++
		}
	}
	if own >= maxSessionCards || r.admitted[sid] == nil && strangers >= maxCards {
		return false
	}
	var questions []string
	if kind == "ask" {
		list, _ := ev["questions"].([]any)
		for _, q := range list {
			entry, _ := q.(map[string]any)
			text, _ := entry["question"].(string)
			if text != "" && len(text) <= maxAnswer && len(questions) < maxQuestions {
				questions = append(questions, text)
			}
		}
		if len(questions) == 0 {
			return false
		}
	}
	always, _ := ev["always"].(string)
	complete, isBool := ev["complete"].(bool)
	r.cards[id] = &card{
		id:        id,
		sid:       sid,
		kind:      kind,
		complete:  !isBool || complete,
		always:    always != "",
		auto:      kind == "permission" && ev["auto"] == true,
		questions: questions,
	}
	return true
}

func (r *Registry) dropCard(id string) {
	c, ok := r.cards[id]
	if !ok {
		return
	}
	if c.waiter != nil {
		c.waiter.finish(waitGone)
		c.waiter = nil
	}
	delete(r.cards, id)
}

func noteOK(v any, present bool) bool {
	if !present {
		return true
	}
	s, ok := v.(string)
	return ok && len(s) <= maxAnswer
}

func onlyKeys(m map[string]any, allowed ...string) bool {
	for k := range m {
		found := slices.Contains(allowed, k)
		if !found {
			return false
		}
	}
	return true
}

func validAnswer(c *card, value any) bool {
	v, ok := value.(map[string]any)
	if !ok {
		return false
	}
	choice, _ := v["choice"].(string)
	note, hasNote := v["note"]
	switch c.kind {
	case "permission":
		if choice == "no" {
			return onlyKeys(v, "choice", "note") && noteOK(note, hasNote)
		}
		if len(v) != 1 {
			return false
		}
		switch choice {
		case "yes":
			return c.complete
		case "always":
			return c.complete && c.always
		case "auto":
			return c.complete && c.auto
		}
		return false
	case "plan":
		if choice == "terminal" {
			return len(v) == 1
		}
		text, _ := note.(string)
		return choice == "revise" && onlyKeys(v, "choice", "note") && strings.TrimSpace(text) != "" && len(text) <= maxAnswer
	case "ask":
		if len(v) == 0 {
			return false
		}
		asked := map[string]bool{}
		for _, q := range c.questions {
			asked[q] = true
		}
		for k, a := range v {
			text, ok := a.(string)
			if !asked[k] || !ok || strings.TrimSpace(text) == "" || len(text) > maxAnswer {
				return false
			}
		}
		return true
	}
	return false
}
