package session

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	HomeKindMasked   = "masked"
	HomeKindNames    = "names"
	homeDigestV1     = "software-factory home settings v1\n"
	homeMemoryPrefix = "~/.claude/projects/"
	homeServerPrefix = "MCP server"
	homeServerList   = "MCP servers (claude mcp list)"
)

var homeHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func MaskSecrets(s string) string {
	return maskSecrets(s)
}

type HomeEntry struct {
	Name  string
	Kind  string
	Hash  string
	Class int
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

type HomeRead struct {
	Total       int
	Box         string
	Entries     []HomeEntry
	Asked       []string
	More        int
	First       bool
	Problem     string
	Digest      string
	listed      bool
	since       time.Time
	changes     []string
	changesMore int
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
	once     map[string]homeOnce
	run      sync.Mutex
}

func validHomeEntry(e HomeEntry) bool {
	if e.Name == "" || len(e.Name) > MaxHomeName || !utf8.ValidString(e.Name) || controlChars.MatchString(e.Name) {
		return false
	}
	switch e.Kind {
	case HomeKindContent, HomeKindMetadata, HomeKindLink, HomeKindMCP, HomeKindMasked, HomeKindNames:
		return homeHash.MatchString(e.Hash)
	case HomeKindUnread, HomeKindStop:
		return e.Hash == "" || homeHash.MatchString(e.Hash)
	}
	return false
}

func MaskHomeLine(line string) string { return maskedHomeName(line) }

func maskedHomeName(name string) string {
	masked := maskSecrets(name)
	if masked == name {
		return name
	}
	room := MaxHomeName - 32 - len("…")
	if len(masked) > room {
		masked = masked[:room]
		for !utf8.ValidString(masked) {
			masked = masked[:len(masked)-1]
		}
	}
	h := sha256.Sum256([]byte(name))
	return masked + "…" + hex.EncodeToString(h[:])[:32]
}

func CheckHomeEntries(entries []HomeEntry) ([]HomeEntry, bool) {
	if len(entries) > MaxHomeEntries+8 {
		return nil, false
	}
	out := make([]HomeEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		e.Name = maskedHomeName(e.Name)
		if e.Class < HomeRuns || e.Class > HomeOther {
			e.Class = HomeRuns
		}
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

func (r HomeRead) Asks() bool {
	return !r.listed || r.First || len(r.Asked) > 0
}

func (r HomeRead) Listed() bool { return r.listed }

func HomeMemory(name string) bool {
	rest, ok := strings.CutPrefix(name, homeMemoryPrefix)
	if !ok {
		return false
	}
	_, tail, ok := strings.Cut(rest, "/")
	return ok && (tail == "memory" || strings.HasPrefix(tail, "memory/"))
}

func homeServer(e HomeEntry) bool {
	return e.Kind == HomeKindMCP || strings.HasPrefix(e.Name, homeServerPrefix)
}

func homeFiles(entries []HomeEntry) []HomeEntry {
	return slices.DeleteFunc(slices.Clone(entries), homeServer)
}

func homeServers(entries []HomeEntry) []HomeEntry {
	return slices.DeleteFunc(slices.Clone(entries), func(e HomeEntry) bool { return !homeServer(e) })
}

func homeUncompared(e HomeEntry) string {
	switch e.Kind {
	case HomeKindLink:
		return "a link, so what it points to isn't compared"
	case HomeKindMetadata:
		return "compared by size and time only"
	case HomeKindMasked:
		return "holds text shaped like a secret, which isn't compared"
	case HomeKindNames:
		return "git takes a program, a hooks folder, another config file or another git folder from it, which isn't compared"
	case HomeKindUnread:
		return "couldn't be read"
	case HomeKindStop:
		return "the list stops here, so what follows isn't compared"
	}
	return ""
}

func homeAsked(name, change string, e HomeEntry) string {
	why := homeUncompared(e)
	switch {
	case change != "" && why != "":
		return name + " (" + change + "; " + why + ")"
	case change != "":
		return name + " (" + change + ")"
	case why != "":
		return name + " (" + why + ")"
	}
	return name
}

const homePlugins = "~/.claude/plugins/"

const (
	HomeRuns  = 1
	HomeTells = 2
	HomeOther = 3
)

func homeClass(name string, e HomeEntry) int {
	if strings.HasPrefix(name, "(") || homeUncompared(e) != "" || e.Class < HomeRuns || e.Class > HomeOther {
		return HomeRuns
	}
	return e.Class
}

type homeItem struct {
	class int
	name  string
	text  string
}

func homeGroup(name string) string {
	segments := map[string]int{"~/.claude/plugins/cache/": 2, "~/.claude/plugins/marketplaces/": 1, "~/.claude/plugins/": 1, "~/.claude/": 1, "~/.ssh/": 0}
	for _, prefix := range []string{"~/.claude/plugins/cache/", "~/.claude/plugins/marketplaces/", "~/.claude/plugins/", "~/.claude/", "~/.ssh/"} {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", segments[prefix]+1)
		if len(parts) <= segments[prefix] {
			return name
		}
		return prefix + strings.Join(parts[:segments[prefix]], "/")
	}
	return name
}

func homeTop(name string) string {
	for _, p := range []string{"~/.claude/plugins/", "~/.claude/", "~/.ssh/", "~/"} {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return "other places"
}

func homeOrdered(items []homeItem) []homeItem {
	groups := map[string][]homeItem{}
	for _, i := range items {
		g := fmt.Sprint(i.class, "\x00", homeGroup(i.name))
		groups[g] = append(groups[g], i)
	}
	type placed struct {
		at   int
		item homeItem
	}
	var all []placed
	for _, list := range groups {
		slices.SortFunc(list, func(a, b homeItem) int { return strings.Compare(a.name, b.name) })
		for at, i := range list {
			all = append(all, placed{at: at, item: i})
		}
	}
	slices.SortFunc(all, func(a, b placed) int {
		return cmp.Or(cmp.Compare(a.item.class, b.item.class), cmp.Compare(a.at, b.at), strings.Compare(a.item.name, b.item.name))
	})
	out := make([]homeItem, 0, len(all))
	for _, p := range all {
		out = append(out, p.item)
	}
	return out
}

func texts(items []homeItem) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.text)
	}
	return out
}

const homePluginShown = 8

func homePluginRoot(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, homePlugins)
	if !ok {
		return "", false
	}
	if cache, ok := strings.CutPrefix(rest, "cache/"); ok {
		if parts := strings.SplitN(cache, "/", 4); len(parts) == 4 {
			return homePlugins + "cache/" + strings.Join(parts[:3], "/"), true
		}
		return homePlugins + "cache", true
	}
	if first, _, ok := strings.Cut(rest, "/"); ok {
		return homePlugins + first, true
	}
	return strings.TrimSuffix(homePlugins, "/"), true
}

