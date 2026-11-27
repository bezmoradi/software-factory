package state

import (
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	copiesFile      = "copies.json"
	maxCopyRecords  = 4000
	maxCopyWhy      = 1000
	copiesVersion   = 1
	copyRecordBytes = 4096
)

var (
	copyCardID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	copyCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type Copies struct {
	Dir string
	mu  *sync.Mutex
}

func NewCopies(dir string) Copies { return Copies{Dir: dir, mu: &sync.Mutex{}} }

var _ session.CopyStore = Copies{}

type copyJSON struct {
	Card   string    `json:"card"`
	SID    string    `json:"sid"`
	Box    string    `json:"box"`
	URL    string    `json:"url"`
	Ref    string    `json:"ref,omitempty"`
	Folder string    `json:"folder"`
	Token  bool      `json:"token,omitempty"`
	State  string    `json:"state"`
	Commit string    `json:"commit,omitempty"`
	Why    string    `json:"why,omitempty"`
	Asked  time.Time `json:"asked"`
	At     time.Time `json:"at"`
}

type copiesJSON struct {
	Version int        `json:"version"`
	Copies  []copyJSON `json:"copies"`
}

func copyState(s string) bool {
	switch s {
	case session.CopyWaiting, session.CopyFetching, session.CopyReady, session.CopyFailed:
		return true
	}
	return false
}

func copyOf(c copyJSON) (session.CopyRecord, bool) {
	u, folder, why := session.CopyAddress(c.URL, c.Ref)
	ready := c.State == session.CopyReady
	failed := c.State == session.CopyFailed
	ok := why == "" && u == c.URL && folder == c.Folder && copyCardID.MatchString(c.Card) && sessionID.MatchString(c.SID) && session.ValidBox(c.Box) &&
		copyState(c.State) && (ready && copyCommit.MatchString(c.Commit) || !ready && c.Commit == "") &&
		(failed && c.Why != "" && cleanText(c.Why, maxCopyWhy) || !failed && c.Why == "") && !c.Asked.IsZero() && !c.At.IsZero()
	if !ok {
		return session.CopyRecord{}, false
	}
	return session.CopyRecord{Card: c.Card, SID: c.SID, Box: c.Box, URL: c.URL, Ref: c.Ref, Folder: c.Folder, Token: c.Token, State: c.State,
		Commit: c.Commit, Why: c.Why, Asked: c.Asked, At: c.At}, true
}

func (s Copies) Load() ([]session.CopyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := read(s.Dir, copiesFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f copiesJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != copiesVersion {
		return nil, errors.New(copiesFile + " isn't a record of read-only copies this engine reads")
	}
	var out []session.CopyRecord
	seen := map[string]bool{}
	dropped := 0
	for _, c := range f.Copies {
		v, ok := copyOf(c)
		if !ok || seen[v.Card] || len(out) >= maxCopyRecords {
			dropped++
			continue
		}
		seen[v.Card] = true
		out = append(out, v)
	}
	noteDropped(s.Dir, copiesFile, dropped)
	return out, nil
}

func (s Copies) Save(list []session.CopyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := copiesJSON{Version: copiesVersion, Copies: []copyJSON{}}
	for _, c := range list {
		j := copyJSON{Card: c.Card, SID: c.SID, Box: c.Box, URL: c.URL, Ref: c.Ref, Folder: c.Folder, Token: c.Token, State: c.State,
			Commit: c.Commit, Why: c.Why, Asked: c.Asked.UTC(), At: c.At.UTC()}
		if _, ok := copyOf(j); !ok || len(f.Copies) >= maxCopyRecords || (len(f.Copies)+1)*copyRecordBytes > maxFile {
			continue
		}
		f.Copies = append(f.Copies, j)
	}
	if err := write(s.Dir, copiesFile, f); err != nil {
		return err
	}
	noteDropped(s.Dir, copiesFile, len(list)-len(f.Copies))
	return nil
}
