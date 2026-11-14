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

const boxesFile = "boxes.json"

type Boxes struct {
	Dir string
	mu  *sync.Mutex
}

func NewBoxes(dir string) Boxes { return Boxes{Dir: dir, mu: &sync.Mutex{}} }

var _ session.BoxStore = Boxes{}

type boxJSON struct {
	Box     string    `json:"box"`
	Project string    `json:"project"`
	Agent   string    `json:"agent"`
	Created time.Time `json:"created"`
	Pending bool      `json:"pending,omitempty"`
}

type boxesJSON struct {
	Version int       `json:"version"`
	Boxes   []boxJSON `json:"boxes"`
}

func (b Boxes) lock() func() {
	if b.mu == nil {
		return func() {}
	}
	b.mu.Lock()
	return b.mu.Unlock
}

func (b Boxes) Load() ([]session.BoxRecord, error) {
	defer b.lock()()
	data, err := read(b.Dir, boxesFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f boxesJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, errors.New("boxes.json isn't a record of session containers this engine reads")
	}
	var out []session.BoxRecord
	for _, r := range f.Boxes {
		if !session.ValidBox(r.Box) || !repoName.MatchString(r.Project) || !agentID.MatchString(r.Agent) || len(out) >= session.MaxBoxRecords {
			continue
		}
		if slices.ContainsFunc(out, func(o session.BoxRecord) bool { return o.Box == r.Box }) {
			continue
		}
		out = append(out, session.BoxRecord{Box: r.Box, Project: r.Project, Agent: r.Agent, Created: r.Created, Pending: r.Pending})
	}
	return out, nil
}

func (b Boxes) Save(list []session.BoxRecord) error {
	defer b.lock()()
	if len(list) > session.MaxBoxRecords {
		return errors.New("more session containers than the engine records")
	}
	f := boxesJSON{Version: 1, Boxes: make([]boxJSON, 0, len(list))}
	for _, r := range list {
		if !session.ValidBox(r.Box) || !repoName.MatchString(r.Project) || !agentID.MatchString(r.Agent) {
			return errors.New("a session container the engine can't record")
		}
		f.Boxes = append(f.Boxes, boxJSON{Box: r.Box, Project: r.Project, Agent: r.Agent, Created: r.Created.UTC(), Pending: r.Pending})
	}
	slices.SortFunc(f.Boxes, func(a, c boxJSON) int { return strings.Compare(a.Box, c.Box) })
	return write(b.Dir, boxesFile, f)
}
