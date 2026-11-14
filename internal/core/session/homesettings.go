package session

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxHomeEntries   = 1000
	MaxHomeName      = 400
	MaxHomeChanged   = 200
	homeSettingsFor  = 90 * time.Second
	homeMCPFor       = 60 * time.Second
	HomeKindContent  = "content"
	HomeKindMetadata = "metadata"
	HomeKindLink     = "link"
	HomeKindMCP      = "mcp"
	HomeKindUnread   = "unread"
	HomeKindStop     = "stop"
)

var homeHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func MaskSecrets(s string) string {
	return maskSecrets(s)
}

type HomeEntry struct {
	Name string
	Kind string
	Hash string
}

type HomeRecord struct {
	Entries []HomeEntry
	At      time.Time
	Since   time.Time
	Changed []string
	More    int
	Problem string
}

type HomeView struct {
	Checked  time.Time
	Since    time.Time
	Changed  []string
	More     int
	First    bool
	Running  bool
	Problem  string
	Watching int
}

type HomeReader interface {
	HomeSettings(ctx context.Context, mcp *Command) ([]HomeEntry, error)
}

type HomeStore interface {
	Load() (HomeRecord, bool, error)
	Save(HomeRecord) error
}

type HomeSettings struct {
	Reader  HomeReader
	Store   HomeStore
	MCP     func() *Command
	Now     func() time.Time
	Logger  shared.Logger
	mu      sync.Mutex
	running bool
	last    HomeView
	loaded  bool
	run     sync.Mutex
}

func validHomeEntry(e HomeEntry) bool {
	if e.Name == "" || len(e.Name) > MaxHomeName || !utf8.ValidString(e.Name) || controlChars.MatchString(e.Name) {
		return false
	}
	switch e.Kind {
	case HomeKindContent, HomeKindMetadata, HomeKindLink, HomeKindMCP:
		return homeHash.MatchString(e.Hash)
	case HomeKindUnread, HomeKindStop:
		return e.Hash == "" || homeHash.MatchString(e.Hash)
	}
	return false
}

func CheckHomeEntries(entries []HomeEntry) ([]HomeEntry, bool) {
	if len(entries) > MaxHomeEntries+8 {
		return nil, false
	}
	out := make([]HomeEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if !validHomeEntry(e) || seen[e.Name] {
			return nil, false
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b HomeEntry) int { return strings.Compare(a.Name, b.Name) })
	return out, true
}

func HomeChanges(before, after []HomeEntry) []string {
	old := map[string]HomeEntry{}
	for _, e := range before {
		old[e.Name] = e
	}
	var changed []string
	now := map[string]bool{}
	for _, e := range after {
		now[e.Name] = true
		if o, ok := old[e.Name]; !ok {
			changed = append(changed, e.Name+" (new)")
		} else if o.Kind != e.Kind || o.Hash != e.Hash {
			changed = append(changed, e.Name)
		}
	}
	for _, e := range before {
		if !now[e.Name] {
			changed = append(changed, e.Name+" (removed)")
		}
	}
	slices.Sort(changed)
	return changed
}

func (h *HomeSettings) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func viewOfHome(r HomeRecord, first bool) HomeView {
	return HomeView{Checked: r.At, Since: r.Since, Changed: slices.Clone(r.Changed), More: r.More, First: first, Problem: r.Problem, Watching: len(r.Entries)}
}

func (h *HomeSettings) Started() {
	if h == nil || h.Reader == nil || h.Store == nil {
		return
	}
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), homeSettingsFor)
		defer cancel()
		h.Check(ctx)
	}()
}

func (h *HomeSettings) Check(ctx context.Context) {
	if h == nil || h.Reader == nil || h.Store == nil {
		return
	}
	h.run.Lock()
	defer h.run.Unlock()
	defer func() {
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}()
	var mcp *Command
	if h.MCP != nil {
		mcp = h.MCP()
	}
	prev, had, loadErr := h.Store.Load()
	entries, err := h.Reader.HomeSettings(ctx, mcp)
	now := h.now().UTC()
	if err == nil {
		var ok bool
		if entries, ok = CheckHomeEntries(entries); !ok {
			err = &HostError{Code: "reply", Message: "agent-host answered a list of settings files the engine refuses"}
		}
	}
	if err != nil {
		h.warn("home-settings-unread")
		h.mu.Lock()
		h.last = viewOfHome(prev, !had)
		h.last.Problem = "agent-host couldn't list the agents' user settings at the last start: " + hostMessage(err)
		h.loaded = true
		h.mu.Unlock()
		return
	}
	next := HomeRecord{Entries: entries, At: now}
	first := !had || loadErr != nil
	if !first {
		next.Since = prev.At
		changed := HomeChanges(prev.Entries, entries)
		if len(changed) > MaxHomeChanged {
			next.More = len(changed) - MaxHomeChanged
			changed = changed[:MaxHomeChanged]
		}
		next.Changed = changed
	}
	if saveErr := h.Store.Save(next); saveErr != nil {
		h.warn("home-settings-unsaved")
		next.Problem = "The engine couldn't save this start's list, so the next start compares with an older one."
	}
	h.mu.Lock()
	h.last = viewOfHome(next, first)
	h.loaded = true
	h.mu.Unlock()
}

func (h *HomeSettings) warn(event string) {
	if h.Logger != nil {
		h.Logger.Warn("agent settings report", "event", event)
	}
}

func (h *HomeSettings) View() HomeView {
	if h == nil || h.Reader == nil || h.Store == nil {
		return HomeView{Problem: "This engine has no agent-host, so it lists no agent settings."}
	}
	h.mu.Lock()
	loaded, v, running := h.loaded, h.last, h.running
	h.mu.Unlock()
	if !loaded {
		r, had, err := h.Store.Load()
		if err != nil {
			return HomeView{Running: running, Problem: "The engine couldn't read its record of the agents' user settings."}
		}
		v = viewOfHome(r, !had)
	}
	v.Running = running
	v.Changed = slices.Clone(v.Changed)
	return v
}

func (d *Dispatcher) UseHomeSettings(h *HomeSettings) {
	d.mu.Lock()
	d.home = h
	d.mu.Unlock()
}

func (d *Dispatcher) homeStarted() {
	d.mu.Lock()
	h := d.home
	d.mu.Unlock()
	h.Started()
}
