package session

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	ProctorConfig   = gate.SpecFolder + "/proctor.yaml"
	maxProctorProbe = 1 << 10
	maxLinkTarget   = 4 << 10

	makefileWhy = "it is the root Makefile, whose proctor target is how make proctor runs Proctor's specifications, so a change to it can change what a pass means: a commit the push sends adds, changes, deletes or renames it, so the person allows each change"
	linkWhy     = "it is a symbolic link under proctor/ whose target leaves proctor/, or can't be read, so Proctor would read a file this hold doesn't cover; it can't be allowed, only taken out of the branch"
	makeLinkWhy = "it is the root Makefile as a symbolic link, so make proctor would run a file this hold doesn't cover; it can't be allowed, only taken out of the branch"
	deletedWhy  = "; this push deletes it"
)

type SpecProjects interface {
	Specs(ctx context.Context, sid, head string) bool
}

var _ SpecProjects = (*Work)(nil)

var (
	specWaiver     = regexp.MustCompile(`(^|[\s{,])["']?(no_state_check|waive)["']?\s*:`)
	specExpectLogs = regexp.MustCompile(`^\s*(-\s+)?expect_logs\s*:\s*(.*)$`)
	specShowsFlow  = regexp.MustCompile(`\bshows\s*:\s*\[([^\]]*)\]`)
	specScenarios  = regexp.MustCompile(`^scenarios\s*:\s*(#.*)?$`)
	specRows       = regexp.MustCompile(`^\s*(-\s+)?rows\s*:\s*(#.*)?$`)
	specReads      = regexp.MustCompile(`^\s*(-\s+)?reads\s*:\s*(#.*)?$`)
	specShows      = regexp.MustCompile(`^\s*(-\s+)?shows\s*:\s*(#.*)?$`)
	specLogsBlock  = regexp.MustCompile(`^\s*(-\s+)?expect_logs\s*:\s*(#.*)?$`)
	specConfig     = regexp.MustCompile(`(?i)^proctor/((proctor|manifest|dimensions|mutants)\.yaml|env/.+)$`)
	specApproved   = regexp.MustCompile(`(?i)^proctor/approved/`)
)

func (w *Work) proctorAt(repo, commit string) bool {
	if commit == "" {
		return false
	}
	var err error
	if w.cfg.Bounded != nil {
		_, err = w.cfg.Bounded.ReadFileWithin(repo, commit, ProctorConfig, maxProctorProbe)
	} else {
		_, err = w.cfg.Files.ReadFile(repo, commit, ProctorConfig)
	}
	return !errors.Is(err, push.ErrNotFound)
}

func specWhy(p string) string {
	if gate.SpecMakefile(p) {
		return makefileWhy
	}
	return flagWhy[FlagSpec]
}

func specAlso(p string) string {
	if gate.SpecMakefile(p) {
		return "; it is also the root Makefile, whose proctor target runs Proctor's specifications, so the person allows each change to it"
	}
	return "; it is also a Proctor specification under proctor/, which decides what a pass means, so the person allows each change to it"
}

func (w *Work) linkLeaves(repo, commit, p string) bool {
	var f push.File
	var err error
	if w.cfg.Bounded != nil {
		f, err = w.cfg.Bounded.ReadFileWithin(repo, commit, p, maxLinkTarget)
	} else {
		f, err = w.cfg.Files.ReadFile(repo, commit, p)
	}
	return err != nil || f.Mode != push.ModeSymlink || gate.SpecLinkLeaves(p, string(f.Data))
}

