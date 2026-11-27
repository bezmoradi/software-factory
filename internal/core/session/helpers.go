package session

import (
	"context"
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	HelpersFolder      = "helpers"
	HelperFolderPrefix = "h-"
	HelperBranchPrefix = "helper/"
	MaxHelperCopies    = 8
	AuditHelperCopy    = "helper-copy"
	helperSweepEvery   = time.Minute
	helperSweepFor     = 5 * time.Minute
	helperAddFor       = 3 * time.Minute
)

var (
	helperName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,55}$`)
	helperFolder = regexp.MustCompile(`^h-[a-z0-9][a-z0-9-]{0,55}$`)
)

type HelperSweep struct {
	Removed []string
	Kept    int
	Debris  int
}

type HelperHost interface {
	AddHelper(ctx context.Context, repo, name, branch, base string, id CommitIdentity) (string, error)
	RemoveHelper(ctx context.Context, repo, name, branch string) error
	SweepHelpers(ctx context.Context, repo, branch string) (HelperSweep, error)
}

func HelperFolderName(name string) bool {
	return helperFolder.MatchString(name)
}

func HelperTop(area, file string) string {
	if area == "" || !path.IsAbs(file) {
		return ""
	}
	rest, ok := strings.CutPrefix(path.Clean(file), area+"/"+HelpersFolder+"/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	if !helperFolder.MatchString(name) {
		return ""
	}
	return area + "/" + HelpersFolder + "/" + name
}

func SessionArea(cwd, box string) string {
	trees := path.Dir(cwd)
	area := path.Dir(trees)
	if !ValidBox(box) || !path.IsAbs(cwd) || path.Clean(cwd) != cwd || path.Base(trees) != "worktrees" || !workspaceTree.MatchString(path.Base(cwd)) ||
		path.Base(area) != box || path.Base(path.Dir(area)) != BoxesFolder {
		return ""
	}
	return area
}

type CheckPlace struct {
	Top   string
	Paths []string
}

func CheckPlaces(area, top string, paths []string) []CheckPlace {
	out := []CheckPlace{{Top: top, Paths: paths}}
	for _, p := range paths {
		t := HelperTop(area, p)
		if t == "" || t == top {
			continue
		}
		i := 1
		for i < len(out) && out[i].Top != t {
			i++
		}
		if i == len(out) {
			out = append(out, CheckPlace{Top: t})
		}
		out[i].Paths = append(out[i].Paths, p)
	}
	return out
}

type helperTarget struct {
	box    string
	repo   string
	branch string
	clone  string
}

func (w *SessionWork) helperTarget(sid, box string) (helperTarget, error) {
	d := w.d
	if d == nil || !d.boxed() {
		return helperTarget{}, conflict("no-helper", "This engine has no box helper, so it makes no helper copies.")
	}
	d.mu.Lock()
	r := d.runs[sid]
	var t helperTarget
	var cwd string
	running, stopping, readOnly := false, false, false
	if r != nil {
		t = helperTarget{box: r.box, repo: r.repo, branch: r.branch, clone: r.clone}
		cwd, running, stopping, readOnly = r.cwd, r.running, r.stopping, r.doubled || r.history
	}
	d.mu.Unlock()
	switch {
	case r == nil:
		return helperTarget{}, notFound("session", "Software Factory doesn't know this session, so it makes no helper copy for it.")
	case box != "" && t.box != box:
		return helperTarget{}, conflict("box", "This request didn't come from the session's own container, so Software Factory makes no helper copy.")
	case !ValidBox(t.box) || t.clone == "" || cwd == "":
		return helperTarget{}, conflict("worktree", "This session has no worktree of its own yet, so it gets no helper copies.")
	case readOnly:
		return helperTarget{}, conflict("read-only", "This session is read-only, so it gets no helper copies.")
	case !running || stopping:
		return helperTarget{}, conflict("stopped", "This session isn't running, so it gets no helper copies.")
	}
	return t, nil
}

func (w *SessionWork) helperHost(ctx context.Context, box string) (HelperHost, error) {
	if err := w.d.ConfirmBox(ctx, box); err != nil {
		return nil, err
	}
	host := w.d.hostFor(box)
	if h, ok := host.(HelperHost); ok {
		return h, nil
	}
	if down, ok := host.(downHost); ok {
		return nil, conflict("stopped", hostMessage(down.err))
	}
	return nil, conflict("helper", "agent-host in the session's container makes no helper copies.")
}

func (w *SessionWork) helperSlot(t helperTarget, sid string) (func(), error) {
	if w.cfg.HelperCount == nil {
		return nil, conflict("helper", "This engine can't count a session's helper copies, so it makes none.")
	}
	w.helperMu.Lock()
	defer w.helperMu.Unlock()
	n, err := w.cfg.HelperCount(t.box)
	if err != nil {
		return nil, conflict("helper", "The engine couldn't read the session's helpers folder, so it makes no helper copy.")
	}
	if n+w.helping[sid] >= MaxHelperCopies {
		return nil, conflict("helpers-full", "Software Factory gives a session at most "+strconv.Itoa(MaxHelperCopies)+" helper copies at once. Merge a finished helper's branch into your branch, so Software Factory can remove its copy, then try again.")
	}
	w.helping[sid]++
	return func() {
		w.helperMu.Lock()
		if w.helping[sid]--; w.helping[sid] <= 0 {
			delete(w.helping, sid)
		}
		w.helperMu.Unlock()
	}, nil
}

func (w *SessionWork) AddHelper(ctx context.Context, sid, box, name string) (string, error) {
	if !helperName.MatchString(name) {
		return "", invalid("name", "A helper's name is lower-case letters, digits and dashes, starts with a letter or digit, and is at most 56 characters, so Software Factory made no copy for it.")
	}
	t, err := w.helperTarget(sid, box)
	if err != nil {
		return "", err
	}
	id := CommitIdentity{}
	if w.d.cfg.Identity != nil {
		id = w.d.cfg.Identity.Current()
	}
	if !id.Set() {
		return "", noIdentity()
	}
	done, err := w.helperSlot(t, sid)
	if err != nil {
		return "", err
	}
	defer done()
	heads, _ := w.reg.worktreeReaders()
	if heads == nil {
		return "", conflict("helper", "This engine can't read the session's branch, so it makes no helper copy.")
	}
	base, err := heads.Head(t.clone, t.branch)
	if err != nil || !sha1Hex.MatchString(base) {
		return "", conflict("helper", "The engine couldn't read the head of the session's branch, so it made no helper copy.")
	}
	host, err := w.helperHost(ctx, t.box)
	if err != nil {
		return "", err
	}
	folder, branch := HelperFolderPrefix+name, HelperBranchPrefix+name
	hctx, cancel := context.WithTimeout(ctx, helperAddFor)
	defer cancel()
	got, err := host.AddHelper(hctx, t.repo, folder, branch, base, id)
	if err != nil {
		w.cfg.Logger.Warn("helper copy", "event", "add-failed")
		return "", conflict("helper", "agent-host couldn't make the helper's copy: "+hostMessage(err))
	}
	want := path.Join(BoxFolder(w.d.cfg.Workspace, t.box), HelpersFolder, folder)
	if got != want {
		w.cfg.Logger.Warn("helper copy", "event", "path-refused")
		return "", conflict("helper", "agent-host answered a helper copy path that isn't the session's own helpers folder, so the engine doesn't use it.")
	}
	w.audit(AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: t.repo, Branch: branch, Commit: base, By: "agent", Kind: AuditHelperCopy, Result: "added", Path: folder, Box: t.box})
	w.cfg.Logger.Info("helper copy", "event", "added", "base", shortSHA(base))
	return got, nil
}

func (w *SessionWork) RemoveHelper(ctx context.Context, sid, box, folder string) error {
	area := ""
	if w.d != nil {
		area = BoxFolder(w.d.cfg.Workspace, box)
	}
	if area == "" || HelperTop(area, folder) != folder {
		return invalid("path", "Software Factory removes only a helper's copy in this session's helpers folder, so it left that folder alone.")
	}
	t, err := w.helperTarget(sid, box)
	if err != nil {
		return err
	}
	host, err := w.helperHost(ctx, t.box)
	if err != nil {
		return err
	}
	name := path.Base(folder)
	if err := host.RemoveHelper(ctx, t.repo, name, t.branch); err != nil {
		if h, ok := errors.AsType[*HostError](err); ok && h.Code == HostDirty {
			return conflict("dirty", "This helper's copy has changes that aren't committed, so Software Factory keeps it. Commit them on its branch and merge that branch into yours, then remove it again.")
		}
		w.cfg.Logger.Warn("helper copy", "event", "remove-failed")
		return conflict("helper", "agent-host couldn't remove the helper's copy: "+hostMessage(err))
	}
	w.audit(AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: t.repo, By: "agent", Kind: AuditHelperCopy, Result: "removed", Path: name, Box: t.box})
	w.cfg.Logger.Info("helper copy", "event", "removed")
	return nil
}

func (w *SessionWork) SweepHelpers(sid string) {
	now := w.cfg.Now()
	w.helperMu.Lock()
	last, seen := w.swept[sid]
	busy := w.helping[sid] > 0
	if busy || seen && now.Sub(last) < helperSweepEvery {
		w.helperMu.Unlock()
		return
	}
	w.swept[sid] = now
	w.helperMu.Unlock()
	t, err := w.helperTarget(sid, "")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(w.cfg.Base, helperSweepFor)
	defer cancel()
	host, err := w.helperHost(ctx, t.box)
	if err != nil {
		return
	}
	out, err := host.SweepHelpers(ctx, t.repo, t.branch)
	if err != nil {
		w.cfg.Logger.Warn("helper copy", "event", "sweep-failed")
		return
	}
	for _, name := range out.Removed {
		w.audit(AuditEntry{Time: w.cfg.Now(), Session: sid, Repository: t.repo, By: ByStart, Kind: AuditHelperCopy, Result: "swept", Path: name, Box: t.box})
	}
	if len(out.Removed) > 0 || out.Debris > 0 {
		w.cfg.Logger.Info("helper copy", "event", "swept", "removed", len(out.Removed), "kept", out.Kept, "debris", out.Debris)
	}
}

func (w *SessionWork) SweepAfterWake(sid string) {
	w.helperMu.Lock()
	delete(w.swept, sid)
	w.helperMu.Unlock()
	w.reg.sweepIfIdle(sid)
}

func (r *Registry) sweepIfIdle(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if live := r.liveFor(sid, r.now()); len(live) == 1 {
		r.helpersIdle(live[0])
	}
}

func (r *Registry) helpersIdle(c *conn) {
	a := r.admitted[c.sid]
	if r.work == nil || a == nil || c.busy || a.compacting || a.agents.running(c.epoch) {
		return
	}
	go r.work.SweepHelpers(c.sid)
}
