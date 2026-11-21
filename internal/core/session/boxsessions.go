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
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	PendingBoxFor   = 30 * time.Minute
	MaxBoxRecords   = 1000
	BoxesFolder     = "s"
	boxCallFor      = 3 * time.Minute
	OldLayoutReason = "This session ran in the old shared container; start a new session."
)

const AsideFrame = "worktree-aside"

var (
	asidePattern = regexp.MustCompile(`^worktrees/\.broken-sf-[0-9a-f]{8}-[0-9a-f]{16}$`)
	maxBoxesSet  = regexp.MustCompile(`SF_MAX_BOXES=([0-9]{1,4})`)
	clonePath    = regexp.MustCompile(`^/.+?/` + BoxesFolder + `/[0-9a-f]{16}/repos/([A-Za-z0-9][A-Za-z0-9._-]{0,99})$`)
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
	Info(box string) (BoxInfo, bool)
	Routed() []BoxInfo
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
	Head       string
	Objects    []push.Object
}

type CloneSyncer interface {
	SyncClone(ctx context.Context, box, repo, mainBranch, main string) error
}

type SessionClones interface {
	SessionClone(ctx context.Context, q CloneSpec) error
}

type WorkspaceClearer interface {
	ClearWorkspace(ctx context.Context, box string) error
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

func (d *Dispatcher) readdWorktree(ctx context.Context, r *run, id CommitIdentity, during string) (string, error) {
	host := d.host(r)
	aside, err := host.RemoveWorktree(ctx, r.repo, r.name, false)
	if err != nil {
		if h, ok := errors.AsType[*HostError](err); ok && h.Code == HostDirty {
			return "", conflict("dirty", "The worktree "+r.name+" has changes that aren't committed, so the engine left it as it is.")
		}
		shared.For(ctx, d.cfg.Logger).Warn("worktree", "event", "not-cleared", "worktree", r.name)
	}
	aside = d.tellAside(r, aside, during)
	cwd, err := host.AddExistingWorktree(ctx, r.repo, r.name, r.branch, id)
	if err != nil {
		return aside, conflict("worktree", "agent-host couldn't add the session's worktree again: "+hostMessage(err))
	}
	if err := d.checkWorktree(r, cwd); err != nil {
		return aside, err
	}
	d.mu.Lock()
	r.cwd = cwd
	d.mu.Unlock()
	d.saveRuns()
	shared.For(ctx, d.cfg.Logger).Info("worktree", "event", "added-again", "worktree", r.name)
	return aside, nil
}

func asideFolder(name, got string) string {
	if got == "" {
		return ""
	}
	if asidePattern.MatchString(got) && got[len("worktrees/.broken-"):len(got)-17] == name {
		return got
	}
	return "a folder whose name agent-host gave wrongly"
}

func AsideNotice(name, folder, during string) string {
	return "agent-host found the worktree " + name + " broken " + during + " and moved it, with its files, to " + folder + " in the session's folder, so files that weren't committed are there."
}

func (d *Dispatcher) tellAside(r *run, got, during string) string {
	folder := asideFolder(r.name, got)
	if folder == "" {
		return ""
	}
	d.cfg.Logger.Info("worktree", "event", "set-aside", "worktree", r.name)
	d.mu.Lock()
	r.aside = folder
	d.mu.Unlock()
	d.saveRuns()
	d.reg.Notify(Fields{"type": AsideFrame, "sid": r.sid, "text": AsideNotice(r.name, folder, during)})
	return folder
}

func (d *Dispatcher) wakeWorktree(ctx context.Context, r *run, id CommitIdentity) error {
	linked, err := d.worktreeLinked(r)
	if err != nil {
		return conflict("worktree", "The engine couldn't read the session's worktree without following a link, so the session didn't wake: "+clip(err.Error(), 300))
	}
	if linked {
		return nil
	}
	if _, err := d.readdWorktree(ctx, r, id, "while the session woke"); err != nil {
		return conflict("worktree", "The session's worktree is missing or broken, and agent-host couldn't add it again on its branch, so the session didn't wake: "+clip(errText(err), 600)+" If the engine holds a copy of the branch, press Restore branch.")
	}
	return nil
}

type CloneReader interface {
	ObjectsToSend(repo, commit string, remoteHas []string) ([]push.Object, error)
	Commit(path, sha string) (push.Commit, error)
	IsAncestor(ctx context.Context, repo, ancestor, commit string) (bool, error)
}

type boxBook struct {
	mu        sync.Mutex
	stopMu    sync.Mutex
	unstopped map[string]bool
	seenMu    sync.Mutex
	seen      map[string]BoxInfo
	seenAt    time.Time
	sweepMu   sync.Mutex
	swept     bool
	synced    map[string]string
	unsynced  map[string]syncFailure
}

type syncFailure struct {
	commit, why string
}

func (d *Dispatcher) markUnstopped(box string, on bool) {
	d.boxes.stopMu.Lock()
	defer d.boxes.stopMu.Unlock()
	if d.boxes.unstopped == nil {
		d.boxes.unstopped = map[string]bool{}
	}
	if on {
		d.boxes.unstopped[box] = true
	} else {
		delete(d.boxes.unstopped, box)
	}
}

func (d *Dispatcher) isUnstopped(box string) bool {
	d.boxes.stopMu.Lock()
	defer d.boxes.stopMu.Unlock()
	return d.boxes.unstopped[box]
}

func (d *Dispatcher) retryStop(ctx context.Context, box string) bool {
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boxCallFor)
	defer cancel()
	err := d.cfg.Boxes.Stop(bctx, box)
	ok := err == nil || errors.Is(err, ErrBoxUnknown)
	if ok {
		d.markUnstopped(box, false)
	}
	shared.For(ctx, d.cfg.Logger).Info("boxes", "event", "stop-retried", "box", box, "ok", ok)
	return ok
}

