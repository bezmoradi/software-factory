package session

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	KeepSessionCap     = 256 << 20
	KeepAllCap         = 2 << 30
	MaxKeptPacks       = 500
	AuditRestoreBranch = "restore-branch"
	keepFrame          = "keep"
	keepSettle         = 2 * time.Second
	maxKeepHolds       = 256
)

var ErrKeepFull = errors.New("over the copy's cap")

type KeepStop string

const (
	KeepSecret   KeepStop = "secret"
	KeepPassword KeepStop = "password"
	KeepLarge    KeepStop = "large"
	KeepFull     KeepStop = "session-cap"
	KeepAllFull  KeepStop = "all-cap"
	KeepUnread   KeepStop = "unreadable"
)

var keepWhy = map[KeepStop]string{
	KeepSecret:   "it, or a file or folder name it adds, contains what looks like a private key or an access token, which the engine never stores",
	KeepPassword: "a file it adds contains what looks like a password in a URL, which the engine never stores",
	KeepLarge:    "the commits after the copy hold more than 128 MiB or 250,000 objects, more than the engine copies at once",
	KeepFull:     "the copy reached 256 MiB or 500 packs, the most the engine keeps for one session, so it copies nothing more for this session",
	KeepAllFull:  "the copies of all sessions together reached 2 GiB; copying goes on at a later turn's end once there is room, for example after a session is deleted",
	KeepUnread:   "the engine couldn't read the branch's new commits when the last turn ended; it tries again at the next one",
}

type KeptPack struct {
	Commit string
	Pack   string
	Bytes  int64
	At     time.Time
}

type KeepRecord struct {
	Packs   []KeptPack
	Stopped string
	Why     KeepStop
}

func (k KeepRecord) Last() string {
	if len(k.Packs) == 0 {
		return ""
	}
	return k.Packs[len(k.Packs)-1].Commit
}

func (k KeepRecord) Bytes() int64 {
	var n int64
	for _, p := range k.Packs {
		n += p.Bytes
	}
	return n
}

type KeepStore interface {
	Load(sid string) (KeepRecord, error)
	Save(sid string, rec KeepRecord) error
	WritePack(sid string, objects []push.Object, limit int64) (string, int64, error)
	Used() (int64, error)
	Sessions() ([]string, error)
	Remove(sid string) error
	RestoreObjects(sid, box, repo, commit string) error
	WriteBranch(box, repo, branch, commit string) error
}

type KeepReader interface {
	ObjectsToSend(repo, commit string, remoteHas []string) ([]push.Object, error)
	IsAncestor(repo, ancestor, commit string) (bool, error)
	Commit(repo, sha string) (push.Commit, error)
}

type KeepPage interface {
	View(sid string) (KeepView, error)
	Restore(ctx context.Context, sid string) (KeepView, bool, error)
}

var _ KeepPage = (*Keep)(nil)

type KeepConfig struct {
	Store  KeepStore
	Reader KeepReader
	Heads  Heads
	Main   func(name string) (string, error)
	Audit  Audit
	Logger shared.Logger
	Now    func() time.Time
}

type Keep struct {
	cfg   KeepConfig
	reg   *Registry
	d     *Dispatcher
	run   sync.Mutex
	mu    sync.Mutex
	busy  map[string]bool
	again map[string]bool
	notes map[string]KeepStop
	holds map[string]bool
}

type KeepView struct {
	Branch     string
	Commit     string
	At         time.Time
	Bytes      int64
	Packs      int
	Stopped    string
	Kind       KeepStop
	Why        string
	Head       string
	Holds      bool
	Copying    bool
	Restorable bool
	Refusal    string
	SessionCap int64
	AllCap     int64
}

func NewKeep(reg *Registry, d *Dispatcher, cfg KeepConfig) *Keep {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Audit = masking(cfg.Audit)
	k := &Keep{cfg: cfg, reg: reg, d: d, busy: map[string]bool{}, again: map[string]bool{}, notes: map[string]KeepStop{}, holds: map[string]bool{}}
	reg.mu.Lock()
	reg.branchCopy = k
	reg.mu.Unlock()
	d.mu.Lock()
	d.branchCopy = k
	d.mu.Unlock()
	return k
}

