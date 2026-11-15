package session

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const maxSavedCards = 1000

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type SavedForgeCard struct {
	SID    string
	Card   string
	Kind   ForgeKind
	Title  string
	Body   string
	Head   string
	Base   string
	Commit string
	Asked  time.Time
	From   string
}

type ForgeCardStore interface {
	Load() ([]SavedForgeCard, error)
	Save([]SavedForgeCard) error
}

type cardSaves struct {
	mu      sync.Mutex
	version uint64
	written uint64
	last    []SavedForgeCard
	ok      bool
}

func storable(c *forgeCard) bool {
	v := c.view
	if v.State != ForgeWaiting || (v.Kind != ForgeIssue && v.Kind != ForgePull) || !cardID.MatchString(v.Card) {
		return false
	}
	if _, why := ForgeText(v.Title, MaxForgeTitle, false); why != "" {
		return false
	}
	if _, why := ForgeText(v.Body, MaxForgeBody, true); why != "" {
		return false
	}
	where, _ := ForgeSecret(v.Title, v.Body)
	return where == ""
}

func (f *Forges) saveCards() {
	if f.cfg.Cards == nil {
		return
	}
	f.mu.Lock()
	f.saved.mu.Lock()
	f.saved.version++
	version := f.saved.version
	f.saved.mu.Unlock()
	var out []SavedForgeCard
	for sid, list := range f.cards {
		for _, c := range list {
			if len(out) < maxSavedCards && storable(c) {
				v := c.view
				out = append(out, SavedForgeCard{SID: sid, Card: v.Card, Kind: v.Kind, Title: v.Title, Body: v.Body, Head: v.Head, Base: v.Base, Commit: v.Commit, Asked: v.Asked, From: v.From})
			}
		}
	}
	f.mu.Unlock()
	slices.SortFunc(out, func(a, b SavedForgeCard) int { return a.Asked.Compare(b.Asked) })
	f.saved.mu.Lock()
	defer f.saved.mu.Unlock()
	if version <= f.saved.written {
		return
	}
	f.saved.written = version
	if f.saved.ok && slices.Equal(out, f.saved.last) {
		return
	}
	f.saved.last, f.saved.ok = out, true
	if err := f.cfg.Cards.Save(out); err != nil {
		f.saved.ok = false
		f.cfg.Logger.Warn("forge", "event", "cards-not-saved")
	}
}

func (f *Forges) Restore(open AuditOpen) int {
	if f.cfg.Cards == nil {
		return 0
	}
	saved, err := f.cfg.Cards.Load()
	if err != nil {
		f.cfg.Logger.Warn("forge", "event", "cards-not-read")
		return 0
	}
	held := map[string]bool{}
	if open != nil {
		if list, err := open.Open(); err == nil {
			for _, e := range list {
				if (e.Kind == string(ForgeIssue) || e.Kind == string(ForgePull)) && e.Result == ForgeHeld && e.Request != "" {
					held[e.Request] = true
				}
			}
		}
	}
	now := f.cfg.Now()
	shown := map[string]bool{}
	restored := 0
	for _, s := range saved {
		if !held[s.Card] || !sidPattern.MatchString(s.SID) || f.live(s.Card) {
			continue
		}
		need := NeedIssues
		if s.Kind == ForgePull {
			need = NeedPulls
		}
		ft, why := f.target(s.SID, need)
		if why != "" || (s.Kind == ForgePull && (s.Head != ft.t.Branch || s.Base == "")) {
			continue
		}
		v := ForgeView{Card: s.Card, Kind: s.Kind, State: ForgeWaiting, Title: s.Title, Body: s.Body, Host: ft.repo.Host, Repo: ft.repo.Slug(), Asked: s.Asked, At: now, From: s.From}
		if s.Kind == ForgePull {
			v.Head, v.Base = s.Head, s.Base
			if commitSHA.MatchString(s.Commit) {
				v.Commit = s.Commit
			}
		}
		f.mu.Lock()
		if len(f.cards[s.SID]) >= maxForgeKept*2 {
			f.mu.Unlock()
			continue
		}
		f.cards[s.SID] = append(f.cards[s.SID], &forgeCard{view: v, repo: ft.repo, sid: s.SID})
		f.trim(s.SID)
		f.mu.Unlock()
		shown[s.SID] = true
		restored++
	}
	for sid := range shown {
		f.show(sid)
	}
	if len(shown) == 0 {
		f.saveCards()
	}
	if restored > 0 {
		f.cfg.Logger.Info("forge", "event", "cards-restored", "count", restored)
	}
	return restored
}

func (f *Forges) moved(ctx context.Context, t PushTarget, c *forgeCard) bool {
	f.mu.Lock()
	v, repo := c.view, c.repo
	f.mu.Unlock()
	if v.Commit == "" {
		return false
	}
	head, err := f.cfg.API.Branch(ctx, repo, v.Head)
	if err == nil && head == v.Commit {
		return false
	}
	if err != nil || !commitSHA.MatchString(head) {
		reason := "the engine couldn't read the branch on the host to check that it is still at the commit you approved, so it opened nothing; press the button again to try once more"
		f.mu.Lock()
		c.view.State, c.view.Reason, c.view.At = ForgeWaiting, reason, f.cfg.Now()
		v = c.view
		f.mu.Unlock()
		f.show(c.sid)
		f.audit(ctx, c.sid, t, v, ByEngine, ForgeHeld, "kept the pull request card waiting, because "+reason+": "+v.Title)
		shared.For(ctx, f.cfg.Logger).Info("forge", "event", "branch-unread")
		return true
	}
	reason := movedReason(ctx, f.cfg.Commits, t.Repo, v.Commit, head)
	f.mu.Lock()
	c.view.State, c.view.Commit, c.view.Reason, c.view.Draft, c.view.At = ForgeWaiting, head, reason, false, f.cfg.Now()
	v = c.view
	f.mu.Unlock()
	f.show(c.sid)
	f.audit(ctx, c.sid, t, v, ByEngine, ForgeHeld, "asked again before opening the pull request, because "+reason+": "+v.Title)
	shared.For(ctx, f.cfg.Logger).Info("forge", "event", "branch-moved")
	return true
}

func movedReason(ctx context.Context, commits ChangeReader, repo, from, to string) string {
	tail := "; read it again, and approve once more to open it at " + shortSHA7(to)
	if commits == nil {
		return "the branch moved since this was written" + tail
	}
	set, err := commits.Changes(ctx, repo, from, to)
	if err != nil {
		return "the branch moved since this was written, to a commit the engine can't compare with the one it was written for" + tail
	}
	n := len(set.Commits)
	switch {
	case set.More:
		return "the branch has more than " + strconv.Itoa(n) + " new commits since this was written" + tail
	case n == 1:
		return "the branch has 1 new commit since this was written" + tail
	}
	return "the branch has " + strconv.Itoa(n) + " new commits since this was written" + tail
}
