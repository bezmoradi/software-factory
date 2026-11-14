package session

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

const (
	ForgeFromAgent    = ""
	ForgeFromPage     = "page"
	ForgeFromFindings = "findings"
	maxPullCommits    = 50
)

var closesLine = regexp.MustCompile(`(?im)^\s*(close[sd]?|fix(e[sd])?|resolve[sd]?)\s+#(\d+)\b`)

func (f *Forges) useFollows(fl *Follows) {
	f.mu.Lock()
	f.follows = fl
	f.mu.Unlock()
}

func (f *Forges) followsOf() *Follows {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.follows
}

func withCloses(body string, issue int) string {
	if issue <= 0 {
		return body
	}
	for _, m := range closesLine.FindAllStringSubmatch(body, -1) {
		if m[3] == strconv.Itoa(issue) {
			return body
		}
	}
	line := "Closes #" + strconv.Itoa(issue)
	if strings.TrimSpace(body) == "" {
		return line
	}
	return strings.TrimRight(body, "\n") + "\n\n" + line
}

func (f *Forges) issueBody(sid string, kind ForgeKind, body string) string {
	fl := f.followsOf()
	if kind != ForgePull || fl == nil {
		return body
	}
	return withCloses(body, fl.IssueOf(sid))
}

func (f *Forges) PageRequest(ctx context.Context, sid string, kind ForgeKind, title, body, from string) error {
	now := f.cfg.Now()
	need := NeedIssues
	if kind == ForgePull {
		need = NeedPulls
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return explained(invalid("title"), "The title is empty.")
	}
	if _, why := ForgeText(title, MaxForgeTitle, false); why != "" {
		return explained(invalid("title"), "The title "+why+".")
	}
	body = f.issueBody(sid, kind, body)
	if _, why := ForgeText(body, MaxForgeBody, true); why != "" {
		return explained(invalid("body"), "The body "+why+".")
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return explained(conflict("stopping"), stoppingMessage)
	}
	ft, why := f.target(sid, need)
	if why != "" {
		return explained(conflict("target"), strings.ToUpper(why[:1])+why[1:]+".")
	}
	ctx, cancel := context.WithTimeout(ctx, ForgeFor)
	defer cancel()
	v := ForgeView{Kind: kind, State: ForgeWaiting, Title: title, Body: body, Host: ft.repo.Host, Repo: ft.repo.Slug(), Asked: now, At: now, From: from}
	if kind == ForgePull {
		if ft.main == "" {
			return explained(conflict("main"), "The engine doesn't know the repository's main branch yet.")
		}
		v.Head, v.Base = ft.t.Branch, ft.main
		head, err := f.cfg.API.Branch(ctx, ft.repo, ft.t.Branch)
		if err != nil {
			if errors.Is(err, ErrNoBranch) {
				return explained(conflict("not pushed"), "The branch "+ft.t.Branch+" isn't on "+ft.repo.Host+" yet. Push it first.")
			}
			return explained(conflict("host"), f.clean(ft.repo.Token, "The engine couldn't read the branch on the git host: "+forgeWhy(err, NeedContents)+"."))
		}
		if commitSHA.MatchString(head) {
			v.Commit = head
		}
		link, open, err := f.cfg.API.OpenPull(ctx, ft.repo, ft.t.Branch)
		if err != nil {
			return explained(conflict("host"), f.clean(ft.repo.Token, "The engine couldn't list the open pull requests: "+forgeWhy(err, NeedPulls)+"."))
		}
		if open {
			return explained(conflict("exists"), "Pull request #"+strconv.Itoa(link.Number)+" for "+ft.t.Branch+" is already open.")
		}
	} else if err := f.cfg.API.Repository(ctx, ft.repo); err != nil {
		return explained(conflict("host"), f.clean(ft.repo.Token, "The engine couldn't read the repository on the git host: "+forgeWhy(err, NeedMetadata)+"."))
	}
	f.mu.Lock()
	list := f.cards[sid]
	waiting := 0
	for _, c := range list {
		if c.view.Kind == ForgeComment || c.view.State != ForgeWaiting && c.view.State != ForgeSending {
			continue
		}
		if c.view.Kind == kind && (kind == ForgePull || c.view.Title == title && c.view.Body == body) {
			f.mu.Unlock()
			return explained(conflict("waiting"), "The same "+kindWord(kind)+" already waits on its card.")
		}
		waiting++
	}
	if waiting >= maxForgeWait {
		f.mu.Unlock()
		return explained(conflict("full"), strconv.Itoa(maxForgeWait)+" issues and pull requests already wait on their cards. Answer them first.")
	}
	v.Card = randomHex(16)
	f.cards[sid] = append(list, &forgeCard{view: v, repo: ft.repo, sid: sid})
	f.trim(sid)
	f.mu.Unlock()
	f.show(sid)
	f.audit(sid, ft.t, v, ByPage, ForgeHeld, "asked on the page to open "+kindWord(kind)+": "+title)
	f.cfg.Logger.Info("forge", "event", "held", "kind", string(kind), "from", from)
	return nil
}

