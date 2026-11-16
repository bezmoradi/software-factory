package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	repliesFolder  = "review-replies"
	repliesVersion = 1
	maxReplyFile   = 7*session.MaxFullReply + 4096
	givenVersion   = 1
	maxGivenData   = session.MaxGivenPrompt + session.MaxGivenFlags*session.MaxGivenFlag + session.MaxGivenFiles*session.MaxGivenFile + session.MaxGivenWhy
	maxGivenFile   = 7*maxGivenData + 4096
)

var (
	replyName = regexp.MustCompile(`^([0-9a-f]{16})-([0-9a-f]{32})\.json$`)
	givenName = regexp.MustCompile(`^([0-9a-f]{16})-([0-9a-f]{32})-given\.json$`)
)

type Replies struct {
	Dir string
}

var _ session.ReviewReplies = Replies{}

type replyJSON struct {
	Version int       `json:"version"`
	Session string    `json:"session"`
	Commit  string    `json:"commit"`
	Branch  string    `json:"branch"`
	Agent   string    `json:"agent"`
	Run     int       `json:"run"`
	Started time.Time `json:"started"`
	Clipped bool      `json:"clipped,omitempty"`
	Text    string    `json:"text"`
}

func sessionPart(sid string) string {
	sum := sha256.Sum256([]byte("session\x00" + sid))
	return hex.EncodeToString(sum[:8])
}

func keyPart(k review.Key) string {
	sum := sha256.Sum256([]byte(k.Commit + "\x00" + k.Branch + "\x00" + k.Agent + "\x00" + strconv.Itoa(k.Number())))
	return hex.EncodeToString(sum[:16])
}

func replyFile(sid string, k review.Key) string {
	return sessionPart(sid) + "-" + keyPart(k) + ".json"
}

func givenFile(sid string, k review.Key) string {
	return sessionPart(sid) + "-" + keyPart(k) + "-given.json"
}

type givenJSON struct {
	Version int       `json:"version"`
	Session string    `json:"session"`
	Commit  string    `json:"commit"`
	Branch  string    `json:"branch"`
	Agent   string    `json:"agent"`
	Run     int       `json:"run"`
	Started time.Time `json:"started"`
	Why     string    `json:"why,omitempty"`
	Given   bool      `json:"given,omitempty"`
	Prompt  string    `json:"prompt,omitempty"`
	Flags   []string  `json:"flags,omitempty"`
	Files   []string  `json:"files,omitempty"`
}

func keptText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s)
}

func keptTexts(list []string, count, limit int) bool {
	if len(list) > count {
		return false
	}
	for _, s := range list {
		if !keptText(s, limit) {
			return false
		}
	}
	return true
}

func validGiven(g session.KeptGiven) bool {
	if !keptText(g.Why, session.MaxGivenWhy) {
		return false
	}
	if g.Given == nil {
		return true
	}
	return keptText(g.Given.Prompt, session.MaxGivenPrompt) && keptTexts(g.Given.Flags, session.MaxGivenFlags, session.MaxGivenFlag) &&
		keptTexts(g.Given.Files, session.MaxGivenFiles, session.MaxGivenFile)
}