func homeLabelled(i homeItem) int {
	if strings.HasSuffix(i.text, " (new)") {
		return 1
	}
	return 0
}

func homePluginLines(items []homeItem) ([]string, []homeItem, int) {
	byRoot := map[string][]homeItem{}
	var rest []homeItem
	for _, i := range items {
		if root, ok := homePluginRoot(i.name); ok && !strings.HasPrefix(i.name, "(") {
			byRoot[root] = append(byRoot[root], i)
			continue
		}
		rest = append(rest, i)
	}
	roots := slices.Sorted(maps.Keys(byRoot))
	lines := make([]string, 0, len(roots))
	hidden := 0
	for _, root := range roots {
		list := byRoot[root]
		slices.SortFunc(list, func(a, b homeItem) int {
			return cmp.Or(cmp.Compare(homeLabelled(a), homeLabelled(b)), strings.Compare(a.name, b.name))
		})
		var files []string
		for _, i := range list[:min(len(list), homePluginShown)] {
			files = append(files, strings.TrimPrefix(i.text, root+"/"))
		}
		line := root + "/: " + strings.Join(files, ", ")
		if more := len(list) - homePluginShown; more > 0 {
			line += fmt.Sprintf(", and %d more files in this plugin", more)
			hidden += more
		}
		lines = append(lines, line)
	}
	return lines, rest, hidden
}

