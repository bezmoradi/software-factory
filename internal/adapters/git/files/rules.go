package files

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

type rulesWalk struct {
	r       *repo
	list    session.InstructionList
	out     session.RuleSet
	seen    map[string]bool
	folders int
	bytes   int
	full    bool
}

func (v Reviews) Rules(repoPath, commit string) (session.RuleSet, error) {
	return with(repoPath, func(r *repo) (session.RuleSet, error) {
		c, err := r.commit(commit)
		if err != nil {
			return session.RuleSet{}, err
		}
		w := &rulesWalk{r: r, list: session.InstructionLists[session.RulesInstructions], seen: map[string]bool{}}
		if err := w.visit(c.Tree, "", 0); err != nil {
			return session.RuleSet{}, err
		}
		return w.out, nil
	})
}

func (w *rulesWalk) note(s string) {
	if !slices.Contains(w.out.Notes, s) {
		w.out.Notes = append(w.out.Notes, s)
	}
}

func (w *rulesWalk) visit(tree, prefix string, depth int) error {
	if w.full {
		return nil
	}
	if w.folders++; w.folders > session.MaxWalkFolders {
		w.full = true
		w.note(fmt.Sprintf("the search stopped after %d folders", session.MaxWalkFolders))
		return nil
	}
	list, release, err := w.r.heldTree(tree)
	if err != nil {
		return err
	}
	defer release()
	if len(list) > session.MaxWalkEntries {
		w.note(fmt.Sprintf("a folder holding more than %d entries wasn't searched", session.MaxWalkEntries))
		return nil
	}
	for _, e := range list {
		if w.full {
			return nil
		}
		if dotGit(e.name) || !utf8.ValidString(e.name) || strings.ContainsAny(e.name, "\\/\x00") || e.name == "." || e.name == ".." {
			continue
		}
		child := prefix + e.name
		switch e.mode {
		case push.ModeFolder:
			if session.WalkSkips(e.name) {
				continue
			}
			if depth+1 > session.MaxWalkDepth {
				w.note(fmt.Sprintf("folders nested deeper than %d weren't searched", session.MaxWalkDepth))
				continue
			}
			if err := w.visit(e.sha, child+"/", depth+1); err != nil {
				return err
			}
		case push.ModeFile, push.ModeExecutable:
			if w.list.Matches(child) {
				w.add(child, e.sha)
			}
		case push.ModeSymlink:
			if w.list.Matches(child) {
				w.note(session.Escape([]byte(child)) + " is a symbolic link, so it isn't copied")
			}
		}
	}
	return nil
}

func (w *rulesWalk) add(rel, sha string) {
	shown := session.Escape([]byte(rel))
	if w.seen[strings.ToLower(rel)] {
		w.note(shown + " differs only in case from another instruction file, so it isn't copied")
		return
	}
	if len(w.out.Files) >= session.MaxInstructionFiles {
		w.full = true
		w.note(fmt.Sprintf("there are more than %d instruction files: %s and the ones after it aren't copied", session.MaxInstructionFiles, shown))
		return
	}
	data, err := w.r.blob(sha)
	if err != nil {
		w.note(shown + " couldn't be read")
		return
	}
	if len(data) > session.MaxStartupFile {
		w.note(fmt.Sprintf("%s is larger than %d KiB and isn't copied", shown, session.MaxStartupFile>>10))
		return
	}
	if w.bytes += len(data); w.bytes > session.MaxInstructionBytes {
		w.full = true
		w.note(fmt.Sprintf("the instruction files hold more than %d MiB: %s and the ones after it aren't copied", session.MaxInstructionBytes>>20, shown))
		return
	}
	w.seen[strings.ToLower(rel)] = true
	w.out.Files = append(w.out.Files, session.RuleFile{Path: rel, Data: data})
}

func rulesPath(name string) (string, bool) {
	rel, ok := strings.CutPrefix(name, session.RulesDir+"/")
	if !ok || rel == "" || path.Clean(rel) != rel || path.IsAbs(rel) || !utf8.ValidString(rel) || strings.ContainsAny(rel, "\\\x00") {
		return "", false
	}
	parts := strings.Split(rel, "/")
	if len(parts) > session.MaxWalkDepth+1 {
		return "", false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || dotGit(p) {
			return "", false
		}
	}
	return rel, true
}

func writeRule(dir, rel, text string) ([]string, error) {
	if len(text) > session.MaxStartupFile {
		return nil, fmt.Errorf("an instruction file is larger than %d KiB", session.MaxStartupFile>>10)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var made []string
	at := session.RulesDir
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		at = path.Join(at, p)
		info, err := root.Lstat(filepath.FromSlash(at))
		switch {
		case err == nil && info.IsDir():
			continue
		case err == nil:
			return nil, fmt.Errorf("%s isn't a folder", session.Escape([]byte(at)))
		case !os.IsNotExist(err):
			return nil, err
		}
		if err := root.Mkdir(filepath.FromSlash(at), 0o755); err != nil {
			return nil, err
		}
		made = append(made, filepath.Join(dir, filepath.FromSlash(at)))
	}
	name := filepath.FromSlash(path.Join(session.RulesDir, rel))
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return made, root.Chmod(name, 0o644)
}
