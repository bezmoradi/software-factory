package session

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	maxTypePaths   = 50
	keptLoosenings = 50
)

type TypeFile struct {
	Path string
	What string
}

type TypeLine struct {
	What  string
	Files []TypeFile
	More  int
}

type TypeChange struct {
	Type  string
	Lines []TypeLine
}

type TypeChanges interface {
	TypeChange(ctx context.Context, sid, base, head string) (TypeChange, bool)
}

var _ TypeChanges = (*Work)(nil)

type loosening struct {
	files  []TypeFile
	unread int
}

func typeLine(what string, files []TypeFile) TypeLine {
	l := TypeLine{What: what, More: max(0, len(files)-maxTypePaths)}
	for _, f := range files[:min(len(files), maxTypePaths)] {
		l.Files = append(l.Files, TypeFile{Path: shownPath(f.Path), What: maskSecrets(f.What)})
	}
	return l
}

func fixName(kind workflow.WorkType) string {
	if kind.ID == "fix" {
		return "A Fix"
	}
	return "Work of type " + clip(kind.Name, 60) + ", which gets a Fix's checks,"
}

func (w *Work) TypeChange(ctx context.Context, sid, base, head string) (TypeChange, bool) {
	ws, ok := w.session(sid)
	if !ok || head == "" {
		return TypeChange{}, false
	}
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return TypeChange{}, false
	}
	setup := w.setupFor(ws.repo)
	kind, ok := typeIn(setup.setup.Types, ws.typeID)
	if !ok {
		kind, _ = typeIn(setup.setup.Types, DefaultWorkType)
	}
	changes, err := w.cfg.Files.ChangedFiles(ctx, t.Repo, base, head)
	main, _ := w.cfg.Repos.Main(t.Name)
	out := w.typeChange(t.Repo, kind, setup.setup.Protect, w.specsProtected(t, main, head), base, head, changes, err == nil)
	return out, len(out.Lines) > 0
}

func (w *Work) typeChange(repo string, kind workflow.WorkType, protect []string, specs bool, base, head string, changes []push.Change, listed bool) TypeChange {
	out := TypeChange{Type: kind.Name}
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	if kind.Edits == workflow.DocsEdits {
		if _, note := workflow.Effective(kind, paths, listed); !listed && note != "" {
			out.Lines = append(out.Lines, TypeLine{What: strings.TrimSuffix(note, ".")})
		}
		if others := workflow.NotDocs(paths); listed && len(others) > 0 {
			files := make([]TypeFile, 0, len(others))
			for _, p := range others {
				files = append(files, TypeFile{Path: p})
			}
			out.Lines = append(out.Lines, typeLine(clip(kind.Name, 60)+" is for documentation, and the branch changes files that aren't documentation, so it needs what a Feature needs: an approved plan and a Feature's reviews", files))
		}
	}
	if specs {
		var files []TypeFile
		for _, p := range paths {
			if gate.SpecPath(p) {
				files = append(files, TypeFile{Path: p})
			}
		}
		if len(files) > 0 {
			out.Lines = append(out.Lines, typeLine("Files under proctor/ or the root Makefile, Proctor's specifications and how make proctor runs them, which decide what a pass means: whatever the work type, each change to them holds the push until the person allows it on the page", files))
		}
	}
	if len(protect) > 0 {
		var files []TypeFile
		for _, p := range paths {
			if i := slices.IndexFunc(protect, func(pattern string) bool { return workflow.PathMatches(pattern, p, true) }); i >= 0 {
				files = append(files, TypeFile{Path: p, What: "matches " + protect[i]})
			}
		}
		if len(files) > 0 {
			out.Lines = append(out.Lines, typeLine("Files that match the protect list of the team file on main, which Claude Code's file tools ask the person about before each write; a shell command or another agent doesn't ask", files))
		}
	}
	if specs && listed && kind.Profile() == workflow.Fix {
		l := w.loosenings(repo, base, head, changes)
		what := fixName(kind) + " that loosens the specifications: in each file listed, the engine's line counts found new waivers, fewer scenarios or table rows, new declared reads or shows, or new expect_logs entries, which a fix rarely needs; the counts read lines, not YAML, so one can be wrong"
		if l.unread > 0 {
			what += "; " + strconv.Itoa(l.unread) + " more " + plural(l.unread, "specification", "specifications") + " the engine couldn't read or count for this may loosen them too"
		}
		if len(l.files) > 0 || l.unread > 0 {
			out.Lines = append(out.Lines, typeLine(what, l.files))
		}
	}
	return out
}

func (w *Work) loosenings(repo, base, head string, changes []push.Change) loosening {
	if w.cfg.Blobs == nil {
		n := 0
		for _, c := range changes {
			if gate.SpecFolderPath(c.Path) && specYAML(c.Path) {
				n++
			}
		}
		return loosening{unread: n}
	}
	key := repo + "\x00" + base + "\x00" + head
	w.looseMu.Lock()
	defer w.looseMu.Unlock()
	if l, ok := w.loose[key]; ok {
		return l
	}
	l, read := specLoosenings(w.cfg.Blobs, repo, changes)
	if !read {
		return l
	}
	w.loose[key] = l
	w.looseKeys = append(w.looseKeys, key)
	if len(w.looseKeys) > keptLoosenings {
		delete(w.loose, w.looseKeys[0])
		w.looseKeys = w.looseKeys[1:]
	}
	return l
}

func specLoosenings(files ChangeBlobs, repo string, changes []push.Change) (loosening, bool) {
	var out loosening
	var picked []push.Change
	var shas []string
	for _, c := range changes {
		if !gate.SpecFolderPath(c.Path) || !specYAML(c.Path) {
			continue
		}
		if len(picked) >= maxSpecFlags {
			out.unread++
			continue
		}
		picked = append(picked, c)
		if c.NewMode == push.ModeSubmodule || c.OldMode == push.ModeSubmodule || c.NewMode == push.ModeSymlink || c.OldMode == push.ModeSymlink {
			continue
		}
		for _, sha := range []string{c.OldSHA, c.NewSHA} {
			if sha != "" {
				shas = append(shas, sha)
			}
		}
	}
	blobs := map[string]BlobData{}
	read := true
	if len(shas) > 0 {
		var err error
		blobs, err = files.Blobs(repo, slices.Compact(slices.Sorted(slices.Values(shas))), maxScannedBlob, maxScannedBytes)
		read = err == nil
	}
	text := func(sha string) (string, bool) {
		if sha == "" {
			return "", true
		}
		b, ok := blobs[sha]
		if !ok || b.Cut {
			return "", false
		}
		return string(b.Data), true
	}
	for _, c := range picked {
		if c.NewMode == push.ModeSubmodule || c.OldMode == push.ModeSubmodule || c.NewMode == push.ModeSymlink || c.OldMode == push.ModeSymlink {
			out.unread++
			continue
		}
		before, okBefore := text(c.OldSHA)
		after, okAfter := text(c.NewSHA)
		if !okBefore || !okAfter {
			out.unread++
			continue
		}
		if notes := specLoosened(countSpec(before), countSpec(after)); len(notes) > 0 {
			out.files = append(out.files, TypeFile{Path: c.Path, What: strings.Join(notes, ", ")})
		}
	}
	return out, read
}