func (s Replies) PutGiven(sid string, e session.ReviewEntry, g session.KeptGiven) error {
	some := g.Why != "" || g.Given != nil
	dir, err := s.folder(some)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	name := givenFile(sid, e.Key)
	if !some {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if !validGiven(g) {
		return errors.New("what a review was given is larger than the engine keeps, or isn't UTF-8")
	}
	f := givenJSON{Version: givenVersion, Session: sid, Commit: e.Key.Commit, Branch: e.Key.Branch, Agent: e.Key.Agent, Run: e.Key.Number(), Started: e.StartedAt.UTC(), Why: g.Why}
	if g.Given != nil {
		f.Given, f.Prompt, f.Flags, f.Files = true, g.Given.Prompt, g.Given.Flags, g.Given.Files
	}
	data, err := encode(f)
	if err != nil {
		return err
	}
	if len(data) > maxGivenFile {
		return errors.New("what a review was given would make a file larger than the engine reads back")
	}
	return writeFile(dir, name, data)
}

func (s Replies) GetGiven(sid string, k review.Key, started time.Time) (session.KeptGiven, bool) {
	dir, err := s.folder(false)
	if err != nil {
		return session.KeptGiven{}, false
	}
	data, err := read(dir, givenFile(sid, k))
	if err != nil || data == nil || len(data) > maxGivenFile {
		return session.KeptGiven{}, false
	}
	var f givenJSON
	if json.Unmarshal(data, &f) != nil || f.Version < 1 || f.Version > givenVersion {
		return session.KeptGiven{}, false
	}
	if f.Session != sid || f.Commit != k.Commit || f.Branch != k.Branch || f.Agent != k.Agent || f.Run != k.Number() || !f.Started.Equal(started) {
		return session.KeptGiven{}, false
	}
	out := session.KeptGiven{Why: f.Why}
	if f.Given {
		out.Given = &session.RunGiven{Prompt: f.Prompt, Flags: f.Flags, Files: f.Files}
	}
	if !validGiven(out) {
		return session.KeptGiven{}, false
	}
	return out, true
}

func (s Replies) folder(create bool) (string, error) {
	if !ownDir(s.Dir) {
		return "", errDir
	}
	dir := filepath.Join(s.Dir, repliesFolder)
	if create {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return "", os.ErrNotExist
	}
	if !ownDir(dir) {
		return "", errDir
	}
	return dir, nil
}

func (s Replies) Put(sid string, e session.ReviewEntry) error {
	dir, err := s.folder(e.Reply != "")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	name := replyFile(sid, e.Key)
	if e.Reply == "" {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if len(e.Reply) > session.MaxFullReply || !utf8.ValidString(e.Reply) {
		return errors.New("a reviewer's reply is larger than the engine keeps, or isn't UTF-8")
	}
	data, err := encode(replyJSON{Version: repliesVersion, Session: sid, Commit: e.Key.Commit, Branch: e.Key.Branch, Agent: e.Key.Agent, Run: e.Key.Number(),
		Started: e.StartedAt.UTC(), Clipped: e.ReplyClipped, Text: e.Reply})
	if err != nil {
		return err
	}
	if len(data) > maxReplyFile {
		return errors.New("a reviewer's reply would make a file larger than the engine reads back")
	}
	return writeFile(dir, name, data)
}

func (s Replies) Get(sid string, k review.Key, started time.Time) (string, bool, bool) {
	dir, err := s.folder(false)
	if err != nil {
		return "", false, false
	}
	data, err := read(dir, replyFile(sid, k))
	if err != nil || data == nil || len(data) > maxReplyFile {
		return "", false, false
	}
	var r replyJSON
	if json.Unmarshal(data, &r) != nil || r.Version < 1 || r.Version > repliesVersion {
		return "", false, false
	}
	if r.Session != sid || r.Commit != k.Commit || r.Branch != k.Branch || r.Agent != k.Agent || r.Run != k.Number() || !r.Started.Equal(started) {
		return "", false, false
	}
	if r.Text == "" || len(r.Text) > session.MaxFullReply || !utf8.ValidString(r.Text) {
		return "", false, false
	}
	return r.Text, r.Clipped, true
}

func (s Replies) Keep(keys []review.Key) error {
	kept := make(map[string]bool, len(keys))
	for _, k := range keys {
		kept[keyPart(k)] = true
	}
	return s.sweep(func(_, key string) bool { return !kept[key] })
}

func (s Replies) Forget(sid string) error {
	mine := sessionPart(sid)
	return s.sweep(func(owner, _ string) bool { return owner == mine })
}

func (s Replies) sweep(drop func(owner, key string) bool) error {
	dir, err := s.folder(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var failed error
	for _, e := range entries {
		name := e.Name()
		gone := false
		m := replyName.FindStringSubmatch(name)
		if m == nil {
			m = givenName.FindStringSubmatch(name)
		}
		if m != nil && e.Type().IsRegular() {
			gone = drop(m[1], m[2])
		} else if strings.HasPrefix(name, ".") && e.Type().IsRegular() {
			info, err := e.Info()
			gone = err == nil && time.Since(info.ModTime()) >= strayTempAfter && ownedByMe(info)
		}
		if !gone {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = err
		}
	}
	return failed
}