func (w *Work) withSpecs(t PushTarget, head string, sc scanned, net []push.Change) scanned {
	out := scanned{complete: sc.complete, lfs: sc.lfs, linkOver: sc.linkOver, failed: sc.failed}
	byPath := map[string]int{}
	over := map[string]bool{}
	for _, p := range sc.specOver {
		over[p] = true
	}
	specCount := 0
	var commits []Flag
	for _, f := range sc.flags {
		if f.Commit != "" {
			commits = append(commits, f)
			continue
		}
		if gate.SpecPath(f.Path) {
			specCount++
		}
		f.Versions = slices.Clone(f.Versions)
		byPath[f.Path] = len(out.flags)
		out.flags = append(out.flags, f)
	}
	add := func(p, why, sha string, link func() bool) {
		i, ok := byPath[p]
		if !ok && (over[p] || specCount >= maxSpecFlags) {
			over[p] = true
			if link != nil {
				out.linkOver = true
			}
			return
		}
		if !ok {
			specCount++
			byPath[p] = len(out.flags)
			out.flags = append(out.flags, Flag{Path: p, Kind: FlagSpec, Why: why})
			i = len(out.flags) - 1
		}
		fl := &out.flags[i]
		if link != nil && !fl.Link && link() {
			fl.Link = true
		}
		addVersion(fl, sha)
	}
	for _, c := range net {
		p := c.Path
		switch {
		case !gate.SpecPath(p):
		case c.Status == push.Deleted:
			add(p, specWhy(p)+deletedWhy, absentBlob, nil)
		case c.NewSHA == "" || c.NewMode == push.ModeFolder:
		case c.NewMode == push.ModeSymlink:
			add(p, specWhy(p), c.NewSHA, func() bool { return gate.SpecMakefile(p) || w.linkLeaves(t.Repo, head, p) })
		default:
			add(p, specWhy(p), c.NewSHA, nil)
		}
	}
	out.specOver = slices.Sorted(maps.Keys(over))
	for i := range out.flags {
		fl := &out.flags[i]
		if !gate.SpecPath(fl.Path) {
			continue
		}
		if fl.Kind != FlagSpec {
			fl.Why += specAlso(fl.Path)
		}
		if fl.Link {
			why := linkWhy
			if gate.SpecMakefile(fl.Path) {
				why = makeLinkWhy
			}
			if fl.Kind == FlagSpec {
				fl.Why = why
			} else {
				fl.Why += "; " + why
			}
		}
	}
	slices.SortFunc(out.flags, func(a, b Flag) int { return strings.Compare(a.Path, b.Path) })
	out.flags = append(out.flags, commits...)
	return out
}

func linkRefusal(g gathered) string {
	if !g.specs || g.head == "" {
		return ""
	}
	if !g.listed || g.scan.failed {
		return "this project uses Proctor, and the engine couldn't list this change or read the commits the push sends, so it couldn't check them for a symbolic link under proctor/ that leaves it or a root Makefile that is a link; it pushes nothing it couldn't check, and the person's Allow doesn't change that: have the agent make the change smaller, or fix what the page names, then push again"
	}
	var shown []string
	for _, f := range g.flags {
		if f.Link && f.Commit == "" {
			shown = append(shown, f.Label())
		}
	}
	if len(shown) == 0 && !g.scan.linkOver {
		return ""
	}
	more := ""
	if len(shown) > 5 {
		more = " and " + strconv.Itoa(len(shown)-5) + " more"
		shown = shown[:5]
	}
	if g.scan.linkOver {
		shown = append(shown, "one among the specification files past the "+strconv.Itoa(maxSpecFlags)+" the gate lists")
	}
	return "this push carries a symbolic link under proctor/ whose target leaves proctor/, or a root Makefile that is a symbolic link (" + strings.Join(shown, ", ") + more + "), so Proctor would read, or make proctor would run, a file the hold on its specifications doesn't cover; the engine pushes no commit that holds one, and neither the person's Allow nor an allowed file changes that: have the agent take it out of every commit on the branch"
}

func specOverItem(g gathered) string {
	if len(g.scan.specOver) == 0 {
		return ""
	}
	listed := 0
	for _, f := range g.scan.flags {
		if f.Commit == "" && gate.SpecPath(f.Path) {
			listed++
		}
	}
	all := listed + len(g.scan.specOver)
	return "the developer's decision on Proctor's specifications: this push changes " + strconv.Itoa(all) + " files under proctor/ or the root Makefile, more than the " + strconv.Itoa(maxSpecFlags) + " the gate lists one by one, so " + strconv.Itoa(len(g.scan.specOver)) + " of them aren't listed and can't be allowed one by one: have the agent make the change smaller, or decide on the whole push with your Allow and reason"
}

func (w *Work) flagDigest(f Flag) string {
	versions := slices.Sorted(slices.Values(f.Versions))
	m := hmac.New(sha256.New, w.digestKey[:])
	m.Write([]byte(f.Path + "\x00" + f.Commit + "\x00" + string(f.Field) + "\x00" + strings.Join(versions, ",") + "\x00" + strconv.FormatBool(f.Full) + strconv.FormatBool(f.Link)))
	return hex.EncodeToString(m.Sum(nil))
}

func (w *Work) specsProtected(t PushTarget, main, head string) bool {
	return t.Project != "" && w.proctorAt(t.Project, main) || w.proctorAt(t.Repo, main) || w.proctorAt(t.Repo, head)
}