func (d *Dispatcher) retryStops(ctx context.Context) {
	if !d.boxed() {
		return
	}
	d.boxes.stopMu.Lock()
	boxes := make([]string, 0, len(d.boxes.unstopped))
	for b := range d.boxes.unstopped {
		boxes = append(boxes, b)
	}
	d.boxes.stopMu.Unlock()
	slices.Sort(boxes)
	for _, b := range boxes {
		if d.liveBox(b) {
			continue
		}
		d.retryStop(ctx, b)
	}
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
	return conflict("old container", OldLayoutReason)
}

type downHost struct{ err error }

func (h downHost) AddWorktree(context.Context, string, string, string, string, CommitIdentity) (string, error) {
	return "", h.err
}
func (h downHost) AddExistingWorktree(context.Context, string, string, string, CommitIdentity) (string, error) {
	return "", h.err
}
func (h downHost) SetIdentity(context.Context, string, string, CommitIdentity) error { return h.err }
func (h downHost) RemoveWorktree(context.Context, string, string, bool) (string, error) {
	return "", h.err
}
func (h downHost) Run(context.Context, Command) (RunResult, error) { return RunResult{}, h.err }
func (h downHost) Start(context.Context, Command) (Process, error) { return nil, h.err }
func (h downHost) Stop(context.Context, string) error              { return h.err }

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
			return limited("boxes", "As many sessions are running as this stack allows. Stop one to start or wake this session.")
		}
		return limited("boxes", n+" sessions are running, the most this stack allows. Stop one to start or wake this session.")
	case errors.Is(err, ErrBoxUnknown):
		return conflict("box unknown", "The box helper doesn't know the session's container any more: "+msg)
	case errors.Is(err, ErrBoxUnavailable):
		return conflict("box helper", "The box helper, which makes each session's container, didn't answer, so the session's container didn't start: "+msg)
	}
	return conflict("box", "The session's container didn't start. Docker said: "+msg)
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
		return "", conflict("no agent-host", "This engine has no box helper to start sessions' containers in.")
	}
	box, err := NewBox()
	if err != nil {
		return "", conflict("box", "The engine couldn't make a name for the session's container.")
	}
	if err := d.recordBox(box, project, agent); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "not-recorded")
		return "", conflict("state", "The engine couldn't record the session's container, so no session started.")
	}
	if err := d.cfg.Folders.Make(box); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "folders-not-made", "box", box)
		d.dropBox(box, false)
		return "", conflict("workspace", "The engine couldn't make the session's folders in the workspace, so no session started.")
	}
	bctx, cancel := context.WithTimeout(ctx, boxCallFor)
	defer cancel()
	if err := d.cfg.Boxes.Create(bctx, box); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "not-created", "box", box)
		d.dropBox(box, false)
		return "", boxError(err)
	}
	if err := d.upBox(bctx, box); err != nil {
		d.dropBox(box, false)
		return "", err
	}
	shared.For(ctx, d.cfg.Logger).Info("boxes", "event", "started", "box", box)
	return box, nil
}