func homeCard(items []homeItem) ([]string, int) {
	plugins, items, pluginHidden := homePluginLines(items)
	lines, hidden := homeCardRest(items)
	return append(plugins, lines...), hidden + pluginHidden
}

func homeCardRest(items []homeItem) ([]string, int) {
	items = homeOrdered(items)
	if len(items) <= MaxHomeChanged {
		return texts(items), 0
	}
	var runs, tells []homeItem
	var tops []string
	other := map[string]int{}
	for _, i := range items {
		switch i.class {
		case 1:
			runs = append(runs, i)
		case 2:
			tells = append(tells, i)
		default:
			t := homeTop(i.name)
			if other[t] == 0 {
				tops = append(tops, t)
			}
			other[t]++
		}
	}
	room := MaxHomeChanged - len(tops)
	var out []string
	hidden := 0
	switch {
	case len(runs)+len(tells) <= room:
		out = append(texts(runs), texts(tells)...)
	case len(runs) <= room-1:
		show := room - 1 - len(runs)
		out = append(texts(runs), texts(tells[:show])...)
		hidden += len(tells) - show
		out = append(out, fmt.Sprintf("%d more items that give the agent instructions (rules, agents, commands, skills, output styles, CLAUDE.md, plugin.json) aren't listed", len(tells)-show))
	default:
		show := room - 1
		if len(tells) > 0 {
			show--
		}
		out = texts(runs[:show])
		hidden += len(runs) - show
		out = append(out, fmt.Sprintf("%d more items that can run programs or start servers aren't listed", len(runs)-show))
		if len(tells) > 0 {
			hidden += len(tells)
			out = append(out, fmt.Sprintf("%d items that give the agent instructions aren't listed", len(tells)))
		}
	}
	for _, t := range tops {
		hidden += other[t]
		out = append(out, fmt.Sprintf("%d other changed items in %s aren't listed", other[t], t))
	}
	return out, hidden
}

func homeQuestion(before, after []HomeEntry) []homeItem {
	old := map[string]HomeEntry{}
	for _, e := range before {
		old[e.Name] = e
	}
	now := map[string]bool{}
	var asked []homeItem
	for _, e := range after {
		now[e.Name] = true
		if HomeMemory(e.Name) {
			continue
		}
		o, had := old[e.Name]
		item := homeItem{class: homeClass(e.Name, e), name: e.Name}
		switch {
		case !had:
			item.text = homeAsked(e.Name, "new", e)
		case o.Kind != e.Kind || o.Hash != e.Hash:
			item.text = homeAsked(e.Name, "changed", e)
		case homeUncompared(e) != "":
			item.text = homeAsked(e.Name, "", e)
		default:
			continue
		}
		asked = append(asked, item)
	}
	gone := " (removed)"
	if slices.ContainsFunc(after, func(e HomeEntry) bool { return e.Kind == HomeKindStop && strings.HasPrefix(e.Name, "(stopped after ") }) {
		gone = " (not reached: the list stopped first, so it isn't compared)"
	}
	for _, e := range before {
		if !now[e.Name] && !HomeMemory(e.Name) {
			asked = append(asked, homeItem{class: homeClass(e.Name, HomeEntry{Kind: HomeKindContent, Class: e.Class}), name: e.Name, text: e.Name + gone})
		}
	}
	return asked
}

