package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	splitsFile    = "splits.json"
	maxSplitBytes = 3 << 20
)

var splitCardID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Splits struct {
	Dir string
	mu  *sync.Mutex
}

func NewSplits(dir string) Splits { return Splits{Dir: dir, mu: &sync.Mutex{}} }

var _ session.SplitStore = Splits{}

type splitCardJSON struct {
	Card    string    `json:"card"`
	Goal    string    `json:"goal"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
	State   string    `json:"state"`
	SID     string    `json:"sid,omitempty"`
	Edited  bool      `json:"edited,omitempty"`
	Asked   time.Time `json:"asked"`
	At      time.Time `json:"at"`
}

type splitsJSON struct {
	Version int             `json:"version"`
	Cards   []splitCardJSON `json:"cards"`
}

func splitState(s string) bool {
	switch s {
	case session.SplitWaiting, session.SplitQueued, session.SplitStarting, session.SplitStarted, session.SplitDismissed:
		return true
	}
	return false
}

func splitCardOf(c splitCardJSON) (session.SplitCard, bool) {
	started := c.State == session.SplitStarted
	ok := splitCardID.MatchString(c.Card) && sessionID.MatchString(c.Goal) && splitState(c.State) &&
		c.Title != "" && cleanText(c.Title, session.MaxSplitTitle) &&
		c.Message != "" && utf8.ValidString(c.Message) && utf8.RuneCountInString(c.Message) <= session.MaxSplitMessage &&
		(started && sessionID.MatchString(c.SID) || !started && c.SID == "") && !c.Asked.IsZero()
	if !ok {
		return session.SplitCard{}, false
	}
	return session.SplitCard{Card: c.Card, Goal: c.Goal, Title: c.Title, Message: c.Message, State: c.State, SID: c.SID, Edited: c.Edited, Asked: c.Asked, At: c.At}, true
}

func (s Splits) Load() ([]session.SplitCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, splitsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f splitsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New(splitsFile + " isn't a record of split task cards this engine reads")
	}
	var out []session.SplitCard
	seen := map[string]bool{}
	for _, c := range f.Cards {
		v, ok := splitCardOf(c)
		if !ok || seen[v.Card] || len(out) >= session.MaxSplitCards {
			continue
		}
		seen[v.Card] = true
		out = append(out, v)
	}
	return out, nil
}

func (s Splits) Save(cards []session.SplitCard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := splitsJSON{Version: 1, Cards: []splitCardJSON{}}
	size := 0
	for _, c := range cards {
		j := splitCardJSON{Card: c.Card, Goal: c.Goal, Title: c.Title, Message: c.Message, State: c.State, SID: c.SID, Edited: c.Edited, Asked: c.Asked.UTC(), At: c.At.UTC()}
		cost := 2*(len(j.Title)+len(j.Message)) + 512
		if _, ok := splitCardOf(j); !ok || len(f.Cards) >= session.MaxSplitCards || size+cost > maxSplitBytes {
			continue
		}
		size += cost
		f.Cards = append(f.Cards, j)
	}
	if err := write(s.Dir, splitsFile, f); err != nil {
		return err
	}
	noteDropped(s.Dir, splitsFile, len(cards)-len(f.Cards))
	return nil
}

func (s Splits) Dropped(n int) { noteDropped(s.Dir, splitsFile, n) }