func (d *Dispatcher) upBox(ctx context.Context, box string) error {
	info, err := d.cfg.Boxes.Start(ctx, box)
	if err != nil {
		d.markUnstopped(box, true)
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "not-started", "box", box)
		return boxError(err)
	}
	if info.Box != box || !info.Agent.IsValid() || !info.Engine.IsValid() || !info.Subnet.IsValid() {
		_ = d.cfg.Boxes.Stop(context.WithoutCancel(ctx), box)
		return conflict("box", "The box helper answered with a container the engine can't use.")
	}
	d.forgetSeen()
	if err := d.cfg.Routes.Up(ctx, info); err != nil {
		d.cfg.Routes.Down(box)
		_ = d.cfg.Boxes.Stop(context.WithoutCancel(ctx), box)
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "not-routed", "box", box)
		return conflict("box", "The engine couldn't reach the session's container: "+clip(err.Error(), 300))
	}
	return nil
}

func (d *Dispatcher) wakeBox(ctx context.Context, box string) error {
	if !d.boxed() {
		return conflict("no agent-host", "This engine has no box helper to start sessions' containers in.")
	}
	if d.liveBox(box) {
		err := d.ConfirmBox(ctx, box)
		if err == nil || d.liveBox(box) {
			return err
		}
	}
	if d.isUnstopped(box) && !d.retryStop(ctx, box) {
		return conflict("box not stopped", "The box helper couldn't stop the session's container after its last stop, so the engine doesn't start the agent in it again. It retries the stop at every sandbox check; try again once the box helper answers.")
	}
	if d.cfg.Folders != nil {
		if err := d.cfg.Folders.Make(box); err != nil {
			return conflict("workspace", "The engine couldn't check the session's folders in the workspace.")
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
	d.forgetSeen()
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boxCallFor)
	defer cancel()
	if err := d.cfg.Boxes.Stop(bctx, box); err != nil && !errors.Is(err, ErrBoxUnknown) {
		d.markUnstopped(box, true)
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "not-stopped", "box", box)
		return
	}
	d.markUnstopped(box, false)
	shared.For(ctx, d.cfg.Logger).Info("boxes", "event", "stopped", "box", box)
}

func (d *Dispatcher) clearBox(ctx context.Context, box string) {
	h, ok := d.hostFor(box).(WorkspaceClearer)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boxCallFor)
	defer cancel()
	if err := d.ConfirmBox(cctx, box); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "workspace-clear-skipped", "box", box)
		return
	}
	if err := h.ClearWorkspace(cctx, box); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "workspace-not-cleared", "box", box)
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
	folders := true
	if d.cfg.Folders != nil {
		if err := d.cfg.Folders.Remove(box); err != nil {
			folders = false
			d.cfg.Logger.Warn("boxes", "event", "folders-not-removed", "box", box)
		}
	}
	if d.cfg.KeyRoutes != nil {
		d.forgetBoxKeys(box)
	}
	d.unwait(box)
	d.markUnstopped(box, false)
	if err := d.cfg.Expected.Set(BoxTarget(box), ExpectAny); err != nil && !errors.Is(err, errNoExpectations) {
		d.cfg.Logger.Warn("boxes", "event", "sign-in-expectation-not-removed", "box", box)
	}
	d.forgetSynced(box)
	d.forgetHome(box)
	if !folders {
		d.pendBox(box)
		return true
	}
	d.forgetBox(box)
	d.cfg.Logger.Info("boxes", "event", "removed", "box", box)
	return true
}

