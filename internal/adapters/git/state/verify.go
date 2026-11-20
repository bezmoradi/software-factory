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
	verifyRunsFile = "verify-runs.json"
	maxVerifyText  = session.MaxCommandTail + 1024
)

var (
	verifyRunID   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	verifyHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	verifyImageID = regexp.MustCompile(`^(sha256:[0-9a-f]{64})?$`)
	verifyStates  = map[string]bool{session.RunRunning: true, session.RunPassed: true, session.RunFailed: true, session.RunTimedOut: true, session.RunError: true,
		session.VerifyOOM: true, session.VerifyInterrupted: true, session.VerifyStopped: true}
)

type VerifyRuns struct {
	Dir string
	mu  *sync.Mutex
}

func NewVerifyRuns(dir string) VerifyRuns { return VerifyRuns{Dir: dir, mu: &sync.Mutex{}} }

var _ session.VerifyStore = VerifyRuns{}

type verifyRunJSON struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	URL         string    `json:"url"`
	SID         string    `json:"sid"`
	Branch      string    `json:"branch"`
	Commit      string    `json:"commit"`
	Kind        string    `json:"kind"`
	CommandHash string    `json:"commandHash"`
	Image       string    `json:"image"`
	ImageID     string    `json:"imageId,omitempty"`
	State       string    `json:"state"`
	Code        int       `json:"code"`
	Tail        string    `json:"tail,omitempty"`
	Cut         bool      `json:"cut,omitempty"`
	Withheld    bool      `json:"withheld,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	By          string    `json:"by"`
	LimitSec    int64     `json:"limitSeconds,omitempty"`
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended,omitzero"`
	Owner       string    `json:"owner,omitempty"`
}

type verifyRunsJSON struct {
	Version int             `json:"version"`
	Runs    []verifyRunJSON `json:"runs"`
}

func validVerifyRun(r verifyRunJSON) bool {
	image, okImage := session.CheckVerifyImage(r.Image)
	return verifyRunID.MatchString(r.ID) && validProject(session.ProjectID{Name: r.Project, URL: r.URL}) && (r.SID == "" || sessionID.MatchString(r.SID)) &&
		commitID.MatchString(r.Commit) && r.Kind == string(session.VerifyCheck) && verifyHash.MatchString(r.CommandHash) &&
		okImage && image != "" && verifyImageID.MatchString(r.ImageID) && verifyStates[r.State] &&
		(r.By == session.VerifyByPage || r.By == session.VerifyByEngine) && cleanText(r.Branch, 400) &&
		len(r.Tail) <= maxVerifyText && utf8.ValidString(r.Tail) && len(r.Reason) <= maxVerifyText && utf8.ValidString(r.Reason) &&
		r.LimitSec >= 0 && r.LimitSec <= 24*60*60
}

func (s VerifyRuns) Load() ([]session.VerifyRun, error) {
	defer locked(s.mu)()
	data, err := read(s.Dir, verifyRunsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f verifyRunsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("verify-runs.json isn't a record of verification runs this engine reads")
	}
	out := make([]session.VerifyRun, 0, len(f.Runs))
	seen := map[string]bool{}
	for _, r := range f.Runs {
		if !validVerifyRun(r) || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, session.VerifyRun{ID: r.ID, Project: session.ProjectID{Name: r.Project, URL: r.URL}, SID: r.SID, Branch: r.Branch, Commit: r.Commit,
			Kind: session.VerifyKind(r.Kind), CommandHash: r.CommandHash, Image: r.Image, ImageID: r.ImageID, State: r.State, Code: r.Code, Tail: r.Tail, Cut: r.Cut,
			Withheld: r.Withheld, Reason: r.Reason, By: r.By, Limit: time.Duration(r.LimitSec) * time.Second, Started: r.Started, Ended: r.Ended})
	}
	if over := len(out) - session.MaxVerifyRecords; over > 0 {
		out = out[over:]
	}
	return out, nil
}

func (s VerifyRuns) Save(runs []session.VerifyRun) error {
	owner := ownerIn(s.Dir)
	defer locked(s.mu)()
	if over := len(runs) - session.MaxVerifyRecords; over > 0 {
		runs = runs[over:]
	}
	f := verifyRunsJSON{Version: 1, Runs: make([]verifyRunJSON, 0, len(runs))}
	for _, r := range runs {
		f.Runs = append(f.Runs, verifyRunJSON{ID: r.ID, Project: r.Project.Name, URL: r.Project.URL, SID: r.SID, Branch: r.Branch, Commit: r.Commit, Kind: string(r.Kind),
			CommandHash: r.CommandHash, Image: r.Image, ImageID: r.ImageID, State: r.State, Code: r.Code, Tail: r.Tail, Cut: r.Cut, Withheld: r.Withheld, Reason: r.Reason,
			By: r.By, LimitSec: int64(r.Limit / time.Second), Started: r.Started.UTC(), Ended: r.Ended.UTC(), Owner: owner})
	}
	return write(s.Dir, verifyRunsFile, f)
}
