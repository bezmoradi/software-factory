package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	forgeStopping = "the engine is stopping, so it takes no request now; end your turn and ask again once it's back"
	maxMatches    = 5
	matchSlack    = time.Minute
)

type IssueFound struct {
	Number      int
	URL         string
	Title       string
	Body        string
	Created     time.Time
	Unconfirmed bool
}

type ForgeMatch struct {
	Number  int
	URL     string
	Title   string
	Created time.Time
	Exact   bool
}

var (
	errNotLogged = &ForgeError{Message: "the engine couldn't write its audit log, so it sent nothing to the git host"}
	errSettled   = errors.New("settled")
)

func (f *Forges) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func ForgeHash(title, body string) string {
	sum := sha256.Sum256([]byte(title + "\n" + body))
	return hex.EncodeToString(sum[:])
}

func forgeUnclear(err error) bool {
	if err == nil || errors.Is(err, errNotLogged) {
		return false
	}
	fe, ok := errors.AsType[*ForgeError](err)
	return !ok || fe.Status == 0 || fe.Status >= 500
}

func (f *Forges) sending(t PushTarget, v ForgeView) error {
	if f.cfg.Audit == nil {
		return nil
	}
	e := AuditEntry{Time: f.cfg.Now(), Session: t.SID, Repository: t.Name, Branch: t.Branch, Remote: v.Host + "/" + v.Repo, By: ByPage, Result: ResultSending,
		Reason: clip("opening "+kindWord(v.Kind)+": "+v.Title, maxForgeReason), Kind: string(v.Kind), Request: v.Card, Hash: ForgeHash(v.Title, v.Body)}
	if err := f.cfg.Audit.Append(e); err != nil {
		f.cfg.Logger.Warn("forge", "event", "audit-not-written")
		return errNotLogged
	}
	return nil
}

func (f *Forges) expired(c *forgeCard) {
	t, _ := f.cfg.Targets(c.sid)
	f.audit(c.sid, t, c.view, ByEngine, ResultExpired, "the card for "+kindWord(c.view.Kind)+" \""+clip(c.view.Title, 200)+"\" expired after 24 hours without an answer")
}

func (f *Forges) recheck(c *forgeCard, t PushTarget, v ForgeView, cause error) (ForgeLink, string, error) {
	ctx, cancel := context.WithTimeout(f.cfg.Base, ReconcileFor)
	defer cancel()
	why, hash := forgeWhy(cause, NeedIssues), ForgeHash(v.Title, v.Body)
	if v.Kind == ForgePull {
		link, open, err := f.cfg.API.OpenPull(ctx, c.repo, v.Head)
		switch {
		case err == nil && open:
			return link, ForgeOpened, nil
		case err == nil:
			return ForgeLink{}, ForgeOpened, cause
		}
		f.markUnclear(c, t, nil, why, hash)
		return ForgeLink{}, "", errSettled
	}
	issues, err := f.cfg.API.RecentIssues(ctx, c.repo)
	if err != nil {
		f.markUnclear(c, t, nil, why, hash)
		return ForgeLink{}, "", errSettled
	}
	matches := matchesOf(issues, v.At, hash)
	if len(matches) == 0 {
		return ForgeLink{}, ForgeOpened, cause
	}
	f.markUnclear(c, t, matches, why, hash)
	return ForgeLink{}, "", errSettled
}

func matchesOf(issues []IssueFound, since time.Time, hash string) []ForgeMatch {
	var out []ForgeMatch
	for _, i := range issues {
		if i.Number <= 0 || i.Created.Before(since.Add(-matchSlack)) || len(out) >= maxMatches {
			continue
		}
		out = append(out, ForgeMatch{Number: i.Number, URL: i.URL, Title: CleanLine(i.Title, MaxForgeTitle), Created: i.Created, Exact: !i.Unconfirmed && ForgeHash(i.Title, i.Body) == hash})
	}
	return out
}

func unclearReason(word, why string, matches []ForgeMatch) string {
	r := "the engine can't tell whether the git host opened the " + word + ": " + why
	switch {
	case len(matches) == 0 && word == "pull request":
		r += "; check the repository's pull requests before asking again"
	case len(matches) == 0:
		r += "; the engine couldn't list the issues opened since, so check the repository before asking again"
	default:
		r += "; these issues were opened since you approved it, so check them before asking again"
	}
	return r
}

func (f *Forges) markUnclear(c *forgeCard, t PushTarget, matches []ForgeMatch, why, hash string) {
	f.mu.Lock()
	word := kindWord(c.view.Kind)
	c.view.State, c.view.Body, c.view.At, c.view.Matches = ForgeUnclear, "", f.cfg.Now(), matches
	c.view.Reason = f.clean(c.repo.Token, unclearReason(word, why, matches))
	v := c.view
	f.mu.Unlock()
	f.show(c.sid)
	f.auditUnclear(c.sid, t, v, hash)
	f.note(c.sid, "the person approved the "+word+" \""+clip(v.Title, 200)+"\", but the engine can't tell whether the git host opened it. The person checks on the page; don't ask for it again.")
	f.cfg.Logger.Warn("forge", "event", "unclear", "kind", string(v.Kind), "matches", len(matches))
}

func (f *Forges) auditUnclear(sid string, t PushTarget, v ForgeView, hash string) {
	reason := v.Reason
	if len(v.Matches) > 0 {
		nums := make([]string, 0, len(v.Matches))
		for _, m := range v.Matches {
			nums = append(nums, "#"+strconv.Itoa(m.Number))
		}
		reason += " (" + strings.Join(nums, ", ") + ")"
	}
	if f.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: f.cfg.Now(), Session: sid, Repository: t.Name, Branch: t.Branch, Remote: v.Host + "/" + v.Repo, By: ByPage, Result: ResultUnclear,
		Reason: clip(reason, maxForgeReason), Kind: string(v.Kind), Request: v.Card, Hash: hash}
	if err := f.cfg.Audit.Append(e); err != nil {
		f.cfg.Logger.Warn("forge", "event", "audit-not-written")
	}
}
