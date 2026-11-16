package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const openIssuesPage = 30

var needIssuesRead = strings.TrimSuffix(session.NeedIssues, " and write")

func foundOf(i issueJSON) session.IssueFound {
	body := ""
	if i.Body != nil {
		body = *i.Body
	}
	out := session.IssueFound{Number: i.Number, URL: session.CleanLine(i.HTMLURL, 300), Title: i.Title, Body: body, Created: i.Created, Open: i.State == "open"}
	if i.User != nil {
		out.Author = i.User.Login
	}
	for _, l := range i.Labels {
		if len(out.Labels) < 20 && l.Name != "" {
			out.Labels = append(out.Labels, l.Name)
		}
	}
	return out
}

func (a API) OpenIssues(ctx context.Context, r session.ForgeRepo, label string, page int) ([]session.IssueFound, bool, error) {
	q := url.Values{"state": {"open"}, "sort": {"created"}, "direction": {"desc"}, "per_page": {strconv.Itoa(openIssuesPage)}, "page": {strconv.Itoa(page)}}
	if label != "" {
		q.Set("labels", label)
	}
	var list []issueJSON
	_, h, err := a.send(ctx, r, http.MethodGet, repoPath(r)+"/issues?"+q.Encode(), nil, needIssuesRead, &list)
	if err != nil {
		return nil, false, err
	}
	out := []session.IssueFound{}
	for _, i := range list {
		if i.Pull != nil || i.Number <= 0 {
			continue
		}
		out = append(out, foundOf(i))
	}
	return out, strings.Contains(h.Get("Link"), `rel="next"`), nil
}

func (a API) IssueByNumber(ctx context.Context, r session.ForgeRepo, number int) (session.IssueFound, error) {
	var i issueJSON
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/issues/"+strconv.Itoa(number), nil, needIssuesRead, &i); err != nil {
		return session.IssueFound{}, err
	}
	if i.Number != number {
		return session.IssueFound{}, errors.New("GitHub's answer for the issue isn't the one asked for")
	}
	if i.Pull != nil {
		return session.IssueFound{}, &session.ForgeError{Status: http.StatusOK, Message: "#" + strconv.Itoa(number) + " is a pull request, not an issue"}
	}
	return foundOf(i), nil
}