func homeDigest(box, since string, entries []HomeEntry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.Name+"\x00"+e.Kind+"\x00"+e.Hash)
	}
	slices.Sort(lines)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s%s\x00%s\n", homeDigestV1, box, since)
	for _, l := range lines {
		_, _ = fmt.Fprintf(h, "%s\n", l)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func capped(list []string) ([]string, int) {
	if len(list) > MaxHomeChanged {
		return list[:MaxHomeChanged], len(list) - MaxHomeChanged
	}
	return list, 0
}

func (h *HomeSettings) Read(ctx context.Context, box string, reader HomeReader) HomeRead {
	out := HomeRead{Box: box}
	if h == nil || h.Store == nil || !ValidBox(box) {
		out.Problem = "This engine keeps no record of the agent's user settings, so it can't compare them."
		return out
	}
	h.setRunning(box, 1)
	defer h.setRunning(box, -1)
	var entries []HomeEntry
	err := error(&HostError{Code: "reply", Message: "the session's container has no agent-host that lists the agent's settings"})
	if reader != nil {
		entries, err = reader.HomeSettings(ctx, nil)
	}
	if err == nil {
		var ok bool
		if entries, ok = CheckHomeEntries(entries); !ok || slices.ContainsFunc(entries, homeServer) {
			err = &HostError{Code: "reply", Message: "agent-host answered a list of settings files the engine refuses"}
		}
	}
	h.run.Lock()
	defer h.run.Unlock()
	records, loadErr := h.Store.Load()
	switch {
	case err != nil:
		h.warn("home-settings-unread")
		out.Problem = "agent-host couldn't list the agent's user settings in the session's container, so the engine can't tell whether they changed: " + hostMessage(err)
		return out
	case loadErr != nil:
		h.warn("home-settings-unread")
		out.Problem = "The engine couldn't read its record of the agents' user settings (agent-settings.json in its state folder), so it can't tell whether they changed."
		return out
	}
	out.Entries, out.listed = entries, true
	prev, had := homeOf(records, box)
	since := "none"
	if !had || prev.Dropped {
		out.First = true
		var all []homeItem
		for _, e := range entries {
			if !HomeMemory(e.Name) {
				all = append(all, homeItem{class: homeClass(e.Name, e), name: e.Name, text: homeAsked(e.Name, "", e)})
			}
		}
		out.Total = len(all)
		out.Asked, out.More = homeCard(all)
		out.Problem = "The engine has no list of these settings from this container's last start, so it can't show what changed."
		if had {
			out.Problem = "The list from this container's last start was dropped because the engine's record of these lists was full, so it can't show what changed."
		}
	} else {
		since = prev.At.UTC().Format(time.RFC3339Nano)
		out.since = prev.At
		items := homeQuestion(homeFiles(prev.Entries), entries)
		out.Total = len(items)
		out.Asked, out.More = homeCard(items)
		out.changes, out.changesMore = capped(HomeChanges(homeFiles(prev.Entries), entries))
	}
	out.Digest = homeDigest(box, since, entries)
	return out
}

func (h *HomeSettings) knownBoxes() func(string) bool {
	h.mu.Lock()
	knownNow := h.known
	h.mu.Unlock()
	if knownNow == nil {
		return nil
	}
	return knownNow()
}

func (h *HomeSettings) Accept(r HomeRead) error {
	if h == nil || h.Store == nil || !r.listed || !ValidBox(r.Box) {
		return errHomeNotSaved
	}
	known := h.knownBoxes()
	h.run.Lock()
	defer h.run.Unlock()
	records, err := h.Store.Load()
	if err != nil {
		h.warn("home-settings-unread")
		return err
	}
	prev, had := homeOf(records, r.Box)
	entries := slices.Clone(r.Entries)
	if had && !prev.Dropped {
		entries = append(entries, homeServers(prev.Entries)...)
	}
	slices.SortFunc(entries, func(a, b HomeEntry) int { return strings.Compare(a.Name, b.Name) })
	next := HomeRecord{Box: r.Box, Entries: entries, At: h.now().UTC(), Since: r.since, Changed: r.changes, More: r.changesMore}
	dropped, saveErr := h.Store.Save(keptHomes(records, next, known))
	switch {
	case saveErr != nil:
		h.warn("home-settings-unsaved")
		next.Problem = "The engine couldn't save this start's list, so the next wake compares with an older one and asks about what changed since then."
	case slices.Contains(dropped, r.Box):
		h.warn("home-settings-dropped")
		next.Problem = "This start's list didn't fit in the engine's record beside the other containers' lists, so the engine dropped it, and the next wake asks with no list to compare."
		saveErr = errHomeNotSaved
	case len(dropped) > 0:
		h.warn("home-settings-dropped")
	}
	v := viewOfHome(next, r.First)
	v.Dropped = had && prev.Dropped
	h.remember(r.Box, v)
	return saveErr
}

var errHomeNotSaved = errors.New("the list of the agent's settings wasn't saved")

func (h *HomeSettings) Servers(box string, reader HomeReader, dir string) {
	if h == nil || reader == nil || h.Store == nil || h.MCP == nil || !ValidBox(box) {
		return
	}
	mcp := h.MCP()
	if mcp == nil {
		return
	}
	c := *mcp
	c.Dir = dir
	h.setRunning(box, 1)
	go func() {
		defer h.setRunning(box, -1)
		ctx, cancel := context.WithTimeout(context.Background(), homeSettingsFor)
		defer cancel()
		h.listServers(ctx, box, reader, &c)
	}()
}

func (h *HomeSettings) listServers(ctx context.Context, box string, reader HomeReader, mcp *Command) {
	entries, err := reader.HomeSettings(ctx, mcp)
	if err == nil {
		var ok bool
		if entries, ok = CheckHomeEntries(entries); !ok {
			err = errHomeNotSaved
		}
	}
	servers := []HomeEntry{{Name: homeServerList, Kind: HomeKindUnread}}
	if err == nil {
		servers = homeServers(entries)
	}
	known := h.knownBoxes()
	h.run.Lock()
	defer h.run.Unlock()
	records, loadErr := h.Store.Load()
	if loadErr != nil {
		return
	}
	prev, had := homeOf(records, box)
	if !had || prev.Dropped {
		return
	}
	before := homeServers(prev.Entries)
	var changed []string
	if len(before) > 0 {
		changed = HomeChanges(before, servers)
	}
	for _, c := range prev.Changed {
		if !strings.HasPrefix(c, homeServerPrefix) {
			changed = append(changed, c)
		}
	}
	slices.Sort(changed)
	next := prev
	next.Entries = append(homeFiles(prev.Entries), servers...)
	slices.SortFunc(next.Entries, func(a, b HomeEntry) int { return strings.Compare(a.Name, b.Name) })
	next.Changed, next.More = capped(changed)
	next.More += prev.More
	if _, err := h.Store.Save(keptHomes(records, next, known)); err != nil {
		h.warn("home-settings-unsaved")
		return
	}
	h.mu.Lock()
	v, ok := h.last[box]
	h.mu.Unlock()
	if !ok {
		v = viewOfHome(next, next.Since.IsZero())
	}
	v.Changed, v.More, v.Watching = slices.Clone(next.Changed), next.More, len(next.Entries)
	h.remember(box, v)
}

func (h *HomeSettings) Forget(box string) {
	if h == nil || h.Store == nil || !ValidBox(box) {
		return
	}
	h.mu.Lock()
	delete(h.last, box)
	delete(h.once, box)
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

func (d *Dispatcher) homeServers(r *run) {
	d.mu.Lock()
	h, box := d.home, r.box
	d.mu.Unlock()
	reader, _ := d.hostFor(box).(HomeReader)
	h.Servers(box, reader, BoxFolder(d.cfg.Workspace, box))
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
