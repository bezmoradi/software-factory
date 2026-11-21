package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type VerifyKind string

const (
	VerifyCheck         VerifyKind = "check"
	VerifyBrowser       VerifyKind = "browser"
	VerifyOOM                      = "oom"
	VerifyInterrupted              = "interrupted"
	VerifyStopped                  = "stopped"
	MaxVerifyRuns                  = 2
	MaxEngineVerifyRuns            = 1
	verifyPress                    = "press Verify this commit in the session's verification panel"
	MaxVerifyRecords               = 500
	MaxVerifyImage                 = 300
	AuditVerify                    = "verify"
	VerifyByPage                   = "page"
	VerifyByEngine                 = "engine"
	verifyFrame                    = "verify"
	verifyPoll                     = 2 * time.Second
	verifySlack                    = 30 * time.Second
	verifyStartFor                 = 4 * time.Minute
	verifyCallFor                  = 30 * time.Second
	verifyRemoveFor                = 90 * time.Second
	verifySweepEach                = 5 * time.Minute
	verifySweepWait                = 10 * time.Second
	verifyStateTries               = 5
	verifyShown                    = 5
	maxVerifyReason                = 400
)

var verifyImage = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9.-]{0,252}[a-z0-9])?(?::[0-9]{1,5})?/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$`)

var VerifyKinds = []VerifyKind{VerifyCheck, VerifyBrowser}

func verifyCommand(kind VerifyKind, cmds ProjectCommands) string {
	if kind == VerifyBrowser {
		return cmds.Browser
	}
	return cmds.Check
}

var (
	urlUserinfo   = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s@]+@`)
	passwordParam = regexp.MustCompile(`(?i)(^|[?&;\s])(pass(word)?|pwd|secret|token|api_?key|accountkey|sharedaccesskey|sig|access_?token)=`)
	javaProperty  = regexp.MustCompile(`(?i)-D[A-Za-z0-9_.-]*(password|passwd|secret|token|key)=`)
	keyShapes     = regexp.MustCompile(`\b([sr]k_(live|test)_[A-Za-z0-9]{10,}|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}|xox[abposr]-[A-Za-z0-9-]{10,})|hooks\.slack\.com/services/|(?i)\bpassword\s+\S`)
)

func SecretValue(s string) bool {
	return secretText(s) || urlUserinfo.MatchString(s) || passwordParam.MatchString(s) || javaProperty.MatchString(s) || keyShapes.MatchString(s)
}

func CheckVerifyImage(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	if len(s) > MaxVerifyImage || !verifyImage.MatchString(s) {
		return "", false
	}
	return s, true
}

func VerifyDigest(image string) string {
	_, digest, ok := strings.Cut(image, "@sha256:")
	if !ok || len(digest) != 64 {
		return ""
	}
	return digest
}

type VerifierInfo struct {
	Run    string
	Image  string
	Digest string
	Limit  time.Duration
	Recipe string
}

type VerifierState struct {
	Run        string
	State      string
	ExitCode   int
	OOMKilled  bool
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
	Image      string
	Digest     string
	Limit      time.Duration
}

type VerifierHost interface {
	StartVerifier(ctx context.Context, run, digest string, browser bool) (VerifierInfo, error)
	VerifierState(ctx context.Context, run string) (VerifierState, error)
	VerifierLog(ctx context.Context, run string) (string, error)
	VerifierOut(ctx context.Context, run string) (io.ReadCloser, error)
	RemoveVerifier(ctx context.Context, run string) error
	ListVerifiers(ctx context.Context) ([]BoxInfo, error)
}

type VerifyTrees interface {
	Write(repo, commit, run, command string) error
	Remove(run string) error
	Prune(keep func(run string) bool) error
}

type VerifyStore interface {
	Load() ([]VerifyRun, error)
	Save([]VerifyRun) error
}

type VerifyRun struct {
	ID          string
	Project     ProjectID
	SID         string
	Branch      string
	Commit      string
	Kind        VerifyKind
	CommandHash string
	Image       string
	ImageID     string
	State       string
	Code        int
	Tail        string
	Cut         bool
	Withheld    bool
	Reason      string
	By          string
	Limit       time.Duration
	Started     time.Time
	Ended       time.Time
	Attached    []VerifyAttachment
	AttachNote  string
}

func (r VerifyRun) Ran() bool {
	return r.State != RunRunning
}

type VerifyView struct {
	Head     string
	Branch   string
	Command  string
	HasCheck bool
	Image    string
	Required bool
	Busy     bool
	Queued   bool
	Current  *VerifyRun
	Why      string
	Runs     []VerifyRun
	Browser  VerifyKindView
}

type VerifyKindView struct {
	Command  string
	Has      bool
	Required bool
	Queued   bool
	Current  *VerifyRun
	Why      string
}

type VerifyPage interface {
	View(ctx context.Context, sid string) (VerifyView, error)
	Start(ctx context.Context, sid string, kind VerifyKind, by string) (VerifyRun, error)
	Stop(ctx context.Context, sid, id string) error
	MainVerifyPage
	Attachment(ctx context.Context, sid, id, name string) (VerifyAttachment, io.ReadCloser, error)
}

