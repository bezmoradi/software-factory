package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var _ session.PullAPI = API{}

const readyMutation = `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft number } } }`

type pullStatusJSON struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	NodeID         string `json:"node_id"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	Draft          bool   `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func GraphQL(host string) string {
	h := bare(host)
	switch {
	case h == "github.com":
		return "https://api.github.com/graphql"
	case strings.HasSuffix(h, ".ghe.com") && !strings.HasPrefix(h, "api."):
		return "https://api." + h + "/graphql"
	}
	return "https://" + h + "/api/graphql"
}

func (a API) PullStatus(ctx context.Context, r session.ForgeRepo, number int) (session.PullStatus, error) {
	var p pullStatusJSON
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/pulls/"+strconv.Itoa(number), nil, session.NeedPulls, &p); err != nil {
		return session.PullStatus{}, err
	}
	if p.Number != number || p.State != "open" && p.State != "closed" {
		return session.PullStatus{}, errors.New("GitHub's answer for the pull request isn't the one asked for")
	}
	out := session.PullStatus{Number: p.Number, URL: session.CleanLine(p.HTMLURL, 300), NodeID: session.CleanLine(p.NodeID, 200), State: p.State,
		Merged: p.Merged || p.MergedAt != "", Draft: p.Draft, HeadRef: session.CleanLine(p.Head.Ref, 255), Base: session.CleanLine(p.Base.Ref, 255),
		MergeState: session.CleanLine(p.MergeableState, 40)}
	if len(p.Head.SHA) == 40 {
		out.Head = p.Head.SHA
	}
	if p.Mergeable != nil {
		out.Mergeable = "no"
		if *p.Mergeable {
			out.Mergeable = "yes"
		}
	}
	return out, nil
}

func (a API) CheckStates(ctx context.Context, r session.ForgeRepo, commit string) (session.CheckReport, error) {
	return a.checkReport(ctx, r, commit, false)
}

func (a API) MarkReady(ctx context.Context, r session.ForgeRepo, nodeID string) (bool, error) {
	if nodeID == "" {
		return true, errors.New("GitHub's answer for the pull request has no node id")
	}
	var out struct {
		Data struct {
			Mark *struct {
				PullRequest struct {
					IsDraft bool `json:"isDraft"`
					Number  int  `json:"number"`
				} `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	in := map[string]any{"query": readyMutation, "variables": map[string]string{"id": nodeID}}
	if _, _, err := a.sendTo(ctx, r, http.MethodPost, GraphQL(r.Host), in, session.NeedPulls, &out); err != nil {
		return true, err
	}
	if len(out.Errors) > 0 {
		msg := session.CleanLine(out.Errors[0].Message, 300)
		need := ""
		if strings.EqualFold(out.Errors[0].Type, "FORBIDDEN") || strings.Contains(strings.ToLower(msg), "not accessible") {
			need = session.NeedPulls
		}
		return true, &session.ForgeError{Status: http.StatusOK, Need: need, Message: "GitHub's GraphQL API refused it: " + msg}
	}
	if out.Data.Mark == nil {
		return true, errors.New("GitHub's GraphQL answer has no pull request")
	}
	return out.Data.Mark.PullRequest.IsDraft, nil
}

func (a API) AssignTo(ctx context.Context, r session.ForgeRepo, number int, login string) error {
	var out struct {
		Assignees []userJSON `json:"assignees"`
	}
	if _, err := a.do(ctx, r, http.MethodPost, repoPath(r)+"/issues/"+strconv.Itoa(number)+"/assignees", map[string][]string{"assignees": {login}}, session.NeedIssues, &out); err != nil {
		return err
	}
	for _, u := range out.Assignees {
		if strings.EqualFold(u.Login, login) {
			return nil
		}
	}
	return &session.ForgeError{Status: http.StatusOK, Message: "GitHub answered, but didn't assign it; the token's user may not be allowed to be an assignee there"}
}
