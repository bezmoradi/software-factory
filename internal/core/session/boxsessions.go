package session

import (
	"context"
	"errors"
	"path"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	PendingBoxFor   = 30 * time.Minute
	MaxBoxRecords   = 1000
	BoxesFolder     = "s"
	boxCallFor      = 3 * time.Minute
	OldLayoutReason = "This session ran in the old shared container; start a new session."
)

var (
	maxBoxesSet = regexp.MustCompile(`SF_MAX_BOXES=([0-9]{1,4})`)
	clonePath   = regexp.MustCompile(`^/.+?/` + BoxesFolder + `/[0-9a-f]{16}/repos/([A-Za-z0-9][A-Za-z0-9._-]{0,99})$`)
)

func CloneProject(p string) (string, bool) {
	m := clonePath.FindStringSubmatch(p)
	if m == nil || path.Clean(p) != p {
		return "", false
	}
	return m[1], true
}

type BoxRecord struct {
	Box     string
	Project string
	Agent   string
	Created time.Time
	Pending bool
}

type BoxStore interface {
	Load() ([]BoxRecord, error)
	Save([]BoxRecord) error
}

type BoxRoutes interface {
	Up(ctx context.Context, info BoxInfo) error
	Down(box string)
	Host(box string) (Host, bool)
}

type BoxFolders interface {
	Make(box string) error
	Remove(box string) error
}

type CloneSpec struct {
	Box        string
	Repo       string
	MainBranch string
	Main       string
	Branch     string
	Head       string
	Objects    []push.Object
}

type SessionClones interface {
	SessionClone(ctx context.Context, q CloneSpec) error
}

type WorkspaceClearer interface {
	ClearWorkspace(ctx context.Context) error
}

type WorktreeLinks interface {
	Linked(clone, dir string) (bool, error)
}

func (d *Dispatcher) worktreeLinked(r *run) (bool, error) {
	_, reader := d.reg.worktreeReaders()
	l, ok := reader.(WorktreeLinks)
	if !ok {
		return true, nil
	}
	d.mu.Lock()
	clone, cwd := r.clone, r.cwd
	d.mu.Unlock()
	return l.Linked(clone, cwd)
}

func (d *Dispatcher) readdWorktree(ctx context.Context, r *run, id CommitIdentity) error {
	host := d.host(r)
	if err := host.RemoveWorktree(ctx, r.repo, r.name, false); err != nil {
		if h, ok := errors.AsType[*HostError](err); ok && h.Code == HostDirty {
			return explained(conflict("dirty"), "The worktree "+r.name+" has changes that aren't committed, so the engine left it as it is.")
		}
		d.cfg.Logger.Warn("worktree", "event", "not-cleared", "worktree", r.name)
	}
	cwd, err := host.AddExistingWorktree(ctx, r.repo, r.name, r.branch, id)
	if err != nil {
		return explained(conflict("worktree"), "agent-host couldn't add the session's worktree again: "+hostMessage(err))
	}
	if err := d.checkWorktree(r, cwd); err != nil {
		return err
	}
	d.mu.Lock()
	r.cwd = cwd
	d.mu.Unlock()
	d.saveRuns()
	d.cfg.Logger.Info("worktree", "event", "added-again", "worktree", r.name)
	return nil
}

func (d *Dispatcher) wakeWorktree(ctx context.Context, r *run, id CommitIdentity) error {
	linked, err := d.worktreeLinked(r)
	if err != nil {
		return explained(conflict("worktree"), "The engine couldn't read the session's worktree without following a link, so the session didn't wake: "+clip(err.Error(), 300))
	}
	if linked {
		return nil
	}
	if err := d.readdWorktree(ctx, r, id); err != nil {
		return explained(conflict("worktree"), "The session's worktree is missing or broken, and agent-host couldn't add it again on its branch, so the session didn't wake: "+clip(errText(err), 600)+" If the engine holds a copy of the branch, press Restore branch.")
	}
	return nil
}

type CloneReader interface {
	ObjectsToSend(repo, commit string, remoteHas []string) ([]push.Object, error)
	Commit(path, sha string) (push.Commit, error)
}

type boxBook struct {
	mu sync.Mutex
}

