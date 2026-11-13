package session

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"unicode/utf8"
)

const (
	MaxWorkflowText = 100000
	maxWorkflowLine = 2000
	maxWorkflowList = 200
	maxWorkflowRan  = 100
	maxWorkflowName = 200
)

type Doc struct {
	Path    string
	Text    string
	Hash    string
	Clipped bool
}

type Intent struct {
	Path    string
	Title   string
	Status  string
	Agreed  []string
	Assumed []string
	Open    []string
	Built   []string
	Files   []string
}

type Ran struct {
	Name string
	Via  string
	At   int64
}

type Workflow struct {
	Knackpack    bool
	Plan         *Doc
	Research     *Doc
	Requirements *Doc
	Intent       *Intent
	Ran          []Ran
}

func (r *Registry) Workflow(sid string) (Workflow, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.workflows[sid]
	if w == nil {
		return Workflow{}, false
	}
	return *w, true
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func line(v any, max int) (string, bool) {
	s, ok := v.(string)
	if !ok || !utf8.ValidString(s) || utf16Len(s) > max {
		return "", false
	}
	return s, true
}

func docOf(v any, withText bool) (*Doc, bool) {
	if v == nil {
		return nil, true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	p, ok := m["path"].(string)
	if !ok || !absolute(p) {
		return nil, false
	}
	d := &Doc{Path: p}
	if withText {
		text, ok := m["text"].(string)
		if !ok || !utf8.ValidString(text) {
			return nil, false
		}
		if utf16Len(text) > MaxWorkflowText {
			text, d.Clipped = clip(text, MaxWorkflowText), true
		}
		d.Text = text
		d.Hash = hashText(text)
	}
	return d, true
}

func linesOf(v any) ([]string, bool) {
	if v == nil {
		return []string{}, true
	}
	list, ok := v.([]any)
	if !ok || len(list) > maxWorkflowList {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := line(item, maxWorkflowLine)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func intentOf(v any) (*Intent, bool) {
	if v == nil {
		return nil, true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	p, ok := m["path"].(string)
	if !ok || !absolute(p) {
		return nil, false
	}
	in := &Intent{Path: p}
	var good bool
	if in.Title, good = line(orEmpty(m["title"]), maxWorkflowLine); !good {
		return nil, false
	}
	if in.Status, good = line(orEmpty(m["status"]), maxWorkflowName); !good {
		return nil, false
	}
	lists := []*[]string{&in.Agreed, &in.Assumed, &in.Open, &in.Built, &in.Files}
	for i, key := range []string{"agreed", "assumed", "open", "built", "files"} {
		got, ok := linesOf(m[key])
		if !ok {
			return nil, false
		}
		*lists[i] = got
	}
	return in, true
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func ranOf(v any) ([]Ran, bool) {
	if v == nil {
		return []Ran{}, true
	}
	list, ok := v.([]any)
	if !ok || len(list) > maxWorkflowRan {
		return nil, false
	}
	out := make([]Ran, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		name, ok := line(m["name"], maxWorkflowName)
		if !ok || name == "" {
			return nil, false
		}
		via, _ := m["via"].(string)
		if via != "command" && via != "skill" {
			return nil, false
		}
		at, ok := m["at"].(float64)
		if !ok || at < 0 || at > 1<<53 || at != math.Trunc(at) {
			return nil, false
		}
		out = append(out, Ran{Name: name, Via: via, At: int64(at)})
	}
	return out, true
}

func workflowOf(ev map[string]any) (Workflow, bool) {
	knackpack, ok := ev["knackpack"].(bool)
	if !ok {
		return Workflow{}, false
	}
	w := Workflow{Knackpack: knackpack}
	if w.Plan, ok = docOf(ev["plan"], true); !ok {
		return Workflow{}, false
	}
	if w.Research, ok = docOf(ev["research"], false); !ok {
		return Workflow{}, false
	}
	if w.Requirements, ok = docOf(ev["requirements"], true); !ok {
		return Workflow{}, false
	}
	if w.Intent, ok = intentOf(ev["intent"]); !ok {
		return Workflow{}, false
	}
	if w.Ran, ok = ranOf(ev["ran"]); !ok {
		return Workflow{}, false
	}
	return w, true
}

func docFields(d *Doc, withText bool) any {
	if d == nil {
		return nil
	}
	f := map[string]any{"path": d.Path}
	if withText {
		f["text"] = d.Text
		f["hash"] = d.Hash
		if d.Clipped {
			f["clipped"] = true
		}
	}
	return f
}

func anyList(list []string) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

func (w Workflow) fields(seq, ts any) Fields {
	f := Fields{
		"type":         "workflow",
		"seq":          seq,
		"knackpack":    w.Knackpack,
		"plan":         docFields(w.Plan, true),
		"research":     docFields(w.Research, false),
		"requirements": docFields(w.Requirements, true),
		"intent":       nil,
	}
	if ts != nil {
		f["ts"] = ts
	}
	if in := w.Intent; in != nil {
		f["intent"] = map[string]any{
			"path": in.Path, "title": in.Title, "status": in.Status, "agreed": anyList(in.Agreed),
			"assumed": anyList(in.Assumed), "open": anyList(in.Open), "built": anyList(in.Built), "files": anyList(in.Files),
		}
	}
	ran := make([]any, 0, len(w.Ran))
	for _, x := range w.Ran {
		ran = append(ran, map[string]any{"name": x.Name, "via": x.Via, "at": float64(x.At)})
	}
	f["ran"] = ran
	return f
}