func (r *Registry) copyAfterTurn(sid string) {
	if k := r.branchCopy; k != nil {
		go k.afterTurn(sid)
	}
}

func (d *Dispatcher) forgetCopy(sid string) {
	d.mu.Lock()
	k := d.branchCopy
	d.mu.Unlock()
	if k != nil {
		k.forget(sid)
	}
}

func (d *Dispatcher) hasRun(sid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs[sid] != nil
}

func (k *Keep) Prune() {
	sids, err := k.cfg.Store.Sessions()
	if err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "unlisted")
		return
	}
	for _, sid := range sids {
		if !k.d.hasRun(sid) {
			k.forget(sid)
		}
	}
}

func (k *Keep) forget(sid string) {
	k.run.Lock()
	defer k.run.Unlock()
	if err := k.cfg.Store.Remove(sid); err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "not-removed")
	}
	k.mu.Lock()
	delete(k.notes, sid)
	k.mu.Unlock()
}

func (k *Keep) afterTurn(sid string) {
	k.mu.Lock()
	if k.busy[sid] {
		k.again[sid] = true
		k.mu.Unlock()
		return
	}
	k.busy[sid] = true
	k.mu.Unlock()
	for {
		time.Sleep(keepSettle)
		k.copyOnce(sid)
		k.mu.Lock()
		again := k.again[sid]
		delete(k.again, sid)
		if !again {
			delete(k.busy, sid)
		}
		k.mu.Unlock()
		k.reg.Notify(Fields{"type": keepFrame, "sid": sid})
		if !again {
			return
		}
	}
}

func (k *Keep) note(sid string, why KeepStop) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if why == "" {
		delete(k.notes, sid)
		return
	}
	k.notes[sid] = why
}

func (k *Keep) copyOnce(sid string) {
	k.run.Lock()
	defer k.run.Unlock()
	t, ok := k.d.PushTarget(sid)
	if !ok || k.cfg.Heads == nil || k.cfg.Main == nil {
		return
	}
	head, err := k.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil || !sha1Hex.MatchString(head) {
		return
	}
	main, err := k.cfg.Main(t.Name)
	if err != nil || !sha1Hex.MatchString(main) {
		return
	}
	rec, err := k.cfg.Store.Load(sid)
	if err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "unreadable-record")
		k.note(sid, KeepUnread)
		return
	}
	if rec.Why == KeepFull {
		k.note(sid, "")
		return
	}
	resumed := false
	if rec.Stopped != "" {
		if k.holdsCommit(t.Repo, rec.Stopped, head) {
			k.note(sid, "")
			return
		}
		rec.Stopped, rec.Why, resumed = "", "", true
	}
	if head == rec.Last() || head == main {
		k.settle(sid, rec, resumed)
		return
	}
	haves := []string{main}
	for _, p := range slices.Backward(rec.Packs) {
		if !slices.Contains(haves, p.Commit) {
			haves = append(haves, p.Commit)
		}
	}
	allowed := k.allowedBlobs(sid)
	objects, err := k.cfg.Reader.ObjectsToSend(t.Repo, head, haves)
	if errors.Is(err, push.ErrTooLarge) {
		k.note(sid, "")
		k.stop(sid, &rec, head, KeepLarge)
		return
	}
	if err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "unreadable-commits")
		k.note(sid, KeepUnread)
		return
	}
	if len(objects) == 0 {
		k.settle(sid, rec, resumed)
		return
	}
	target := head
	if kind, flagged := keepFlagOf(objects, allowed); flagged {
		chain := firstParents(objects, head)
		bad, why, good, goodObjects, err := k.firstFlagged(t.Repo, chain, haves, kind, allowed)
		if err != nil {
			k.cfg.Logger.Warn("branch copy", "event", "unreadable-commits")
			k.note(sid, KeepUnread)
			return
		}
		rec.Stopped, rec.Why = bad, why
		target, objects = good, goodObjects
	}
	k.note(sid, "")
	if target == "" || len(objects) == 0 {
		k.save(sid, rec)
		return
	}
	if len(rec.Packs) >= MaxKeptPacks {
		k.stop(sid, &rec, "", KeepFull)
		return
	}
	used, err := k.cfg.Store.Used()
	if err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "usage-unread")
		k.note(sid, KeepUnread)
		return
	}
	own := int64(KeepSessionCap) - rec.Bytes()
	all := int64(KeepAllCap) - used
	if min(own, all) <= 0 {
		k.full(sid, &rec, own <= all)
		return
	}
	pack, size, err := k.cfg.Store.WritePack(sid, objects, min(own, all))
	if errors.Is(err, ErrKeepFull) {
		k.full(sid, &rec, own <= all)
		return
	}
	if err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "pack-not-written")
		k.note(sid, KeepUnread)
		return
	}
	rec.Packs = append(rec.Packs, KeptPack{Commit: target, Pack: pack, Bytes: size, At: k.cfg.Now()})
	k.save(sid, rec)
	k.cfg.Logger.Info("branch copy", "event", "copied", "objects", len(objects))
}

