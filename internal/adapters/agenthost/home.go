package agenthost

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	homeRoute         = "/v1/home-settings"
	maxHomeFile       = 1 << 20
	maxHomeRead       = 32 << 20
	maxHomeDepth      = 6
	maxHomeDir        = 2000
	maxMCPName        = 100
	mcpEntryName      = "MCP servers (claude mcp list)"
	mcpOtherName      = "MCP servers (claude mcp list, other lines)"
	maxMemoryDirs     = 500
	maxPluginDepth    = 10
	homeWalkFor       = 60 * time.Second
	maxTreeDepth      = 24
	maxTreeItems      = 20000
	homeShareItems    = 80
	homeShareBytes    = 1 << 20
	homeSingleItems   = 40
	homeSuffix        = 32
	maxPlugins        = 64
	maxPluginVersions = 8
)

var gitData = []string{"objects", "refs", "logs", "packed-refs", "index", "shallow", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_HEAD", "AUTO_MERGE"}

var homeSkipped = []string{".git", "node_modules"}

type homeJSON struct {
	MCP *commandJSON `json:"mcp,omitempty"`
}

type homeEntryJSON struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Hash  string `json:"hash,omitempty"`
	Class int    `json:"class,omitempty"`
}

type homeReply struct {
	Entries []homeEntryJSON `json:"entries"`
}

type homeSource struct {
	base     string
	rel      string
	folder   bool
	metadata bool
	depth    int
	group    string
	global   bool
	skip     []string
}

type homeWalk struct {
	ctx       context.Context
	home      string
	entries   []homeEntryJSON
	seen      map[string]bool
	read      int64
	trees     int
	stopped   bool
	group     string
	limit     int
	bytes     int64
	groupStop bool
	dup       bool
	paths     map[string]string
	classes   map[string]int
	named     map[string]bool
	claude    string
	skip      []string
	share     int
	shareB    int64
}

func (w *homeWalk) startGroup(group string, later int) {
	w.group, w.groupStop = group, false
	w.limit = session.MaxHomeEntries - later*w.share
	w.bytes = maxHomeRead - int64(later)*w.shareB
}

func (w *homeWalk) done() bool {
	if w.stopped || w.groupStop {
		return true
	}
	if w.ctx != nil && w.ctx.Err() != nil {
		w.stopped = true
		w.entries = append(w.entries, homeEntryJSON{Name: fmt.Sprintf("(stopped after %d seconds)", int(homeWalkFor.Seconds())), Kind: session.HomeKindStop})
		return true
	}
	return false
}

func sum(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s;", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func homeSources(home string) []homeSource {
	claude := filepath.Clean(claudeConfigDir(home))
	out := []homeSource{
		{base: claude, rel: "settings.json"},
		{base: claude, rel: "CLAUDE.md"},
		{base: home, rel: ".gitconfig", global: true},
		{base: home, rel: ".config/git/config", global: true},
		{base: home, rel: ".npmrc", metadata: true},
	}
	for _, name := range []string{".profile", ".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".zshenv", ".zprofile", ".zshrc", ".zlogin", ".config/fish/config.fish"} {
		out = append(out, homeSource{base: home, rel: name})
	}
	out = append(out,
		homeSource{base: claude, rel: "hooks", folder: true, group: "hooks"},
		homeSource{base: claude, rel: "rules", folder: true, group: "rules"},
		homeSource{base: claude, rel: "agents", folder: true, group: "agents"},
		homeSource{base: claude, rel: "commands", folder: true, group: "commands"},
		homeSource{base: claude, rel: "skills", folder: true, group: "skills"},
		homeSource{base: claude, rel: "output-styles", folder: true, group: "output-styles"},
		homeSource{base: home, rel: ".ssh", folder: true, metadata: true, group: ".ssh"},
	)
	out = append(out, pluginSources(claude)...)
	if root, err := openPlainFolder(claude); err == nil {
		projects, _ := readDirCapped(root, "projects", maxMemoryDirs)
		_ = root.Close()
		for _, p := range projects {
			if p.IsDir() && fs.ValidPath(p.Name()) && !strings.Contains(p.Name(), "/") {
				out = append(out, homeSource{base: claude, rel: path.Join("projects", p.Name(), "memory"), folder: true, group: "projects/*/memory"})
			}
		}
	}
	return out
}

