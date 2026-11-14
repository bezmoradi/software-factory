package session

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	MaxStartBranches = 300
	MaxStartPulls    = 100
	MaxStints        = 20
	maxPullTitle     = 200
	OriginMain       = "main"
	OriginHandover   = "hand-over"
)

type StartFrom struct {
	Branch string
	Pull   int
	SID    string
	Commit string
}

func (f StartFrom) chosen() int {
	n := 0
	for _, set := range []bool{f.Branch != "", f.Pull != 0, f.SID != ""} {
		if set {
			n++
		}
	}
	return n
}

type RemoteRef struct {
	Name   string
	Commit string
}

type PullHead struct {
	Number int
	Title  string
	Branch string
	Commit string
}

type HeadFetch struct {
	Name    string
	URL     string
	Token   string
	Branch  string
	Commit  string
	Have    []string
	Present func(sha string) bool
}

type StartRemote interface {
	ListHeads(ctx context.Context, url, token string) ([]RemoteRef, error)
	FetchHead(ctx context.Context, q HeadFetch) (string, error)
}

type PullHeads interface {
	OpenPullHeads(ctx context.Context, r ForgeRepo) ([]PullHead, error)
}

type StartSources interface {
	Branches(ctx context.Context, name string) ([]RemoteRef, error)
	FetchBranch(ctx context.Context, name, branch, commit string) (string, error)
	FetchCommit(ctx context.Context, name, commit string) error
}

type StartPoints struct {
	Main     string
	Branches []RemoteRef
	Pulls    []PullHead
	PullsWhy string
}

type Stint struct {
	Agent string
	SID   string
	Name  string
	From  string
	To    string
}

type Lineage struct {
	Branch   string
	Origin   string
	From     string
	Earlier  []Stint
	HandedTo string
	Last     string
}

type BranchPage interface {
	StartPoints(ctx context.Context, path string) (StartPoints, error)
	Handover(ctx context.Context, sid, agent string, consent Consent) (Started, error)
	BringMain(ctx context.Context, sid string) (string, error)
}

var _ BranchPage = (*Dispatcher)(nil)

func (d *Dispatcher) UseForges(f *Forges) {
	d.mu.Lock()
	d.forges = f
	d.mu.Unlock()
}

func lineageOf(r *run) *Lineage {
	if (r.origin == "" || r.origin == OriginMain) && len(r.earlier) == 0 && r.handedTo == "" {
		return nil
	}
	return &Lineage{Branch: r.branch, Origin: r.origin, From: r.base, Earlier: slices.Clone(r.earlier), HandedTo: r.handedTo, Last: r.handedHead}
}

func checkStartFrom(q Dispatch) error {
	n := q.From.chosen()
	switch {
	case n > 1 || n == 1 && q.Continues != nil:
		return explained(invalid("from"), "Start from one place: main, a branch, a pull request or a session's commit.")
	case q.From.Branch != "" && !push.ValidBranch(q.From.Branch):
		return explained(invalid("from"), "That isn't a branch name the engine accepts.")
	case q.From.Pull < 0 || q.From.Pull > 1_000_000_000:
		return explained(invalid("from"), "That isn't a pull request number.")
	case q.From.SID != "" && !sidPattern.MatchString(q.From.SID):
		return explained(invalid("from"), "That isn't a session of this engine.")
	case (q.From.Branch != "" || q.From.Pull != 0) && !sha1Hex.MatchString(q.From.Commit):
		return explained(invalid("from"), "Choose the branch or pull request again: the start needs the commit the page showed.")
	case q.From.Commit != "" && q.From.Branch == "" && q.From.Pull == 0:
		return explained(invalid("from"), "A commit goes only with a branch or a pull request.")
	case q.Continues != nil && (!sessionBranch.MatchString(q.Continues.Branch) || !sha1Hex.MatchString(q.Continues.Head) || len(q.Continues.Earlier) > MaxStints):
		return explained(invalid("continues"), "That branch can't be continued here.")
	}
	return nil
}

type startPoint struct {
	commit  string
	origin  string
	earlier []Stint
}

func (d *Dispatcher) sources() (StartSources, bool) {
	s, ok := d.cfg.Repositories.(StartSources)
	return s, ok
}

func (d *Dispatcher) startAt(ctx context.Context, repo Repository, q Dispatch, main string) (startPoint, error) {
	switch {
	case q.Continues != nil:
		return startPoint{commit: q.Continues.Head, origin: OriginHandover, earlier: slices.Clone(q.Continues.Earlier)}, nil
	case q.From.SID != "":
		return d.forkPoint(repo, q.From.SID)
	case q.From.Branch != "":
		src, ok := d.sources()
		if !ok {
			return startPoint{}, explained(conflict("from"), "This engine can't fetch other branches.")
		}
		c, err := src.FetchBranch(ctx, repo.Name, q.From.Branch, q.From.Commit)
		if err != nil {
			return startPoint{}, err
		}
		return startPoint{commit: c, origin: "branch " + q.From.Branch}, nil
	case q.From.Pull > 0:
		return d.pullPoint(ctx, repo, q.From.Pull, q.From.Commit)
	}
	return startPoint{commit: main, origin: OriginMain}, nil
}