func (w *Work) Specs(_ context.Context, sid, head string) bool {
	t, ok := w.cfg.Targets(sid)
	if !ok {
		return false
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	return w.specsProtected(t, main, head)
}

func (w *Work) specWrite(t PushTarget, top string, paths []string) string {
	why := ""
	for _, p := range paths {
		if why = gate.SpecWrite(top, p); why != "" {
			break
		}
	}
	if why == "" {
		return ""
	}
	main, _ := w.cfg.Repos.Main(t.Name)
	head := ""
	if w.cfg.Heads != nil {
		head, _ = w.cfg.Heads.Head(t.Repo, t.Branch)
	}
	if !w.specsProtected(t, main, head) {
		return ""
	}
	return why
}

func yamlItems(text string, key *regexp.Regexp, top bool) int {
	n := 0
	in, keyIndent, itemIndent := false, 0, -1
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		item := trimmed == "-" || strings.HasPrefix(trimmed, "- ")
		if in {
			switch {
			case itemIndent < 0 && item && indent >= keyIndent:
				itemIndent = indent
				n++
				continue
			case itemIndent >= 0 && item && indent == itemIndent:
				n++
				continue
			case itemIndent >= 0 && indent > itemIndent:
				continue
			}
			in = false
		}
		if (!top || indent == 0) && key.MatchString(line) {
			in, keyIndent, itemIndent = true, indent, -1
		}
	}
	return n
}

func flowShows(text string) int {
	n := 0
	for _, m := range specShowsFlow.FindAllStringSubmatch(text, -1) {
		for part := range strings.SplitSeq(m[1], ",") {
			if strings.TrimSpace(part) != "" {
				n++
			}
		}
	}
	return n
}

func flowLogs(text string) int {
	n := 0
	for line := range strings.SplitSeq(text, "\n") {
		if m := specExpectLogs.FindStringSubmatch(line); m != nil && strings.HasPrefix(strings.TrimSpace(m[2]), "[") {
			n += strings.Count(m[2], "{")
		}
	}
	return n
}

type specCounts struct {
	waivers, scenarios, rows, reads, shows, logs int
}

func countSpec(text string) specCounts {
	return specCounts{
		waivers:   countMatches(specWaiver, text),
		scenarios: yamlItems(text, specScenarios, true),
		rows:      yamlItems(text, specRows, false),
		reads:     yamlItems(text, specReads, false),
		shows:     yamlItems(text, specShows, false) + flowShows(text),
		logs:      yamlItems(text, specLogsBlock, false) + flowLogs(text),
	}
}

func specStatus(s push.Status) string {
	switch s {
	case push.Added:
		return "added"
	case push.Deleted:
		return "removed"
	}
	return "changed"
}

func specNotes(c push.Change, before, after string, read bool) []ChangeNote {
	if !gate.SpecPath(c.Path) {
		return nil
	}
	var out []ChangeNote
	note := func(what string) { out = append(out, ChangeNote{Path: c.Path, What: what}) }
	if specConfig.MatchString(c.Path) {
		note("Proctor's configuration or run scripts were " + specStatus(c.Status))
	}
	if specApproved.MatchString(c.Path) {
		note("approved transcript or log changed")
	}
	if gate.SpecMakefile(c.Path) {
		note("the root Makefile, whose proctor target runs Proctor's specifications, was " + specStatus(c.Status))
		return out
	}
	if !read {
		note("couldn't read; treat as changed")
		return out
	}
	if !specYAML(c.Path) {
		return out
	}
	counted := len(out)
	for _, what := range specLoosened(countSpec(before), countSpec(after)) {
		note(what)
	}
	if len(out) == counted {
		note("Proctor specification " + specStatus(c.Status) + "; the engine's line counts found no new waiver, fewer scenarios or table rows, or new reads or expect_logs entries, but its counts read lines, not YAML, so a count of 0 doesn't prove there are none")
	}
	return out
}

func specYAML(p string) bool {
	return strings.EqualFold(path.Ext(p), ".yaml") || strings.EqualFold(path.Ext(p), ".yml")
}

func specLoosened(was, now specCounts) []string {
	var out []string
	if d := now.waivers - was.waivers; d > 0 {
		out = append(out, strconv.Itoa(d)+" new "+plural(d, "waiver", "waivers"))
	}
	if d := max(0, was.scenarios-now.scenarios) + max(0, was.rows-now.rows); d > 0 {
		out = append(out, strconv.Itoa(d)+" fewer scenarios or table rows")
	}
	if d := max(0, now.reads-was.reads) + max(0, now.shows-was.shows); d > 0 {
		out = append(out, strconv.Itoa(d)+" new declared reads or shows")
	}
	if d := now.logs - was.logs; d > 0 {
		out = append(out, strconv.Itoa(d)+" new expect_logs "+plural(d, "entry", "entries"))
	}
	return out
}
