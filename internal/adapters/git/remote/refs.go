package remote

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const maxHeads = 5000

var _ session.StartRemote = Remote{}

func (r Remote) ListHeads(ctx context.Context, raw, token string) ([]session.RemoteRef, error) {
	k, err := r.open(ctx, raw, token)
	if err != nil {
		return nil, err
	}
	defer k.close()
	var out []session.RemoteRef
	for name, h := range k.adv.References {
		branch, ok := strings.CutPrefix(name, "refs/heads/")
		if !ok || !push.ValidBranch(branch) {
			continue
		}
		if len(out) >= maxHeads {
			return nil, errors.New("the remote lists more branches than the engine reads")
		}
		out = append(out, session.RemoteRef{Name: branch, Commit: h.String()})
	}
	slices.SortFunc(out, func(a, b session.RemoteRef) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (r Remote) FetchHead(ctx context.Context, q session.HeadFetch) (string, error) {
	if !repoName.MatchString(q.Name) || strings.Contains(q.Name, "..") {
		return "", errors.New("the repository's folder name isn't one the engine uses")
	}
	if q.Branch != "" && !push.ValidBranch(q.Branch) || q.Branch == "" && !sha1Pattern.MatchString(q.Commit) {
		return "", errors.New("the engine fetches a branch or a full commit hash")
	}
	if _, err := endpoint(q.URL); err != nil {
		return "", err
	}
	git, done, err := r.baseGit(q.Name)
	if err != nil {
		return "", err
	}
	defer done()
	k, err := r.open(ctx, q.URL, q.Token)
	if err != nil {
		return "", err
	}
	defer k.close()
	want := q.Commit
	if q.Branch != "" {
		h, ok := k.adv.References["refs/heads/"+q.Branch]
		if !ok {
			return "", errors.New("the remote has no branch " + q.Branch)
		}
		if want != "" && h.String() != want {
			return "", errors.New("the branch " + q.Branch + " moved on the remote")
		}
		want = h.String()
	} else if !k.offers(want) {
		return "", errors.New("the remote neither lists the commit nor allows asking for it by its hash")
	}
	if !slices.Contains(q.Have, want) && (q.Present == nil || !q.Present(want)) {
		if err := k.download(ctx, git, want, q.Have); err != nil {
			return "", err
		}
	}
	if q.Branch != "" {
		if err := writeFile(git, "refs/remotes/origin/"+q.Branch, []byte(want+"\n")); err != nil {
			return "", errors.New("refs/remotes/origin/" + q.Branch + " couldn't be updated in the clone")
		}
	}
	return want, nil
}

func (r Remote) BranchHead(ctx context.Context, raw, branch string) (string, error) {
	if !push.ValidBranch(branch) {
		return "", errors.New("the branch name isn't one the engine reads")
	}
	k, err := r.open(ctx, raw, "")
	if err != nil {
		return "", err
	}
	defer k.close()
	h, ok := k.adv.References["refs/heads/"+branch]
	if !ok {
		return "", errors.New("the remote has no branch " + branch)
	}
	return h.String(), nil
}