type VerifyConfig struct {
	Host    VerifierHost
	Trees   VerifyTrees
	Files   VerifyFiles
	Store   VerifyStore
	Heads   Heads
	Targets func(sid string) (PushTarget, bool)
	Project func(name string) (ProjectID, ProjectCommands, bool)
	Kinds   func(project string) []string
	Mains   MainRepos
	Audit   Audit
	Base    context.Context
	Now     func() time.Time
	Logger  shared.Logger
}

type verifyLive struct {
	stop chan struct{}
	once sync.Once
}

func (l *verifyLive) cancel() {
	l.once.Do(func() { close(l.stop) })
}

type Verifier struct {
	cfg        VerifyConfig
	reg        *Registry
	mu         sync.Mutex
	saveMu     sync.Mutex
	runs       []VerifyRun
	live       map[string]*verifyLive
	queue      []queuedVerify
	noted      map[string]bool
	starting   map[string]string
	mains      map[string]bool
	ready      func(ctx context.Context, sid, head string, kind VerifyKind) bool
	commitDone func(commit string)
	done       func(sid, head string)
	wg         sync.WaitGroup
}

type queuedVerify struct {
	sid  string
	head string
	kind VerifyKind
}

type VerifyAsk struct {
	Start bool
	Agent bool
}

var _ VerifyPage = (*Verifier)(nil)

func NewVerifier(reg *Registry, cfg VerifyConfig) *Verifier {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	cfg.Audit = masking(cfg.Audit)
	v := &Verifier{cfg: cfg, reg: reg, live: map[string]*verifyLive{}}
	v.load()
	return v
}

func (v *Verifier) load() {
	if v.cfg.Store == nil {
		return
	}
	saved, err := v.cfg.Store.Load()
	if err != nil {
		v.cfg.Logger.Warn("verify", "event", "records-unreadable")
		return
	}
	changed := false
	now := v.cfg.Now()
	for i := range saved {
		if saved[i].State == RunRunning {
			saved[i].State, saved[i].Reason, saved[i].Ended = VerifyInterrupted, "the engine restarted while it ran, so nothing the run did afterwards counts", now
			changed = true
		}
	}
	v.runs = saved
	if changed {
		v.save()
	}
}

func (v *Verifier) OnDone(fn func(sid, head string)) {
	v.mu.Lock()
	v.done = fn
	v.mu.Unlock()
}

func (v *Verifier) Wait() { v.wg.Wait() }

