package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	pairsFile    = "pairs.json"
	maxPairBytes = 3 << 20
	maxPairName  = 80
	maxPairURL   = 300
)

var (
	pairModel  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,79}$`)
	pairEffort = regexp.MustCompile(`^[a-z]{1,20}$`)
)

type Pairs struct {
	Dir string
	mu  *sync.Mutex
}

func NewPairs(dir string) Pairs { return Pairs{Dir: dir, mu: &sync.Mutex{}} }

var _ session.PairStore = Pairs{}

type pairMemberJSON struct {
	Agent  string `json:"agent"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	SID    string `json:"sid,omitempty"`
}

type benchJSON struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
	Base   string `json:"base"`
	Head   string `json:"head"`
	Merge  string `json:"merge,omitempty"`
}

type pairJSON struct {
	ID      string           `json:"id"`
	Repo    string           `json:"repo"`
	Path    string           `json:"path"`
	Commit  string           `json:"commit"`
	Origin  string           `json:"origin,omitempty"`
	Name    string           `json:"name,omitempty"`
	Type    string           `json:"type,omitempty"`
	Message string           `json:"message"`
	Members []pairMemberJSON `json:"members"`
	Kept    string           `json:"kept,omitempty"`
	Bench   *benchJSON       `json:"benchmark,omitempty"`
	Issue   int              `json:"issue,omitempty"`
	At      time.Time        `json:"at"`
	History bool             `json:"historyOnly,omitempty"`
	Owner   string           `json:"owner,omitempty"`
}

type pairsJSON struct {
	Version int        `json:"version"`
	Pairs   []pairJSON `json:"pairs"`
}

func memberOK(m pairMemberJSON) bool {
	return agentID.MatchString(m.Agent) && (m.Model == "" || pairModel.MatchString(m.Model)) && (m.Effort == "" || pairEffort.MatchString(m.Effort)) &&
		(m.SID == "" || sessionID.MatchString(m.SID))
}

func benchOK(b *benchJSON) bool {
	return b == nil || b.Number > 0 && b.Number <= session.MaxIssueNumber && cleanText(b.Title, session.MaxForgeTitle) && cleanText(b.URL, maxPairURL) &&
		commitID.MatchString(b.Base) && commitID.MatchString(b.Head) && (b.Merge == "" || commitID.MatchString(b.Merge))
}

func pairOf(p pairJSON) (session.Pair, bool) {
	ok := pairID.MatchString(p.ID) && repoName.MatchString(p.Repo) && cleanAbs(p.Path) && commitID.MatchString(p.Commit) && cleanText(p.Origin, maxOrigin) &&
		cleanText(p.Name, maxPairName) && (p.Type == "" || typeName.MatchString(p.Type)) &&
		p.Message != "" && utf8.ValidString(p.Message) && utf8.RuneCountInString(p.Message) <= session.MaxText &&
		len(p.Members) >= 1 && len(p.Members) <= session.MaxPairAgents && benchOK(p.Bench) && p.Issue >= 0 && p.Issue <= session.MaxIssueNumber && !p.At.IsZero()
	if !ok {
		return session.Pair{}, false
	}
	out := session.Pair{ID: p.ID, Repo: p.Repo, Path: p.Path, Commit: p.Commit, Origin: p.Origin, Name: p.Name, Type: p.Type, Message: p.Message, Issue: p.Issue, At: p.At, History: p.History}
	for _, m := range p.Members {
		if !memberOK(m) {
			return session.Pair{}, false
		}
		out.Members = append(out.Members, session.PairMember{Agent: m.Agent, Model: m.Model, Effort: m.Effort, SID: m.SID})
	}
	if sessionID.MatchString(p.Kept) {
		out.Kept = p.Kept
	}
	if b := p.Bench; b != nil {
		out.Bench = &session.Benchmark{Number: b.Number, Title: b.Title, URL: b.URL, Base: b.Base, Head: b.Head, Merge: b.Merge}
	}
	return out, true
}

func (s Pairs) Load() ([]session.Pair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, pairsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f pairsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New(pairsFile + " isn't a record of paired sessions this engine reads")
	}
	var out []session.Pair
	seen := map[string]bool{}
	for _, p := range f.Pairs {
		v, ok := pairOf(p)
		if !ok || seen[v.ID] || len(out) >= session.MaxPairs {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out, nil
}

func (s Pairs) Save(list []session.Pair) error {
	owner := ownerIn(s.Dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := pairsJSON{Version: 1, Pairs: []pairJSON{}}
	size := 0
	for _, p := range slices.Backward(list) {
		j := pairJSON{Owner: owner, ID: p.ID, Repo: p.Repo, Path: p.Path, Commit: p.Commit, Origin: p.Origin, Name: p.Name, Type: p.Type, Message: p.Message, Kept: p.Kept, Issue: p.Issue, At: p.At.UTC(), History: p.History}
		for _, m := range p.Members {
			j.Members = append(j.Members, pairMemberJSON{Agent: m.Agent, Model: m.Model, Effort: m.Effort, SID: m.SID})
		}
		if b := p.Bench; b != nil {
			j.Bench = &benchJSON{Number: b.Number, Title: b.Title, URL: b.URL, Base: b.Base, Head: b.Head, Merge: b.Merge}
		}
		cost := 2*len(j.Message) + 1024
		if _, ok := pairOf(j); !ok || len(f.Pairs) >= session.MaxPairs || size+cost > maxPairBytes {
			continue
		}
		size += cost
		f.Pairs = append(f.Pairs, j)
	}
	slices.Reverse(f.Pairs)
	if err := write(s.Dir, pairsFile, f); err != nil {
		return err
	}
	noteDropped(s.Dir, pairsFile, len(list)-len(f.Pairs))
	return nil
}

func (s Pairs) Dropped(n int) { noteDropped(s.Dir, pairsFile, n) }