var pluginFirst = []string{"hooks/hooks.json", ".mcp.json", ".lsp.json", ".claude-plugin/plugin.json", "plugin.json"}

func pluginSources(claude string) []homeSource {
	var out, rest []homeSource
	var skip []string
	root, err := openPlainFolder(claude)
	if err == nil {
		markets, _ := readDirCapped(root, "plugins/cache", maxPlugins)
		for _, m := range markets {
			if !m.IsDir() || len(skip) >= maxPlugins {
				continue
			}
			plugins, _ := readDirCapped(root, path.Join("plugins/cache", m.Name()), maxPlugins)
			for _, p := range plugins {
				if !p.IsDir() || len(skip) >= maxPlugins {
					continue
				}
				rel := path.Join("plugins/cache", m.Name(), p.Name())
				skip = append(skip, rel)
				versions, _ := readDirCapped(root, rel, maxPluginVersions)
				for _, v := range versions {
					if v.IsDir() {
						for _, f := range pluginFirst {
							out = append(out, homeSource{base: claude, rel: path.Join(rel, v.Name(), f), group: rel})
						}
					}
				}
				out = append(out, homeSource{base: claude, rel: rel, folder: true, depth: maxPluginDepth, group: rel})
			}
		}
		_ = root.Close()
	}
	rest = append(rest, homeSource{base: claude, rel: "plugins", folder: true, depth: maxPluginDepth, group: "plugins", skip: skip})
	return append(out, rest...)
}

func (w *homeWalk) add(name, kind, hash string) {
	if w.seen[name] {
		return
	}
	if w.stopped || w.groupStop {
		return
	}
	if len(w.entries) >= w.limit-1 {
		w.groupStop = true
		where := "the single files"
		if w.group != "" {
			where = "~/.claude/" + w.group + "/"
			if w.group == ".ssh" {
				where = "~/.ssh/"
			}
		}
		w.entries = append(w.entries, homeEntryJSON{Name: cleanName(fmt.Sprintf("(stopped after %d files, in %s", len(w.entries), where), where, ")"), Kind: session.HomeKindStop})
		return
	}
	w.seen[name] = true
	class := w.classes[name]
	if class == 0 {
		class = 1
	}
	w.entries = append(w.entries, homeEntryJSON{Name: name, Kind: kind, Hash: hash, Class: class})
}

func (w *homeWalk) addServer(name, kind, hash string) {
	if w.seen[name] || len(w.entries) >= session.MaxHomeEntries+8 {
		return
	}
	w.seen[name] = true
	w.entries = append(w.entries, homeEntryJSON{Name: name, Kind: kind, Hash: hash})
}

func (w *homeWalk) shown(base, rel, tail string) string {
	full := filepath.Join(base, filepath.FromSlash(rel))
	name := cleanName(homeShown(w.home, full), full, tail)
	if was, ok := w.paths[name]; ok && was != full && !w.dup {
		w.dup = true
		w.entries = append(w.entries, homeEntryJSON{Name: "(two items showed the same name)", Kind: session.HomeKindUnread})
	}
	w.paths[name] = full
	w.classes[name] = w.classOf(full)
	return name
}

var (
	tellsParts = []string{"rules", "agents", "commands", "skills", "output-styles"}
	imageExt   = []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp"}
	namesFiles = []string{"hooks.json", ".mcp.json", ".lsp.json", "plugin.json", "settings.json"}
)

func inTells(parts []string) bool {
	return slices.ContainsFunc(parts, func(p string) bool { return slices.Contains(tellsParts, p) })
}

func (w *homeWalk) classOf(full string) int {
	crel, err := filepath.Rel(w.claude, full)
	if w.named[full] || err != nil || crel == "." || strings.HasPrefix(crel, "..") {
		return 1
	}
	parts := strings.Split(filepath.ToSlash(crel), "/")
	base := strings.ToLower(parts[len(parts)-1])
	ext := path.Ext(base)
	switch {
	case parts[0] == "plugins" || parts[0] == "hooks" || ext == ".json":
		return 1
	case crel == "CLAUDE.md":
		return 2
	case parts[0] == "projects":
		return 3
	case slices.Contains(tellsParts, parts[0]) && ext == ".md":
		return 2
	case !inTells(parts) && (ext == ".md" || ext == ".txt" || slices.Contains(imageExt, ext)):
		return 3
	}
	return 1
}

