package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	ApprovedFolder  = "approved"
	approvedVersion = 1
	approvedSuffix  = ".json"
)

type ApprovedTexts struct {
	Dir string
	mu  *sync.Mutex
}

func NewApprovedTexts(dir string) ApprovedTexts {
	return ApprovedTexts{Dir: dir, mu: &sync.Mutex{}}
}

var _ session.ApprovedStore = ApprovedTexts{}

type keptTextJSON struct {
	Hash string `json:"hash"`
	Text string `json:"text"`
}

type approvedTextsJSON struct {
	Version      int           `json:"version"`
	Requirements *keptTextJSON `json:"requirements,omitempty"`
	Plan         *keptTextJSON `json:"plan,omitempty"`
}

func (a ApprovedTexts) lock() func() {
	if a.mu == nil {
		return func() {}
	}
	a.mu.Lock()
	return a.mu.Unlock
}

func (a ApprovedTexts) folder() string { return filepath.Join(a.Dir, ApprovedFolder) }

func keptJSONOf(t session.KeptText) *keptTextJSON {
	if t.Hash == "" {
		return nil
	}
	return &keptTextJSON{Hash: t.Hash, Text: t.Text}
}

func keptOf(t *keptTextJSON) session.KeptText {
	if t == nil || !teamSHA.MatchString(t.Hash) || !utf8.ValidString(t.Text) {
		return session.KeptText{}
	}
	return session.KeptText{Hash: t.Hash, Text: t.Text}
}

func (a ApprovedTexts) Load(sid string) (session.ApprovedTexts, error) {
	if !sessionID.MatchString(sid) {
		return session.ApprovedTexts{}, errors.New("not a session id")
	}
	defer a.lock()()
	dir := a.folder()
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return session.ApprovedTexts{}, nil
	}
	data, err := read(dir, sid+approvedSuffix)
	if err != nil || data == nil {
		return session.ApprovedTexts{}, err
	}
	var f approvedTextsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != approvedVersion {
		return session.ApprovedTexts{}, errors.New("an approved-texts file isn't one this engine reads")
	}
	return session.ApprovedTexts{Requirements: keptOf(f.Requirements), Plan: keptOf(f.Plan)}, nil
}

func (a ApprovedTexts) Save(sid string, t session.ApprovedTexts) error {
	if !sessionID.MatchString(sid) {
		return errors.New("not a session id")
	}
	defer a.lock()()
	dir := a.folder()
	if err := ownFolder(dir); err != nil {
		return err
	}
	return write(dir, sid+approvedSuffix, approvedTextsJSON{Version: approvedVersion, Requirements: keptJSONOf(t.Requirements), Plan: keptJSONOf(t.Plan)})
}

func (a ApprovedTexts) Drop(sid string) error {
	if !sessionID.MatchString(sid) {
		return errors.New("not a session id")
	}
	defer a.lock()()
	err := os.Remove(filepath.Join(a.folder(), sid+approvedSuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (a ApprovedTexts) Keep(sids []string) error {
	keep := map[string]bool{}
	for _, sid := range sids {
		keep[sid] = true
	}
	defer a.lock()()
	dir := a.folder()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil || !ownDir(dir) {
		return errDir
	}
	var failed error
	for _, e := range entries {
		sid, ok := strings.CutSuffix(e.Name(), approvedSuffix)
		if !ok || !e.Type().IsRegular() || !sessionID.MatchString(sid) || keep[sid] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = err
		}
	}
	return failed
}

func approvedFiles(dir string) []string {
	folder := filepath.Join(dir, ApprovedFolder)
	if !ownDir(folder) {
		return nil
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		sid, ok := strings.CutSuffix(e.Name(), approvedSuffix)
		if ok && e.Type().IsRegular() && sessionID.MatchString(sid) {
			out = append(out, e.Name())
		}
	}
	return out
}
