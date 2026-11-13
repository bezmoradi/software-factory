package files

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxEntryName    = 255
	maxPath         = 4096
	maxPathBytes    = 64 << 20
	maxTreeObject   = 8 << 20
	maxHeldTrees    = 64 << 20
	heldEntryCost   = 400
	maxCommitObject = 1 << 20
	maxTagObject    = 1 << 20
	maxKeptMessage  = 4 << 10
)

type entry struct {
	name string
	mode push.Mode
	sha  string
}

func (r *repo) commit(sha string) (push.Commit, error) {
	o, err := r.store.read(sha)
	if err != nil {
		return push.Commit{}, err
	}
	if o.kind != kindCommit {
		return push.Commit{}, fmt.Errorf("%w: %s is a %s, not a commit", push.ErrNotFound, sha, o.kind)
	}
	return parseCommit(sha, o.data)
}

func parseCommit(sha string, data []byte) (push.Commit, error) {
	if len(data) > maxCommitObject {
		return push.Commit{}, fmt.Errorf("%w: commit %s is over %d MiB, more than the engine reads in one commit", push.ErrTooLarge, sha, maxCommitObject>>20)
	}
	bad := fmt.Errorf("%w: commit %s can't be parsed", push.ErrCorrupt, sha)
	c := push.Commit{SHA: sha}
	header, message, _ := bytes.Cut(data, []byte("\n\n"))
	c.Message = string(clipMessage(message))
	lines := bytes.Split(header, []byte("\n"))
	tree, ok := bytes.CutPrefix(lines[0], []byte("tree "))
	if !ok || !validSHA(string(tree)) {
		return push.Commit{}, bad
	}
	c.Tree = string(tree)
	i := 1
	for ; i < len(lines); i++ {
		p, ok := bytes.CutPrefix(lines[i], []byte("parent "))
		if !ok {
			break
		}
		if !validSHA(string(p)) {
			return push.Commit{}, bad
		}
		c.Parents = append(c.Parents, string(p))
	}
	for ; i < len(lines); i++ {
		rest, ok := bytes.CutPrefix(lines[i], []byte("committer "))
		if !ok {
			continue
		}
		fields := bytes.Fields(rest[bytes.LastIndexByte(rest, '>')+1:])
		if len(fields) >= 1 {
			if secs, err := strconv.ParseInt(string(fields[0]), 10, 64); err == nil {
				c.Committed = time.Unix(secs, 0).UTC()
			}
		}
		break
	}
	return c, nil
}

func clipMessage(m []byte) []byte {
	if len(m) <= maxKeptMessage {
		return m
	}
	end := maxKeptMessage
	for end > 0 && !utf8.RuneStart(m[end]) {
		end--
	}
	return m[:end]
}

func linksOnly(c push.Commit) push.Commit {
	c.Message = ""
	return c
}

func (r *repo) tree(sha string) ([]entry, error) {
	o, err := r.store.read(sha)
	if err != nil {
		return nil, err
	}
	if o.kind != kindTree {
		return nil, fmt.Errorf("%w: %s is a %s, not a tree", push.ErrCorrupt, sha, o.kind)
	}
	return parseTree(sha, o.data)
}

func parseTree(sha string, d []byte) ([]entry, error) {
	if len(d) > maxTreeObject {
		return nil, fmt.Errorf("%w: tree %s is over %d MiB, more than the engine reads in one folder", push.ErrTooLarge, sha, maxTreeObject>>20)
	}
	bad := fmt.Errorf("%w: tree %s can't be parsed", push.ErrCorrupt, sha)
	var out []entry
	seen := map[string]bool{}
	for len(d) > 0 {
		space := bytes.IndexByte(d, ' ')
		if space <= 0 || space > 7 {
			return nil, bad
		}
		modeText := d[:space]
		d = d[space+1:]
		nul := bytes.IndexByte(d, 0)
		if nul <= 0 || nul+21 > len(d) {
			return nil, bad
		}
		if nul > maxEntryName {
			return nil, fmt.Errorf("%w: tree %s has a name longer than %d bytes", push.ErrTooLarge, sha, maxEntryName)
		}
		name := string(d[:nul])
		id := hex.EncodeToString(d[nul+1 : nul+21])
		d = d[nul+21:]
		if name == "." || name == ".." || strings.Contains(name, "/") || seen[name] {
			return nil, bad
		}
		seen[name] = true
		raw, err := strconv.ParseUint(string(modeText), 8, 32)
		if err != nil {
			return nil, bad
		}
		var mode push.Mode
		switch {
		case raw == 0o40000:
			mode = push.ModeFolder
		case raw == 0o120000:
			mode = push.ModeSymlink
		case raw == 0o160000:
			mode = push.ModeSubmodule
		case raw&0o170000 == 0o100000 && raw&0o100 != 0:
			mode = push.ModeExecutable
		case raw&0o170000 == 0o100000:
			mode = push.ModeFile
		default:
			return nil, bad
		}
		out = append(out, entry{name: name, mode: mode, sha: id})
	}
	return out, nil
}

func (r *repo) join(prefix, name string) (string, error) {
	n := len(prefix) + len(name)
	if n > maxPath {
		return "", fmt.Errorf("%w: a path in the tree is longer than %d bytes: %s", push.ErrTooLarge, maxPath, short(prefix))
	}
	if err := r.charge(n); err != nil {
		return "", err
	}
	return prefix + name, nil
}

func (r *repo) charge(n int) error {
	r.pathBytes += int64(n)
	if r.pathBytes > maxPathBytes {
		return fmt.Errorf("%w: the tree's paths add up to more than %d MiB", push.ErrTooLarge, maxPathBytes>>20)
	}
	return nil
}

func (r *repo) hold(list []entry) (func(), error) {
	var cost int64
	for _, e := range list {
		cost += int64(len(e.name)) + heldEntryCost
	}
	if r.heldBytes+cost > maxHeldTrees {
		return nil, fmt.Errorf("%w: the folders being read at once hold more than %d MiB of entries", push.ErrTooLarge, maxHeldTrees>>20)
	}
	r.heldBytes += cost
	return func() { r.heldBytes -= cost }, nil
}

func (r *repo) heldTree(sha string) ([]entry, func(), error) {
	list, err := r.tree(sha)
	if err != nil {
		return nil, nil, err
	}
	release, err := r.hold(list)
	if err != nil {
		return nil, nil, err
	}
	return list, release, nil
}

func (r *repo) blob(sha string) ([]byte, error) {
	o, err := r.store.read(sha)
	if err != nil {
		return nil, err
	}
	if o.kind != kindBlob {
		return nil, fmt.Errorf("%w: %s is a %s, not a blob", push.ErrCorrupt, sha, o.kind)
	}
	return o.data, nil
}