func requirementsTitle(text string) string {
	for l := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		for _, p := range []string{"Requirements brief:", "Requirements:", "Requirements brief -", "Requirements -"} {
			if strings.HasPrefix(strings.ToLower(t), strings.ToLower(p)) {
				t = strings.TrimSpace(t[len(p):])
			}
		}
		if t = CleanLine(t, MaxForgeTitle); t != "" {
			return t
		}
	}
	return ""
}

func subject(message string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return CleanLine(first, 200)
}

func (fl *Follows) OpenPull(ctx context.Context, sid string) error {
	f := fl.cfg.Forges
	t, ok := f.cfg.Targets(sid)
	if !ok {
		return explained(conflict("not started here"), "Only a session this engine started can open a pull request from the page.")
	}
	_, _, mainName, err := f.cfg.Repos.ForgeTarget(t.Name)
	if err != nil {
		return explained(conflict("repository"), "The session's repository isn't in the engine's list any more.")
	}
	ft, why := f.target(sid, NeedPulls)
	if why != "" {
		return explained(conflict("target"), strings.ToUpper(why[:1])+why[1:]+".")
	}
	rctx, cancel := context.WithTimeout(ctx, ForgeFor)
	head, err := f.cfg.API.Branch(rctx, ft.repo, t.Branch)
	cancel()
	if err != nil {
		if errors.Is(err, ErrNoBranch) {
			return explained(conflict("not pushed"), "The branch "+t.Branch+" isn't on "+ft.repo.Host+" yet. Push it first.")
		}
		return explained(conflict("host"), f.clean(ft.repo.Token, "The engine couldn't read the branch on the git host: "+forgeWhy(err, NeedContents)+"."))
	}
	title := ""
	if fl.cfg.Approved != nil {
		title = requirementsTitle(fl.cfg.Approved(sid))
	}
	var commits []string
	more := false
	base := ""
	if fl.cfg.MainCommit != nil && f.cfg.Commits != nil && commitSHA.MatchString(head) {
		if m, err := fl.cfg.MainCommit(t.Name); err == nil && commitSHA.MatchString(m) {
			base = m
			if set, err := f.cfg.Commits.Changes(t.Repo, m, head); err == nil {
				more = set.More || len(set.Commits) > maxPullCommits
				for i, c := range set.Commits {
					if i >= maxPullCommits {
						break
					}
					commits = append(commits, "- `"+shortSHA7(c.SHA)+"` "+strings.ReplaceAll(subject(c.Message), "`", "'"))
				}
				if title == "" && len(set.Commits) == 1 {
					title = subject(set.Commits[0].Message)
				}
			}
		}
	}
	if title == "" {
		title = "Changes from " + CleanLine(t.Branch, 200)
	}
	var b strings.Builder
	if len(commits) > 0 {
		b.WriteString("Commits on `" + CleanLine(t.Branch, 200) + "` since `" + CleanLine(mainName, 200) + "` (`" + shortSHA7(base) + "`):\n\n")
		b.WriteString(strings.Join(commits, "\n"))
		if more {
			b.WriteString("\n- and more")
		}
		b.WriteString("\n\n")
	}
	b.WriteString("Opened from Software Factory's page. The engine wrote this description from its own records: the branch, its commits and the approved requirements' title.")
	return f.PageRequest(ctx, sid, ForgePull, title, b.String(), ForgeFromPage)
}

