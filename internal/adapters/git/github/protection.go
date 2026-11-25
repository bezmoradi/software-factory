package github

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var _ session.ProtectionReader = API{}

type known int

const (
	unknownYet known = iota
	knownNo
	knownYes
)

type protectionRead struct {
	protected   known
	reviews     known
	approvals   int
	owners      bool
	pullRequest bool
	checks      known
	names       []string
	why         string
}

type ruleJSON struct {
	Type       string `json:"type"`
	Parameters struct {
		RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
		RequireCodeOwnerReview       bool `json:"require_code_owner_review"`
		RequiredStatusChecks         []struct {
			Context string `json:"context"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

type classicJSON struct {
	RequiredPullRequestReviews *struct {
		RequiredApprovingReviewCount int  `json:"required_approving_review_count"`
		RequireCodeOwnerReviews      bool `json:"require_code_owner_reviews"`
	} `json:"required_pull_request_reviews"`
	RequiredStatusChecks *struct {
		Contexts []string `json:"contexts"`
		Checks   []struct {
			Context string `json:"context"`
		} `json:"checks"`
	} `json:"required_status_checks"`
}

type branchJSON struct {
	Protected  bool `json:"protected"`
	Protection struct {
		RequiredStatusChecks struct {
			EnforcementLevel string   `json:"enforcement_level"`
			Contexts         []string `json:"contexts"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

func (p *protectionRead) check(name string) {
	name = strings.TrimSpace(name)
	if name != "" && !slices.Contains(p.names, name) {
		p.names = append(p.names, name)
	}
}

func (p *protectionRead) yes(field *known) { *field = knownYes }

func (p *protectionRead) pull(approvals int, owners bool) {
	p.pullRequest = true
	p.approvals = max(p.approvals, approvals)
	p.owners = p.owners || owners
	if approvals > 0 || owners {
		p.yes(&p.reviews)
	} else {
		p.no(&p.reviews)
	}
}

func (p *protectionRead) unread(why string) {
	if p.why == "" {
		p.why = why
	}
}

func whyUnread(err error, what string) string {
	var fe *session.ForgeError
	if errors.As(err, &fe) && fe.Need != "" && (fe.Status == http.StatusForbidden || fe.Status == http.StatusNotFound) {
		return "Needs a token with " + fe.Need
	}
	return "Couldn't read " + what
}

func (p *protectionRead) no(field *known) {
	if *field == unknownYet {
		*field = knownNo
	}
}

func (a API) Protection(ctx context.Context, r session.ForgeRepo, branch string) (session.Protection, error) {
	var p protectionRead
	rulesKnown, classicKnown := a.rules(ctx, r, branch, &p), false
	var classic classicJSON
	_, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/branches/"+refPath(branch)+"/protection", nil, session.NeedAdministration, &classic)
	var fe *session.ForgeError
	switch {
	case err == nil:
		classicKnown = true
		p.yes(&p.protected)
		if c := classic.RequiredPullRequestReviews; c != nil {
			p.pull(c.RequiredApprovingReviewCount, c.RequireCodeOwnerReviews)
		} else {
			p.no(&p.reviews)
		}
		if c := classic.RequiredStatusChecks; c != nil {
			p.yes(&p.checks)
			for _, n := range c.Contexts {
				p.check(n)
			}
			for _, n := range c.Checks {
				p.check(n.Context)
			}
		} else {
			p.no(&p.checks)
		}
	case errors.As(err, &fe) && fe.Status == http.StatusNotFound && strings.Contains(strings.ToLower(fe.Message), "not protected"):
		classicKnown = true
		p.no(&p.protected)
		p.no(&p.reviews)
		p.no(&p.checks)
	case errors.As(err, &fe) && (fe.Status == http.StatusForbidden || fe.Status == http.StatusNotFound):
		classicKnown = a.branch(ctx, r, branch, &p)
	default:
		return session.Protection{}, err
	}
	if !rulesKnown || !classicKnown {
		for _, f := range []*known{&p.protected, &p.reviews, &p.checks} {
			if *f == knownNo {
				*f = unknownYet
			}
		}
	}
	word := func(k known, yes, no string) string {
		switch k {
		case knownYes:
			return yes
		case knownNo:
			return no
		}
		return session.ProtectionUnknown
	}
	return session.Protection{
		State:       word(p.protected, session.ProtectionOn, session.ProtectionOff),
		Reviews:     word(p.reviews, session.RequiredYes, session.RequiredNo),
		Approvals:   p.approvals,
		AtLeast:     !rulesKnown || !classicKnown,
		CodeOwners:  p.owners,
		PullRequest: p.pullRequest,
		Checks:      word(p.checks, session.RequiredYes, session.RequiredNo),
		CheckNames:  p.names,
		Why:         p.why,
	}, nil
}

func (a API) rules(ctx context.Context, r session.ForgeRepo, branch string, p *protectionRead) bool {
	var list []ruleJSON
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/rules/branches/"+refPath(branch)+"?per_page=100", nil, session.NeedMetadata, &list); err != nil {
		p.unread(whyUnread(err, "the branch's rulesets"))
		return false
	}
	active := false
	for _, rule := range list {
		switch rule.Type {
		case "pull_request":
			active = true
			p.pull(rule.Parameters.RequiredApprovingReviewCount, rule.Parameters.RequireCodeOwnerReview)
		case "required_status_checks":
			active = true
			p.yes(&p.checks)
			for _, c := range rule.Parameters.RequiredStatusChecks {
				p.check(c.Context)
			}
		case "deletion", "non_fast_forward", "update", "required_linear_history", "required_signatures", "merge_queue", "required_deployments", "code_scanning", "workflows":
			active = true
		}
	}
	if active {
		p.yes(&p.protected)
	}
	return true
}

func (a API) branch(ctx context.Context, r session.ForgeRepo, branch string, p *protectionRead) bool {
	var b branchJSON
	if _, err := a.do(ctx, r, http.MethodGet, repoPath(r)+"/branches/"+refPath(branch), nil, session.NeedContents, &b); err != nil {
		p.unread(whyUnread(err, "the branch"))
		return false
	}
	if !b.Protected {
		p.no(&p.protected)
		p.no(&p.reviews)
		p.no(&p.checks)
		return true
	}
	p.yes(&p.protected)
	p.unread("Needs a token with " + session.NeedAdministration)
	if c := b.Protection.RequiredStatusChecks; c.EnforcementLevel != "" && c.EnforcementLevel != "off" {
		p.yes(&p.checks)
		for _, n := range c.Contexts {
			p.check(n)
		}
	}
	return false
}