func BoxFolder(workspace, box string) string {
	if workspace == "" || !ValidBox(box) {
		return ""
	}
	return path.Join(workspace, BoxesFolder, box)
}

func BoxClone(workspace, box, repo string) string {
	dir := BoxFolder(workspace, box)
	if dir == "" || !recordName.MatchString(repo) {
		return ""
	}
	return path.Join(dir, "repos", repo)
}

func BoxWorktree(workspace, box, name string) string {
	dir := BoxFolder(workspace, box)
	if dir == "" || !workspaceTree.MatchString(name) {
		return ""
	}
	return path.Join(dir, "worktrees", name)
}

func oldLayout() *Error {
	return explained(conflict("old container"), OldLayoutReason)
}

type downHost struct{ err error }

func (h downHost) AddWorktree(context.Context, string, string, string, string, CommitIdentity) (string, error) {
	return "", h.err
}
func (h downHost) AddExistingWorktree(context.Context, string, string, string, CommitIdentity) (string, error) {
	return "", h.err
}
func (h downHost) SetIdentity(context.Context, string, string, CommitIdentity) error { return h.err }
func (h downHost) RemoveWorktree(context.Context, string, string, bool) error        { return h.err }
func (h downHost) Run(context.Context, Command) (RunResult, error)                   { return RunResult{}, h.err }
func (h downHost) Start(context.Context, Command) (Process, error)                   { return nil, h.err }
func (h downHost) Stop(context.Context, string) error                                { return h.err }

func (d *Dispatcher) boxed() bool {
	return d.cfg.Boxes != nil && d.cfg.Routes != nil && d.cfg.Workspace != ""
}

func (d *Dispatcher) host(r *run) Host {
	d.mu.Lock()
	box := r.box
	d.mu.Unlock()
	return d.hostFor(box)
}

func (d *Dispatcher) hostFor(box string) Host {
	switch {
	case box == "":
		return downHost{err: &HostError{Code: "old-container", Message: OldLayoutReason}}
	case !d.boxed():
		return downHost{err: &HostError{Code: "no-helper", Message: "This engine has no box helper to run sessions' containers."}}
	}
	if h, ok := d.cfg.Routes.Host(box); ok && h != nil {
		return h
	}
	return downHost{err: &HostError{Code: "box-stopped", Message: "The session's container isn't running. Wake the session first."}}
}

func (d *Dispatcher) HostOf(sid string) (Host, bool) {
	d.mu.Lock()
	r := d.runs[sid]
	box := ""
	if r != nil {
		box = r.box
	}
	d.mu.Unlock()
	if r == nil || box == "" || !d.boxed() {
		return nil, false
	}
	return d.cfg.Routes.Host(box)
}

func (d *Dispatcher) SessionBox(sid string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.runs[sid]
	if r == nil || r.box == "" {
		return "", false
	}
	return r.box, true
}

func (d *Dispatcher) liveBox(box string) bool {
	if !d.boxed() || box == "" {
		return false
	}
	_, ok := d.cfg.Routes.Host(box)
	return ok
}

func boxError(err error) error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	msg := clip(err.Error(), 600)
	switch {
	case errors.Is(err, ErrBoxLimit):
		n := ""
		if m := maxBoxesSet.FindStringSubmatch(err.Error()); m != nil {
			n = m[1]
		}
		if n == "" {
			return explained(limited("boxes"), "As many sessions are running as this stack allows. Stop one to start or wake this session.")
		}
		return explained(limited("boxes"), n+" sessions are running, the most this stack allows. Stop one to start or wake this session.")
	case errors.Is(err, ErrBoxUnknown):
		return explained(conflict("box unknown"), "The box helper doesn't know the session's container any more: "+msg)
	case errors.Is(err, ErrBoxUnavailable):
		return explained(conflict("box helper"), "The box helper, which makes each session's container, didn't answer, so the session's container didn't start: "+msg)
	}
	return explained(conflict("box"), "The session's container didn't start. Docker said: "+msg)
}

func (d *Dispatcher) loadBoxes() ([]BoxRecord, error) {
	if d.cfg.BoxStore == nil {
		return nil, nil
	}
	return d.cfg.BoxStore.Load()
}

