package session

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

type ChangeFrom string

const (
	FromFork     ChangeFrom = "fork"
	FromReviewed ChangeFrom = "reviewed"
	FromTurn     ChangeFrom = "turn"
	FromCommit   ChangeFrom = "commit"

	MaxBranchWalk = 5000
)

var changeCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

type ChangePoint struct {
	From   ChangeFrom
	Commit string
}

type ChangePoints struct {
	Reviewed   string
	ReviewedAt time.Time
	Turn       string
}

type branchSet struct {
	commits  map[string]bool
	complete bool
}

func (b branchSet) has(c string) bool {
	return b.commits[c]
}

func ChangePointOf(from, commit string) (ChangePoint, bool) {
	at := ChangePoint{From: ChangeFrom(from), Commit: commit}
	switch at.From {
	case "":
		at.From = FromFork
		return at, commit == ""
	case FromFork, FromReviewed, FromTurn:
		return at, commit == ""
	case FromCommit:
		return at, changeCommit.MatchString(commit)
	}
	return ChangePoint{}, false
}

func (p *Pushes) branchWalk(ctx context.Context, t PushTarget, head, fork string) (branchSet, error) {
	if p.cfg.Branch == nil {
		return branchSet{commits: map[string]bool{head: true}}, nil
	}
	commits, complete, err := p.cfg.Branch.BranchCommits(ctx, t.Repo, head, fork, MaxBranchWalk)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return branchSet{}, explained(conflict("changes"), "The engine stopped reading the branch's history: the request ran out of time.")
	}
	if err != nil {
		return branchSet{}, explained(conflict("changes"), clip("The engine couldn't read the branch's history: "+err.Error(), maxOutput))
	}
	return branchSet{commits: commits, complete: complete}, nil
}

func (p *Pushes) changePoints(sid string, t PushTarget, head string, branch branchSet) ChangePoints {
	var out ChangePoints
	if p.cfg.TurnBase != nil {
		if c := p.cfg.TurnBase(sid); changeCommit.MatchString(c) && branch.has(c) {
			out.Turn = c
		}
	}
	if p.cfg.Reviews == nil {
		return out
	}
	entries, err := p.cfg.Reviews.Entries()
	if err != nil {
		return out
	}
	newest := map[string]time.Time{}
	for k, e := range entries {
		if k.Branch != t.Branch || !branch.has(k.Commit) || (e.Record.State != review.Pass && e.Record.State != review.Block) {
			continue
		}
		at := e.EndedAt
		if at.IsZero() {
			at = e.StartedAt
		}
		if seen, ok := newest[k.Commit]; !ok || at.After(seen) {
			newest[k.Commit] = at
		}
	}
	commits := make([]string, 0, len(newest))
	for c := range newest {
		commits = append(commits, c)
	}
	slices.SortFunc(commits, func(a, b string) int { return newest[b].Compare(newest[a]) })
	if len(commits) > 0 {
		out.Reviewed, out.ReviewedAt = commits[0], newest[commits[0]]
	}
	return out
}

func changeBase(fork string, at ChangePoint, points ChangePoints, branch branchSet) (string, error) {
	switch at.From {
	case FromReviewed:
		if points.Reviewed == "" {
			return "", explained(conflict("no reviewed commit"), "No commit on this session's branch has a reviewer's pass or block yet, within the newest "+strconv.Itoa(MaxBranchWalk)+" commits, so there is no last reviewed commit to compare with.")
		}
		return points.Reviewed, nil
	case FromTurn:
		if points.Turn == "" {
			return "", explained(conflict("no turn"), "The session hasn't reported starting a turn since the engine started, at a commit still on its branch, so there is no turn to compare with.")
		}
		return points.Turn, nil
	case FromCommit:
		if !branch.has(at.Commit) {
			if !branch.complete {
				return "", explained(conflict("commit"), "Commit "+shortSHA(at.Commit)+" isn't within the newest "+strconv.Itoa(MaxBranchWalk)+" commits of this session's branch, so the engine doesn't compare with it.")
			}
			return "", explained(conflict("commit"), "Commit "+shortSHA(at.Commit)+" isn't on this session's branch, so the engine doesn't compare with it.")
		}
		return at.Commit, nil
	}
	return fork, nil
}