func (d *Dispatcher) pendBox(box string) {
	err := d.changeBoxes(func(list []BoxRecord) ([]BoxRecord, error) {
		for i := range list {
			if list[i].Box == box {
				list[i].Pending = true
			}
		}
		return list, nil
	})
	if err != nil {
		d.cfg.Logger.Warn("boxes", "event", "not-saved")
	}
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
	_, waiting := d.waiting[box]
	return waiting && d.starting
}

func (d *Dispatcher) StopLeftovers(ctx context.Context) {
	if !d.boxed() {
		return
	}
	d.sweepLeftovers(ctx)
	d.dropPending(true)
	d.pruneExpectations()
}

func (d *Dispatcher) sweepLeftovers(ctx context.Context) {
	if !d.boxed() {
		return
	}
	d.boxes.sweepMu.Lock()
	defer d.boxes.sweepMu.Unlock()
	if d.boxes.swept {
		return
	}
	list, err := d.cfg.Boxes.List(ctx)
	if err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("boxes", "event", "leftovers-not-listed")
		return
	}
	stopped := 0
	for _, b := range list {
		if !ValidBox(b.Box) || b.State == "stopped" {
			continue
		}
		if d.stopLeftover(ctx, b.Box) {
			stopped++
		}
	}
	d.boxes.swept = true
	if stopped > 0 {
		shared.For(ctx, d.cfg.Logger).Info("boxes", "event", "leftovers-stopped", "count", stopped)
	}
}

func (d *Dispatcher) stopLeftover(ctx context.Context, box string) bool {
	d.mu.Lock()
	var owner *run
	for _, r := range d.runs {
		if r.box == box {
			owner = r
			break
		}
	}
	if owner == nil {
		_, waiting := d.waiting[box]
		inUse := waiting || d.starting
		d.mu.Unlock()
		if inUse {
			return false
		}
		d.stopBox(ctx, box)
		return true
	}
	waits := !owner.waitAt.IsZero() && d.reg.now().Sub(owner.waitAt) < PendingBoxFor
	if owner.running || owner.busy || waits {
		d.mu.Unlock()
		return false
	}
	owner.busy = true
	d.mu.Unlock()
	defer d.release(owner)
	d.stopBox(ctx, box)
	return true
}

func (d *Dispatcher) named(box string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.box == box {
			return true
		}
	}
	return false
}

func (d *Dispatcher) pruneExpectations() {
	records, err := d.loadBoxes()
	if err != nil || d.cfg.BoxStore == nil {
		return
	}
	known := map[string]bool{}
	for _, b := range records {
		known[b.Box] = true
	}
	if err := d.cfg.Expected.Prune(func(box string) bool { return known[box] }); err != nil && !errors.Is(err, errNoExpectations) {
		d.cfg.Logger.Warn("boxes", "event", "sign-in-expectations-not-pruned")
	}
}

