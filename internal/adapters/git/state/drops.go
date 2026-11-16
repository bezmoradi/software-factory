package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	dropsFile    = "drops.json"
	maxDropRows  = 64
	maxDropStore = 64
)

var (
	dropsMu       sync.Mutex
	devAuditLimit int64
)

func DevAuditLimit(n int64) error {
	if n < 1024 || n > maxAudit {
		return errors.New("the development audit size must be between 1024 and 33554432 bytes")
	}
	devAuditLimit = n
	return nil
}

func auditLimit() int64 {
	if devAuditLimit > 0 {
		return devAuditLimit
	}
	return maxAudit
}

type Drops struct {
	Dir string
}

type dropJSON struct {
	Store string    `json:"store"`
	Count int       `json:"count"`
	Since time.Time `json:"since"`
	Last  time.Time `json:"last"`
}

type dropsJSON struct {
	Version int        `json:"version"`
	Drops   []dropJSON `json:"drops"`
}

func dropOK(d dropJSON) bool {
	return d.Store != "" && len(d.Store) <= maxDropStore && cleanText(d.Store, maxDropStore) && d.Count > 0 && !d.Since.IsZero() && !d.Last.Before(d.Since)
}

func (d Drops) load() ([]dropJSON, error) {
	data, err := read(d.Dir, dropsFile)
	if err != nil || data == nil {
		return nil, err
	}
	var f dropsJSON
	if err := json.Unmarshal(data, &f); err != nil || !readsVersion(dropsFile, f.Version) {
		return nil, errors.New("drops.json isn't a record of dropped records this engine reads")
	}
	var out []dropJSON
	for _, r := range f.Drops {
		if dropOK(r) && len(out) < maxDropRows && !slices.ContainsFunc(out, func(x dropJSON) bool { return x.Store == r.Store }) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (d Drops) Load() ([]session.DroppedRecords, error) {
	dropsMu.Lock()
	defer dropsMu.Unlock()
	list, err := d.load()
	if err != nil {
		return nil, err
	}
	out := make([]session.DroppedRecords, 0, len(list))
	for _, r := range list {
		out = append(out, session.DroppedRecords{Store: r.Store, Count: r.Count, Since: r.Since, Last: r.Last})
	}
	slices.SortFunc(out, func(a, b session.DroppedRecords) int { return strings.Compare(a.Store, b.Store) })
	return out, nil
}

func (a Archive) Drops() ([]session.DroppedRecords, error) {
	return Drops{Dir: a.Dir}.Load()
}

func noteDropped(dir, store string, n int) {
	if n <= 0 {
		return
	}
	dropsMu.Lock()
	defer dropsMu.Unlock()
	list, err := Drops{Dir: dir}.load()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	i := slices.IndexFunc(list, func(x dropJSON) bool { return x.Store == store })
	switch {
	case i >= 0:
		list[i].Count = min(list[i].Count+n, 1<<40)
		list[i].Last = now
	case len(list) < maxDropRows:
		list = append(list, dropJSON{Store: store, Count: n, Since: now, Last: now})
	default:
		return
	}
	_ = write(dir, dropsFile, dropsJSON{Version: writesVersion(dropsFile), Drops: list})
}

func countLines(path string) int {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	n := 0
	buf := make([]byte, 64<<10)
	for {
		k, err := f.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if err != nil {
			return n
		}
	}
}

type reviewKeyJSON struct {
	Commit string `json:"commit"`
	Branch string `json:"branch"`
	Agent  string `json:"agent"`
}

func reviewsGone(dir string, kept []session.ReviewEntry) int {
	data, err := read(dir, reviewsFile)
	if err != nil || data == nil {
		return 0
	}
	var f struct {
		Reviews []reviewKeyJSON `json:"reviews"`
	}
	if json.Unmarshal(data, &f) != nil {
		return 0
	}
	keep := make(map[reviewKeyJSON]bool, len(kept))
	for _, e := range kept {
		keep[reviewKeyJSON{Commit: e.Key.Commit, Branch: e.Key.Branch, Agent: e.Key.Agent}] = true
	}
	gone := 0
	for _, k := range f.Reviews {
		if !keep[k] {
			gone++
		}
	}
	return gone
}
