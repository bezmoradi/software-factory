package session

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	LowSpaceBytes   = 2 << 30
	LowSpacePercent = 5
	MaxTreeEntries  = 200_000
	MaxBoxEntries   = MaxTreeEntries * 5
	TreeSizeFor     = 5 * time.Second
	treeSizeKept    = 2 * time.Minute
	maxTreeSizes    = 500
	workspaceFor    = 30 * time.Second
)

const (
	TreeMerged   = "merged"
	TreePushed   = "pushed"
	TreeUnpushed = "unpushed"
	TreeUnknown  = "unknown"
)

var workspaceTree = regexp.MustCompile(`^sf-[0-9a-f]{8}$`)

type DiskSpace interface {
	Space(dir string) (free, total uint64, err error)
}

type TreeSize struct {
	Bytes   int64
	Entries int
	More    bool
}

type TreeSizes interface {
	Size(ctx context.Context, workspace, box, tree string, maxEntries int) (TreeSize, error)
	BoxSize(ctx context.Context, workspace, box string, maxEntries int) (TreeSize, error)
}

type WorkspaceSession struct {
	SID     string
	Name    string
	Repo    string
	Path    string
	Branch  string
	Tree    string
	Box     string
	Running bool
}

type WorkspaceRow struct {
	WorkspaceSession
	Size      TreeSize
	SizeKnown bool
	State     string
	Head      string
	Deletable bool
	Why       string
}

type WorkspaceView struct {
	SpaceKnown bool
	Free       uint64
	Total      uint64
	Low        bool
	LowBelow   uint64
	Sessions   []WorkspaceRow
}

type WorkspacePage interface {
	WorkspaceView(ctx context.Context) (WorkspaceView, error)
	DeleteSession(ctx context.Context, sid string, offer *Offer) (*Offer, error)
}

type WorkspaceConfig struct {
	Dir      string
	Disk     DiskSpace
	Sizes    TreeSizes
	Sessions func() []WorkspaceSession
	Heads    Heads
	History  History
	Main     func(name string) (string, error)
	Audit    AuditAll
	Remove   func(ctx context.Context, sid string, offer *Offer) (*Offer, error)
	Now      func() time.Time
	Logger   shared.Logger
}

type keptSize struct {
	size TreeSize
	at   time.Time
}

type Workspace struct {
	cfg   WorkspaceConfig
	mu    sync.Mutex
	sizes map[string]keptSize
	walk  sync.Mutex
}

var _ WorkspacePage = (*Workspace)(nil)

func NewWorkspace(cfg WorkspaceConfig) *Workspace {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Workspace{cfg: cfg, sizes: map[string]keptSize{}}
}

func (d *Dispatcher) WorkspaceSessions() []WorkspaceSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]WorkspaceSession, 0, len(d.runs))
	for sid, r := range d.runs {
		out = append(out, WorkspaceSession{SID: sid, Name: r.display, Repo: r.repo, Path: r.clone, Branch: r.branch, Tree: r.name, Box: r.box, Running: r.running})
	}
	slices.SortFunc(out, func(a, b WorkspaceSession) int {
		if a.Repo != b.Repo {
			if a.Repo < b.Repo {
				return -1
			}
			return 1
		}
		if a.SID < b.SID {
			return -1
		}
		if a.SID > b.SID {
			return 1
		}
		return 0
	})
	return out
}

func LowSpace(free, total uint64) bool {
	return free < LowSpaceBytes || total > 0 && free*100 < total*LowSpacePercent
}

func (w *Workspace) WorkspaceView(ctx context.Context) (WorkspaceView, error) {
	var v WorkspaceView
	v.LowBelow = LowSpaceBytes
	if w.cfg.Disk != nil && w.cfg.Dir != "" {
		if free, total, err := w.cfg.Disk.Space(w.cfg.Dir); err == nil {
			v.SpaceKnown, v.Free, v.Total = true, free, total
			v.Low = LowSpace(free, total)
		}
	}
	if w.cfg.Sessions == nil {
		return v, nil
	}
	pushed := w.pushedCommits()
	ctx, cancel := context.WithTimeout(ctx, workspaceFor)
	defer cancel()
	for _, s := range w.cfg.Sessions() {
		row := w.row(s, pushed)
		row.Size, row.SizeKnown = w.size(ctx, s.Box, s.Tree)
		v.Sessions = append(v.Sessions, row)
	}
	return v, nil
}

func (w *Workspace) pushedCommits() map[string][]string {
	out := map[string][]string{}
	if w.cfg.Audit == nil {
		return out
	}
	entries, err := w.cfg.Audit.ReadAll(AuditReadAll)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Kind != AuditPush || e.Commit == "" || (e.Result != string(gate.Pushed) && e.Result != string(ResultUpToDate)) {
			continue
		}
		k := e.Repository + "\x00" + e.Branch
		if len(out[k]) < 50 && !slices.Contains(out[k], e.Commit) {
			out[k] = append(out[k], e.Commit)
		}
	}
	return out
}