func (d *Dispatcher) dropPending(schedule bool) {
	d.boxes.sweepMu.Lock()
	defer d.boxes.sweepMu.Unlock()
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
		if d.named(b.Box) {
			d.settleBox(b.Box)
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
	if d.named(box) {
		d.settleBox(box)
		return
	}
	d.cfg.Logger.Info("boxes", "event", "pending-dropped", "box", box)
	d.dropBox(box, false)
}

func (d *Dispatcher) makeClone(ctx context.Context, r *run, main string, from *run) error {
	if d.cfg.Clones == nil {
		return conflict("no clone", "This engine can't make the session's own clone.")
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
	if from != nil {
		objects, err := d.carried(from, r.base, main)
		if err != nil {
			return err
		}
		q.Objects = objects
	}
	cctx, cancel := context.WithTimeout(ctx, CloneFor)
	defer cancel()
	if err := d.cfg.Clones.SessionClone(cctx, q); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("dispatch", "event", "clone-failed")
		return conflict("clone", "The engine couldn't make the session's own clone of "+r.repo+": "+clip(err.Error(), 600))
	}
	return nil
}

func (d *Dispatcher) carried(from *run, head, main string) ([]push.Object, error) {
	if d.cfg.CloneReader == nil {
		return nil, conflict("no reader", "This engine can't read the other session's commits.")
	}
	d.mu.Lock()
	clone, box := from.clone, from.box
	base := ""
	if from.origin == "" || from.origin == OriginMain {
		base = from.base
	}
	d.mu.Unlock()
	d.boxes.stopMu.Lock()
	synced := d.boxes.synced[box]
	d.boxes.stopMu.Unlock()
	if clone == "" {
		return nil, oldLayout()
	}
	if _, err := d.cfg.CloneReader.Commit(clone, head); err != nil {
		return nil, conflict("from", "The engine couldn't read that session's commit "+shortSHA(head)+".")
	}
	have := []string{}
	for _, h := range []string{main, synced, base} {
		if sha1Hex.MatchString(h) && h != head && !slices.Contains(have, h) {
			have = append(have, h)
		}
	}
	objects, err := d.cfg.CloneReader.ObjectsToSend(clone, head, have)
	if errors.Is(err, push.ErrTooLarge) {
		return nil, conflict("from", "That session's commits are too large to carry over.")
	}
	if err != nil {
		return nil, conflict("from", "The engine couldn't read that session's commits.")
	}
	return objects, nil
}

func (d *Dispatcher) checkWorktree(r *run, cwd string) error {
	want := BoxWorktree(d.cfg.Workspace, r.box, r.name)
	if want == "" || cwd != want {
		d.cfg.Logger.Warn("dispatch", "event", "worktree-path-refused")
		return conflict("worktree", "agent-host answered a worktree path that isn't the session's own folder, so the engine doesn't use it.")
	}
	return nil
}

func (d *Dispatcher) hostDown(r *run) error {
	if h, ok := d.host(r).(downHost); ok {
		return h.err
	}
	return &HostError{Code: "sign-in", Message: "agent-host there doesn't report sign-ins"}
}

func markNeeds(r *run, err error) (*Needs, bool) {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Needs == nil {
		return nil, false
	}
	e.Needs.Box, e.Needs.Agent, e.Needs.Code = r.box, r.spec.ID, r.spec.codeSignIn()
	return e.Needs, true
}

func (d *Dispatcher) holdForAnswer(r *run, q Dispatch, err error) bool {
	if !ValidBox(r.box) || !d.liveBox(r.box) {
		return false
	}
	if _, ok := markNeeds(r, err); !ok {
		return false
	}
	d.mu.Lock()
	first := d.waiting[r.box] == nil
	d.waiting[r.box] = r
	d.started = time.Time{}
	d.mu.Unlock()
	if first {
		box := r.box
		time.AfterFunc(PendingBoxFor, func() { d.dropStillPending(box) })
	}
	d.cfg.Logger.Info("boxes", "event", "waiting", "box", r.box)
	return true
}

func (d *Dispatcher) unwait(box string) {
	d.mu.Lock()
	delete(d.waiting, box)
	d.mu.Unlock()
}

func (d *Dispatcher) resume(ctx context.Context, q Dispatch, spec AgentSpec, repo Repository) (Started, error) {
	if !ValidBox(q.Box) {
		return Started{}, invalid("box", "That isn't a session's container on this engine. Reload the page and try again.")
	}
	d.mu.Lock()
	r := d.waiting[q.Box]
	d.mu.Unlock()
	if r == nil {
		return Started{}, notFound("box", "That start isn't waiting any more. Start the session again.")
	}
	if r.repoPath != repo.Path || r.spec.ID != spec.ID {
		return Started{}, conflict("box", "That session's container waits for another project or agent.")
	}
	kept := false
	defer func() {
		if !kept {
			d.dropBox(r.box, true)
		}
	}()
	if err := d.ConfirmBox(ctx, r.box); err != nil {
		return Started{}, err
	}
	check, err := d.startCheck(ctx, r.box)
	if err != nil {
		return Started{}, err
	}
	d.mu.Lock()
	r.check = check
	r.group, r.groupKind = q.group, q.groupKind
	d.mu.Unlock()
	shared.For(ctx, d.cfg.Logger).Info("boxes", "event", "resumed", "box", r.box)
	return d.launch(ctx, r, q, &kept)
}

func (d *Dispatcher) waitInWake(r *run, err error) bool {
	needs, ok := markNeeds(r, err)
	if !ok || !d.liveBox(r.box) {
		return false
	}
	d.mu.Lock()
	r.waitAt = d.reg.now()
	d.mu.Unlock()
	if needs.SignIn != nil && !needs.Cost {
		d.reg.WaitSignIn(r.sid, true)
	}
	time.AfterFunc(PendingBoxFor, func() { d.stopWaiting(r) })
	d.cfg.Logger.Info("boxes", "event", "waiting", "box", r.box)
	return true
}

func (d *Dispatcher) stopWaiting(r *run) {
	d.mu.Lock()
	stale := !r.running && !r.busy && !r.waitAt.IsZero() && d.reg.now().Sub(r.waitAt) >= PendingBoxFor
	if stale {
		r.homeAsk, r.homeClear = "", false
	}
	d.mu.Unlock()
	if !stale {
		return
	}
	d.dropOnce(r.box)
	d.reg.WaitSignIn(r.sid, false)
	d.idleBox(r)
	if !d.liveBox(r.box) {
		d.ungateBox(r)
	}
}

func (d *Dispatcher) startSource(repo Repository, q Dispatch) (*run, error) {
	sid := ""
	if q.From.SID == "" {
		return nil, nil
	}
	sid = q.From.SID
	d.mu.Lock()
	r := d.runs[sid]
	d.mu.Unlock()
	if r == nil || r.repoPath != repo.Path {
		return nil, notFound("from", "That session isn't one of this project's.")
	}
	if r.history {
		return nil, conflict("history only", historyMessage)
	}
	if r.box == "" || r.clone == "" {
		return nil, oldLayout()
	}
	return r, nil
}

func (d *Dispatcher) SyncClones(ctx context.Context, name, branch, commit string) {
	if _, ok := d.cfg.Clones.(CloneSyncer); !ok || !sha1Hex.MatchString(commit) {
		return
	}
	d.mu.Lock()
	var runs []*run
	for _, r := range d.runs {
		if r.repo == name && r.box != "" && r.clone != "" && !r.history {
			runs = append(runs, r)
		}
	}
	d.mu.Unlock()
	synced := 0
	for _, r := range runs {
		d.mu.Lock()
		free := !r.busy && d.runs[r.sid] == r
		if free {
			r.busy = true
		}
		d.mu.Unlock()
		if !free {
			continue
		}
		err := d.syncTo(r, branch, commit)
		d.release(r)
		if err == nil {
			synced++
		}
	}
	if synced > 0 {
		shared.For(ctx, d.cfg.Logger).Info("session clone", "event", "main-brought", "clones", synced)
	}
}

func (d *Dispatcher) syncTo(r *run, branch, commit string) error {
	syncer, ok := d.cfg.Clones.(CloneSyncer)
	if !ok {
		return errors.New("this engine can't bring main into a session's clone")
	}
	d.mu.Lock()
	box, repo := r.box, r.repo
	d.mu.Unlock()
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	if d.syncedAt(box, commit) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), FetchFor)
	defer cancel()
	if err := syncer.SyncClone(ctx, box, repo, branch, commit); err != nil {
		d.cfg.Logger.Warn("session clone", "event", "main-not-brought", "box", box, "why", clip(err.Error(), 200))
		d.boxes.stopMu.Lock()
		if d.boxes.unsynced == nil {
			d.boxes.unsynced = map[string]syncFailure{}
		}
		d.boxes.unsynced[box] = syncFailure{commit: commit, why: CleanLine(err.Error(), 300)}
		d.boxes.stopMu.Unlock()
		return err
	}
	d.boxes.stopMu.Lock()
	if d.boxes.synced == nil {
		d.boxes.synced = map[string]string{}
	}
	d.boxes.synced[box] = commit
	delete(d.boxes.unsynced, box)
	d.boxes.stopMu.Unlock()
	return nil
}