func (w *homeWalk) noteNamed(full, text string) {
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return
	}
	root := filepath.Dir(full)
	if b := filepath.Base(root); b == "hooks" || b == ".claude-plugin" {
		root = filepath.Dir(root)
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, y := range x {
				visit(y)
			}
		case []any:
			for _, y := range x {
				visit(y)
			}
		case string:
			x = strings.NewReplacer(`"`, "", "'", "").Replace(x)
			for _, f := range strings.FieldsFunc(x, func(r rune) bool { return r == ' ' || r == '\t' || r == '=' || r == ';' || r == '&' || r == '|' }) {
				f = strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", root, "$CLAUDE_PLUGIN_ROOT", root, "${HOME}", w.home, "$HOME", w.home).Replace(f)
				if strings.HasPrefix(f, "~/") {
					f = filepath.Join(w.home, f[2:])
				}
				if strings.HasPrefix(f, "./") || strings.HasPrefix(f, "../") {
					f = filepath.Join(root, f)
				}
				if filepath.IsAbs(f) && len(w.named) < maxTreeItems {
					w.named[filepath.Clean(f)] = true
				}
			}
		}
	}
	visit(value)
}

func cleanName(name, salt, tail string) string {
	given := name
	name = strings.ToValidUTF8(name, "?")
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			b.WriteString("?")
			continue
		}
		b.WriteRune(r)
	}
	name = session.MaskSecrets(b.String())
	room := session.MaxHomeName - homeSuffix - len("…") - len(tail)
	changed := name != given
	if len(name) > room {
		name = name[:room]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
		changed = true
	}
	if changed {
		name += "…" + sum(salt)[:homeSuffix]
	}
	return name + tail
}

func metadataHash(info fs.FileInfo) string {
	return sum(info.Mode().String(), fmt.Sprint(info.Size()), fmt.Sprint(info.ModTime().UnixNano()))
}

func (w *homeWalk) file(root *os.Root, base, rel string, info fs.FileInfo, metadata bool) {
	name := w.shown(base, rel, "")
	if info.Mode().Perm()&0o111 != 0 {
		w.classes[name] = 1
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(rel)
		if err != nil {
			w.add(name, session.HomeKindUnread, "")
			return
		}
		w.add(name, session.HomeKindLink, sum("link", target))
		return
	case !info.Mode().IsRegular():
		w.add(name, session.HomeKindMetadata, sum(info.Mode().Type().String()))
		return
	}
	if metadata || !oneLink(info) || info.Size() > maxHomeFile || w.read+info.Size() > w.bytes {
		w.add(name, session.HomeKindMetadata, metadataHash(info))
		return
	}
	data, _, err := readPlain(root, rel, maxHomeFile)
	if err != nil {
		w.add(name, session.HomeKindUnread, "")
		return
	}
	w.read += int64(len(data))
	text := string(data)
	if slices.Contains(namesFiles, path.Base(rel)) {
		w.noteNamed(filepath.Join(base, filepath.FromSlash(rel)), text)
	}
	masked := session.MaskSecrets(text)
	kind := session.HomeKindContent
	if masked != text {
		kind = session.HomeKindMasked
	}
	w.add(name, kind, sum("content", masked))
}

var gitInclude = regexp.MustCompile(`(?i)include`)

func (w *homeWalk) global(root *os.Root, base, rel string, info fs.FileInfo) {
	name := w.shown(base, rel, "")
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() || !oneLink(info) || info.Size() > maxHomeFile || w.read+info.Size() > w.bytes {
		w.file(root, base, rel, info, false)
		return
	}
	data, _, err := readPlain(root, rel, maxHomeFile)
	if err != nil {
		w.add(name, session.HomeKindUnread, "")
		return
	}
	w.read += int64(len(data))
	text := string(data)
	masked := session.MaskSecrets(text)
	switch {
	case gitInclude.MatchString(text):
		w.add(name, session.HomeKindNames, sum("content", masked))
	case masked != text:
		w.add(name, session.HomeKindMasked, sum("content", masked))
	default:
		w.add(name, session.HomeKindContent, sum("content", masked))
	}
}

type treeSum struct {
	h      hash.Hash
	git    bool
	masked bool
	why    string
}