func (w *Workspace) row(s WorkspaceSession, pushed map[string][]string) WorkspaceRow {
	row := WorkspaceRow{WorkspaceSession: s, State: TreeUnknown}
	head, state, why := w.state(s, pushed)
	row.Head, row.State = head, state
	switch {
	case s.Running:
		row.Why = saidStopFirst
	case state == TreeMerged || state == TreePushed:
		row.Deletable = true
	default:
		row.Why = why
	}
	return row
}

func (w *Workspace) BranchState(s WorkspaceSession) (string, string, string) {
	return w.state(s, w.pushedCommits())
}

func (w *Workspace) state(s WorkspaceSession, pushed map[string][]string) (string, string, string) {
	if w.cfg.Heads == nil || w.cfg.History == nil || w.cfg.Main == nil {
		return "", TreeUnknown, "The engine can't read this session's branch here."
	}
	if s.Box == "" || s.Path == "" {
		return "", TreeUnknown, OldLayoutReason
	}
	if pushed == nil {
		return "", TreeUnknown, "The engine couldn't read its audit file, so it can't tell what was pushed."
	}
	head, err := w.cfg.Heads.Head(s.Path, s.Branch)
	if err != nil || head == "" {
		return "", TreeUnknown, "The engine couldn't read the branch " + s.Branch + "."
	}
	if main, err := w.cfg.Main(s.Repo); err == nil && main != "" {
		if in, err := w.cfg.History.IsAncestor(context.Background(), s.Path, head, main); err == nil && in {
			return head, TreeMerged, ""
		}
	}
	for _, c := range pushed[s.Repo+"\x00"+s.Branch] {
		if c == head {
			return head, TreePushed, ""
		}
		if in, err := w.cfg.History.IsAncestor(context.Background(), s.Path, head, c); err == nil && in {
			return head, TreePushed, ""
		}
	}
	return head, TreeUnpushed, "The branch " + s.Branch + " has commits that aren't pushed or on main. Push them first, or delete the session from its own menu."
}

func (w *Workspace) size(ctx context.Context, box, tree string) (TreeSize, bool) {
	if w.cfg.Sizes == nil || w.cfg.Dir == "" || !workspaceTree.MatchString(tree) || !ValidBox(box) {
		return TreeSize{}, false
	}
	now := w.cfg.Now()
	key := box + "/" + tree
	w.mu.Lock()
	k, ok := w.sizes[key]
	w.mu.Unlock()
	if ok && now.Sub(k.at) >= 0 && now.Sub(k.at) < treeSizeKept {
		return k.size, true
	}
	if ctx.Err() != nil {
		return TreeSize{}, false
	}
	w.walk.Lock()
	defer w.walk.Unlock()
	if ctx.Err() != nil {
		return TreeSize{}, false
	}
	one, cancel := context.WithTimeout(ctx, TreeSizeFor)
	defer cancel()
	size, err := w.cfg.Sizes.Size(one, w.cfg.Dir, box, tree, MaxTreeEntries)
	if err != nil {
		return TreeSize{}, false
	}
	w.mu.Lock()
	if len(w.sizes) >= maxTreeSizes {
		clear(w.sizes)
	}
	w.sizes[key] = keptSize{size: size, at: now}
	w.mu.Unlock()
	return size, true
}

var errNoBoxSize = errors.New("the engine can't measure this session's folder")

func (w *Workspace) BoxSize(ctx context.Context, box string) (TreeSize, error) {
	if w.cfg.Sizes == nil || w.cfg.Dir == "" || !ValidBox(box) {
		return TreeSize{}, errNoBoxSize
	}
	w.walk.Lock()
	defer w.walk.Unlock()
	if err := ctx.Err(); err != nil {
		return TreeSize{}, err
	}
	return w.cfg.Sizes.BoxSize(ctx, w.cfg.Dir, box, MaxBoxEntries)
}

func (w *Workspace) DeleteSession(ctx context.Context, sid string, offer *Offer) (*Offer, error) {
	if w.cfg.Sessions == nil || w.cfg.Remove == nil {
		return nil, conflict("no agent-host", "This engine has no agent-host, so it has no sessions to delete.")
	}
	list := w.cfg.Sessions()
	i := slices.IndexFunc(list, func(s WorkspaceSession) bool { return s.SID == sid })
	if i < 0 {
		return nil, notFound("session", "Only a session this engine started can be deleted from here.")
	}
	s := list[i]
	row := w.row(s, w.pushedCommits())
	if !row.Deletable {
		reason := row.State
		if s.Running {
			reason = "running"
		}
		return nil, conflict(reason, row.Why)
	}
	out, err := w.cfg.Remove(ctx, sid, offer)
	if err == nil {
		w.mu.Lock()
		delete(w.sizes, s.Box+"/"+s.Tree)
		w.mu.Unlock()
		if w.cfg.Logger != nil {
			shared.For(ctx, w.cfg.Logger).Info("workspace", "event", "session-deleted", "state", row.State)
		}
	}
	return out, err
}
