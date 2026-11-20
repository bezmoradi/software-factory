package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	auditFile      = "audit.jsonl"
	maxAudit       = 32 << 20
	maxPassedRoles = 50
)

type Audit struct {
	Dir string
	mu  *sync.Mutex
}

var (
	_ session.Audit       = Audit{}
	_ session.AuditReader = Audit{}
)

var (
	auditsMu sync.Mutex
	audits   = map[string]*sync.Mutex{}
)

func NewAudit(dir string) Audit {
	auditsMu.Lock()
	defer auditsMu.Unlock()
	mu := audits[dir]
	if mu == nil {
		mu = &sync.Mutex{}
		audits[dir] = mu
	}
	return Audit{Dir: dir, mu: mu}
}

type auditJSON struct {
	Time         time.Time `json:"time"`
	Session      string    `json:"session"`
	Repository   string    `json:"repository,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	Commit       string    `json:"commit,omitempty"`
	Remote       string    `json:"remote,omitempty"`
	By           string    `json:"by"`
	Result       string    `json:"result"`
	Reason       string    `json:"reason"`
	Missing      []string  `json:"missing"`
	PersonReason string    `json:"personReason,omitempty"`
	Kind         string    `json:"kind"`
	Reviewer     string    `json:"reviewer,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	Path         string    `json:"path,omitempty"`
	Blobs        []string  `json:"blobs,omitempty"`
	Request      string    `json:"request,omitempty"`
	Req          string    `json:"req,omitempty"`
	Person       string    `json:"person,omitempty"`
	FormerPerson string    `json:"formerPerson,omitempty"`
	Box          string    `json:"box,omitempty"`
	From         string    `json:"from,omitempty"`
	FromRef      string    `json:"fromRef,omitempty"`
	Source       string    `json:"source,omitempty"`
	Hash         string    `json:"hash,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	PluginCommit string    `json:"pluginCommit,omitempty"`
	ClaudeCode   string    `json:"claudeCode,omitempty"`
	Models       []string  `json:"models,omitempty"`
	Passed       []string  `json:"passed,omitempty"`
	AllPassed    bool      `json:"allPassed,omitempty"`
}

func (a Audit) Append(e session.AuditEntry) error {
	if a.mu != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	if !ownDir(a.Dir) {
		return errDir
	}
	missing := e.Missing
	if missing == nil {
		missing = []string{}
	}
	kind := e.Kind
	if kind == "" {
		kind = session.AuditPush
	}
	line, err := json.Marshal(auditJSON{Time: e.Time.UTC(), Session: e.Session, Repository: e.Repository, Branch: e.Branch, Commit: e.Commit,
		Remote: e.Remote, By: e.By, Result: e.Result, Reason: e.Reason, Missing: missing, PersonReason: e.PersonReason,
		Kind: kind, Reviewer: e.Reviewer, Verdict: e.Verdict, Path: e.Path, Blobs: e.Blobs, Request: e.Request, Hash: e.Hash, Agent: e.Agent,
		PluginCommit: e.PluginCommit, ClaudeCode: e.ClaudeCode, Models: e.Models, Req: e.Req, Person: personOf(a.Dir, e.Person), FormerPerson: matching(e.FormerPerson, personID),
		Box: e.Box, From: e.From, FromRef: e.FromRef, Source: e.Source, Passed: e.Passed, AllPassed: e.AllPassed})
	if err != nil {
		return err
	}
	path := filepath.Join(a.Dir, auditFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return errors.New("audit.jsonl can't be opened")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return errors.New("audit.jsonl isn't a 0600 file this user owns")
	}
	if info.Size() > auditLimit() {
		_ = f.Close()
		noteDropped(a.Dir, auditFile, countLines(filepath.Join(a.Dir, "audit.1.jsonl")))
		if err := os.Rename(path, filepath.Join(a.Dir, "audit.1.jsonl")); err != nil {
			return errors.New("audit.jsonl couldn't be rotated")
		}
		return a.appendNew(path, line)
	}
	return finish(f, line)
}

func (a Audit) appendNew(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("audit.jsonl can't be opened")
	}
	return finish(f, line)
}

func finish(f *os.File, line []byte) error {
	_, err := f.Write(append(line, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

const auditTail = 4 << 20

func (a Audit) Read(repository, branch string, limit int) ([]session.AuditEntry, error) {
	if a.mu != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	if !ownDir(a.Dir) {
		return nil, errDir
	}
	var out []session.AuditEntry
	for _, name := range []string{auditFile, "audit.1.jsonl"} {
		lines, err := tail(filepath.Join(a.Dir, name))
		if err != nil {
			return nil, err
		}
		for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
			var j auditJSON
			if json.Unmarshal(lines[i], &j) != nil || j.Repository != repository {
				continue
			}
			kind := j.Kind
			if kind == "" {
				kind = session.AuditPush
			}
			if kind == session.AuditReview || (j.Branch != branch && !session.ProjectWideAudit(kind) && (j.Branch != "" || !session.ProjectLevelAudit(kind))) {
				continue
			}
			e := entryOf(j, kind)
			if e.Person == "" {
				e.Person = ownerIn(a.Dir)
			}
			out = append(out, e)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func entryOf(j auditJSON, kind string) session.AuditEntry {
	return session.AuditEntry{Time: j.Time, Session: j.Session, Repository: j.Repository, Branch: j.Branch, Commit: j.Commit, Remote: j.Remote,
		By: j.By, Result: j.Result, Reason: j.Reason, Missing: j.Missing, PersonReason: j.PersonReason, Kind: kind, Reviewer: j.Reviewer,
		Verdict: j.Verdict, Path: j.Path, Blobs: j.Blobs, Request: matching(j.Request, requestID), Hash: matching(j.Hash, teamSHA),
		Agent: matching(j.Agent, agentID), PluginCommit: matching(j.PluginCommit, commitID), ClaudeCode: matching(j.ClaudeCode, versionText),
		Models: matchingAll(j.Models, modelName, maxModels), Req: matching(j.Req, requestTag), Person: matching(j.Person, personID),
		Box: matching(j.Box, boxID), From: matching(j.From, startKind), FromRef: matching(j.FromRef, startRef), Source: matching(j.Source, sessionID),
		Passed: passedRoles(j.Passed), AllPassed: j.AllPassed}
}

func passedRoles(list []string) []string {
	var out []string
	for _, s := range list {
		if len(out) >= maxPassedRoles {
			break
		}
		if session.ValidRole(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

var (
	requestTag = regexp.MustCompile(`^[0-9a-f]{16}$`)
	boxID      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	startKind  = regexp.MustCompile(`^[a-z][a-z-]{0,19}$`)
	startRef   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

func personOf(dir, given string) string {
	if personID.MatchString(given) {
		return given
	}
	return ownerIn(dir)
}

func tail(path string) ([][]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New(filepath.Base(path) + " can't be opened")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New(filepath.Base(path) + " isn't a 0600 file this user owns")
	}
	from := max(info.Size()-auditTail, 0)
	buf := make([]byte, info.Size()-from)
	n, err := f.ReadAt(buf, from)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New(filepath.Base(path) + " can't be read")
	}
	buf = buf[:n]
	if from > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	var lines [][]byte
	for line := range bytes.SplitSeq(buf, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines, nil
}