func commandHash(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

func (v *Verifier) save() {
	v.saveMu.Lock()
	defer v.saveMu.Unlock()
	v.mu.Lock()
	snapshot := slices.Clone(v.runs)
	v.mu.Unlock()
	if v.cfg.Store == nil {
		return
	}
	if err := v.cfg.Store.Save(snapshot); err != nil {
		v.cfg.Logger.Warn("verify", "event", "records-not-saved")
	}
}

func (v *Verifier) notify(sid string) {
	if v.reg != nil {
		v.reg.Notify(Fields{"type": verifyFrame, "sid": sid})
	}
}

func shownCommand(command string) string {
	shown, ok := CheckProjectCommand(command)
	if !ok || secretText(shown) {
		return ""
	}
	return maskSecrets(shown)
}

func (v *Verifier) View(_ context.Context, sid string) (VerifyView, error) {
	t, ok := v.targetOf(sid)
	if !ok {
		return VerifyView{}, explained(conflict("not started here"), "Only a session this engine started has verification runs here.")
	}
	out := VerifyView{Branch: t.Branch}
	if head, err := v.cfg.Heads.Head(t.Repo, t.Branch); err == nil {
		out.Head = head
	}
	id, cmds, known := v.project(t.Name)
	if known {
		out.Command, out.HasCheck, out.Image = shownCommand(cmds.Check), cmds.Check != "", cmds.Image
		out.Browser.Command, out.Browser.Has = shownCommand(cmds.Browser), cmds.Browser != ""
	}
	if v.cfg.Kinds != nil {
		kinds := v.cfg.Kinds(t.Name)
		out.Required = slices.Contains(kinds, string(VerifyCheck))
		out.Browser.Required = slices.Contains(kinds, string(VerifyBrowser))
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	out.Busy = len(v.live) >= MaxVerifyRuns
	out.Queued = out.Head != "" && v.queuedKindLocked(sid, out.Head, VerifyCheck)
	out.Browser.Queued = out.Head != "" && v.queuedKindLocked(sid, out.Head, VerifyBrowser)
	for _, r := range slices.Backward(v.runs) {
		if r.SID == sid && len(out.Runs) < verifyShown {
			out.Runs = append(out.Runs, r)
		}
	}
	if out.Head != "" && known {
		out.Current, out.Why = v.kindLocked(id, out.Head, VerifyCheck, cmds, sid)
		out.Browser.Current, out.Browser.Why = v.kindLocked(id, out.Head, VerifyBrowser, cmds, sid)
	}
	return out, nil
}

func (v *Verifier) kindLocked(id ProjectID, head string, kind VerifyKind, cmds ProjectCommands, sid string) (*VerifyRun, string) {
	if r, ok := v.currentLocked(id, head, kind, cmds); ok {
		return &r, ""
	}
	if last, ok := v.latestLocked(id, head, kind); ok {
		return nil, staleWhy(last, kind, cmds)
	}
	for _, r := range slices.Backward(v.runs) {
		if r.SID == sid && r.Kind == kind {
			if r.Commit != head {
				return nil, "The last run was of commit " + shortSHA(r.Commit) + ", and the branch is at " + shortSHA(head) + " now, so it doesn't count for this commit."
			}
			break
		}
	}
	return nil, ""
}

func staleWhy(r VerifyRun, kind VerifyKind, cmds ProjectCommands) string {
	switch {
	case r.CommandHash != commandHash(verifyCommand(kind, cmds)):
		return "The last run of this commit ran another " + string(kind) + " command than the one set now, so it doesn't count."
	case VerifyDigest(r.Image) != VerifyDigest(cmds.Image):
		return "The last run of this commit ran in another image than the one set now, so it doesn't count."
	}
	return ""
}

func (v *Verifier) targetOf(sid string) (PushTarget, bool) {
	if v.cfg.Targets == nil || v.cfg.Heads == nil {
		return PushTarget{}, false
	}
	return v.cfg.Targets(sid)
}

func (v *Verifier) project(name string) (ProjectID, ProjectCommands, bool) {
	if v.cfg.Project == nil {
		return ProjectID{}, ProjectCommands{}, false
	}
	return v.cfg.Project(name)
}

func (v *Verifier) latestLocked(id ProjectID, commit string, kind VerifyKind) (VerifyRun, bool) {
	for _, r := range slices.Backward(v.runs) {
		if r.SID != "" && r.Project == id && r.Commit == commit && r.Kind == kind {
			return r, true
		}
	}
	return VerifyRun{}, false
}

func (v *Verifier) currentLocked(id ProjectID, commit string, kind VerifyKind, cmds ProjectCommands) (VerifyRun, bool) {
	hash, digest := commandHash(verifyCommand(kind, cmds)), VerifyDigest(cmds.Image)
	for _, r := range slices.Backward(v.runs) {
		if r.SID != "" && r.Project == id && r.Commit == commit && r.Kind == kind && r.CommandHash == hash && digest != "" && VerifyDigest(r.Image) == digest {
			return r, true
		}
	}
	return VerifyRun{}, false
}

func (v *Verifier) Start(ctx context.Context, sid string, kind VerifyKind, by string) (VerifyRun, error) {
	return v.start(ctx, sid, kind, by, false, "")
}

var errVerifyQueued = explained(conflict("queued"), "The run waits for a free verification slot.")

func (v *Verifier) start(ctx context.Context, sid string, kind VerifyKind, by string, front bool, want string) (VerifyRun, error) {
	if !slices.Contains(VerifyKinds, kind) {
		return VerifyRun{}, explained(invalid("kind"), "The engine runs two kinds of verification: check and browser.")
	}
	if v.cfg.Host == nil || v.cfg.Trees == nil {
		return VerifyRun{}, explained(conflict("no-helper"), "This engine has no box helper or verification folder, so it runs no verification.")
	}
	t, ok := v.targetOf(sid)
	if !ok {
		return VerifyRun{}, explained(conflict("not started here"), "Only a session this engine started can be verified here.")
	}
	head, err := v.cfg.Heads.Head(t.Repo, t.Branch)
	if err != nil {
		return VerifyRun{}, explained(conflict("branch"), "The engine couldn't read the session's branch "+t.Branch+", so it has no commit to verify.")
	}
	if want != "" && head != want {
		return VerifyRun{}, explained(conflict("moved"), "The session's branch moved since the run was queued.")
	}
	id, cmds, known := v.project(t.Name)
	switch {
	case !known:
		return VerifyRun{}, explained(notFound("project"), "The engine has no record of this session's project.")
	case kind == VerifyCheck && cmds.Check == "":
		return VerifyRun{}, explained(conflict("no-check"), "This project has no check command, so there is nothing to verify. Set one under Check and setup commands on the project's card.")
	case kind == VerifyBrowser && cmds.Browser == "":
		return VerifyRun{}, explained(conflict("no-browser-command"), "This project has no browser command, so there is no browser check to run. Set one under Check and setup commands on the project's card.")
	case kind == VerifyBrowser && v.cfg.Files == nil:
		return VerifyRun{}, explained(conflict("no-files"), "This engine has no folder for a browser run's files, so it runs no browser check.")
	case VerifyDigest(cmds.Image) == "":
		return VerifyRun{}, explained(conflict("no-image"), "This project names no image for verification runs, so it can't run one. Set an image pinned by digest under Check and setup commands on the project's card.")
	}
	run, err := NewBox()
	if err != nil {
		return VerifyRun{}, explained(conflict("random"), "The engine couldn't make an id for the run.")
	}
	command := verifyCommand(kind, cmds)
	rec := VerifyRun{ID: run, Project: id, SID: sid, Branch: t.Branch, Commit: head, Kind: kind, CommandHash: commandHash(command), Image: cmds.Image,
		State: RunRunning, By: by, Started: v.cfg.Now()}
	live := &verifyLive{stop: make(chan struct{})}
	v.mu.Lock()
	if !front && v.starting[sid] == startingKey(head, kind) {
		v.mu.Unlock()
		if by == VerifyByEngine {
			return VerifyRun{}, errVerifyQueued
		}
		return VerifyRun{}, explained(conflict("starting"), "A run of this commit is about to start for a push. Wait for it.")
	}
	for _, r := range v.runs {
		if _, running := v.live[r.ID]; running && r.SID == sid && r.Kind == kind && r.Commit == head && r.CommandHash == rec.CommandHash && VerifyDigest(r.Image) == VerifyDigest(rec.Image) {
			v.mu.Unlock()
			return r, nil
		}
	}
	for _, r := range v.runs {
		if _, running := v.live[r.ID]; running && r.SID == sid && r.Kind == kind {
			if by == VerifyByEngine {
				v.queueLocked(queuedVerify{sid: sid, head: head, kind: kind}, front)
				v.mu.Unlock()
				v.notify(sid)
				shared.For(ctx, v.cfg.Logger).Info("verify", "event", "queued", "kind", string(kind))
				return VerifyRun{}, errVerifyQueued
			}
			v.mu.Unlock()
			return VerifyRun{}, explained(conflict("running"), "A verification run of this session is running. Wait for it, or stop it first.")
		}
	}
	engineRuns := 0
	for _, r := range v.runs {
		if _, running := v.live[r.ID]; running && r.By == VerifyByEngine {
			engineRuns++
		}
	}
	ahead := slices.ContainsFunc(v.queue, func(x queuedVerify) bool { return x.sid != sid && !v.liveSessionLocked(x.sid) })
	if by == VerifyByEngine && (engineRuns >= MaxEngineVerifyRuns || len(v.live) >= MaxVerifyRuns || ahead && !front) {
		v.queueLocked(queuedVerify{sid: sid, head: head, kind: kind}, front)
		v.mu.Unlock()
		v.notify(sid)
		shared.For(ctx, v.cfg.Logger).Info("verify", "event", "queued", "kind", string(kind))
		return VerifyRun{}, errVerifyQueued
	}
	if len(v.live) >= MaxVerifyRuns {
		v.mu.Unlock()
		return VerifyRun{}, explained(limited("verify-busy"), "The engine runs at most "+strconv.Itoa(MaxVerifyRuns)+" verification runs at once. Try again when one finishes.")
	}
	v.live[run] = live
	if by == VerifyByPage {
		v.queue = slices.DeleteFunc(v.queue, func(x queuedVerify) bool { return x.sid == sid && x.head == head && x.kind == kind })
	}
	v.runs = append(v.runs, rec)
	if over := len(v.runs) - MaxVerifyRecords; over > 0 {
		v.runs = slices.Delete(v.runs, 0, over)
	}
	v.mu.Unlock()
	v.save()
	v.audit(ctx, rec, "started")
	shared.For(ctx, v.cfg.Logger).Info("verify", "event", "started", "run", run, "kind", string(kind), "by", by)
	v.notify(sid)
	req := shared.RequestOf(ctx)
	v.wg.Go(func() { v.execute(shared.WithRequest(v.cfg.Base, req), rec, t, command, live) })
	return rec, nil
}

func startingKey(head string, kind VerifyKind) string {
	if head == "" {
		return ""
	}
	return head + " " + string(kind)
}

func (v *Verifier) queueLocked(q queuedVerify, front bool) {
	same := func(x queuedVerify) bool { return x.sid == q.sid && x.kind == q.kind }
	if i := slices.IndexFunc(v.queue, same); i >= 0 && !front {
		v.queue[i] = q
		return
	}
	v.queue = slices.DeleteFunc(v.queue, same)
	if front {
		v.queue = append([]queuedVerify{q}, v.queue...)
		return
	}
	v.queue = append(v.queue, q)
}

func (v *Verifier) queuedLocked(sid, head string) bool {
	return slices.ContainsFunc(VerifyKinds, func(k VerifyKind) bool { return v.queuedKindLocked(sid, head, k) })
}

func (v *Verifier) queuedKindLocked(sid, head string, kind VerifyKind) bool {
	if head != "" && v.starting[sid] == startingKey(head, kind) {
		return true
	}
	return slices.ContainsFunc(v.queue, func(x queuedVerify) bool { return x.sid == sid && x.head == head && x.kind == kind })
}

func (v *Verifier) liveSessionLocked(sid string) bool {
	for _, r := range v.runs {
		if _, running := v.live[r.ID]; running && r.SID == sid {
			return true
		}
	}
	return false
}

func (v *Verifier) UseReady(fn func(ctx context.Context, sid, head string, kind VerifyKind) bool) {
	v.mu.Lock()
	v.ready = fn
	v.mu.Unlock()
}

func (v *Verifier) serveQueue(ctx context.Context) {
	for {
		if v.cfg.Base.Err() != nil {
			return
		}
		v.mu.Lock()
		i := slices.IndexFunc(v.queue, func(x queuedVerify) bool { return !v.liveSessionLocked(x.sid) })
		if i < 0 || len(v.live) >= MaxVerifyRuns {
			v.mu.Unlock()
			return
		}
		next := v.queue[i]
		v.queue = slices.Delete(v.queue, i, i+1)
		if v.starting == nil {
			v.starting = map[string]string{}
		}
		v.starting[next.sid] = startingKey(next.head, next.kind)
		fn, ready := v.done, v.ready
		v.mu.Unlock()
		unmark := func() {
			v.mu.Lock()
			if v.starting[next.sid] == startingKey(next.head, next.kind) {
				delete(v.starting, next.sid)
			}
			v.mu.Unlock()
		}
		v.notify(next.sid)
		settle := func() {
			if fn != nil && v.cfg.Base.Err() == nil {
				fn(next.sid, next.head)
			}
		}
		if v.ranAlready(next) || ready != nil && !ready(ctx, next.sid, next.head, next.kind) {
			shared.For(ctx, v.cfg.Logger).Info("verify", "event", "queued-dropped", "why", "the push no longer needs it")
			unmark()
			settle()
			continue
		}
		_, err := v.start(ctx, next.sid, next.kind, VerifyByEngine, true, next.head)
		unmark()
		if errors.Is(err, errVerifyQueued) {
			return
		}
		if err != nil {
			settle()
		}
	}
}

func (v *Verifier) ranAlready(q queuedVerify) bool {
	t, ok := v.targetOf(q.sid)
	if !ok {
		return false
	}
	id, cmds, known := v.project(t.Name)
	if !known {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	cur, found := v.currentLocked(id, q.head, q.kind, cmds)
	_, live := v.live[cur.ID]
	return found && !live
}

func (v *Verifier) OnCommitDone(fn func(commit string)) {
	v.mu.Lock()
	v.commitDone = fn
	v.mu.Unlock()
}

func (v *Verifier) audit(ctx context.Context, r VerifyRun, result string) {
	if v.cfg.Audit == nil {
		return
	}
	reason := "kind " + string(r.Kind) + ", by " + r.By
	if r.Ran() {
		reason = "kind " + string(r.Kind) + ", state " + r.State + ", exit " + strconv.Itoa(r.Code)
	}
	e := AuditEntry{Time: v.cfg.Now(), Session: r.SID, Repository: r.Project.Name, Branch: r.Branch, Commit: r.Commit, Kind: AuditVerify, Result: result, Reason: reason, Req: shared.RequestOf(ctx)}
	if err := v.cfg.Audit.Append(e); err != nil {
		v.cfg.Logger.Warn("verify", "event", "audit-not-written")
	}
}

type verifyEnd struct {
	state    string
	code     int
	reason   string
	imageID  string
	limit    time.Duration
	tail     string
	cut      bool
	withheld bool
	attached []VerifyAttachment
	note     string
}

func (v *Verifier) execute(ctx context.Context, rec VerifyRun, t PushTarget, command string, live *verifyLive) {
	log := shared.For(ctx, v.cfg.Logger)
	end := v.attempt(ctx, rec, t, command, live)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), verifyRemoveFor)
	defer cancel()
	if err := v.cfg.Host.RemoveVerifier(rctx, rec.ID); err != nil {
		log.Warn("verify", "event", "container-not-removed", "run", rec.ID, "fix", "the engine's sweep removes it within 5 minutes")
	}
	if err := v.cfg.Trees.Remove(rec.ID); err != nil {
		log.Warn("verify", "event", "files-not-removed", "run", rec.ID)
	}
	v.finish(ctx, rec.ID, end)
}

func (v *Verifier) attempt(ctx context.Context, rec VerifyRun, t PushTarget, command string, live *verifyLive) verifyEnd {
	if err := v.cfg.Trees.Write(t.Repo, rec.Commit, rec.ID, command); err != nil {
		return verifyEnd{state: RunError, reason: "The engine couldn't write commit " + shortSHA(rec.Commit) + "'s files for the run: " + cleanReason(err.Error())}
	}
	if end, stopped := v.stoppedEnd(live); stopped {
		return end
	}
	sctx, cancel := context.WithTimeout(ctx, verifyStartFor)
	info, err := v.cfg.Host.StartVerifier(sctx, rec.ID, VerifyDigest(rec.Image), rec.Kind == VerifyBrowser)
	cancel()
	if err != nil {
		return verifyEnd{state: RunError, reason: "The box helper didn't start the run's container: " + cleanReason(errText(err))}
	}
	if info.Run != rec.ID || info.Limit <= 0 || info.Image == "" || info.Digest != VerifyDigest(rec.Image) {
		return verifyEnd{state: RunError, reason: "The box helper's answer doesn't describe the run the engine asked for."}
	}
	v.mu.Lock()
	for i := range v.runs {
		if v.runs[i].ID == rec.ID {
			v.runs[i].ImageID, v.runs[i].Limit = info.Image, info.Limit
		}
	}
	v.mu.Unlock()
	v.notifyRun(rec)
	deadline := v.cfg.Now().Add(info.Limit + verifySlack)
	var st VerifierState
	fails := 0
	ended := false
	for !ended {
		timer := time.NewTimer(verifyPoll)
		select {
		case <-live.stop:
			timer.Stop()
			end, _ := v.stoppedEnd(live)
			end.imageID, end.limit = info.Image, info.Limit
			return end
		case <-ctx.Done():
			timer.Stop()
			return verifyEnd{state: VerifyInterrupted, reason: "The engine stopped while it ran, so nothing the run did afterwards counts.", imageID: info.Image, limit: info.Limit}
		case <-timer.C:
		}
		cctx, cancel := context.WithTimeout(ctx, verifyCallFor)
		got, err := v.cfg.Host.VerifierState(cctx, rec.ID)
		cancel()
		switch {
		case err != nil:
			fails++
			if fails >= verifyStateTries {
				return verifyEnd{state: RunError, reason: "The box helper stopped answering about the run: " + cleanReason(errText(err)), imageID: info.Image, limit: info.Limit}
			}
		default:
			fails, st = 0, got
			ended = !containerActive(got.State)
		}
		if !ended && v.cfg.Now().After(deadline) {
			return verifyEnd{state: RunTimedOut, code: -1, reason: "It didn't finish within " + minutes(info.Limit) + ", so the engine stopped it.", imageID: info.Image, limit: info.Limit}
		}
	}
	end := verdictOf(info, st, VerifyDigest(rec.Image))
	end.imageID, end.limit = info.Image, info.Limit
	if st.State != "exited" {
		return end
	}
	if rec.Kind == VerifyBrowser {
		end.attached, end.note = v.collect(ctx, rec.ID)
	}
	lctx, cancel := context.WithTimeout(ctx, verifyCallFor)
	text, err := v.cfg.Host.VerifierLog(lctx, rec.ID)
	cancel()
	if err != nil {
		if end.reason == "" {
			end.reason = "Its output couldn't be read."
		}
		return end
	}
	tail := strings.TrimRight(ansi.ReplaceAllString(text, ""), "\n ")
	if len(tail) > MaxCommandTail {
		tail, end.cut = lastBytes(tail, MaxCommandTail), true
	}
	if secretText(tail) {
		end.withheld = true
	} else {
		end.tail = tail
	}
	return end
}

func (v *Verifier) stoppedEnd(live *verifyLive) (verifyEnd, bool) {
	select {
	case <-live.stop:
		return verifyEnd{state: VerifyStopped, reason: "It was stopped before it finished."}, true
	default:
	}
	return verifyEnd{}, false
}

func containerActive(state string) bool {
	switch state {
	case "created", RunRunning, "restarting", "paused":
		return true
	}
	return false
}

func minutes(d time.Duration) string {
	n := int(d / time.Minute)
	if n == 1 {
		return "1 minute"
	}
	return strconv.Itoa(n) + " minutes"
}

func verdictOf(info VerifierInfo, st VerifierState, digest string) verifyEnd {
	switch {
	case st.Run != info.Run || st.Image == "" || st.Image != info.Image || st.Digest != digest || digest == "":
		return verifyEnd{state: RunError, code: -1, reason: "Docker's record of the container isn't the one the engine started, so the run doesn't count."}
	case st.State != "exited":
		return verifyEnd{state: RunError, code: -1, reason: "The container ended as " + clip(oneWord(st.State), 32) + ", not exited, so the run has no result."}
	case st.StartedAt.IsZero() || st.FinishedAt.IsZero() || st.FinishedAt.Before(st.StartedAt):
		return verifyEnd{state: RunError, code: -1, reason: "Docker's record of the run has no start or end time, so it has no result."}
	case st.FinishedAt.Sub(st.StartedAt) > info.Limit:
		return verifyEnd{state: RunTimedOut, code: st.ExitCode, reason: "It didn't finish within " + minutes(info.Limit) + ", so the box helper stopped it."}
	case st.OOMKilled:
		return verifyEnd{state: VerifyOOM, code: st.ExitCode, reason: "It ran out of memory, or its files filled the run's disk, which counts against the same memory."}
	case st.Error != "":
		return verifyEnd{state: RunError, code: st.ExitCode, reason: "Docker reported an error: " + cleanReason(st.Error)}
	case st.ExitCode != 0:
		return verifyEnd{state: RunFailed, code: st.ExitCode}
	}
	return verifyEnd{state: RunPassed}
}

func oneWord(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 'a' || r > 'z' {
			return '-'
		}
		return r
	}, s)
}