func (w *homeWalk) gitDir(root *os.Root, rel string) bool {
	if rel == "." {
		return true
	}
	if !strings.HasPrefix(rel, "modules/") && !strings.HasPrefix(rel, "worktrees/") {
		return false
	}
	info, err := root.Lstat(path.Join(rel, "HEAD"))
	return err == nil && info.Mode().IsRegular()
}

var (
	gitHeader = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9-]*)(?: "([^"\\]*)")?\]$`)
	gitLine   = regexp.MustCompile(`^[ \t]*([A-Za-z][A-Za-z0-9-]*) = ([^"\\#;]*)$`)
	gitURL    = regexp.MustCompile(`^https://[^\s]+$`)
)

var gitInert = map[string][]string{
	"core":       {"repositoryformatversion", "filemode", "bare", "logallrefupdates", "ignorecase", "precomposeunicode", "symlinks"},
	"remote":     {"url", "fetch", "promisor", "partialclonefilter"},
	"branch":     {"remote", "merge"},
	"extensions": {"objectformat"},
	"user":       {"name", "email"},
}

var gitSub = map[string]bool{"remote": true, "branch": true}

func gitConfigInert(text string) bool {
	if !utf8.ValidString(text) || strings.HasPrefix(text, "\ufeff") || strings.ContainsRune(text, '\r') {
		return false
	}
	section, name, sub := "", "", false
	remotes, safe := map[string]bool{}, map[string]bool{}
	var branches []string
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case trimmed == "":
			continue
		case trimmed[0] == '#' || trimmed[0] == ';':
			if strings.HasSuffix(line, "\\") {
				return false
			}
			continue
		case line[0] == '[':
			m := gitHeader.FindStringSubmatch(line)
			if m == nil {
				return false
			}
			section, name, sub = strings.ToLower(m[1]), m[2], strings.Contains(line, `"`)
			if _, ok := gitInert[section]; !ok || sub != gitSub[section] {
				return false
			}
			if section == "remote" {
				remotes[name] = true
			}
			continue
		}
		m := gitLine.FindStringSubmatch(line)
		if m == nil || section == "" {
			return false
		}
		key, value := strings.ToLower(m[1]), strings.TrimSpace(m[2])
		if !slices.Contains(gitInert[section], key) {
			return false
		}
		switch {
		case section == "remote" && key == "url":
			if !gitURL.MatchString(value) {
				return false
			}
			safe[name] = true
		case section == "branch" && key == "remote":
			branches = append(branches, value)
		}
	}
	for r := range remotes {
		if !safe[r] {
			return false
		}
	}
	for _, b := range branches {
		if !safe[b] {
			return false
		}
	}
	return true
}

func (w *homeWalk) treeFile(root *os.Root, rel, shown string, t *treeSum, gitHere bool) {
	info, err := root.Lstat(rel)
	switch {
	case err != nil:
		t.why = "unread"
	case info.Mode()&fs.ModeSymlink != 0:
		if t.git {
			t.why = "link"
			return
		}
		target, err := root.Readlink(rel)
		if err != nil {
			t.why = "unread"
			return
		}
		if _, err := root.Stat(rel); err != nil {
			t.why = "link"
			return
		}
		_, _ = fmt.Fprintf(t.h, "%s", sum("link", shown, target))
	case info.IsDir():
		_, _ = fmt.Fprintf(t.h, "%s", sum("folder", shown))
	case !info.Mode().IsRegular():
		_, _ = fmt.Fprintf(t.h, "%s", sum("other", shown, info.Mode().Type().String()))
	case gitHere && path.Base(rel) == "commondir":
		t.why = "names"
	case !oneLink(info) || info.Size() > maxHomeFile || w.read+info.Size() > w.bytes:
		t.why = "metadata"
	default:
		data, _, err := readPlain(root, rel, maxHomeFile)
		if err != nil {
			t.why = "unread"
			return
		}
		w.read += int64(len(data))
		text := string(data)
		if gitHere && (path.Base(rel) == "config" || path.Base(rel) == "config.worktree") && !gitConfigInert(text) {
			t.why = "names"
			return
		}
		masked := session.MaskSecrets(text)
		t.masked = t.masked || masked != text
		_, _ = fmt.Fprintf(t.h, "%s", sum("file", shown, info.Mode().Perm().String(), masked))
	}
}

