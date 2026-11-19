package gate

import (
	"path"
	"slices"
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
	if slices.Contains(strings.Split(p, "/"), ".git") {
		return "it is git's own metadata, which the engine reads to find the session's commits"
	}
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
			return "it is a settings folder other coding tools read in the worktree, which can decide what runs when one of them starts here"
		}
	}
	if environmentFile(segments[len(segments)-1]) {
		return "it is an environment file that agents load at start, which can change where their traffic goes"
	}
	return ""
}

func environmentFile(name string) bool {
	return name == ".env" || name == ".envrc" || strings.HasPrefix(name, ".env.")
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

const SpecFolder = "proctor"

func SpecFolderPath(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return strings.EqualFold(first, SpecFolder)
}

func SpecMakefile(rel string) bool {
	return strings.EqualFold(rel, "Makefile") || strings.EqualFold(rel, "GNUmakefile")
}

func SpecPath(rel string) bool {
	return SpecFolderPath(rel) || SpecMakefile(rel)
}

func SpecLinkLeaves(rel, target string) bool {
	if target == "" || path.IsAbs(target) || strings.ContainsAny(target, "\x00\\") {
		return true
	}
	to := path.Join(path.Dir(rel), target)
	return to == ".." || strings.HasPrefix(to, "../") || !SpecFolderPath(to)
}

func SpecWrite(top, file string) string {
	rel, ok := worktreePath(top, file)
	switch {
	case !ok:
		return ""
	case SpecMakefile(rel):
		return "it is the root Makefile, whose proctor target is how make proctor runs Proctor's specifications, which decide what a pass means; the person answers every write to it"
	case SpecFolderPath(rel):
		return "it is under proctor/, Proctor's specifications on main, which decide what a pass means; the person answers every write to it"
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