func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = clip(strings.TrimSpace(s), maxVerifyReason)
	if secretText(s) {
		return "the message held a token-shaped string, so the engine didn't keep it"
	}
	return s
}

func (v *Verifier) finish(ctx context.Context, run string, end verifyEnd) {
	v.mu.Lock()
	if _, ok := v.live[run]; !ok {
		v.mu.Unlock()
		return
	}
	delete(v.live, run)
	i := slices.IndexFunc(v.runs, func(r VerifyRun) bool { return r.ID == run })
	if i < 0 {
		v.mu.Unlock()
		return
	}
	r := &v.runs[i]
	r.State, r.Code, r.Reason, r.Tail, r.Cut, r.Withheld, r.Ended = end.state, end.code, end.reason, end.tail, end.cut, end.withheld, v.cfg.Now()
	r.Attached, r.AttachNote = end.attached, end.note
	if end.imageID != "" {
		r.ImageID = end.imageID
	}
	if end.limit > 0 {
		r.Limit = end.limit
	}
	done := *r
	fn := v.done
	v.mu.Unlock()
	v.retain()
	v.save()
	v.audit(ctx, done, "ended")
	shared.For(ctx, v.cfg.Logger).Info("verify", "event", "ended", "run", run, "state", done.State, "code", done.Code)
	v.notifyRun(done)
	if fn != nil && v.cfg.Base.Err() == nil && done.SID != "" {
		fn(done.SID, done.Commit)
	}
	v.mu.Lock()
	others := v.commitDone
	v.mu.Unlock()
	if others != nil && v.cfg.Base.Err() == nil && done.SID != "" {
		others(done.Commit)
	}
	v.serveQueue(shared.WithRequest(v.cfg.Base, shared.RequestOf(ctx)))
}