func (k *Keep) settle(sid string, rec KeepRecord, changed bool) {
	k.note(sid, "")
	if changed {
		k.save(sid, rec)
	}
}

func (k *Keep) full(sid string, rec *KeepRecord, own bool) {
	if own {
		k.stop(sid, rec, "", KeepFull)
		return
	}
	k.save(sid, *rec)
	k.note(sid, KeepAllFull)
}

func (k *Keep) stop(sid string, rec *KeepRecord, at string, why KeepStop) {
	rec.Stopped, rec.Why = at, why
	k.save(sid, *rec)
}

func (k *Keep) save(sid string, rec KeepRecord) {
	if err := k.cfg.Store.Save(sid, rec); err != nil {
		k.cfg.Logger.Warn("branch copy", "event", "record-not-saved")
	}
}

func (k *Keep) firstFlagged(repo string, chain, haves []string, kind FlagKind, allowed map[string]bool) (string, KeepStop, string, []push.Object, error) {
	lo, hi := 0, len(chain)-1
	why := stopOf(kind)
	var good string
	var goodObjects []push.Object
	for lo < hi {
		mid := (lo + hi) / 2
		got, err := k.cfg.Reader.ObjectsToSend(repo, chain[mid], haves)
		if err != nil && !errors.Is(err, push.ErrTooLarge) {
			return "", "", "", nil, err
		}
		if err != nil {
			hi, why = mid, KeepLarge
			continue
		}
		if midKind, flagged := keepFlagOf(got, allowed); flagged {
			hi, why = mid, stopOf(midKind)
			continue
		}
		lo = mid + 1
		good, goodObjects = chain[mid], got
	}
	if lo == 0 {
		return chain[0], why, "", nil, nil
	}
	if good != chain[lo-1] {
		got, err := k.cfg.Reader.ObjectsToSend(repo, chain[lo-1], haves)
		if err != nil {
			return "", "", "", nil, err
		}
		good, goodObjects = chain[lo-1], got
	}
	return chain[lo], why, good, goodObjects, nil
}

func stopOf(kind FlagKind) KeepStop {
	if kind == FlagPassword {
		return KeepPassword
	}
	return KeepSecret
}

func firstParents(objects []push.Object, head string) []string {
	parents := map[string]string{}
	for _, o := range objects {
		if o.Kind == "commit" {
			parents[o.SHA] = firstParent(o.Data)
		}
	}
	var chain []string
	seen := map[string]bool{}
	for sha := head; sha != ""; {
		p, ok := parents[sha]
		if !ok || seen[sha] {
			break
		}
		seen[sha] = true
		chain = append(chain, sha)
		sha = p
	}
	slices.Reverse(chain)
	if len(chain) == 0 {
		chain = []string{head}
	}
	return chain
}

func firstParent(data []byte) string {
	header, _, _ := bytes.Cut(data, []byte("\n\n"))
	for line := range bytes.SplitSeq(header, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("parent ")); ok && sha1Hex.Match(rest) {
			return string(rest)
		}
	}
	return ""
}

