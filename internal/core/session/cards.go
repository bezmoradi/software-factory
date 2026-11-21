package session

import (
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
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

type numberRange struct {
	min, max       float64
	hasMin, hasMax bool
}

type card struct {
	id        string
	sid       string
	kind      string
	complete  bool
	always    bool
	auto      bool
	questions []string
	numbers   map[string]numberRange
	multi     map[string]bool
	waiter    *waiter
	answer    any
	answered  bool
	at        time.Time
	tool      string
	hash      string
	away      bool
}

func (r *Registry) indexCard(sid string, ev map[string]any) bool {
	id, _ := ev["id"].(string)
	kind, _ := ev["type"].(string)
	if !cardID.MatchString(id) || !cardTypes[kind] {
		return false
	}
	if kind == "permission" && ev["irreversible"] == true {
		delete(ev, "always")
		ev["auto"] = false
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
	numbers := map[string]numberRange{}
	multi := map[string]bool{}
	if kind == "ask" {
		list, _ := ev["questions"].([]any)
		for _, q := range list {
			entry, _ := q.(map[string]any)
			text, _ := entry["question"].(string)
			if text != "" && len(text) <= maxAnswer && len(questions) < maxQuestions {
				questions = append(questions, text)
				if entry["kind"] == "number" {
					numbers[text] = rangeOf(entry)
				}
				if entry["multiSelect"] == true {
					multi[text] = true
				}
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
		numbers:   numbers,
		multi:     multi,
	}
	if kind == "permission" {
		r.cards[id].tool, r.cards[id].hash = toolKindOf(ev), shownInputHash(ev)
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

func rangeOf(entry map[string]any) numberRange {
	var r numberRange
	if v, ok := entry["min"].(float64); ok && !math.IsInf(v, 0) && !math.IsNaN(v) {
		r.min, r.hasMin = v, true
	}
	if v, ok := entry["max"].(float64); ok && !math.IsInf(v, 0) && !math.IsNaN(v) {
		r.max, r.hasMax = v, true
	}
	return r
}

func shownNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func (r numberRange) problem(text string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	inRange := err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) && (!r.hasMin || n >= r.min) && (!r.hasMax || n <= r.max)
	switch {
	case inRange:
		return ""
	case r.hasMin && r.hasMax:
		return "That question takes a number from " + shownNumber(r.min) + " to " + shownNumber(r.max) + "."
	case r.hasMin:
		return "That question takes a number of at least " + shownNumber(r.min) + "."
	case r.hasMax:
		return "That question takes a number of at most " + shownNumber(r.max) + "."
	}
	return "That question takes a number."
}

const (
	emptyAnswer = "Type an answer first."
	longAnswer  = "An answer holds at most 2,000 characters."
	longNote    = "A note holds at most 2,000 characters."
	emptyRevise = "Say what should change in the plan first."
)

func normalAnswer(c *card, value any) any {
	v, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := maps.Clone(v)
	switch c.kind {
	case "permission", "plan":
		if note, isText := out["note"].(string); isText {
			out["note"] = strings.TrimSpace(note)
			if c.kind == "permission" && out["note"] == "" {
				delete(out, "note")
			}
		}
	case "ask":
		for k, x := range out {
			text, joined := x.(string)
			if list, isList := x.([]any); isList && (c.multi[k] || len(list) == 1) {
				parts := make([]string, 0, len(list))
				for _, item := range list {
					t, isText := item.(string)
					if !isText {
						parts = nil
						break
					}
					if t = strings.TrimSpace(t); t != "" {
						parts = append(parts, t)
					}
				}
				if parts != nil || len(list) == 0 {
					text, joined = strings.Join(parts, ", "), true
				}
			}
			if !joined {
				continue
			}
			if text = strings.TrimSpace(text); text == "" {
				delete(out, k)
			} else {
				out[k] = text
			}
		}
	}
	return out
}

func answerError(c *card, value any) error {
	ok, why := checkAnswer(c, value)
	switch {
	case ok:
		return nil
	case why != "":
		return explained(invalid("answer"), why)
	}
	return invalid("answer")
}

func checkAnswer(c *card, value any) (bool, string) {
	v, ok := value.(map[string]any)
	if !ok {
		return false, ""
	}
	choice, _ := v["choice"].(string)
	note, hasNote := v["note"]
	switch c.kind {
	case "permission":
		if choice == "no" {
			_, isText := note.(string)
			switch {
			case !onlyKeys(v, "choice", "note") || hasNote && !isText:
				return false, ""
			case !noteOK(note, hasNote):
				return false, longNote
			}
			return true, ""
		}
		if len(v) != 1 {
			return false, ""
		}
		switch choice {
		case "yes":
			return c.complete, ""
		case "always":
			return c.complete && c.always, ""
		case "auto":
			return c.complete && c.auto, ""
		}
		return false, ""
	case "plan":
		if choice == "terminal" {
			return len(v) == 1, ""
		}
		text, isText := note.(string)
		switch {
		case choice != "revise" || !onlyKeys(v, "choice", "note") || hasNote && !isText:
			return false, ""
		case strings.TrimSpace(text) == "":
			return false, emptyRevise
		case len(text) > maxAnswer:
			return false, longNote
		}
		return true, ""
	case "ask":
		if len(v) == 0 {
			return false, emptyAnswer
		}
		asked := map[string]bool{}
		for _, q := range c.questions {
			asked[q] = true
		}
		for k, a := range v {
			text, ok := a.(string)
			switch {
			case !asked[k] || !ok:
				return false, ""
			case strings.TrimSpace(text) == "":
				return false, emptyAnswer
			case len(text) > maxAnswer:
				return false, longAnswer
			}
			if r, isNumber := c.numbers[k]; isNumber {
				if why := r.problem(text); why != "" {
					return false, why
				}
			}
		}
		return true, ""
	}
	return false, ""
}
