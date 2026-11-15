package session

import (
	"cmp"
	"context"
	"maps"
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
	MaxHomeBoxes     = 256
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
	Box     string
	Entries []HomeEntry
	At      time.Time
	Since   time.Time
	Changed []string
	More    int
	Problem string
	Dropped bool
}

type HomeView struct {
	SID      string
	Session  string
	Checked  time.Time
	Since    time.Time
	Changed  []string
	More     int
	First    bool
	Dropped  bool
	Running  bool
	Problem  string
	Watching int
}

type HomeReport struct {
	Problem  string
	Sessions []HomeView
}

type HomeSession struct {
	Box  string
	SID  string
	Name string
}

type HomeReader interface {
	HomeSettings(ctx context.Context, mcp *Command) ([]HomeEntry, error)
}

type HomeStore interface {
	Load() ([]HomeRecord, error)
	Save([]HomeRecord) ([]string, error)
}

type HomeSettings struct {
	Store    HomeStore
	MCP      func() *Command
	Now      func() time.Time
	Logger   shared.Logger
	mu       sync.Mutex
	running  map[string]int
	last     map[string]HomeView
	sessions func() []HomeSession
	known    func() func(string) bool
	run      sync.Mutex
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

func storedView(r HomeRecord) HomeView {
	if r.Dropped {
		return HomeView{Problem: "The list from this container's last start was dropped because the engine's record of these lists was full, so the next start makes a new baseline and can't show what changed before it."}
	}
	return viewOfHome(r, r.Since.IsZero())
}

func homeOf(records []HomeRecord, box string) (HomeRecord, bool) {
	i := slices.IndexFunc(records, func(r HomeRecord) bool { return r.Box == box })
	if i < 0 {
		return HomeRecord{}, false
	}
	return records[i], true
}

func keptHomes(records []HomeRecord, next HomeRecord, known func(string) bool) []HomeRecord {
	out := []HomeRecord{next}
	for _, r := range records {
		if r.Box != next.Box && ValidBox(r.Box) && (known == nil || known(r.Box)) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out[1:], func(a, b HomeRecord) int { return b.At.Compare(a.At) })
	if len(out) > MaxHomeBoxes {
		out = out[:MaxHomeBoxes]
	}
	return out
}

func (h *HomeSettings) setRunning(box string, step int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running == nil {
		h.running = map[string]int{}
	}
	if h.running[box] += step; h.running[box] <= 0 {
		delete(h.running, box)
	}
}

func (h *HomeSettings) remember(box string, v HomeView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil {
		h.last = map[string]HomeView{}
	}
	h.last[box] = v
}

func (h *HomeSettings) Started(box string, reader HomeReader, dir string) {
	if h == nil || reader == nil || h.Store == nil || !ValidBox(box) {
		return
	}
	h.setRunning(box, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), homeSettingsFor)
		defer cancel()
		h.Check(ctx, box, reader, dir)
	}()
}

func (h *HomeSettings) Check(ctx context.Context, box string, reader HomeReader, dir string) {
	if h == nil || reader == nil || h.Store == nil {
		return
	}
	defer h.setRunning(box, -1)
	var mcp *Command
	if h.MCP != nil {
		if mcp = h.MCP(); mcp != nil {
			c := *mcp
			c.Dir = dir
			mcp = &c
		}
	}
	entries, err := reader.HomeSettings(ctx, mcp)
	now := h.now().UTC()
	if err == nil {
		var ok bool
		if entries, ok = CheckHomeEntries(entries); !ok {
			err = &HostError{Code: "reply", Message: "agent-host answered a list of settings files the engine refuses"}
		}
	}
	h.run.Lock()
	defer h.run.Unlock()
	records, loadErr := h.Store.Load()
	prev, had := homeOf(records, box)
	if err != nil {
		h.warn("home-settings-unread")
		v := HomeView{First: true}
		if had {
			v = storedView(prev)
		}
		v.Problem = strings.TrimSpace(v.Problem + " agent-host couldn't list the agent's user settings in the session's container at the last start: " + hostMessage(err))
		h.remember(box, v)
		return
	}
	next := HomeRecord{Box: box, Entries: entries, At: now}
	if loadErr != nil {
		h.warn("home-settings-unread")
		v := viewOfHome(next, true)
		v.Problem = "The engine couldn't read its record of the agents' user settings (agent-settings.json in its state folder), so it didn't compare this start's list and didn't save it, leaving the other containers' lists as they are. Fix or remove the file, then wake the session."
		h.remember(box, v)
		return
	}
	first := !had || prev.Dropped
	if !first {
		next.Since = prev.At
		changed := HomeChanges(prev.Entries, entries)
		if len(changed) > MaxHomeChanged {
			next.More = len(changed) - MaxHomeChanged
			changed = changed[:MaxHomeChanged]
		}
		next.Changed = changed
	}
	h.mu.Lock()
	knownNow := h.known
	h.mu.Unlock()
	var known func(string) bool
	if knownNow != nil {
		known = knownNow()
	}
	dropped, saveErr := h.Store.Save(keptHomes(records, next, known))
	switch {
	case saveErr != nil:
		h.warn("home-settings-unsaved")
		next.Problem = "The engine couldn't save this start's list, so the next start compares with an older one."
	case slices.Contains(dropped, box):
		h.warn("home-settings-dropped")
		next.Problem = "This start's list didn't fit in the engine's record beside the other containers' lists, so the engine dropped it, and the next start makes a new baseline."
	case len(dropped) > 0:
		h.warn("home-settings-dropped")
	}
	v := viewOfHome(next, first)
	v.Dropped = had && prev.Dropped
	h.remember(box, v)
}

