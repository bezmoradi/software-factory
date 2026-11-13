package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	commentPages   = 3
	commentPerPage = 100
)

func draftRefused(err error) bool {
	fe, ok := errors.AsType[*session.ForgeError](err)
	return ok && strings.Contains(strings.ToLower(fe.Message), "draft")
}

func (a API) readyPull(ctx context.Context, r session.ForgeRepo, title, body, head, base string) (session.ForgeLink, bool, error) {
	if link, open, err := a.OpenPull(ctx, r, head); err != nil {
		return session.ForgeLink{}, false, err
	} else if open {
		return link, false, nil
	}
	var out pullJSON
	in := map[string]any{"title": title, "body": body, "head": head, "base": base, "maintainer_can_modify": false}
	if _, err := a.do(ctx, r, http.MethodPost, repoPath(r)+"/pulls", in, session.NeedPulls, &out); err != nil {
		return session.ForgeLink{}, false, err
	}
	link, err := linkOf(out)
	return link, false, err
}

func (a API) Comment(ctx context.Context, r session.ForgeRepo, number int, body string) (session.ForgeLink, error) {
	var out struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if _, err := a.do(ctx, r, http.MethodPost, repoPath(r)+"/issues/"+strconv.Itoa(number)+"/comments", map[string]string{"body": body}, session.NeedPulls, &out); err != nil {
		return session.ForgeLink{}, err
	}
	if out.ID <= 0 {
		return session.ForgeLink{}, errors.New("GitHub's answer for the comment has no id")
	}
	return session.ForgeLink{Number: number, URL: session.CleanLine(out.HTMLURL, 300)}, nil
}

func (a API) Login(ctx context.Context, r session.ForgeRepo) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if _, err := a.do(ctx, r, http.MethodGet, "/user", nil, session.NeedMetadata, &out); err != nil {
		return "", err
	}
	login := strings.TrimSpace(out.Login)
	if login == "" || len(login) > 100 || strings.ContainsAny(login, " \t\r\n") {
		return "", errors.New("GitHub's answer for the token's user has no login")
	}
	return login, nil
}

type userJSON struct {
	Login string `json:"login"`
}

type commentJSON struct {
	User        *userJSON `json:"user"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	CreatedAt   time.Time `json:"created_at"`
	SubmittedAt time.Time `json:"submitted_at"`
	State       string    `json:"state"`
	Path        string    `json:"path"`
	Line        *int      `json:"line"`
	Original    *int      `json:"original_line"`
	CommitID    string    `json:"commit_id"`
}

func (a API) pages(ctx context.Context, r session.ForgeRepo, path string) ([]commentJSON, bool, error) {
	get := func(page int) ([]commentJSON, http.Header, error) {
		var list []commentJSON
		q := url.Values{"per_page": {strconv.Itoa(commentPerPage)}, "page": {strconv.Itoa(page)}}
		_, h, err := a.send(ctx, r, http.MethodGet, path+"?"+q.Encode(), nil, session.NeedPulls, &list)
		return list, h, err
	}
	first, h, err := get(1)
	if err != nil {
		return nil, false, err
	}
	last := lastPage(h)
	if len(first) < commentPerPage || last <= 1 {
		return first, false, nil
	}
	from, all := 2, first
	if last > commentPages {
		from, all = last-commentPages+1, nil
	}
	for page := from; page <= last; page++ {
		list, _, err := get(page)
		if err != nil {
			return all, false, err
		}
		all = append(all, list...)
	}
	return all, last > commentPages, nil
}

func lastPage(h http.Header) int {
	for part := range strings.SplitSeq(h.Get("Link"), ",") {
		target, rel, ok := strings.Cut(part, ";")
		if !ok || !strings.Contains(rel, `rel="last"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return 0
		}
		n, err := strconv.Atoi(u.Query().Get("page"))
		if err != nil || n < 1 || n > 100000 {
			return 0
		}
		return n
	}
	return 0
}

func (a API) PullComments(ctx context.Context, r session.ForgeRepo, number int) (session.CommentReport, error) {
	var rep session.CommentReport
	n := strconv.Itoa(number)
	parts := []struct {
		kind, path, name string
	}{
		{session.CommentReview, repoPath(r) + "/pulls/" + n + "/reviews", "reviews"},
		{session.CommentInline, repoPath(r) + "/pulls/" + n + "/comments", "inline review comments"},
		{session.CommentConversation, repoPath(r) + "/issues/" + n + "/comments", "conversation comments"},
	}
	var errs []string
	for _, p := range parts {
		list, more, err := a.pages(ctx, r, p.path)
		if err != nil {
			errs = append(errs, p.name+": "+why(err))
			rep.Notes = append(rep.Notes, "the "+p.name+" couldn't be read: "+why(err))
			continue
		}
		if more {
			rep.Notes = append(rep.Notes, "only the newest "+p.name+" (up to "+strconv.Itoa(commentPages*commentPerPage)+") were read; older ones were left out")
		}
		for _, c := range list {
			if c.User == nil {
				continue
			}
			pc := session.PullComment{Kind: p.kind, Author: c.User.Login, Body: c.Body, Created: c.CreatedAt, URL: c.HTMLURL}
			switch p.kind {
			case session.CommentReview:
				pc.Created, pc.State, pc.Commit = c.SubmittedAt, c.State, c.CommitID
				if c.State == "PENDING" || strings.TrimSpace(c.Body) == "" && c.State == "COMMENTED" {
					continue
				}
			case session.CommentInline:
				pc.Path, pc.Commit = c.Path, c.CommitID
				switch {
				case c.Line != nil:
					pc.Line = *c.Line
				case c.Original != nil:
					pc.Line, pc.Outdated = *c.Original, true
				default:
					pc.Outdated = true
				}
			}
			rep.Comments = append(rep.Comments, pc)
		}
	}
	if len(errs) == len(parts) {
		return rep, &session.ForgeError{Message: strings.Join(errs, "; ")}
	}
	return rep, nil
}
