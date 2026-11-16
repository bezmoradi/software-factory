package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	forgeCardsFile   = "forge-cards.json"
	maxForgeCards    = 1000
	maxCardsSession  = 5
	maxForgeBranch   = 255
	maxForgeBytes    = 3 << 20
	maxForgeTitleLen = session.MaxForgeTitle
	maxForgeBodyLen  = session.MaxForgeBody
)

var forgeCardID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type ForgeCards struct {
	Dir string
	mu  *sync.Mutex
}

func NewForgeCards(dir string) ForgeCards { return ForgeCards{Dir: dir, mu: &sync.Mutex{}} }

var _ session.ForgeCardStore = ForgeCards{}

type forgeCardJSON struct {
	SID    string    `json:"sid"`
	Card   string    `json:"card"`
	Kind   string    `json:"kind"`
	Title  string    `json:"title"`
	Body   string    `json:"body"`
	Head   string    `json:"head,omitempty"`
	Base   string    `json:"base,omitempty"`
	Commit string    `json:"commit,omitempty"`
	Asked  time.Time `json:"asked"`
	From   string    `json:"from,omitempty"`
}

type forgeCardsJSON struct {
	Version int             `json:"version"`
	Cards   []forgeCardJSON `json:"cards"`
}

func forgeCardOf(c forgeCardJSON) (session.SavedForgeCard, bool) {
	kind := session.ForgeKind(c.Kind)
	ok := sessionID.MatchString(c.SID) && forgeCardID.MatchString(c.Card) && (kind == session.ForgeIssue || kind == session.ForgePull) &&
		c.Title != "" && utf8.ValidString(c.Title) && utf8.RuneCountInString(c.Title) <= maxForgeTitleLen &&
		utf8.ValidString(c.Body) && utf8.RuneCountInString(c.Body) <= maxForgeBodyLen && !c.Asked.IsZero()
	if ok && kind == session.ForgePull {
		ok = c.Head != "" && c.Base != "" && cleanText(c.Head, maxForgeBranch) && cleanText(c.Base, maxForgeBranch) && (c.Commit == "" || commitID.MatchString(c.Commit))
	}
	if ok && kind == session.ForgeIssue {
		ok = c.Head == "" && c.Base == "" && c.Commit == ""
	}
	ok = ok && (c.From == session.ForgeFromAgent || c.From == session.ForgeFromPage || c.From == session.ForgeFromFindings)
	if !ok {
		return session.SavedForgeCard{}, false
	}
	if where, _ := session.ForgeSecret(c.Title, c.Body); where != "" {
		return session.SavedForgeCard{}, false
	}
	return session.SavedForgeCard{SID: c.SID, Card: c.Card, Kind: kind, Title: c.Title, Body: c.Body, Head: c.Head, Base: c.Base, Commit: c.Commit, Asked: c.Asked, From: c.From}, true
}

func (s ForgeCards) Load() ([]session.SavedForgeCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, forgeCardsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f forgeCardsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New(forgeCardsFile + " isn't a record of waiting issue and pull request cards this engine reads")
	}
	var out []session.SavedForgeCard
	seen := map[string]bool{}
	per := map[string]int{}
	for _, c := range f.Cards {
		v, ok := forgeCardOf(c)
		if !ok || seen[v.Card] || per[v.SID] >= maxCardsSession || len(out) >= maxForgeCards {
			continue
		}
		seen[v.Card] = true
		per[v.SID]++
		out = append(out, v)
	}
	return out, nil
}

func (s ForgeCards) Save(cards []session.SavedForgeCard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := forgeCardsJSON{Version: 1, Cards: []forgeCardJSON{}}
	per := map[string]int{}
	size := 0
	for _, c := range cards {
		j := forgeCardJSON{SID: c.SID, Card: c.Card, Kind: string(c.Kind), Title: c.Title, Body: c.Body, Head: c.Head, Base: c.Base, Commit: c.Commit, Asked: c.Asked.UTC(), From: c.From}
		cost := 2*(len(j.Title)+len(j.Body)) + 1024
		if _, ok := forgeCardOf(j); !ok || per[c.SID] >= maxCardsSession || len(f.Cards) >= maxForgeCards || size+cost > maxForgeBytes {
			continue
		}
		size += cost
		per[c.SID]++
		f.Cards = append(f.Cards, j)
	}
	sort.SliceStable(f.Cards, func(i, j int) bool { return f.Cards[i].Asked.Before(f.Cards[j].Asked) })
	return write(s.Dir, forgeCardsFile, f)
}