func (h *HomeSettings) Forget(box string) {
	if h == nil || h.Store == nil || !ValidBox(box) {
		return
	}
	h.mu.Lock()
	delete(h.last, box)
	h.mu.Unlock()
	h.run.Lock()
	defer h.run.Unlock()
	records, err := h.Store.Load()
	if err != nil {
		return
	}
	if _, had := homeOf(records, box); !had {
		return
	}
	kept := slices.DeleteFunc(records, func(r HomeRecord) bool { return r.Box == box })
	if _, err := h.Store.Save(kept); err != nil {
		h.warn("home-settings-unsaved")
	}
}

func (h *HomeSettings) warn(event string) {
	if h.Logger != nil {
		h.Logger.Warn("agent settings report", "event", event)
	}
}

func (h *HomeSettings) View() HomeReport {
	if h == nil || h.Store == nil {
		return HomeReport{Problem: "This engine has no agent-host, so it lists no agent settings."}
	}
	h.mu.Lock()
	list, last, running := h.sessions, maps.Clone(h.last), maps.Clone(h.running)
	h.mu.Unlock()
	out := HomeReport{Sessions: []HomeView{}}
	if list == nil {
		return out
	}
	var records []HomeRecord
	var loadErr error
	loaded := false
	for _, s := range list() {
		v, ok := last[s.Box]
		if !ok {
			if !loaded {
				h.run.Lock()
				records, loadErr = h.Store.Load()
				h.run.Unlock()
				loaded = true
			}
			if loadErr != nil {
				v = HomeView{Problem: "The engine couldn't read its record of the agents' user settings."}
			} else if r, had := homeOf(records, s.Box); had {
				v = storedView(r)
			}
		}
		v.SID, v.Session = s.SID, s.Name
		v.Running = running[s.Box] > 0
		v.Changed = slices.Clone(v.Changed)
		out.Sessions = append(out.Sessions, v)
	}
	return out
}

func (d *Dispatcher) UseHomeSettings(h *HomeSettings) {
	d.mu.Lock()
	d.home = h
	d.mu.Unlock()
	if h != nil {
		h.mu.Lock()
		h.sessions, h.known = d.homeSessions, d.knownBoxes
		h.mu.Unlock()
	}
}

func (d *Dispatcher) homeSessions() []HomeSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []HomeSession{}
	for sid, r := range d.runs {
		if r.running && ValidBox(r.box) {
			out = append(out, HomeSession{Box: r.box, SID: sid, Name: runName(r)})
		}
	}
	slices.SortFunc(out, func(a, b HomeSession) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.SID, b.SID))
	})
	return out
}

func (d *Dispatcher) homeStarted(r *run) {
	d.mu.Lock()
	h, box := d.home, r.box
	d.mu.Unlock()
	reader, _ := d.hostFor(box).(HomeReader)
	h.Started(box, reader, BoxFolder(d.cfg.Workspace, box))
}

func (d *Dispatcher) knownBoxes() func(string) bool {
	records, err := d.loadBoxes()
	if err != nil || d.cfg.BoxStore == nil {
		return nil
	}
	known := map[string]bool{}
	for _, b := range records {
		known[b.Box] = true
	}
	return func(box string) bool { return known[box] }
}

func (d *Dispatcher) forgetHome(box string) {
	d.mu.Lock()
	h := d.home
	d.mu.Unlock()
	h.Forget(box)
}