func (v *Verifier) Stop(ctx context.Context, sid, id string) error {
	if !ValidBox(id) {
		return explained(invalid("id"), "A verification run's id is 16 hexadecimal digits.")
	}
	v.mu.Lock()
	l, ok := v.live[id]
	owned := slices.ContainsFunc(v.runs, func(r VerifyRun) bool { return r.ID == id && r.SID == sid })
	v.mu.Unlock()
	if !ok || !owned {
		return explained(conflict("not running"), "That verification run isn't running.")
	}
	l.cancel()
	shared.For(ctx, v.cfg.Logger).Info("verify", "event", "stop-asked", "run", id)
	return nil
}

func (v *Verifier) Running(sid, head string) bool {
	var id ProjectID
	var cmds ProjectCommands
	known := false
	if t, ok := v.targetOf(sid); ok {
		id, cmds, known = v.project(t.Name)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.queuedLocked(sid, head) {
		return true
	}
	for _, r := range v.runs {
		if _, live := v.live[r.ID]; live && r.SID == sid && r.Commit == head {
			return true
		}
	}
	if known {
		for _, k := range VerifyKinds {
			if cur, found := v.currentLocked(id, head, k, cmds); found {
				if _, live := v.live[cur.ID]; live {
					return true
				}
			}
		}
	}
	return false
}

func (v *Verifier) Forget(sid string) {
	v.mu.Lock()
	v.queue = slices.DeleteFunc(v.queue, func(x queuedVerify) bool { return x.sid == sid })
	for _, r := range v.runs {
		if l, live := v.live[r.ID]; live && r.SID == sid {
			l.cancel()
		}
	}
	v.forgetLocked(func(r VerifyRun) bool { return r.SID == sid })
}

func (v *Verifier) ForgetProject(id ProjectID) {
	v.mu.Lock()
	v.forgetLocked(func(r VerifyRun) bool { return r.Project == id })
}

func (v *Verifier) forgetLocked(match func(VerifyRun) bool) {
	var gone []string
	before := len(v.runs)
	v.runs = slices.DeleteFunc(v.runs, func(r VerifyRun) bool {
		_, live := v.live[r.ID]
		if match(r) && !live {
			if len(r.Attached) > 0 {
				gone = append(gone, r.ID)
			}
			return true
		}
		return false
	})
	changed := len(v.runs) != before
	v.mu.Unlock()
	if changed {
		v.save()
	}
	v.dropFiles(gone)
}

func (v *Verifier) Items(ctx context.Context, sid string, t PushTarget, head string, kinds []string, ask VerifyAsk) []string {
	var out []string
	for _, k := range kinds {
		if !slices.Contains(VerifyKinds, VerifyKind(k)) {
			out = append(out, "a verification run of a kind this engine doesn't run ("+clip(oneWord(k), 20)+"), which the approved team file requires")
			continue
		}
		if item := v.item(ctx, sid, t, head, VerifyKind(k), ask); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (v *Verifier) item(ctx context.Context, sid string, t PushTarget, head string, kind VerifyKind, ask VerifyAsk) string {
	of := "a passing verification run (" + string(kind) + ") of commit " + shortSHA(head)
	pending := of + ", which the engine doesn't have yet; the session's verification panel on the page shows the run's state, and the engine decides an automatic push again when a run it started or queued ends"
	id, cmds, known := v.project(t.Name)
	switch {
	case !known:
		return of + ": the engine has no record of this project"
	case kind == VerifyCheck && cmds.Check == "":
		return "a check command for this project, which the approved team file requires a verification run of: set it under Check and setup commands"
	case kind == VerifyBrowser && cmds.Browser == "":
		return "a browser command for this project, which the approved team file requires a verification run of: set it under Check and setup commands"
	case VerifyDigest(cmds.Image) == "":
		return "an image for this project's verification runs, which the approved team file requires: set one pinned by digest under Check and setup commands"
	}
	v.mu.Lock()
	cur, found := v.currentLocked(id, head, kind, cmds)
	_, ranAny := v.latestLocked(id, head, kind)
	_, running := v.live[cur.ID]
	queued := v.queuedKindLocked(sid, head, kind)
	v.mu.Unlock()
	switch {
	case found && cur.State == RunPassed:
		return ""
	case found && (running || cur.State == RunRunning):
		if ask.Agent {
			return pending
		}
		return "the verification run (" + string(kind) + ") of commit " + shortSHA(head) + " to finish"
	case queued:
		if ask.Agent {
			return pending
		}
		return "the verification run (" + string(kind) + ") of commit " + shortSHA(head) + " to start, which waits for a free verification slot"
	case found:
		if ask.Start {
			v.noteNotRestarted(ctx, cur)
		}
		if ask.Agent {
			switch cur.State {
			case RunFailed, RunTimedOut, VerifyOOM:
				return pending
			}
		}
		return of + ": " + lastRunWhy(cur)
	}
	none := of + ": none ran; " + verifyPress
	if ranAny {
		none = of + " with the current check command and image: the last one ran another; " + verifyPress
	}
	if !ask.Start {
		return none
	}
	_, err := v.Start(ctx, sid, kind, VerifyByEngine)
	switch {
	case errors.Is(err, errVerifyQueued):
		if ask.Agent {
			return pending
		}
		return "the verification run (" + string(kind) + ") of commit " + shortSHA(head) + " to start, which the engine queued for this push until a verification slot is free"
	case err != nil:
		var e *Error
		why := "the engine couldn't start one"
		if errors.As(err, &e) && e.Message != "" {
			why += " (" + strings.TrimSuffix(e.Message, ".") + ")"
		}
		return none + "; " + why
	case ask.Agent:
		return pending
	}
	return "the verification run (" + string(kind) + ") of commit " + shortSHA(head) + " to finish, which the engine started for this push"
}

func (v *Verifier) noteNotRestarted(ctx context.Context, r VerifyRun) {
	switch r.State {
	case VerifyInterrupted, VerifyStopped, RunError:
	default:
		return
	}
	v.mu.Lock()
	if v.noted == nil {
		v.noted = map[string]bool{}
	}
	seen := v.noted[r.ID]
	v.noted[r.ID] = true
	v.mu.Unlock()
	if seen || v.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: v.cfg.Now(), Session: r.SID, Repository: r.Project.Name, Branch: r.Branch, Commit: r.Commit, Kind: AuditVerify, Result: "not-restarted",
		Reason: "a push of this commit needs a passing run, and the last one ended " + r.State + " without a result; the engine doesn't start another on its own, so the push stays held until the person presses Verify this commit", Req: shared.RequestOf(ctx)}
	if err := v.cfg.Audit.Append(e); err != nil {
		v.cfg.Logger.Warn("verify", "event", "audit-not-written")
	}
}

func verifyItem(s string) bool {
	for _, p := range []string{"a passing verification run (", "the verification run (", "a verification run of a kind", "a check command for this project, which the approved team file requires", "a browser command for this project, which the approved team file requires", "an image for this project's verification runs"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func lastRunWhy(r VerifyRun) string {
	switch r.State {
	case RunFailed, RunTimedOut, VerifyOOM:
		return "the last one didn't pass; the session's verification panel on the page shows why"
	case VerifyInterrupted:
		return "the last one was cut off by an engine restart; " + verifyPress
	case VerifyStopped:
		return "the last one was stopped; " + verifyPress
	}
	return "the last one couldn't run; " + verifyPress
}

func (v *Verifier) Sweep(ctx context.Context) error {
	if v.cfg.Host == nil {
		return nil
	}
	list, err := v.cfg.Host.ListVerifiers(ctx)
	if err != nil {
		return err
	}
	var failed error
	for _, c := range list {
		v.mu.Lock()
		_, ours := v.live[c.Box]
		v.mu.Unlock()
		if ours {
			continue
		}
		if err := v.cfg.Host.RemoveVerifier(ctx, c.Box); err != nil {
			v.cfg.Logger.Warn("verify", "event", "leftover-not-removed", "run", c.Box)
			failed = errors.Join(failed, err)
			continue
		}
		v.cfg.Logger.Info("verify", "event", "leftover-removed", "run", c.Box, "state", c.State)
	}
	if v.cfg.Trees != nil {
		if err := v.cfg.Trees.Prune(func(run string) bool {
			v.mu.Lock()
			defer v.mu.Unlock()
			_, ok := v.live[run]
			return ok
		}); err != nil {
			v.cfg.Logger.Warn("verify", "event", "files-not-pruned")
		}
	}
	v.pruneFiles()
	return failed
}

func (v *Verifier) Watch(ctx context.Context) {
	wait := time.Duration(0)
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wait = verifySweepEach
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := v.Sweep(sctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			wait = verifySweepWait
		}
	}
}
