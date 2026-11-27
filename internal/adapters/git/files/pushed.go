package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func (Reader) Pushed(ctx context.Context, repoPath, base, head string, reviewed []string, budget int64) (session.PushedSet, error) {
	return with(repoPath, func(r *repo) (session.PushedSet, error) {
		out := session.PushedSet{Complete: true, Attributes: map[string][]byte{}}
		commits, err := newCommits(ctx, r, head, base)
		if err != nil {
			return out, err
		}
		if len(commits) > maxScanCommits {
			commits, out.Complete, out.CommitsCut = commits[:maxScanCommits], false, true
		}
		hc, err := r.commit(head)
		if err != nil {
			return out, err
		}
		read := []string{hc.Tree}
		for _, sha := range append([]string{base}, reviewed...) {
			if sha == "" {
				continue
			}
			if c, err := r.commit(sha); err == nil {
				read = append(read, c.Tree)
			}
		}
		seen := map[string]bool{}
		dirs := map[string]bool{"": true}
		left := budget
		for _, commit := range slices.Backward(commits) {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			pc, size, err := pushedCommit(r, commit.SHA)
			if err != nil {
				return out, err
			}
			if size > left {
				out.Complete = false
				continue
			}
			left -= size
			out.Commits = append(out.Commits, pc)
		}
		for _, c := range commits {
			found, err := introduced(r, c)
			if err != nil {
				return out, err
			}
			for _, ch := range found {
				key := ch.Path + "\x00" + ch.NewSHA + "\x00" + string(ch.NewMode)
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
				if f.Reviewed, err = heldIn(r, read, ch); err != nil {
					return out, err
				}
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

func heldIn(r *repo, trees []string, ch push.Change) (bool, error) {
	for _, tree := range trees {
		at, err := lookup(r.tree, tree, ch.Path)
		if err != nil {
			return false, err
		}
		if at != nil && at.sha == ch.NewSHA && at.mode == ch.NewMode {
			return true, nil
		}
	}
	return false, nil
}

func newCommits(ctx context.Context, r *repo, head, base string) ([]push.Commit, error) {
	have, err := reachable(ctx, r, base, maxMergeWalk)
	if err != nil {
		return nil, err
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

func pushedCommit(r *repo, sha string) (session.PushedCommit, int64, error) {
	o, err := r.store.read(sha)
	if err != nil {
		return session.PushedCommit{}, 0, err
	}
	if o.kind != kindCommit {
		return session.PushedCommit{}, 0, fmt.Errorf("%w: %s is a %s, not a commit", push.ErrNotFound, sha, o.kind)
	}
	if len(o.data) > maxCommitObject {
		return session.PushedCommit{}, 0, fmt.Errorf("%w: commit %s is over %d MiB, more than the engine reads in one commit", push.ErrTooLarge, sha, maxCommitObject>>20)
	}
	header, message, _ := bytes.Cut(o.data, []byte("\n\n"))
	c := session.PushedCommit{SHA: sha, Message: string(message)}
	var other [][]byte
	for i, line := range bytes.Split(header, []byte("\n")) {
		if i == 0 && bytes.HasPrefix(line, []byte("tree ")) {
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("parent ")); ok && validSHA(string(rest)) {
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("author ")); ok && c.AuthorName == "" && c.AuthorEmail == "" {
			c.AuthorName, c.AuthorEmail = identity(rest)
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("committer ")); ok && c.CommitterName == "" && c.CommitterEmail == "" {
			c.CommitterName, c.CommitterEmail = identity(rest)
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("encoding ")); ok {
			c.Encodings = append(c.Encodings, string(rest))
		}
		other = append(other, line)
	}
	c.Headers = string(bytes.Join(other, []byte("\n")))
	return c, int64(len(o.data)), nil
}

func identity(line []byte) (string, string) {
	open := bytes.IndexByte(line, '<')
	end := bytes.LastIndexByte(line, '>')
	if open < 0 || end < open {
		return string(line), ""
	}
	name := bytes.TrimSpace(line[:open])
	if tail := bytes.TrimSpace(line[end+1:]); len(tail) > 0 {
		name = slices.Concat(name, []byte(" "), tail)
	}
	return string(name), string(line[open+1 : end])
}