func (d *Dispatcher) changeBoxes(change func([]BoxRecord) ([]BoxRecord, error)) error {
	if d.cfg.BoxStore == nil {
		return nil
	}
	d.boxes.mu.Lock()
	defer d.boxes.mu.Unlock()
	list, err := d.cfg.BoxStore.Load()
	if err != nil {
		return err
	}
	next, err := change(list)
	if err != nil {
		return err
	}
	return d.cfg.BoxStore.Save(next)
}

func (d *Dispatcher) recordBox(box, project, agent string) error {
	return d.changeBoxes(func(list []BoxRecord) ([]BoxRecord, error) {
		if len(list) >= MaxBoxRecords {
			return nil, errors.New("boxes.json holds as many session containers as the engine records")
		}
		return append(list, BoxRecord{Box: box, Project: project, Agent: agent, Created: d.reg.now(), Pending: true}), nil
	})
}

func (d *Dispatcher) settleBox(box string) {
	err := d.changeBoxes(func(list []BoxRecord) ([]BoxRecord, error) {
		for i := range list {
			if list[i].Box == box {
				list[i].Pending = false
			}
		}
		return list, nil
	})
	if err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-saved")
	}
}

func (d *Dispatcher) forgetBox(box string) {
	err := d.changeBoxes(func(list []BoxRecord) ([]BoxRecord, error) {
		return slices.DeleteFunc(list, func(b BoxRecord) bool { return b.Box == box }), nil
	})
	if err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-saved")
	}
}

func (d *Dispatcher) openBox(ctx context.Context, project, agent string) (string, error) {
	if !d.boxed() || d.cfg.Folders == nil {
		return "", explained(conflict("no agent-host"), "This engine has no box helper to start sessions' containers in.")
	}
	box, err := NewBox()
	if err != nil {
		return "", explained(conflict("box"), "The engine couldn't make a name for the session's container.")
	}
	if err := d.recordBox(box, project, agent); err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-recorded")
		return "", explained(conflict("state"), "The engine couldn't record the session's container, so no session started.")
	}
	if err := d.cfg.Folders.Make(box); err != nil {
		d.cfg.Logger.Warn("boxes", "event", "folders-not-made", "box", box)
		d.dropBox(box, false)
		return "", explained(conflict("workspace"), "The engine couldn't make the session's folders in the workspace, so no session started.")
	}
	bctx, cancel := context.WithTimeout(ctx, boxCallFor)
	defer cancel()
	if err := d.cfg.Boxes.Create(bctx, box); err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-created", "box", box)
		d.dropBox(box, false)
		return "", boxError(err)
	}
	if err := d.upBox(bctx, box); err != nil {
		d.dropBox(box, false)
		return "", err
	}
	d.cfg.Logger.Info("boxes", "event", "started", "box", box)
	return box, nil
}

func (d *Dispatcher) upBox(ctx context.Context, box string) error {
	info, err := d.cfg.Boxes.Start(ctx, box)
	if err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-started", "box", box)
		return boxError(err)
	}
	if info.Box != box || !info.Agent.IsValid() || !info.Engine.IsValid() || !info.Subnet.IsValid() {
		_ = d.cfg.Boxes.Stop(context.WithoutCancel(ctx), box)
		return explained(conflict("box"), "The box helper answered with a container the engine can't use.")
	}
	if err := d.cfg.Routes.Up(ctx, info); err != nil {
		d.cfg.Routes.Down(box)
		_ = d.cfg.Boxes.Stop(context.WithoutCancel(ctx), box)
		d.cfg.Logger.Warn("boxes", "event", "not-routed", "box", box)
		return explained(conflict("box"), "The engine couldn't reach the session's container: "+clip(err.Error(), 300))
	}
	return nil
}

func (d *Dispatcher) wakeBox(ctx context.Context, box string) error {
	if !d.boxed() {
		return explained(conflict("no agent-host"), "This engine has no box helper to start sessions' containers in.")
	}
	if d.liveBox(box) {
		return nil
	}
	if d.cfg.Folders != nil {
		if err := d.cfg.Folders.Make(box); err != nil {
			return explained(conflict("workspace"), "The engine couldn't check the session's folders in the workspace.")
		}
	}
	bctx, cancel := context.WithTimeout(ctx, boxCallFor)
	defer cancel()
	return d.upBox(bctx, box)
}