func keepFlagOf(objects []push.Object, allowed map[string]bool) (FlagKind, bool) {
	for _, o := range objects {
		switch o.Kind {
		case "blob":
			if allowed[o.SHA] {
				continue
			}
			if secretData(o.Data) {
				return FlagSecret, true
			}
			if flagURL.Match(o.Data) {
				return FlagPassword, true
			}
		case "tree":
			names, ok := treeNames(o.Data)
			if !ok {
				if secretData(o.Data) {
					return FlagSecret, true
				}
				continue
			}
			if slices.ContainsFunc(names, secretText) {
				return FlagSecret, true
			}
		default:
			if secretData(o.Data) {
				return FlagSecret, true
			}
		}
	}
	return "", false
}

func treeNames(data []byte) ([]string, bool) {
	var names []string
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp < 0 {
			return nil, false
		}
		rest := data[sp+1:]
		nul := bytes.IndexByte(rest, 0)
		if nul < 0 || len(rest) < nul+21 {
			return nil, false
		}
		names = append(names, string(rest[:nul]))
		data = rest[nul+21:]
	}
	return names, true
}

func (k *Keep) allowedBlobs(sid string) map[string]bool {
	out := map[string]bool{}
	w := k.d.cfg.Work
	if w == nil {
		return out
	}
	ws, ok := w.session(sid)
	if !ok {
		return out
	}
	for key, set := range ws.allowed {
		if commitFlagKey(key) {
			continue
		}
		for sha, on := range set {
			if on && sha1Hex.MatchString(sha) {
				out[sha] = true
			}
		}
	}
	return out
}

func commitFlagKey(key string) bool {
	sha, field, ok := strings.Cut(key, ":")
	if !ok || !sha1Hex.MatchString(sha) {
		return false
	}
	_, known := FieldWords[CommitField(field)]
	return known
}

func (k *Keep) holdsCommit(repo, last, head string) bool {
	if head == "" || last == "" {
		return false
	}
	if head == last {
		return true
	}
	key := repo + "\x00" + last + "\x00" + head
	k.mu.Lock()
	held, ok := k.holds[key]
	k.mu.Unlock()
	if ok {
		return held
	}
	in, err := k.cfg.Reader.IsAncestor(repo, last, head)
	held = err == nil && in
	k.mu.Lock()
	if len(k.holds) >= maxKeepHolds {
		clear(k.holds)
	}
	k.holds[key] = held
	k.mu.Unlock()
	return held
}

func (k *Keep) View(sid string) (KeepView, error) {
	t, ok := k.d.PushTarget(sid)
	if !ok {
		return KeepView{}, explained(notFound("session"), "This engine holds no branch for that session: it was handed over, deleted or not started here.")
	}
	rec, err := k.cfg.Store.Load(sid)
	if err != nil {
		return KeepView{}, explained(conflict("copy"), "The engine couldn't read its copy of this session's commits.")
	}
	v := KeepView{Branch: t.Branch, Commit: rec.Last(), Bytes: rec.Bytes(), Packs: len(rec.Packs), Stopped: rec.Stopped, Kind: rec.Why, SessionCap: KeepSessionCap, AllCap: KeepAllCap}
	if n := len(rec.Packs); n > 0 {
		v.At = rec.Packs[n-1].At
	}
	k.mu.Lock()
	if note := k.notes[sid]; note != "" && v.Kind == "" {
		v.Kind = note
	}
	v.Copying = k.busy[sid]
	k.mu.Unlock()
	v.Why = keepWhy[v.Kind]
	if k.cfg.Heads != nil {
		if head, err := k.cfg.Heads.Head(t.Repo, t.Branch); err == nil && sha1Hex.MatchString(head) {
			v.Head = head
		}
	}
	v.Holds = k.holdsCommit(t.Repo, v.Commit, v.Head)
	broken := false
	k.d.mu.Lock()
	r := k.d.runs[sid]
	k.d.mu.Unlock()
	if r != nil && v.Holds {
		linked, err := k.d.worktreeLinked(r)
		broken = err == nil && !linked
	}
	switch {
	case k.d.historyOnly(sid) != nil:
		v.Refusal = historyMessage
	case v.Commit == "":
		v.Refusal = "Nothing is copied yet: the engine copies the branch's commits that main lacks when a turn ends."
	case v.Holds && !broken:
		v.Refusal = "The branch holds the last copied commit, so there is nothing to restore."
	case !k.reg.TurnSince(sid).IsZero():
		v.Refusal = "The session is in its turn. Restore works once the turn ends or is stopped."
	default:
		v.Restorable = true
	}
	return v, nil
}

