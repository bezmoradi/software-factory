package session

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	ForgeComment   ForgeKind = "comment"
	ForgePosting             = ResultPosting
	ForgeCommented           = ResultCommented
	PushNoteMarker           = "**Pushed by Software Factory**"
	CommentCalls             = 10
	CommentWindow            = 10 * time.Minute

	noDrafts = "GitHub doesn't offer drafts for this repository, so it opened as ready for review"
)

type PushedRole struct {
	Role         review.Role
	Name         string
	Agent        string
	Recorded     bool
	Passed       bool
	Overridden   bool
	Since        string
	PluginCommit string
	ClaudeCode   string
	Models       []string
}

type PushedFacts struct {
	SID        string
	Repository string
	Branch     string
	Commit     string
	Remote     string
	By         string
	Allowed    bool
	Missing    int
	Roles      []PushedRole
	At         time.Time
}

type forgeOwn struct {
	comments  *limiter
	pushed    map[string]PushedFacts
	commented map[string]string
}

func (f *Forges) owned() *forgeOwn {
	o := &f.own
	if o.pushed == nil {
		o.comments = newLimiter(CommentCalls, CommentWindow)
		o.pushed = map[string]PushedFacts{}
		o.commented = map[string]string{}
	}
	return o
}

func countWhere(list []*forgeCard, match func(*forgeCard) bool) int {
	n := 0
	for _, c := range list {
		if match(c) {
			n++
		}
	}
	return n
}

func finishedCard(c *forgeCard) bool {
	switch c.view.State {
	case ForgeWaiting, ForgeSending, ForgePosting:
		return false
	}
	return true
}

func openedAs(v ForgeView, asked bool) string {
	switch {
	case v.Draft:
		return " as a draft; the person marks it ready for review on GitHub"
	case asked:
		return "; " + noDrafts
	}
	return ""
}

func (f *Forges) AfterPush(p PushedFacts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owned().pushed[p.SID] = p
	if f.closed {
		f.cfg.Logger.Info("forge", "event", "comment-skipped", "why", "stopping")
		return
	}
	f.wg.Go(func() {
		ctx, cancel := context.WithTimeout(f.cfg.Base, ForgeFor)
		defer cancel()
		f.commentPush(ctx, p, 0)
	})
}

func (f *Forges) afterOpened(sid string, repo ForgeRepo, branch string, number int) {
	f.mu.Lock()
	p, ok := f.owned().pushed[sid]
	closed := f.closed
	f.mu.Unlock()
	if closed || !ok || p.Branch != branch || !f.commentsOn(p.Repository) {
		return
	}
	f.wg.Go(func() {
		ctx, cancel := context.WithTimeout(f.cfg.Base, ForgeFor)
		defer cancel()
		head, err := f.cfg.API.Branch(ctx, repo, branch)
		if err != nil || head != p.Commit {
			return
		}
		f.commentPush(ctx, p, number)
	})
}

func (f *Forges) commentsOn(name string) bool {
	return f.cfg.PRComments != nil && f.cfg.PRComments(name)
}

func (f *Forges) commentPush(ctx context.Context, p PushedFacts, number int) {
	if !f.commentsOn(p.Repository) {
		return
	}
	ft, why := f.target(p.SID, NeedPulls)
	if why != "" || ft.t.Branch != p.Branch {
		f.cfg.Logger.Info("forge", "event", "comment-skipped", "why", "target")
		return
	}
	if number == 0 {
		link, open, err := f.cfg.API.OpenPull(ctx, ft.repo, p.Branch)
		if err != nil {
			f.cfg.Logger.Warn("forge", "event", "comment-skipped", "why", "pulls-unread")
			return
		}
		if !open {
			return
		}
		number = link.Number
	}
	body := CommentText(p, f.cfg.Version)
	now := f.cfg.Now()
	v := ForgeView{Card: randomHex(16), Kind: ForgeComment, State: ForgePosting, Title: "Comment on pull request #" + strconv.Itoa(number), Body: body,
		Head: p.Branch, Host: ft.repo.Host, Repo: ft.repo.Slug(), Number: number, Asked: now, At: now}
	f.mu.Lock()
	o := f.owned()
	if o.commented[p.SID] == p.Commit {
		f.mu.Unlock()
		return
	}
	o.commented[p.SID] = p.Commit
	c := &forgeCard{view: v, repo: ft.repo, sid: p.SID}
	f.cards[p.SID] = append(f.cards[p.SID], c)
	f.trim(p.SID)
	f.mu.Unlock()
	f.show(p.SID)
	request := randomHex(16)
	f.commentAudit(p, v, request, ForgePosting, "commenting on pull request #"+strconv.Itoa(number)+" about commit "+shortSHA(p.Commit))
	link, err := f.cfg.API.Comment(ctx, ft.repo, number, body)
	f.mu.Lock()
	c.view.At = f.cfg.Now()
	if err != nil {
		c.view.State, c.view.Reason = ForgeFailed, f.clean(ft.repo.Token, forgeWhy(err, NeedPulls))
	} else {
		c.view.State, c.view.URL = ForgeCommented, link.URL
	}
	v = c.view
	f.mu.Unlock()
	f.show(p.SID)
	if err != nil {
		f.commentAudit(p, v, request, ForgeFailed, "couldn't comment on pull request #"+strconv.Itoa(number)+": "+v.Reason)
		f.cfg.Logger.Warn("forge", "event", "comment-failed")
		return
	}
	f.commentAudit(p, v, request, ForgeCommented, "commented on pull request #"+strconv.Itoa(number)+" about commit "+shortSHA(p.Commit)+": "+v.URL)
	f.cfg.Logger.Info("forge", "event", "commented")
}

