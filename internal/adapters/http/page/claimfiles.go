package page

import (
	"os"
	"path"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type TurnFiles struct {
	Workspace string
	OpenTree  func(path string) (*os.Root, error)
}

var _ session.TurnFiles = TurnFiles{}

func (t TurnFiles) Touched(dir string, names []string, since time.Time) (map[string]bool, error) {
	c, err := openCheckout(t.OpenTree, t.Workspace, dir)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := map[string]bool{}
	var bare []string
	for _, name := range names {
		if !strings.Contains(name, "/") {
			bare = append(bare, name)
			continue
		}
		rel, err := c.relative(name)
		if err != nil {
			continue
		}
		info, err := c.lstat(rel)
		if err != nil {
			continue
		}
		out[name] = !info.ModTime().Before(since)
	}
	if len(bare) == 0 {
		return out, nil
	}
	list, cut := c.walk()
	if cut {
		return out, nil
	}
	found := map[string][]string{}
	for _, rel := range list {
		found[path.Base(rel)] = append(found[path.Base(rel)], rel)
	}
	for _, name := range bare {
		paths := found[name]
		if len(paths) == 0 {
			continue
		}
		changed, known := false, true
		for _, rel := range paths {
			info, err := c.lstat(rel)
			if err != nil {
				known = false
				break
			}
			changed = changed || !info.ModTime().Before(since)
		}
		if known {
			out[name] = changed
		}
	}
	return out, nil
}
