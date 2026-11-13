package github

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const recentIssues = 10

var loginName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

type issueJSON struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	Body    *string   `json:"body"`
	HTMLURL string    `json:"html_url"`
	Created time.Time `json:"created_at"`
	Pull    *struct{} `json:"pull_request"`
}

func (a API) RecentIssues(ctx context.Context, r session.ForgeRepo) ([]session.IssueFound, error) {
	login, err := a.Login(ctx, r)
	if err != nil || !loginName.MatchString(login) {
		login = ""
	}
	q := url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {"10"}}
	if login != "" {
		q.Set("creator", login)
	}
	var list []issueJSON
	if _, err = a.do(ctx, r, http.MethodGet, repoPath(r)+"/issues?"+q.Encode(), nil, session.NeedIssues, &list); err != nil {
		return nil, err
	}
	out := []session.IssueFound{}
	for _, i := range list {
		if i.Pull != nil || i.Number <= 0 || i.Created.IsZero() || len(out) >= recentIssues {
			continue
		}
		body := ""
		if i.Body != nil {
			body = *i.Body
		}
		out = append(out, session.IssueFound{Number: i.Number, URL: session.CleanLine(i.HTMLURL, 300), Title: i.Title, Body: body, Created: i.Created, Unconfirmed: login == ""})
	}
	return out, nil
}
