package session

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const AuditHomeSettings = "home-settings"

const ResultRefused = "refused"

type homeOnce struct {
	digest string
	at     time.Time
}

func (h *HomeSettings) makeOnce(box string) string {
	digest := randomHex(32)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.once == nil {
		h.once = map[string]homeOnce{}
	}
	h.once[box] = homeOnce{digest: digest, at: h.now()}
	return digest
}

func (h *HomeSettings) takeOnce(box, digest string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	o, ok := h.once[box]
	if !ok || digest == "" || o.digest != digest {
		return false
	}
	delete(h.once, box)
	age := h.now().Sub(o.at)
	return age >= 0 && age < PendingBoxFor
}

func homeNeeds(read HomeRead, consent Consent) *Error {
	msg := "The agent's user settings in this session's container changed since its last start, and a wake loads them. Read the list, then approve to wake."
	switch {
	case !read.Listed():
		msg = read.Problem + " Approve to wake without comparing them, or don't wake."
	case read.First:
		msg = read.Problem + " Approve to wake and make this list the one later wakes compare with, or don't wake."
	case consent.Home != "":
		msg = "The agent's user settings changed again since you looked at them. Read the list again, then approve to wake."
	}
	return &Error{Kind: Conflict, Reason: "needs approval", Message: msg, Needs: &Needs{Home: &HomeApproval{Box: read.Box, Digest: read.Digest, Changed: read.Asked, More: read.More, First: read.First, Problem: read.Problem}}}
}

func homeAskedDigest(err error) (string, bool) {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Needs == nil || e.Needs.Home == nil {
		return "", false
	}
	return e.Needs.Home.Digest, true
}

func (d *Dispatcher) homeGate(ctx context.Context, r *run, consent Consent) error {
	d.mu.Lock()
	h, box := d.home, r.box
	d.mu.Unlock()
	if h == nil || h.Store == nil {
		return explained(conflict("no settings record"), "This engine keeps no record of the agents' user settings, so it can't tell whether they changed and doesn't wake the session.")
	}
	reader, _ := d.hostFor(box).(HomeReader)
	rctx, cancel := context.WithTimeout(ctx, homeSettingsFor)
	defer cancel()
	read := h.Read(rctx, box, reader)
	log := shared.For(ctx, d.cfg.Logger)
	if !read.Listed() {
		if h.takeOnce(box, consent.Home) {
			d.homeAudit(ctx, r, ResultApproved, consent.Home, "the developer woke the session without a comparison of the agent's user settings, which couldn't be read")
			log.Info("agent settings report", "event", "approved-unread")
			return nil
		}
		read.Digest = h.makeOnce(box)
		log.Info("agent settings report", "event", "needs-approval", "listed", false)
		return homeNeeds(read, consent)
	}
	d.dropOnce(box)
	if !read.Asks() {
		if err := h.Accept(read); err != nil {
			log.Warn("agent settings report", "event", "baseline-not-saved")
		}
		return nil
	}
	if consent.Home != "" && consent.Home == read.Digest {
		if err := h.Accept(read); err != nil {
			log.Warn("agent settings report", "event", "baseline-not-saved")
		}
		n := read.Total
		reason := "the developer approved " + strconv.Itoa(n) + " changed or uncompared items in the agent's user settings before a wake"
		if read.First {
			reason = "the developer approved the agent's user settings before a wake with no earlier list to compare them with"
		}
		d.homeAudit(ctx, r, ResultApproved, read.Digest, reason)
		log.Info("agent settings report", "event", "approved")
		return nil
	}
	log.Info("agent settings report", "event", "needs-approval", "listed", true, "items", read.Total)
	return homeNeeds(read, consent)
}

func (d *Dispatcher) homeAudit(ctx context.Context, r *run, result, digest, reason string) {
	if d.cfg.Audit == nil {
		return
	}
	d.mu.Lock()
	e := AuditEntry{Time: d.reg.now(), Session: r.sid, Repository: r.repo, Branch: r.branch, Box: r.box, Agent: r.spec.ID, By: ByPage, Kind: AuditHomeSettings,
		Result: result, Hash: digest, Reason: reason, Req: shared.RequestOf(ctx)}
	d.mu.Unlock()
	if err := d.cfg.Audit.Append(e); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("agent settings report", "event", "audit-not-written")
	}
}

