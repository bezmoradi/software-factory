package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/agenthost"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	reviewerRemoveFor   = 90 * time.Second
	reviewerProbeFor    = 90 * time.Second
	reviewerSweepFor    = 2 * time.Minute
	reviewerSweepWait   = 10 * time.Second
	reviewerSweepEach   = 5 * time.Minute
	reviewerSignInTries = 6
	reviewerSlotWait    = 10 * time.Second
	reviewerHostCode    = session.ReviewerContainer
	reviewerSandbox     = session.ReviewerSandbox
)

type reviewerHosts struct {
	makers  boxes.ReviewerMaker
	routes  *boxRoutes
	sandbox session.Sandbox
	allow   *session.Allowlist
	dev     string
	dir     string
	logger  shared.Logger

	runs chan struct{}

	mu    sync.Mutex
	live  map[string]bool
	login *reviewerLease
	timer *time.Timer
}

type reviewerLease struct {
	run    string
	host   agentHost
	client *agenthost.Client
	info   session.BoxInfo
}

var (
	_ session.Host          = (*reviewerHosts)(nil)
	_ session.StreamHost    = (*reviewerHosts)(nil)
	_ session.SignInChecker = (*reviewerHosts)(nil)
	_ session.LoginHost     = (*reviewerHosts)(nil)
	_ session.KeyHost       = (*reviewerHosts)(nil)
	_ session.AgentFileHost = (*reviewerHosts)(nil)
)

func newReviewerHosts(makers boxes.ReviewerMaker, routes *boxRoutes, allow *session.Allowlist, dev string, logger shared.Logger) *reviewerHosts {
	return &reviewerHosts{makers: makers, routes: routes, allow: allow, dev: dev, dir: boxes.ReviewsAt, logger: logger, live: map[string]bool{}, runs: make(chan struct{}, session.MaxReviewerRuns)}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func reviewerProblem(code, message string) error {
	return &session.HostError{Code: code, Message: message}
}

func (h *reviewerHosts) snapshotOf(dir string) (string, error) {
	clean := path.Clean(dir)
	if clean == h.dir {
		return "", nil
	}
	if path.Dir(clean) == h.dir && session.ValidBox(path.Base(clean)) {
		return path.Base(clean), nil
	}
	return "", reviewerProblem("cwd", "a reviewer container runs only in the reviews folder or one review's snapshot in it")
}

func (h *reviewerHosts) open(ctx context.Context, snapshot string, brief bool) (*reviewerLease, error) {
	if h.makers == nil || h.routes == nil {
		return nil, reviewerProblem(reviewerHostCode, "this engine has no box helper to make reviewer containers")
	}
	if err := h.slot(ctx, brief); err != nil {
		return nil, err
	}
	run := randomHex(8)
	h.mu.Lock()
	h.live[run] = true
	h.mu.Unlock()
	log := shared.For(ctx, h.logger)
	info, err := h.makers.StartReviewer(ctx, run, snapshot)
	if err != nil {
		h.close(ctx, run)
		if stopped(ctx) {
			log.Info("reviewer container", "event", "stopped", "run", run)
		} else {
			log.Warn("reviewer container", "event", "not-started", "run", run)
		}
		return nil, reviewerProblem(reviewerHostCode, "the box helper didn't start a reviewer container: "+clipError(err))
	}
	if err := h.routes.reviewerUp(ctx, run, info.Subnet); err != nil {
		h.close(ctx, run)
		return nil, reviewerProblem(reviewerHostCode, clipError(err))
	}
	client, err := agenthost.NewClientFor(info.Host, agenthost.ReviewRunTimeout)
	if err != nil {
		h.close(ctx, run)
		return nil, reviewerProblem(reviewerHostCode, clipError(err))
	}
	client.WithKey(randomHex(32))
	if err := client.GiveKey(ctx); err != nil {
		h.close(ctx, run)
		if stopped(ctx) {
			log.Info("reviewer container", "event", "stopped", "run", run)
		} else {
			log.Warn("reviewer container", "event", "key-refused", "run", run)
		}
		return nil, reviewerProblem(reviewerHostCode, "the reviewer container's agent-host didn't take the key the engine made for it, so the engine removed the container")
	}
	log.Info("reviewer container", "event", "started", "run", run, "snapshot", snapshot != "")
	return &reviewerLease{run: run, host: withGateway(client, h.allow, session.SideReviewers, h.dev), client: client, info: info}, nil
}

func (h *reviewerHosts) close(ctx context.Context, run string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reviewerRemoveFor)
	defer cancel()
	err := h.makers.RemoveReviewer(rctx, run)
	h.routes.reviewerDown(rctx, run)
	h.mu.Lock()
	_, held := h.live[run]
	delete(h.live, run)
	h.mu.Unlock()
	if held {
		<-h.runs
	}
	log := shared.For(ctx, h.logger)
	if err != nil {
		log.Warn("reviewer container", "event", "not-removed", "run", run, "fix", "the engine's sweep removes it within 5 minutes")
		return
	}
	log.Info("reviewer container", "event", "removed", "run", run)
}