func (d *Dispatcher) forkPoint(repo Repository, sid string) (startPoint, error) {
	d.mu.Lock()
	r := d.runs[sid]
	var branch, name, path string
	if r != nil {
		branch, name, path = r.branch, r.display, r.repoPath
		if name == "" {
			name = r.spec.Name + " " + r.id
		}
	}
	d.mu.Unlock()
	if r == nil || path != repo.Path {
		return startPoint{}, explained(notFound("from"), "That session isn't one of this project's sessions on this engine.")
	}
	heads, _ := d.reg.worktreeReaders()
	if heads == nil {
		return startPoint{}, explained(conflict("from"), "This engine can't read the session's branch.")
	}
	head, err := heads.Head(path, branch)
	if err != nil || !sha1Hex.MatchString(head) {
		return startPoint{}, explained(conflict("from"), "The engine couldn't read the branch of that session.")
	}
	return startPoint{commit: head, origin: "a commit of session " + clip(name, maxName)}, nil
}

func (d *Dispatcher) pullPoint(ctx context.Context, repo Repository, number int, shown string) (startPoint, error) {
	src, ok := d.sources()
	if !ok {
		return startPoint{}, explained(conflict("from"), "This engine can't fetch pull requests.")
	}
	list, why := d.openPulls(ctx, repo.Name)
	if why != "" {
		return startPoint{}, explained(conflict("from"), why)
	}
	i := slices.IndexFunc(list, func(p PullHead) bool { return p.Number == number })
	if i < 0 {
		return startPoint{}, explained(notFound("from"), "That pull request isn't open on the git host any more.")
	}
	p := list[i]
	if p.Commit != shown {
		return startPoint{}, explained(conflict("from moved"), "Pull request #"+strconv.Itoa(number)+"'s head is now commit "+shortSHA(p.Commit)+", not the "+shortSHA(shown)+" the page showed, so no session started. Choose it again.")
	}
	if err := src.FetchCommit(ctx, repo.Name, p.Commit); err != nil {
		return startPoint{}, err
	}
	origin := "pull request #" + strconv.Itoa(p.Number)
	if p.Branch != "" {
		origin += " (" + p.Branch + ")"
	}
	return startPoint{commit: p.Commit, origin: origin}, nil
}

func (d *Dispatcher) openPulls(ctx context.Context, name string) ([]PullHead, string) {
	d.mu.Lock()
	f := d.forges
	d.mu.Unlock()
	if f == nil {
		return nil, "This engine has no git host API, so it lists no pull requests."
	}
	return f.OpenPulls(ctx, name)
}

func (f *Forges) OpenPulls(ctx context.Context, name string) ([]PullHead, string) {
	api, ok := f.cfg.API.(PullHeads)
	if !ok {
		return nil, "This engine's git host API can't list pull requests."
	}
	raw, token, _, err := f.cfg.Repos.ForgeTarget(name)
	if err != nil {
		return nil, "The project isn't in the engine's list any more."
	}
	host, owner, repo, ok := ForgeRepoOf(raw)
	if !ok || !f.cfg.API.Supports(host) {
		return nil, "Pull requests are listed only for GitHub (github.com or a GitHub Enterprise server)."
	}
	if token == "" {
		return nil, "No token is saved for " + strings.TrimPrefix(host, "https://") + ", so the engine can't list its pull requests."
	}
	list, err := api.OpenPullHeads(ctx, ForgeRepo{Host: host, Owner: owner, Name: repo, Token: token})
	if err != nil {
		return nil, f.clean(token, "The engine couldn't list the open pull requests: "+forgeWhy(err, NeedPulls))
	}
	out := make([]PullHead, 0, min(len(list), MaxStartPulls))
	for _, p := range list {
		if len(out) >= MaxStartPulls {
			break
		}
		if p.Number <= 0 || !sha1Hex.MatchString(p.Commit) || p.Branch != "" && !push.ValidBranch(p.Branch) {
			continue
		}
		p.Title = CleanLine(p.Title, maxPullTitle)
		out = append(out, p)
	}
	return out, ""
}