func (k *Keep) Restore(ctx context.Context, sid string) (KeepView, bool, error) {
	d := k.d
	if err := d.historyOnly(sid); err != nil {
		return KeepView{}, false, err
	}
	r, err := d.claim(sid)
	if err != nil {
		return KeepView{}, false, err
	}
	defer d.release(r)
	d.mu.Lock()
	handed, running := r.handedTo != "" || r.handing, r.running
	d.mu.Unlock()
	if handed {
		return KeepView{}, false, explained(conflict("handed over"), handedMessage)
	}
	if !d.reg.TurnSince(sid).IsZero() {
		return KeepView{}, false, explained(conflict("in turn"), "The session is in its turn. Wait for it to end, or stop the turn, then restore the branch.")
	}
	if !d.boxed() {
		return KeepView{}, false, explained(conflict("no agent-host"), "This engine has no box helper to recreate the worktree in the session's container.")
	}
	k.run.Lock()
	defer k.run.Unlock()
	rec, err := k.cfg.Store.Load(sid)
	if err != nil {
		return KeepView{}, false, explained(conflict("copy"), "The engine couldn't read its copy of this session's commits.")
	}
	last := rec.Last()
	if last == "" {
		return KeepView{}, false, explained(conflict("nothing copied"), "The engine has no copy of this session's commits yet.")
	}
	if k.cfg.Heads == nil || k.cfg.Reader == nil {
		return KeepView{}, false, explained(conflict("no reader"), "This engine can't read the session's branch.")
	}
	linked, lerr := d.worktreeLinked(r)
	if lerr != nil {
		return KeepView{}, false, explained(conflict("worktree"), "The engine couldn't read the session's worktree without following a link, so it touched nothing: "+keepSentence(clip(lerr.Error(), 300)))
	}
	holds := false
	if head, err := k.cfg.Heads.Head(r.clone, r.branch); err == nil && sha1Hex.MatchString(head) && k.holdsCommit(r.clone, last, head) {
		holds = true
	}
	if holds && linked {
		return KeepView{}, false, explained(conflict("holds"), "The branch "+r.branch+" holds the last copied commit, so there is nothing to restore.")
	}
	repair := holds && !linked
	id := d.cfg.Identity.Current()
	if !id.Set() {
		return KeepView{}, false, noIdentity()
	}
	if !repair {
		if err := k.cfg.Store.RestoreObjects(sid, r.box, r.repo, last); err != nil {
			k.cfg.Logger.Warn("restore branch", "event", "objects-not-written")
			return KeepView{}, false, explained(conflict("objects"), "The engine couldn't write its copy's objects into the clone: "+keepSentence(err.Error()))
		}
		if _, err := k.cfg.Reader.Commit(r.clone, last); err != nil {
			return KeepView{}, false, explained(conflict("objects"), "The engine wrote its copy into the clone, but couldn't read commit "+shortSHA(last)+" back from it.")
		}
	}
	stopped := false
	if running {
		if err := d.stopRun(ctx, sid, r); err != nil {
			return KeepView{}, false, err
		}
		d.waitStopped(r)
		d.mu.Lock()
		still := r.running
		d.mu.Unlock()
		if still {
			return KeepView{}, false, explained(conflict("running"), "The session didn't stop in time, so its worktree wasn't touched. Try again in a moment.")
		}
		stopped = true
	}
	if err := d.wakeBox(ctx, r.box); err != nil {
		return KeepView{}, stopped, err
	}
	defer d.stopBox(ctx, r.box)
	if repair {
		return k.readded(ctx, sid, r, last, id, stopped)
	}
	host := d.host(r)
	if err := host.RemoveWorktree(ctx, r.repo, r.name, false); err != nil {
		if h, ok := errors.AsType[*HostError](err); ok && h.Code == HostDirty {
			return KeepView{}, stopped, explained(conflict("dirty"), "The worktree "+r.name+" has changes that aren't committed, so the engine left it and the branch as they are. Wake the session and ask the agent to commit or discard them, then restore again.")
		}
		if linked {
			return KeepView{}, stopped, explained(conflict("worktree"), "agent-host couldn't remove the worktree, so the branch wasn't touched: "+keepSentence(hostMessage(err)))
		}
		k.cfg.Logger.Warn("restore branch", "event", "broken-worktree-not-cleared", "worktree", r.name)
	}
	werr := k.cfg.Store.WriteBranch(r.box, r.repo, r.branch, last)
	if werr != nil {
		k.restoreAudit(r, last, "failed", "the engine couldn't write the branch "+r.branch+" into the clone after agent-host removed the worktree: "+werr.Error())
		return KeepView{}, stopped, explained(conflict("branch"), "agent-host removed the worktree, but the engine couldn't write the branch "+r.branch+": "+keepSentence(werr.Error())+" Press Restore branch again.")
	}
	cwd, err := host.AddExistingWorktree(ctx, r.repo, r.name, r.branch, id)
	if err == nil {
		err = d.checkWorktree(r, cwd)
	}
	if err != nil {
		k.restoreAudit(r, last, "failed", "the branch "+r.branch+" is back at "+shortSHA(last)+", but agent-host couldn't add its worktree again")
		return KeepView{}, stopped, explained(conflict("worktree"), "The branch "+r.branch+" is back at "+shortSHA(last)+", but agent-host couldn't add its worktree again: "+keepSentence(hostMessage(err))+" Press Restore branch again.")
	}
	d.mu.Lock()
	r.cwd = cwd
	d.mu.Unlock()
	d.saveRuns()
	reason := "the person restored the branch " + r.branch + " from the engine's copy at " + shortSHA(last) + "; agent-host removed the session's worktree and added it again at that commit"
	if stopped {
		reason += ", after the engine stopped the idle session"
	}
	k.restoreAudit(r, last, "restored", reason)
	d.cfg.Logger.Info("restore branch", "event", "restored", "stopped", stopped)
	k.mu.Lock()
	clear(k.holds)
	k.mu.Unlock()
	go d.reg.CheckWorktree(sid)
	d.reg.Notify(Fields{"type": keepFrame, "sid": sid})
	v, err := k.View(sid)
	return v, stopped, err
}