func (d *Dispatcher) unsyncedAt(box string) (string, string) {
	d.boxes.stopMu.Lock()
	defer d.boxes.stopMu.Unlock()
	f := d.boxes.unsynced[box]
	return f.commit, f.why
}

func CloneBehind(main, why string) string {
	return "the engine fetched the remote's main branch at " + shortSHA(main) + ", but couldn't bring it into the session's clone: " + why
}

func (d *Dispatcher) cloneSync(sid string) func(main string) string {
	return func(main string) string {
		t, ok := d.PushTarget(sid)
		if !ok || t.SyncFailed == "" || t.SyncFailed != main {
			return ""
		}
		return CloneBehind(main, t.SyncWhy)
	}
}

func (d *Dispatcher) syncedAt(box, commit string) bool {
	d.boxes.stopMu.Lock()
	last := d.boxes.synced[box]
	d.boxes.stopMu.Unlock()
	if last == "" {
		return false
	}
	return last == commit
}

func (d *Dispatcher) forgetSynced(box string) {
	d.boxes.stopMu.Lock()
	delete(d.boxes.synced, box)
	delete(d.boxes.unsynced, box)
	d.boxes.stopMu.Unlock()
}

func (d *Dispatcher) mainOf(repo string) (string, string, error) {
	if d.cfg.Repositories == nil {
		return "", "", errors.New("this engine has no repositories")
	}
	list, err := d.cfg.Repositories.List()
	if err != nil {
		return "", "", err
	}
	i := slices.IndexFunc(list, func(p Repository) bool { return p.Name == repo })
	if i < 0 || list[i].Branch == "" {
		return "", "", errors.New("the project's main branch isn't recorded")
	}
	mains, ok := d.cfg.Repositories.(interface {
		Main(name string) (string, error)
	})
	if !ok {
		return "", "", errors.New("this engine can't read the project's main")
	}
	commit, err := mains.Main(repo)
	if err != nil || !sha1Hex.MatchString(commit) {
		return "", "", errors.New("the project's main commit isn't recorded")
	}
	return list[i].Branch, commit, nil
}

func (d *Dispatcher) syncClone(r *run) {
	if _, ok := d.cfg.Clones.(CloneSyncer); !ok || r.box == "" || r.clone == "" {
		return
	}
	for range 2 {
		branch, commit, err := d.mainOf(r.repo)
		if err != nil || d.syncTo(r, branch, commit) != nil {
			return
		}
		if _, now, err := d.mainOf(r.repo); err != nil || now == commit {
			return
		}
	}
}

func (d *Dispatcher) askedBox(q Dispatch) string {
	if q.asked == "" {
		return ""
	}
	d.mu.Lock()
	box := ""
	var stale []string
	for b, r := range d.waiting {
		if r.asked != q.asked {
			continue
		}
		box = b
	}
	d.mu.Unlock()
	for _, b := range stale {
		d.cfg.Logger.Info("boxes", "event", "waiting-dropped", "box", b, "why", "branch-moved")
		d.dropBox(b, true)
	}
	return box
}