func (w *homeWalk) treeWalk(root *os.Root, rel string, depth int, t *treeSum) {
	if t.why != "" {
		return
	}
	if w.done() {
		t.why = "stop"
		return
	}
	if depth > maxTreeDepth {
		t.why = "stop"
		return
	}
	entries, err := readDirCapped(root, rel, maxHomeDir+1)
	if err != nil || len(entries) > maxHomeDir {
		t.why = "stop"
		return
	}
	gitHere := t.git && w.gitDir(root, rel)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, e := range entries {
		child := path.Join(rel, e.Name())
		if gitHere && slices.Contains(gitData, e.Name()) {
			if info, err := root.Lstat(child); err != nil || !info.IsDir() && !info.Mode().IsRegular() {
				t.why = "link"
				return
			}
			continue
		}
		if w.trees++; w.trees > maxTreeItems {
			t.why = "stop"
			return
		}
		w.treeFile(root, child, child, t, gitHere)
		if t.why != "" {
			return
		}
		if info, err := root.Lstat(child); err == nil && info.IsDir() {
			w.treeWalk(root, child, depth+1, t)
		}
	}
}

func (w *homeWalk) tree(root *os.Root, base, rel string, info fs.FileInfo, git bool) {
	name := w.shown(base, rel, "/")
	sub, err := root.OpenRoot(rel)
	if err != nil {
		w.add(name, session.HomeKindUnread, "")
		return
	}
	defer func() { _ = sub.Close() }()
	t := &treeSum{h: sha256.New(), git: git}
	w.treeWalk(sub, ".", 1, t)
	switch {
	case t.why == "unread":
		w.add(name, session.HomeKindUnread, "")
	case t.why == "link":
		w.add(name, session.HomeKindLink, sum("tree-link", metadataHash(info)))
	case t.why == "names":
		w.add(name, session.HomeKindNames, sum("tree-names", metadataHash(info)))
	case t.why == "stop":
		w.add(name, session.HomeKindStop, sum("tree", fmt.Sprint(maxTreeItems)))
	case t.why == "metadata":
		w.add(name, session.HomeKindMetadata, metadataHash(info))
	case t.masked:
		w.add(name, session.HomeKindMasked, sum("tree", hex.EncodeToString(t.h.Sum(nil))))
	default:
		w.add(name, session.HomeKindContent, sum("tree", hex.EncodeToString(t.h.Sum(nil))))
	}
}

func (w *homeWalk) folder(root *os.Root, base, rel string, depth, limit int, metadata bool) {
	if w.done() {
		return
	}
	entries, err := readDirCapped(root, rel, maxHomeDir+1)
	if err != nil {
		w.add(w.shown(base, rel, "/"), session.HomeKindUnread, "")
		return
	}
	if len(entries) > maxHomeDir {
		w.add(w.shown(base, rel, "/"), session.HomeKindStop, sum("over", fmt.Sprint(maxHomeDir)))
		entries = entries[:maxHomeDir]
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, e := range entries {
		child := path.Join(rel, e.Name())
		if slices.Contains(w.skip, child) {
			continue
		}
		info, err := root.Lstat(child)
		if err != nil {
			continue
		}
		if info.IsDir() {
			switch {
			case slices.Contains(homeSkipped, e.Name()) && metadata:
				w.add(w.shown(base, child, "/"), session.HomeKindMetadata, metadataHash(info))
			case e.Name() == ".git":
				w.tree(root, base, child, info, true)
			case slices.Contains(homeSkipped, e.Name()):
				w.tree(root, base, child, info, false)
			case depth >= limit:
				w.add(w.shown(base, child, "/"), session.HomeKindStop, sum("deep"))
			default:
				w.folder(root, base, child, depth+1, limit, metadata)
			}
			continue
		}
		if w.done() {
			return
		}
		if e.Name() == ".git" && info.Mode().IsRegular() {
			w.add(w.shown(base, child, ""), session.HomeKindNames, metadataHash(info))
			continue
		}
		w.file(root, base, child, info, metadata)
	}
}

func (w *homeWalk) source(src homeSource) {
	root, err := openPlainFolder(src.base)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		w.add(w.shown(src.base, ".", "/"), session.HomeKindUnread, "")
		return
	}
	defer func() { _ = root.Close() }()
	parent := path.Dir(src.rel)
	if parent != "." {
		if _, err := plainFolder(root, parent); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				w.add(w.shown(src.base, src.rel, ""), session.HomeKindUnread, "")
			}
			return
		}
	}
	info, err := root.Lstat(src.rel)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		w.add(w.shown(src.base, src.rel, ""), session.HomeKindUnread, "")
		return
	}
	if src.folder && info.IsDir() {
		w.skip = src.skip
		w.folder(root, src.base, src.rel, 1, cmp.Or(src.depth, maxHomeDepth), src.metadata)
		w.skip = nil
		return
	}
	if src.global {
		w.global(root, src.base, src.rel, info)
		return
	}
	w.file(root, src.base, src.rel, info, src.metadata)
}