func (fl *Follows) Ready(ctx context.Context, sid string) error {
	f := fl.cfg.Forges
	ft, why := f.target(sid, NeedPulls)
	if why != "" {
		return explained(conflict("target"), strings.ToUpper(why[:1])+why[1:]+".")
	}
	now := fl.cfg.Now()
	fl.mu.Lock()
	x := fl.list[sid]
	switch {
	case fl.closed:
		fl.mu.Unlock()
		return explained(conflict("stopping"), stoppingMessage)
	case x == nil || x.saved.Number == 0 || x.saved.Done != "":
		fl.mu.Unlock()
		return explained(conflict("not followed"), "The engine follows no open pull request for this session.")
	case !x.saved.Opened:
		fl.mu.Unlock()
		return explained(conflict("not ours"), "Only a pull request the engine opened can be marked ready from the page. Mark this one ready on the git host.")
	case !fl.readies.allow(sid, now):
		fl.mu.Unlock()
		return explained(limited("ready"), "Wait a few minutes before trying again.")
	}
	number := x.saved.Number
	fl.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, ForgeFor)
	defer cancel()
	ps, err := fl.cfg.API.PullStatus(ctx, ft.repo, number)
	if err != nil {
		return explained(conflict("host"), f.clean(ft.repo.Token, "The engine couldn't read pull request #"+strconv.Itoa(number)+": "+forgeWhy(err, NeedPulls)+"."))
	}
	if ps.State != "open" {
		return explained(conflict("closed"), "Pull request #"+strconv.Itoa(number)+" isn't open any more.")
	}
	if !ps.Draft {
		fl.mu.Lock()
		if fl.list[sid] == x {
			x.pull = ps
		}
		fl.mu.Unlock()
		fl.show(sid)
		return explained(conflict("ready"), "Pull request #"+strconv.Itoa(number)+" is already ready for review.")
	}
	request := randomHex(16)
	fl.auditKind(AuditPRReady, sid, ft.t, ft.repo, ByPage, ResultSending, request, "marking pull request #"+strconv.Itoa(number)+" ready for review, as the person asked on the page")
	draft, err := fl.cfg.API.MarkReady(ctx, ft.repo, ps.NodeID)
	if err == nil && draft {
		err = &ForgeError{Message: "GitHub answered, but the pull request is still a draft"}
	}
	if err != nil {
		why := f.clean(ft.repo.Token, forgeWhy(err, NeedPulls))
		fl.auditKind(AuditPRReady, sid, ft.t, ft.repo, ByPage, ForgeFailed, request, "couldn't mark pull request #"+strconv.Itoa(number)+" ready for review: "+why)
		return explained(conflict("host"), "The engine couldn't mark it ready for review: "+why+".")
	}
	fl.mu.Lock()
	if fl.list[sid] == x {
		ps.Draft = false
		x.pull = ps
	}
	fl.mu.Unlock()
	fl.show(sid)
	fl.auditKind(AuditPRReady, sid, ft.t, ft.repo, ByPage, ResultReady, request, "marked pull request #"+strconv.Itoa(number)+" ready for review: "+ps.URL)
	fl.cfg.Note(sid, "The person marked pull request #"+strconv.Itoa(number)+" ready for review on the page: "+CleanLine(ps.URL, 300))
	fl.cfg.Logger.Info("pull follow", "event", "ready")
	return nil
}

func (f *Forges) afterCreated(sid string, t PushTarget, repo ForgeRepo, v ForgeView) {
	fl := f.followsOf()
	if fl == nil {
		return
	}
	if v.Kind == ForgePull {
		fl.opened(sid, v.Number, v.URL)
	}
	if !fl.assignOn(t.Name) || v.Number <= 0 {
		return
	}
	f.wg.Go(func() {
		ctx, cancel := context.WithTimeout(f.cfg.Base, ForgeFor)
		defer cancel()
		word := kindWord(v.Kind)
		request := randomHex(16)
		login, err := fl.login(ctx, repo)
		if err == nil {
			err = fl.cfg.API.AssignTo(ctx, repo, v.Number, login)
		}
		if err != nil {
			why := f.clean(repo.Token, forgeWhy(err, NeedIssues))
			fl.auditKind(AuditAssign, sid, t, repo, ByEngine, ForgeFailed, request, "couldn't assign "+word+" #"+strconv.Itoa(v.Number)+" to the token's own user: "+why)
			f.cfg.Logger.Warn("forge", "event", "assign-failed")
			return
		}
		fl.auditKind(AuditAssign, sid, t, repo, ByEngine, "assigned", request, "assigned "+word+" #"+strconv.Itoa(v.Number)+" to "+CleanLine(login, 100)+", the token's own user, as the project's settings ask")
		f.cfg.Logger.Info("forge", "event", "assigned", "kind", string(v.Kind))
	})
}
