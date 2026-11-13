package state

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const tokensFile = "tokens.json"

type Tokens struct {
	Dir string
	mu  *sync.Mutex
}

func NewTokens(dir string) Tokens {
	return Tokens{Dir: dir, mu: &sync.Mutex{}}
}

var _ session.Tokens = Tokens{}

type tokenJSON struct {
	Host    string    `json:"host"`
	Token   string    `json:"token"`
	SavedAt time.Time `json:"savedAt"`
}

type tokensJSON struct {
	Version int         `json:"version"`
	Tokens  []tokenJSON `json:"tokens"`
}

func (s Tokens) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func (s Tokens) load() ([]tokenJSON, error) {
	data, err := read(s.Dir, tokensFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f tokensJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("tokens.json isn't a token file this engine reads")
	}
	return f.Tokens, nil
}

func (s Tokens) Token(host string) (string, bool, error) {
	defer s.lock()()
	list, err := s.load()
	if err != nil {
		return "", false, err
	}
	for _, t := range list {
		if t.Host == host {
			return t.Token, true, nil
		}
	}
	return "", false, nil
}

func (s Tokens) List() ([]session.TokenInfo, error) {
	defer s.lock()()
	list, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]session.TokenInfo, 0, len(list))
	for _, t := range list {
		out = append(out, session.TokenInfo{Host: t.Host, SavedAt: t.SavedAt})
	}
	slices.SortFunc(out, func(a, b session.TokenInfo) int { return strings.Compare(a.Host, b.Host) })
	return out, nil
}

func (s Tokens) Save(host, token string, at time.Time) error {
	defer s.lock()()
	list, err := s.load()
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(t tokenJSON) bool { return t.Host == host })
	list = append(list, tokenJSON{Host: host, Token: token, SavedAt: at.UTC()})
	return write(s.Dir, tokensFile, tokensJSON{Version: 1, Tokens: list})
}

func (s Tokens) Remove(host string) (bool, error) {
	defer s.lock()()
	list, err := s.load()
	if err != nil {
		return false, err
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(t tokenJSON) bool { return t.Host == host })
	if len(kept) == len(list) {
		return false, nil
	}
	return true, write(s.Dir, tokensFile, tokensJSON{Version: 1, Tokens: kept})
}