func plainFolder(root *os.Root, rel string) (fs.FileInfo, error) {
	parts := strings.Split(rel, "/")
	var info fs.FileInfo
	for i := range parts {
		var err error
		info, err = root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errNotPlain
		}
	}
	return info, nil
}

func (w *homeWalk) mcp(out string) {
	listed := false
	var other []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Checking MCP server health") {
			continue
		}
		name, rest, ok := strings.Cut(line, ": ")
		if !ok || name == "" || len(name) > maxMCPName || strings.ContainsAny(name, " \t") {
			other = append(other, session.MaskSecrets(line))
			continue
		}
		if i := strings.LastIndex(rest, " - "); i >= 0 {
			rest = rest[:i]
		}
		listed = true
		w.addServer(cleanName("MCP server "+name, name, ""), session.HomeKindMCP, sum("mcp", session.MaskSecrets(rest)))
	}
	if len(other) > 0 {
		w.addServer(mcpOtherName, session.HomeKindMCP, sum(append([]string{"mcp-other"}, other...)...))
	}
	if !listed && len(other) == 0 {
		w.addServer(mcpEntryName, session.HomeKindMCP, sum("mcp-none"))
	}
}

func (s *Server) homeSettings(ctx context.Context, q homeJSON) (homeReply, *problem) {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) {
		return homeReply{}, fail(http.StatusConflict, "home", "The container has no usable home folder.")
	}
	home = filepath.Clean(home)
	wctx, cancel := context.WithTimeout(ctx, homeWalkFor)
	defer cancel()
	w := &homeWalk{ctx: wctx, home: home, seen: map[string]bool{}, paths: map[string]string{}, classes: map[string]int{}, named: map[string]bool{}, claude: filepath.Clean(claudeConfigDir(home))}
	sources := homeSources(home)
	var groups []string
	for _, src := range sources {
		if src.group != "" && !slices.Contains(groups, src.group) {
			groups = append(groups, src.group)
		}
	}
	w.share = min(homeShareItems, (session.MaxHomeEntries-homeSingleItems)/(len(groups)+1))
	w.shareB = min(homeShareBytes, maxHomeRead/2/int64(len(groups)+1))
	w.startGroup("", len(groups))
	for _, src := range sources {
		if src.group != w.group {
			w.startGroup(src.group, len(groups)-1-slices.Index(groups, src.group))
		}
		if w.stopped {
			break
		}
		if !w.groupStop {
			w.source(src)
		}
	}
	if q.MCP != nil {
		res, p := s.run(ctx, *q.MCP)
		if p != nil || res.Code != 0 || res.TimedOut {
			w.addServer(mcpEntryName, session.HomeKindUnread, "")
		} else {
			w.mcp(res.Stdout)
		}
	}
	s.log.Info("agent settings listed", "entries", len(w.entries))
	return homeReply{Entries: w.entries}, nil
}

func (c *Client) HomeSettings(ctx context.Context, mcp *session.Command) ([]session.HomeEntry, error) {
	q := homeJSON{}
	if mcp != nil {
		cmd := wire(*mcp)
		q.MCP = &cmd
	}
	var out homeReply
	if err := c.call(ctx, http.MethodPost, homeRoute, q, &out); err != nil {
		return nil, err
	}
	if len(out.Entries) > session.MaxHomeEntries+8 {
		return nil, &session.HostError{Code: "reply", Message: "agent-host answered more settings files than the engine reads"}
	}
	entries := make([]session.HomeEntry, 0, len(out.Entries))
	for _, e := range out.Entries {
		entries = append(entries, session.HomeEntry{Name: e.Name, Kind: e.Kind, Hash: e.Hash, Class: e.Class})
	}
	return entries, nil
}

var _ session.HomeReader = (*Client)(nil)