func (d *Dispatcher) stopBox(ctx context.Context, box string) {
	if !d.boxed() || box == "" {
		return
	}
	d.cfg.Routes.Down(box)
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boxCallFor)
	defer cancel()
	if err := d.cfg.Boxes.Stop(bctx, box); err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-stopped", "box", box)
		return
	}
	d.cfg.Logger.Info("boxes", "event", "stopped", "box", box)
}

func (d *Dispatcher) clearBox(ctx context.Context, box string) {
	h, ok := d.hostFor(box).(WorkspaceClearer)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boxCallFor)
	defer cancel()
	if err := h.ClearWorkspace(cctx); err != nil {
		d.cfg.Logger.Warn("boxes", "event", "workspace-not-cleared", "box", box)
	}
}

func (d *Dispatcher) dropBox(box string, clear bool) bool {
	if !ValidBox(box) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), boxCallFor)
	defer cancel()
	if clear {
		d.clearBox(ctx, box)
	}
	if d.boxed() {
		d.cfg.Routes.Down(box)
		if err := d.cfg.Boxes.Remove(ctx, box); err != nil && !errors.Is(err, ErrBoxUnknown) {
			d.cfg.Logger.Warn("boxes", "event", "not-removed", "box", box)
			return false
		}
	}
	if d.cfg.Folders != nil {
		if err := d.cfg.Folders.Remove(box); err != nil {
			d.cfg.Logger.Warn("boxes", "event", "folders-not-removed", "box", box)
		}
	}
	if d.cfg.KeyRoutes != nil {
		d.forgetBoxKeys(box)
	}
	d.forgetBox(box)
	d.cfg.Logger.Info("boxes", "event", "removed", "box", box)
	return true
}

func (d *Dispatcher) forgetBoxKeys(box string) {
	routes, err := d.cfg.KeyRoutes.Load()
	if err != nil {
		return
	}
	target := "box:" + box
	if _, ok := routes[target]; !ok {
		return
	}
	delete(routes, target)
	if err := d.cfg.KeyRoutes.Save(routes); err != nil {
		d.cfg.Logger.Warn("boxes", "event", "key-routes-not-saved")
	}
}

func (d *Dispatcher) idleBox(r *run) {
	if r.box == "" || !d.liveBox(r.box) {
		return
	}
	d.mu.Lock()
	if r.running || r.busy || r.stopping {
		d.mu.Unlock()
		return
	}
	r.busy = true
	d.mu.Unlock()
	defer d.release(r)
	d.stopBox(context.Background(), r.box)
}

func (d *Dispatcher) boxInUse(box string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.box == box && (r.running || r.busy) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) StopLeftovers(ctx context.Context) {
	if !d.boxed() {
		return
	}
	list, err := d.cfg.Boxes.List(ctx)
	if err != nil {
		d.cfg.Logger.Warn("boxes", "event", "leftovers-not-listed")
	} else {
		stopped := 0
		for _, b := range list {
			if !ValidBox(b.Box) || d.boxInUse(b.Box) {
				continue
			}
			if b.State == "stopped" {
				continue
			}
			d.stopBox(ctx, b.Box)
			stopped++
		}
		if stopped > 0 {
			d.cfg.Logger.Info("boxes", "event", "leftovers-stopped", "count", stopped)
		}
	}
	d.dropPending(true)
}

func (d *Dispatcher) dropPending(schedule bool) {
	records, err := d.loadBoxes()
	if err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-read")
		return
	}
	now := d.reg.now()
	for _, b := range records {
		if !b.Pending || d.boxInUse(b.Box) {
			continue
		}
		age := now.Sub(b.Created)
		if age >= PendingBoxFor || age < 0 {
			d.cfg.Logger.Info("boxes", "event", "pending-dropped", "box", b.Box)
			d.dropBox(b.Box, false)
			continue
		}
		if schedule {
			box := b.Box
			time.AfterFunc(PendingBoxFor-age, func() { d.dropStillPending(box) })
		}
	}
}

func (d *Dispatcher) dropStillPending(box string) {
	records, err := d.loadBoxes()
	if err != nil {
		return
	}
	i := slices.IndexFunc(records, func(b BoxRecord) bool { return b.Box == box })
	if i < 0 || !records[i].Pending || d.boxInUse(box) || d.reg.now().Sub(records[i].Created) < PendingBoxFor {
		return
	}
	d.cfg.Logger.Info("boxes", "event", "pending-dropped", "box", box)
	d.dropBox(box, false)
}