func (k *Keep) readded(ctx context.Context, sid string, r *run, last string, id CommitIdentity, stopped bool) (KeepView, bool, error) {
	d := k.d
	if err := d.readdWorktree(ctx, r, id); err != nil {
		k.restoreAudit(r, last, "failed", "the branch "+r.branch+" already held "+shortSHA(last)+", but the session's worktree, missing or broken, couldn't be added again")
		var e *Error
		if errors.As(err, &e) && e.Reason == "dirty" {
			return KeepView{}, stopped, err
		}
		return KeepView{}, stopped, explained(conflict("worktree"), keepSentence(errText(err))+" Press Restore branch again.")
	}
	reason := "the branch " + r.branch + " already held the engine's last copied commit " + shortSHA(last) + "; the session's worktree was missing or broken, and agent-host added it again at that branch"
	if stopped {
		reason += ", after the engine stopped the idle session"
	}
	k.restoreAudit(r, last, "restored", reason)
	d.cfg.Logger.Info("restore branch", "event", "worktree-added-again", "stopped", stopped)
	k.mu.Lock()
	clear(k.holds)
	k.mu.Unlock()
	go d.reg.CheckWorktree(sid)
	d.reg.Notify(Fields{"type": keepFrame, "sid": sid})
	v, err := k.View(sid)
	return v, stopped, err
}

func errText(err error) string {
	if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}

func (k *Keep) restoreAudit(r *run, commit, result, reason string) {
	if k.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: k.cfg.Now(), Session: r.sid, Repository: r.repo, Branch: r.branch, Commit: commit, By: ByPage, Kind: AuditRestoreBranch, Result: result, Reason: reason, Missing: []string{}}
	if err := k.cfg.Audit.Append(e); err != nil {
		k.cfg.Logger.Warn("restore branch", "event", "audit-not-written")
	}
}

func keepSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}
