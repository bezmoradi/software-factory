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

const mergedPage = 30

var (
	_            session.MergedPullsAPI = API{}
	needPullRead                        = strings.TrimSuffix(session.NeedPulls, " and write")
)

type mergedJSON struct {
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Body     *string `json:"body"`
	HTMLURL  string  `json:"html_url"`
	MergedAt *string `json:"merged_at"`
	Merge    string  `json:"merge_commit_sha"`
	User     *struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

func mergedOf(p mergedJSON) (session.MergedPull, bool) {
	if p.Number <= 0 || p.MergedAt == nil || !headSHA.MatchString(p.Head.SHA) || !headSHA.MatchString(p.Base.SHA) {
		return session.MergedPull{}, false
	}
	at, err := time.Parse(time.RFC3339, *p.MergedAt)
	if err != nil {
		return session.MergedPull{}, false
	}
	out := session.MergedPull{Number: p.Number, Title: p.Title, URL: session.CleanLine(p.HTMLURL, 300), Base: p.Base.SHA, Head: p.Head.SHA, Merged: at}
	if headSHA.MatchString(p.Merge) {
		out.Merge = p.Merge
	}
	if p.Body != nil {
		out.Body = *p.Body
	}
	if p.User != nil {
		out.Author = p.User.Login
	}
	return out, true
}

func (a API) MergedPulls(ctx context.Context, r session.ForgeRepo, page int) ([]session.MergedPull, bool, error) {
	q := url.Values{"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {strconv.Itoa(mergedPage)}, "page": {strconv.Itoa(page)}}
	var list []mergedJSON
	_, h, err := a.send(ctx, r, http.MethodGet, repoPath(r)+"/pulls?"+q.Encode(), nil, needPullRead, &list)
	if err != nil {
		return nil, false, err
	}
	out := []session.MergedPull{}
	for _, p := range list {
		if m, ok := mergedOf(p); ok {
			m.Body = ""
			out = append(out, m)
		}
	}
	return out, strings.Contains(h.Get("Link"), `rel="next"`), nil
}

func (a API) MergedPull(ctx context.Context, r session.ForgeRepo, number int) (session.MergedPull, error) {
	var p mergedJSON
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/pulls/"+strconv.Itoa(number), nil, needPullRead, &p); err != nil {
		return session.MergedPull{}, err
	}
	if p.Number != number {
		return session.MergedPull{}, errors.New("GitHub's answer for the pull request isn't the one asked for")
	}
	m, ok := mergedOf(p)
	if !ok {
		return session.MergedPull{}, &session.ForgeError{Status: http.StatusOK, Message: "pull request #" + strconv.Itoa(number) + " isn't merged"}
	}
	return m, nil
}