func (h *reviewerHosts) slot(ctx context.Context, brief bool) error {
	select {
	case h.runs <- struct{}{}:
		return nil
	default:
	}
	wait := ctx
	if brief {
		until := time.Now().Add(reviewerSlotWait)
		if d, ok := session.StepDeadline(ctx); ok {
			until = d
		}
		var cancel context.CancelFunc
		wait, cancel = context.WithDeadline(ctx, until)
		defer cancel()
	}
	select {
	case h.runs <- struct{}{}:
		return nil
	case <-wait.Done():
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	shared.For(ctx, h.logger).Warn("reviewer container", "event", "no-slot")
	return slotBusy{}
}

type slotBusy struct{}

func (slotBusy) Error() string {
	return fmt.Sprintf("%d reviewer containers are running, the most the engine runs at once, so this step didn't start. Try again when a review finishes.", session.MaxReviewerRuns)
}

func (slotBusy) ReviewersBusy() bool { return true }

var _ session.SlotRefusal = slotBusy{}

func stopped(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

func clipError(err error) string {
	s := err.Error()
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

func (h *reviewerHosts) probed(ctx context.Context, l *reviewerLease, dir string) error {
	if h.sandbox == nil {
		return reviewerProblem(reviewerSandbox, "this engine has no sandbox check for the reviewer containers")
	}
	cctx, cancel := context.WithTimeout(ctx, reviewerProbeFor)
	defer cancel()
	o := h.sandbox.CheckBox(cctx, session.BoxProbe{Box: l.run, Host: l.host, Dir: dir, Subnet: l.info.Subnet, Gateway: l.info.Gateway})
	if o.Event == level.CheckPassed {
		return nil
	}
	why := "the reviewer container's sandbox check failed"
	if o.Event != level.CheckFailed {
		why = "the reviewer container's sandbox check couldn't run"
	}
	if len(o.Parts) > 0 {
		why += ": " + strings.Join(o.Parts, "; ")
	}
	log := shared.For(ctx, h.logger)
	if stopped(ctx) {
		log.Info("reviewer container", "event", "stopped", "run", l.run)
	} else {
		log.Warn("reviewer container", "event", "sandbox-failed", "run", l.run)
	}
	return reviewerProblem(reviewerSandbox, why)
}

func (h *reviewerHosts) leased(ctx context.Context, dir string, brief bool, do func(*reviewerLease) error) error {
	snapshot, err := h.snapshotOf(dir)
	if err != nil {
		return err
	}
	l, err := h.open(ctx, snapshot, brief)
	if err != nil {
		return err
	}
	defer h.close(ctx, l.run)
	if snapshot != "" {
		if err := h.probed(ctx, l, dir); err != nil {
			return err
		}
	}
	return do(l)
}

func (h *reviewerHosts) Run(ctx context.Context, c session.Command) (session.RunResult, error) {
	var res session.RunResult
	err := h.leased(ctx, c.Dir, false, func(l *reviewerLease) error {
		var err error
		res, err = l.host.Run(ctx, c)
		return err
	})
	return res, err
}

func (h *reviewerHosts) RunStreamed(ctx context.Context, c session.Command, progress func(session.RunProgress)) (session.StreamedRun, error) {
	var res session.StreamedRun
	err := h.leased(ctx, c.Dir, false, func(l *reviewerLease) error {
		s, ok := l.host.(session.StreamHost)
		if !ok {
			return reviewerProblem(reviewerHostCode, "the reviewer container's client can't stream a run")
		}
		var err error
		res, err = s.RunStreamed(ctx, c, progress)
		return err
	})
	return res, err
}

func (h *reviewerHosts) SignIn(ctx context.Context, c session.Command, kind string) (session.SignIn, error) {
	var out session.SignIn
	err := h.leased(ctx, c.Dir, true, func(l *reviewerLease) error {
		var err error
		out, err = l.host.SignIn(ctx, c, kind)
		return err
	})
	return out, err
}

func (h *reviewerHosts) APIKey(ctx context.Context, box, step, key string) error {
	return h.leased(ctx, h.dir, true, func(l *reviewerLease) error { return l.client.APIKey(ctx, box, step, key) })
}

func (h *reviewerHosts) PutAgents(ctx context.Context, files []session.AgentFileOut, keep []string) error {
	return h.leased(ctx, h.dir, false, func(l *reviewerLease) error { return l.client.PutAgents(ctx, files, keep) })
}

func (h *reviewerHosts) Login(ctx context.Context, c session.Command, step, code string) (string, error) {
	if _, err := h.snapshotOf(c.Dir); err != nil {
		return "", err
	}
	switch step {
	case "start":
		h.dropLogin(ctx, nil)
		l, err := h.open(ctx, "", true)
		if err != nil {
			return "", err
		}
		link, err := l.client.Login(ctx, c, step, "")
		if err != nil {
			h.close(ctx, l.run)
			return "", err
		}
		h.mu.Lock()
		earlier := h.login
		if h.timer != nil {
			h.timer.Stop()
		}
		h.login = l
		h.timer = time.AfterFunc(agenthost.LoginLife, func() { h.dropLogin(context.WithoutCancel(ctx), l) })
		h.mu.Unlock()
		if earlier != nil {
			h.close(ctx, earlier.run)
		}
		return link, nil
	case "code":
		l := h.takeLogin(nil)
		if l == nil {
			return "", reviewerProblem("login", "No login is waiting for a code. Start it again.")
		}
		defer h.close(ctx, l.run)
		return l.client.Login(ctx, c, step, code)
	}
	h.dropLogin(ctx, nil)
	return "", nil
}

func (h *reviewerHosts) takeLogin(only *reviewerLease) *reviewerLease {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.login
	if l == nil || (only != nil && l != only) {
		return nil
	}
	h.login = nil
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	return l
}

func (h *reviewerHosts) dropLogin(ctx context.Context, only *reviewerLease) {
	if l := h.takeLogin(only); l != nil {
		h.close(ctx, l.run)
	}
}

func (h *reviewerHosts) AddWorktree(context.Context, string, string, string, string, session.CommitIdentity) (string, error) {
	return "", errNoWorktrees
}

func (h *reviewerHosts) AddExistingWorktree(context.Context, string, string, string, session.CommitIdentity) (string, error) {
	return "", errNoWorktrees
}

func (h *reviewerHosts) SetIdentity(context.Context, string, string, session.CommitIdentity) error {
	return errNoWorktrees
}

func (h *reviewerHosts) RemoveWorktree(context.Context, string, string, bool) (string, error) {
	return "", errNoWorktrees
}

func (h *reviewerHosts) Start(context.Context, session.Command) (session.Process, error) {
	return nil, errNoProcesses
}

func (h *reviewerHosts) Stop(context.Context, string) error {
	return errNoProcesses
}

var (
	errNoWorktrees = reviewerProblem("route", "a reviewer container has no worktrees")
	errNoProcesses = reviewerProblem("route", "a reviewer container runs no long-lived processes")
)

func (h *reviewerHosts) sweep(ctx context.Context) error {
	if h.makers == nil {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, reviewerSweepFor)
	defer cancel()
	list, err := h.makers.ListReviewers(sctx)
	if err != nil {
		return err
	}
	var failed error
	for _, r := range list {
		h.mu.Lock()
		ours := h.live[r.Box]
		h.mu.Unlock()
		if ours {
			continue
		}
		if err := h.makers.RemoveReviewer(sctx, r.Box); err != nil {
			if stopped(ctx) {
				h.logger.Info("reviewer container", "event", "stopped", "run", r.Box)
			} else {
				h.logger.Warn("reviewer container", "event", "leftover-not-removed", "run", r.Box)
			}
			failed = errors.Join(failed, err)
			continue
		}
		h.routes.reviewerDown(sctx, r.Box)
		h.logger.Info("reviewer container", "event", "leftover-removed", "run", r.Box, "state", r.State)
	}
	return failed
}

func (h *reviewerHosts) watch(ctx context.Context) {
	wait := time.Duration(0)
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wait = reviewerSweepEach
		if err := h.sweep(ctx); err != nil && ctx.Err() == nil {
			wait = reviewerSweepWait
		}
	}
}
