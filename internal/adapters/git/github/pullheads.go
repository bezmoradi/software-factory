package github

import (
	"context"
	"net/http"
	"net/url"
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const maxPullHeads = 100

var headSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

var _ session.PullHeads = API{}

func (a API) OpenPullHeads(ctx context.Context, r session.ForgeRepo) ([]session.PullHead, error) {
	var list []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	q := url.Values{"state": {"open"}, "per_page": {"100"}, "sort": {"updated"}, "direction": {"desc"}}
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/pulls?"+q.Encode(), nil, session.NeedPulls, &list); err != nil {
		return nil, err
	}
	out := make([]session.PullHead, 0, min(len(list), maxPullHeads))
	for _, p := range list {
		if len(out) >= maxPullHeads {
			break
		}
		if p.Number <= 0 || !headSHA.MatchString(p.Head.SHA) {
			continue
		}
		out = append(out, session.PullHead{Number: p.Number, Title: session.CleanLine(p.Title, 200), Branch: session.CleanLine(p.Head.Ref, 255), Commit: p.Head.SHA})
	}
	return out, nil
}
