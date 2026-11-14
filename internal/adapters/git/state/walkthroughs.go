package state

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	walkthroughsFile = "walkthroughs.json"
	maxWalkText      = 16000
	maxWalkReason    = 4000
	maxWalkBranch    = 255
)

var walkStates = []string{session.TestListRunning, session.TestListDone, session.TestListFailed, session.TestListWithheld}

type Walkthroughs struct {
	Dir string
	mu  *sync.Mutex
}

func NewWalkthroughs(dir string) Walkthroughs { return Walkthroughs{Dir: dir, mu: &sync.Mutex{}} }

var _ session.Walkthroughs = Walkthroughs{}

type walkStepJSON struct {
	Do     string `json:"do"`
	Expect string `json:"expect,omitempty"`
	Input  string `json:"input,omitempty"`
	Done   bool   `json:"done,omitempty"`
}

type walkthroughJSON struct {
	SID     string         `json:"sid"`
	Commit  string         `json:"commit"`
	Branch  string         `json:"branch"`
	State   string         `json:"state"`
	Text    string         `json:"text,omitempty"`
	Reason  string         `json:"reason,omitempty"`
	CostUSD float64        `json:"costUsd,omitempty"`
	At      time.Time      `json:"at"`
	Steps   []walkStepJSON `json:"steps,omitempty"`
}

type walkthroughsJSON struct {
	Version      int               `json:"version"`
	Walkthroughs []walkthroughJSON `json:"walkthroughs"`
}

func walkText(s string, limit int) bool {
	return utf8.ValidString(s) && len(s) <= limit && !controlChar.MatchString(s)
}

func walkFree(s string, limit int) bool {
	return utf8.ValidString(s) && len(s) <= limit
}

func walkStepsOf(list []walkStepJSON) ([]session.WalkStep, bool) {
	if len(list) > session.MaxWalkSteps {
		return nil, false
	}
	var out []session.WalkStep
	for _, s := range list {
		limit := 4*session.MaxWalkField + 8
		if s.Do == "" || !walkText(s.Do, limit) || !walkText(s.Expect, limit) || !walkText(s.Input, limit) {
			return nil, false
		}
		out = append(out, session.WalkStep{Do: s.Do, Expect: s.Expect, Input: s.Input, Done: s.Done})
	}
	return out, true
}

func walkthroughOf(w walkthroughJSON) (session.TestListView, bool) {
	if !sessionID.MatchString(w.SID) || !commitID.MatchString(w.Commit) || w.Branch == "" || !walkText(w.Branch, maxWalkBranch) || !slices.Contains(walkStates, w.State) ||
		!walkFree(w.Text, maxWalkText) || !walkFree(w.Reason, maxWalkReason) || math.IsNaN(w.CostUSD) || w.CostUSD < 0 || w.CostUSD > 1e7 {
		return session.TestListView{}, false
	}
	steps, ok := walkStepsOf(w.Steps)
	if !ok {
		return session.TestListView{}, false
	}
	return session.TestListView{Commit: w.Commit, Branch: w.Branch, State: w.State, Text: w.Text, Reason: w.Reason, CostUSD: w.CostUSD, At: w.At, Steps: steps}, true
}

func (s Walkthroughs) Load() (map[string]session.TestListView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, walkthroughsFile)
	if err != nil || data == nil {
		return map[string]session.TestListView{}, err
	}
	var f walkthroughsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("walkthroughs.json isn't a record of walkthroughs this engine reads")
	}
	out := map[string]session.TestListView{}
	for _, w := range f.Walkthroughs {
		if len(out) >= session.MaxWalkLists {
			break
		}
		if _, dup := out[w.SID]; dup {
			continue
		}
		if v, ok := walkthroughOf(w); ok {
			out[w.SID] = v
		}
	}
	return out, nil
}

func (s Walkthroughs) Save(all map[string]session.TestListView) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := walkthroughsJSON{Version: 1, Walkthroughs: []walkthroughJSON{}}
	for sid, v := range all {
		w := walkthroughJSON{SID: sid, Commit: v.Commit, Branch: v.Branch, State: v.State, Reason: v.Reason, CostUSD: v.CostUSD, At: v.At.UTC()}
		if len(v.Steps) == 0 {
			w.Text = v.Text
		}
		for _, st := range v.Steps {
			w.Steps = append(w.Steps, walkStepJSON{Do: st.Do, Expect: st.Expect, Input: st.Input, Done: st.Done})
		}
		if _, ok := walkthroughOf(w); ok {
			f.Walkthroughs = append(f.Walkthroughs, w)
		}
	}
	slices.SortFunc(f.Walkthroughs, func(a, b walkthroughJSON) int { return b.At.Compare(a.At) })
	if len(f.Walkthroughs) > session.MaxWalkLists {
		f.Walkthroughs = f.Walkthroughs[:session.MaxWalkLists]
	}
	for {
		data, err := encode(f)
		if err != nil {
			return err
		}
		if len(data) <= maxFile || len(f.Walkthroughs) == 0 {
			return writeBytes(s.Dir, walkthroughsFile, data)
		}
		f.Walkthroughs = f.Walkthroughs[:len(f.Walkthroughs)-1]
	}
}