func (d *Dispatcher) StartPoints(ctx context.Context, path string) (StartPoints, error) {
	if d.cfg.Repositories == nil {
		return StartPoints{}, explained(conflict("no agent-host"), "This engine has no agent-host to start sessions in.")
	}
	repos, err := d.cfg.Repositories.List()
	if err != nil {
		return StartPoints{}, explained(conflict("workspace"), "The engine couldn't read the workspace's repositories.")
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return StartPoints{}, explained(notFound("project"), "That project is no longer in the list.")
	}
	repo := repos[i]
	src, ok := d.sources()
	if !ok {
		return StartPoints{}, explained(conflict("from"), "This engine can't list other branches.")
	}
	branches, err := src.Branches(ctx, repo.Name)
	if err != nil {
		return StartPoints{}, err
	}
	branches = slices.DeleteFunc(branches, func(b RemoteRef) bool { return b.Name == repo.Branch })
	pulls, why := d.openPulls(ctx, repo.Name)
	return StartPoints{Main: repo.Branch, Branches: branches, Pulls: pulls, PullsWhy: why}, nil
}

func (c *Repos) startRemote() (StartRemote, bool) {
	s, ok := c.cfg.Remote.(StartRemote)
	return s, ok
}

func (c *Repos) Branches(ctx context.Context, name string) ([]RemoteRef, error) {
	rec, token, err := c.recordToken(name)
	if err != nil {
		return nil, err
	}
	src, ok := c.startRemote()
	if !ok {
		return nil, explained(conflict("from"), "This engine can't list other branches.")
	}
	fctx, cancel := context.WithTimeout(ctx, c.cfg.FetchFor)
	list, err := src.ListHeads(fctx, rec.URL, token)
	cancel()
	if err != nil {
		c.cfg.Logger.Warn("repository", "event", "heads-failed")
		return nil, explained(conflict("fetch"), "The engine couldn't list the branches of "+rec.URL+": "+clip(err.Error(), 600))
	}
	out := make([]RemoteRef, 0, min(len(list), MaxStartBranches))
	for _, b := range list {
		if len(out) >= MaxStartBranches {
			break
		}
		if push.ValidBranch(b.Name) && sha1Hex.MatchString(b.Commit) {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b RemoteRef) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (c *Repos) FetchBranch(ctx context.Context, name, branch, commit string) (string, error) {
	if !push.ValidBranch(branch) {
		return "", explained(invalid("from"), "That isn't a branch name the engine accepts.")
	}
	if !sha1Hex.MatchString(commit) {
		return "", explained(invalid("from"), "That isn't a commit.")
	}
	return c.fetchHead(ctx, name, branch, commit)
}

func (c *Repos) FetchCommit(ctx context.Context, name, commit string) error {
	if !sha1Hex.MatchString(commit) {
		return explained(invalid("from"), "That isn't a commit.")
	}
	_, err := c.fetchHead(ctx, name, "", commit)
	return err
}

func (c *Repos) recordToken(name string) (Record, string, error) {
	rec, err := c.record(name)
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok && e.Kind == NotFound {
			return Record{}, "", explained(notFound("project"), "That project is no longer in the list.")
		}
		return Record{}, "", explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	host, _ := HostOf(rec.URL)
	token, _, err := c.cfg.Tokens.Token(host)
	if err != nil {
		return Record{}, "", explained(conflict("tokens"), "The engine couldn't read its saved tokens.")
	}
	return rec, token, nil
}

func (c *Repos) fetchHead(ctx context.Context, name, branch, commit string) (string, error) {
	rec, token, err := c.recordToken(name)
	if err != nil {
		return "", err
	}
	src, ok := c.startRemote()
	if !ok {
		return "", explained(conflict("from"), "This engine can't fetch other branches.")
	}
	path := c.path(rec.Name)
	present := func(sha string) bool {
		_, err := c.cfg.Commits.Commit(path, sha)
		return err == nil
	}
	var have []string
	if sha1Hex.MatchString(rec.Commit) && present(rec.Commit) {
		have = []string{rec.Commit}
	}
	fctx, cancel := context.WithTimeout(ctx, c.cfg.FetchFor)
	got, err := src.FetchHead(fctx, HeadFetch{Name: rec.Name, URL: rec.URL, Token: token, Branch: branch, Commit: commit, Have: have, Present: present})
	cancel()
	what := "the branch " + branch
	if branch == "" {
		what = "the commit " + shortSHA(commit)
	}
	if err != nil {
		c.cfg.Logger.Warn("repository", "event", "start-fetch-failed")
		return "", explained(conflict("fetch"), "The engine couldn't fetch "+what+" from "+rec.URL+", so no session started: "+clip(err.Error(), 600))
	}
	if !sha1Hex.MatchString(got) || commit != "" && got != commit {
		return "", explained(conflict("fetch"), "The fetch answered a commit the engine doesn't accept.")
	}
	if !present(got) {
		return "", explained(conflict("fetch"), "The fetched commit "+got[:12]+" couldn't be read back from the clone.")
	}
	c.cfg.Logger.Info("repository", "event", "start-fetched")
	return got, nil
}
