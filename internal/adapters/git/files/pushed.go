package files

import (
	"errors"
	"path"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxScanCommits = 1000
	maxAttributes  = 1 << 20
)

var _ session.PushContents = Reader{}

func (Reader) Pushed(repoPath, base, head string, budget int64) (session.PushedSet, error) {
	return with(repoPath, func(r *repo) (session.PushedSet, error) {
		out := session.PushedSet{Complete: true, Attributes: map[string][]byte{}}
		commits, err := newCommits(r, head, base)
		if err != nil {
			return out, err
		}
		if len(commits) > maxScanCommits {
			commits, out.Complete = commits[:maxScanCommits], false
		}
		seen := map[string]bool{}
		dirs := map[string]bool{"": true}
		left := budget
		for _, c := range commits {
			found, err := introduced(r, c)
			if err != nil {
				return out, err
			}
			for _, ch := range found {
				key := ch.Path + "\x00" + ch.NewSHA
				if seen[key] {
					continue
				}
				seen[key] = true
				if err := r.charge(len(key)); err != nil {
					return out, err
				}
				for d := path.Dir(ch.Path); d != "." && d != "/" && !dirs[d]; d = path.Dir(d) {
					dirs[d] = true
				}
				f := session.PushedFile{Path: ch.Path, SHA: ch.NewSHA, Mode: ch.NewMode}
				if ch.NewMode == push.ModeSubmodule {
					out.Files = append(out.Files, f)
					continue
				}
				data, err := r.blob(ch.NewSHA)
				switch {
				case errors.Is(err, push.ErrTooLarge):
					f.Size = -1
				case err != nil:
					return out, err
				default:
					f.Size = int64(len(data))
					if f.Size <= session.MaxFlaggedFile && f.Size <= left {
						f.Data = data
						left -= f.Size
					}
				}
				out.Files = append(out.Files, f)
			}
		}
		hc, err := r.commit(head)
		if err != nil {
			return out, err
		}
		for d := range dirs {
			name := ".gitattributes"
			if d != "" {
				name = d + "/" + name
			}
			e, err := lookup(r.tree, hc.Tree, name)
			if err != nil {
				return out, err
			}
			if e == nil || (e.mode != push.ModeFile && e.mode != push.ModeExecutable) {
				continue
			}
			data, err := r.blob(e.sha)
			if err != nil {
				return out, err
			}
			if len(data) > maxAttributes {
				out.Complete = false
				continue
			}
			out.Attributes[d] = data
		}
		return out, nil
	})
}

func newCommits(r *repo, head, base string) ([]push.Commit, error) {
	have := map[string]bool{}
	if base != "" {
		queue := []string{base}
		for len(queue) > 0 && len(have) < maxMergeWalk {
			sha := queue[0]
			queue = queue[1:]
			if have[sha] {
				continue
			}
			have[sha] = true
			c, err := r.commit(sha)
			if err != nil {
				return nil, err
			}
			queue = append(queue, c.Parents...)
		}
	}
	var out []push.Commit
	queued := map[string]bool{head: true}
	queue := []string{head}
	for len(queue) > 0 && len(out) <= maxScanCommits {
		sha := queue[0]
		queue = queue[1:]
		if have[sha] {
			continue
		}
		c, err := r.commit(sha)
		if err != nil {
			return nil, err
		}
		out = append(out, linksOnly(c))
		for _, p := range c.Parents {
			if !queued[p] {
				queued[p] = true
				queue = append(queue, p)
			}
		}
	}
	return out, nil
}

func introduced(r *repo, c push.Commit) ([]push.Change, error) {
	parents := c.Parents
	if len(parents) == 0 {
		parents = []string{""}
	}
	var kept []push.Change
	for i, p := range parents {
		left := ""
		if p != "" {
			pc, err := r.commit(p)
			if err != nil {
				return nil, err
			}
			left = pc.Tree
		}
		d := differ{r: r}
		if err := d.trees(left, c.Tree, "", 0); err != nil {
			return nil, err
		}
		var added []push.Change
		for _, ch := range d.out {
			if ch.NewSHA != "" && ch.NewMode != push.ModeFolder {
				added = append(added, ch)
			}
		}
		if i == 0 {
			kept = added
			continue
		}
		kept = slices.DeleteFunc(kept, func(k push.Change) bool {
			return !slices.ContainsFunc(added, func(a push.Change) bool { return a.Path == k.Path && a.NewSHA == k.NewSHA })
		})
	}
	return kept, nil
}
