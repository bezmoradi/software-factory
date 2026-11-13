package session

import (
	"context"
	"strconv"
	"strings"
)

func (f *Forges) Reconcile(ctx context.Context, open AuditOpen) int {
	if open == nil || f.cfg.Audit == nil {
		return 0
	}
	list, err := open.Open()
	if err != nil {
		f.cfg.Logger.Warn("forge reconcile", "event", "audit-unreadable")
		return 0
	}
	done := 0
	for _, e := range list {
		if e.Kind != string(ForgeIssue) && e.Kind != string(ForgePull) || f.live(e.Request) {
			continue
		}
		var ok bool
		switch e.Result {
		case ForgeHeld:
			ok = f.abandon(e)
		case ResultSending:
			ok = f.resend(ctx, e)
		}
		if ok {
			done++
		}
	}
	if done > 0 {
		f.cfg.Logger.Info("forge reconcile", "event", "settled", "count", done)
	}
	return done
}

func titleOf(reason string) string {
	if _, title, ok := strings.Cut(reason, ": "); ok {
		return title
	}
	return ""
}

func wordOf(kind string) string {
	return kindWord(ForgeKind(kind))
}

func (f *Forges) settled(e AuditEntry, result, reason string) bool {
	out := e
	out.Time, out.By, out.Result, out.Reason = f.cfg.Now(), ByStart, result, clip(reason, maxForgeReason)
	if err := f.cfg.Audit.Append(out); err != nil {
		f.cfg.Logger.Warn("forge reconcile", "event", "audit-not-written")
		return false
	}
	return true
}

func (f *Forges) live(request string) bool {
	if request == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range f.cards {
		for _, x := range list {
			if x.view.Card == request {
				return true
			}
		}
	}
	return false
}

func (f *Forges) known(sid string) bool {
	_, ok := f.cfg.Targets(sid)
	return ok
}

func (f *Forges) abandon(e AuditEntry) bool {
	word, title := wordOf(e.Kind), titleOf(e.Reason)
	if !f.settled(e, ResultAbandoned, "the engine restarted before the person answered the request for the "+word+" \""+title+"\", so the request was dropped") {
		return false
	}
	if f.known(e.Session) {
		f.note(e.Session, "the engine restarted before the person answered your request for the "+word+" \""+clip(title, 200)+"\", so it was dropped and nothing was opened. Ask again if it's still needed.")
	}
	return true
}

func (f *Forges) repoOf(e AuditEntry) (ForgeRepo, bool) {
	raw, token, _, err := f.cfg.Repos.ForgeTarget(e.Repository)
	if err != nil || token == "" {
		return ForgeRepo{}, false
	}
	host, owner, name, ok := ForgeRepoOf(raw)
	if !ok || !f.cfg.API.Supports(host) {
		return ForgeRepo{}, false
	}
	r := ForgeRepo{Host: host, Owner: owner, Name: name, Token: token}
	return r, host+"/"+r.Slug() == e.Remote
}

func (f *Forges) resend(ctx context.Context, e AuditEntry) bool {
	repo, ok := f.repoOf(e)
	if !ok {
		f.cfg.Logger.Warn("forge reconcile", "event", "skipped", "why", "repository")
		return false
	}
	word, title := wordOf(e.Kind), titleOf(e.Reason)
	rctx, cancel := context.WithTimeout(ctx, ReconcileFor)
	defer cancel()
	if e.Kind == string(ForgePull) {
		link, open, err := f.cfg.API.OpenPull(rctx, repo, e.Branch)
		if err != nil {
			f.cfg.Logger.Warn("forge reconcile", "event", "host-unreachable")
			return false
		}
		if open {
			if !f.settled(e, ForgeOpened, "pull request #"+strconv.Itoa(link.Number)+" for "+e.Branch+" is open on the git host, so the engine's call before it stopped went through: "+link.URL) {
				return false
			}
			f.tell(e, "the pull request \""+clip(title, 200)+"\" the person approved was opened before the engine restarted: #"+strconv.Itoa(link.Number)+" "+link.URL)
			return true
		}
		if !f.settled(e, ForgeFailed, "no pull request for "+e.Branch+" is open on the git host, so the engine's call didn't go through before it stopped") {
			return false
		}
		f.tell(e, "the pull request \""+clip(title, 200)+"\" the person approved wasn't opened before the engine restarted. Ask again if it's still needed.")
		return true
	}
	issues, err := f.cfg.API.RecentIssues(rctx, repo)
	if err != nil {
		f.cfg.Logger.Warn("forge reconcile", "event", "host-unreachable")
		return false
	}
	matches := matchesOf(issues, e.Time, e.Hash)
	if len(matches) == 0 {
		if !f.settled(e, ForgeFailed, "no issue was opened on the git host since the engine sent it, so the call didn't go through before the engine stopped") {
			return false
		}
		f.tell(e, "the issue \""+clip(title, 200)+"\" the person approved wasn't opened before the engine restarted. Ask again if it's still needed.")
		return true
	}
	v := ForgeView{Card: e.Request, Kind: ForgeIssue, State: ForgeUnclear, Title: title, Host: repo.Host, Repo: repo.Slug(), Asked: e.Time, At: f.cfg.Now(), Matches: matches,
		Reason: unclearReason(word, "the engine stopped while it was opening it", matches)}
	if !f.settled(e, ResultUnclear, v.Reason) {
		return false
	}
	if f.known(e.Session) {
		f.mu.Lock()
		f.cards[e.Session] = append(f.cards[e.Session], &forgeCard{view: v, repo: repo, sid: e.Session})
		f.trim(e.Session)
		f.mu.Unlock()
		f.show(e.Session)
		f.tell(e, "the person approved the issue \""+clip(title, 200)+"\", but the engine stopped while it was opening it, so it can't tell whether the issue exists. The person checks on the page; don't ask for it again.")
	}
	return true
}

func (f *Forges) tell(e AuditEntry, text string) {
	if f.known(e.Session) {
		f.note(e.Session, text)
	}
}