func (d *Dispatcher) homeBaseline(ctx context.Context, r *run) {
	d.mu.Lock()
	h, box := d.home, r.box
	d.mu.Unlock()
	if h == nil || h.Store == nil {
		return
	}
	reader, _ := d.hostFor(box).(HomeReader)
	rctx, cancel := context.WithTimeout(ctx, homeSettingsFor)
	defer cancel()
	read := h.Read(rctx, box, reader)
	if !read.Listed() {
		h.remember(box, HomeView{First: true, Problem: read.Problem})
		return
	}
	if err := h.Accept(read); err != nil {
		shared.For(ctx, d.cfg.Logger).Warn("agent settings report", "event", "baseline-not-saved")
	}
}

func (d *Dispatcher) homeAsking(box string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.box == box && (r.homeAsk != "" || r.homeGated) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) boxLock(box string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.boxRuns[box]
	if m == nil {
		m = &sync.Mutex{}
		d.boxRuns[box] = m
	}
	return m
}

func (d *Dispatcher) gateBox(r *run) {
	m := d.boxLock(r.box)
	m.Lock()
	defer m.Unlock()
	d.mu.Lock()
	r.homeGated = true
	d.mu.Unlock()
}

func (d *Dispatcher) ungateBox(r *run) {
	d.mu.Lock()
	r.homeGated = false
	d.mu.Unlock()
}

func (d *Dispatcher) boxHeld(box string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.box == box && (r.homeGated || r.homeAsk != "" || r.busy && !r.running) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) dropOnce(box string) {
	d.mu.Lock()
	h := d.home
	d.mu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.once, box)
	h.mu.Unlock()
}

func (d *Dispatcher) homeUnapproved(ctx context.Context, r *run) bool {
	d.mu.Lock()
	h := d.home
	d.mu.Unlock()
	if h == nil || h.Store == nil {
		return true
	}
	reader, _ := d.hostFor(r.box).(HomeReader)
	rctx, cancel := context.WithTimeout(ctx, homeSettingsFor)
	defer cancel()
	unapproved := h.Read(rctx, r.box, reader).Asks()
	if unapproved {
		shared.For(ctx, d.cfg.Logger).Info("agent settings report", "event", "delete-without-claude")
	}
	return unapproved
}

func (d *Dispatcher) homeCleared(box string) *run {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.box == box && r.homeClear && !r.running && !r.waitAt.IsZero() && d.reg.now().Sub(r.waitAt) < PendingBoxFor {
			return r
		}
	}
	return nil
}

func (d *Dispatcher) homeAfterKey(ctx context.Context, box string, step func() error) error {
	r := d.homeCleared(box)
	d.mu.Lock()
	h := d.home
	d.mu.Unlock()
	if r == nil || h == nil || h.Store == nil {
		return step()
	}
	reader, _ := d.hostFor(box).(HomeReader)
	rctx, cancel := context.WithTimeout(ctx, homeSettingsFor)
	defer cancel()
	before := h.Read(rctx, box, reader)
	if err := step(); err != nil || !before.Listed() || before.Asks() || d.homeCleared(box) != r {
		return err
	}
	after := h.Read(rctx, box, reader)
	if after.Listed() && d.homeCleared(box) == r {
		if err := h.Accept(after); err != nil {
			shared.For(ctx, d.cfg.Logger).Warn("agent settings report", "event", "baseline-not-saved")
		} else {
			shared.For(ctx, d.cfg.Logger).Info("agent settings report", "event", "key-step-taken")
		}
	}
	return nil
}

func (d *Dispatcher) RefuseWake(ctx context.Context, sid string) error {
	r, err := d.claim(sid)
	if err != nil {
		return err
	}
	defer d.release(r)
	d.mu.Lock()
	digest := r.homeAsk
	asking := digest != "" && !r.running
	if asking {
		r.homeAsk, r.homeClear, r.waitAt = "", false, time.Time{}
	}
	d.mu.Unlock()
	if !asking {
		return explained(conflict("not asking"), "This session's wake isn't waiting for an answer about the agent's settings.")
	}
	d.dropOnce(r.box)
	if d.liveBox(r.box) {
		d.stopBox(ctx, r.box)
	}
	if !d.liveBox(r.box) {
		d.ungateBox(r)
	}
	d.reg.WaitSignIn(sid, false)
	d.reg.HoldHome(sid, true)
	d.homeAudit(ctx, r, ResultRefused, digest, "the developer didn't wake the session after the agent's user settings changed; it stays stopped")
	shared.For(ctx, d.cfg.Logger).Info("agent settings report", "event", "refused")
	return nil
}