func (f *Forges) commentAudit(p PushedFacts, v ForgeView, request, result, reason string) {
	if f.cfg.Audit == nil {
		return
	}
	e := AuditEntry{Time: f.cfg.Now(), Session: p.SID, Repository: p.Repository, Branch: p.Branch, Commit: p.Commit, Remote: v.Host + "/" + v.Repo,
		By: ByEngine, Result: result, Reason: clip(reason, maxForgeReason), Kind: AuditPRComment, Request: request}
	for _, r := range p.Roles {
		if r.PluginCommit != "" && e.PluginCommit == "" {
			e.PluginCommit, e.ClaudeCode, e.Models = r.PluginCommit, r.ClaudeCode, append([]string(nil), r.Models...)
		}
	}
	if err := f.cfg.Audit.Append(e); err != nil {
		f.cfg.Logger.Warn("forge", "event", "audit-not-written")
	}
}

func plainCode(s string) string {
	return "`" + strings.ReplaceAll(CleanLine(s, 200), "`", "") + "`"
}

func pushedBy(p PushedFacts) string {
	switch {
	case p.By == ByAgent:
		return "the agent asked with factory_push, and every required review had passed this commit"
	case p.By == ByReviews:
		return "the agent asked with factory_push, and the engine pushed once the reviews it started had passed"
	case p.By == ByPage && p.Allowed && p.Missing > 0:
		return "the person allowed it on Software Factory's page with " + strconv.Itoa(p.Missing) + " " + plural(p.Missing, "item", "items") + " missing; the reasons are in the engine's audit"
	case p.By == ByPage && p.Allowed:
		return "the person allowed it on Software Factory's page"
	case p.By == ByPage:
		return "the person pressed Push on Software Factory's page"
	}
	return "Software Factory's engine (" + CleanLine(p.By, 60) + ")"
}

func roleLine(r PushedRole) string {
	state := "no review recorded"
	switch {
	case r.Passed:
		state = "passed"
	case r.Overridden:
		state = "blocked, and the person overrode it for this commit"
	case r.Recorded:
		state = "not passed"
	}
	scope := "reviewed the whole branch from where it leaves the main branch"
	if r.Since != "" {
		scope = "reviewed only what changed since " + plainCode(r.Since)
	}
	if !r.Recorded {
		scope = ""
	}
	line := "  - " + CleanLine(r.Name, 80) + " (" + plainCode(r.Agent) + "): " + state
	if scope != "" {
		line += "; " + scope
	}
	return line + "."
}

func CommentText(p PushedFacts, version string) string {
	var b strings.Builder
	line := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	line(PushNoteMarker)
	line("")
	line("Software Factory pushed commit " + plainCode(p.Commit) + " to " + plainCode(p.Branch) + ".")
	line("")
	line("- **How:** " + pushedBy(p) + ".")
	if len(p.Roles) > 0 {
		line("- **Reviews of this commit:**")
		for _, r := range p.Roles {
			line(roleLine(r))
		}
	} else {
		line("- **Reviews of this commit:** none required.")
	}
	versions := []string{}
	if version != "" {
		versions = append(versions, "Software Factory "+plainCode(version))
	}
	plugin, claude, models := "", "", []string{}
	for _, r := range p.Roles {
		if plugin == "" && r.PluginCommit != "" {
			plugin = r.PluginCommit
		}
		if claude == "" && r.ClaudeCode != "" {
			claude = r.ClaudeCode
		}
		for _, m := range r.Models {
			if len(models) < 4 && !slices.Contains(models, plainCode(m)) {
				models = append(models, plainCode(m))
			}
		}
	}
	if plugin != "" {
		versions = append(versions, "knackpack "+plainCode(plugin))
	}
	if claude != "" {
		versions = append(versions, "Claude Code "+plainCode(claude))
	}
	if len(models) > 0 {
		versions = append(versions, plural(len(models), "model ", "models ")+strings.Join(models, ", "))
	}
	if len(versions) > 0 {
		line("- **Versions:** " + strings.Join(versions, ", ") + ".")
	}
	line("")
	line("Written by Software Factory from its own records: it holds no text from the agent, the reviewers or the person.")
	return strings.TrimRight(b.String(), "\n")
}