func (d *Dispatcher) makeClone(ctx context.Context, r *run, main string, from *run, continues bool) error {
	if d.cfg.Clones == nil {
		return explained(conflict("no clone"), "This engine can't make the session's own clone.")
	}
	mainBranch := ""
	if d.cfg.Repositories != nil {
		if list, err := d.cfg.Repositories.List(); err == nil {
			if i := slices.IndexFunc(list, func(p Repository) bool { return p.Name == r.repo }); i >= 0 {
				mainBranch = list[i].Branch
			}
		}
	}
	q := CloneSpec{Box: r.box, Repo: r.repo, MainBranch: mainBranch, Main: main, Head: r.base}
	if continues {
		q.Branch = r.branch
	}
	if from != nil {
		objects, err := d.carried(from, r.base)
		if err != nil {
			return err
		}
		q.Objects = objects
	}
	cctx, cancel := context.WithTimeout(ctx, CloneFor)
	defer cancel()
	if err := d.cfg.Clones.SessionClone(cctx, q); err != nil {
		d.cfg.Logger.Warn("dispatch", "event", "clone-failed")
		return explained(conflict("clone"), "The engine couldn't make the session's own clone of "+r.repo+": "+clip(err.Error(), 600))
	}
	return nil
}

func (d *Dispatcher) carried(from *run, head string) ([]push.Object, error) {
	if d.cfg.CloneReader == nil {
		return nil, explained(conflict("no reader"), "This engine can't read the other session's commits.")
	}
	d.mu.Lock()
	clone, base := from.clone, from.base
	d.mu.Unlock()
	if clone == "" {
		return nil, oldLayout()
	}
	if _, err := d.cfg.CloneReader.Commit(clone, head); err != nil {
		return nil, explained(conflict("from"), "The engine couldn't read commit "+shortSHA(head)+" in the other session's clone.")
	}
	have := []string{}
	if sha1Hex.MatchString(base) && base != head {
		have = append(have, base)
	}
	objects, err := d.cfg.CloneReader.ObjectsToSend(clone, head, have)
	if errors.Is(err, push.ErrTooLarge) {
		return nil, explained(conflict("from"), "The other session's commits are larger than the engine carries into a new session's clone.")
	}
	if err != nil {
		return nil, explained(conflict("from"), "The engine couldn't read the other session's commits: "+clip(err.Error(), 300))
	}
	return objects, nil
}

func (d *Dispatcher) checkWorktree(r *run, cwd string) error {
	want := BoxWorktree(d.cfg.Workspace, r.box, r.name)
	if want == "" || cwd != want {
		d.cfg.Logger.Warn("dispatch", "event", "worktree-path-refused")
		return explained(conflict("worktree"), "agent-host answered a worktree path that isn't the session's own folder, so the engine doesn't use it.")
	}
	return nil
}

func (d *Dispatcher) hostDown(r *run) error {
	if h, ok := d.host(r).(downHost); ok {
		return h.err
	}
	return &HostError{Code: "sign-in", Message: "agent-host there doesn't report sign-ins"}
}

type boxContext struct{}

func withBox(ctx context.Context, box string) context.Context {
	return context.WithValue(ctx, boxContext{}, box)
}

func BoxOf(ctx context.Context) (string, bool) {
	box, ok := ctx.Value(boxContext{}).(string)
	return box, ok && ValidBox(box)
}

func (d *Dispatcher) startSource(repo Repository, q Dispatch) (*run, error) {
	sid := ""
	switch {
	case q.Continues != nil:
		sid = q.Continues.SID
	case q.From.SID != "":
		sid = q.From.SID
	default:
		return nil, nil
	}
	d.mu.Lock()
	r := d.runs[sid]
	d.mu.Unlock()
	if r == nil || r.repoPath != repo.Path {
		return nil, explained(notFound("from"), "That session isn't one of this project's sessions on this engine.")
	}
	if r.history {
		return nil, explained(conflict("history only"), historyMessage)
	}
	if r.box == "" || r.clone == "" {
		return nil, oldLayout()
	}
	return r, nil
}
