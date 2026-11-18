package gate

import (
	"path"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

var agentConfigDirs = set(".codex", ".agents")

func ProtectedWrite(top, file string) string {
	if !path.IsAbs(file) || !path.IsAbs(top) {
		return "the path to write is relative"
	}
	root := strings.ToLower(path.Clean(top))
	p := strings.ToLower(path.Clean(file))
	if root != "/" {
		rest, ok := strings.CutPrefix(p, root+"/")
		if !ok {
			return ""
		}
		p = rest
	} else {
		p = strings.TrimPrefix(p, "/")
	}
	segments := strings.Split(p, "/")
	for _, s := range segments {
		if agentConfigDirs[s] {
			return "it is Codex CLI's configuration in the worktree, which decides what runs when Codex starts here"
		}
	}
	if segments[len(segments)-1] == ".env" {
		return "it is an environment file that agents load at start, which can change where their traffic goes"
	}
	return ""
}

func worktreePath(top, file string) (string, bool) {
	if !path.IsAbs(file) || !path.IsAbs(top) {
		return "", false
	}
	root := strings.ToLower(path.Clean(top))
	p := strings.ToLower(path.Clean(file))
	if root == "/" {
		return strings.TrimPrefix(p, "/"), true
	}
	return strings.CutPrefix(p, root+"/")
}

func GuardedWrite(top, file string, protect []string) string {
	if len(protect) == 0 {
		return ""
	}
	rel, ok := worktreePath(top, file)
	if !ok {
		return ""
	}
	for _, pattern := range protect {
		if workflow.PathMatches(pattern, rel, true) {
			return "it matches " + pattern + " in the protect list of the team file on the main branch, so the person answers every write to it"
		}
	}
	return ""
}

func LockedWrite(top, file string, locked map[string]bool) bool {
	if len(locked) == 0 {
		return false
	}
	rel, ok := worktreePath(top, file)
	return ok && locked[rel]
}
