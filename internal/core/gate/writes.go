package gate

import (
	"path"
	"strings"
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
