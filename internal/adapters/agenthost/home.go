package agenthost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	homeRoute     = "/v1/home-settings"
	maxHomeFile   = 1 << 20
	maxHomeRead   = 32 << 20
	maxHomeDepth  = 6
	maxHomeDir    = 2000
	maxMCPName    = 100
	mcpEntryName  = "MCP servers (claude mcp list)"
	mcpOtherName  = "MCP servers (claude mcp list, other lines)"
	maxMemoryDirs = 500
)

var homeSkipped = []string{".git", "node_modules"}

type homeJSON struct {
	MCP *commandJSON `json:"mcp,omitempty"`
}

type homeEntryJSON struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Hash string `json:"hash,omitempty"`
}

type homeReply struct {
	Entries []homeEntryJSON `json:"entries"`
}

type homeSource struct {
	base     string
	rel      string
	folder   bool
	metadata bool
}

type homeWalk struct {
	home    string
	entries []homeEntryJSON
	seen    map[string]bool
	read    int64
	stopped bool
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
	codex := filepath.Join(home, ".codex")
	if dir := os.Getenv("CODEX_HOME"); filepath.IsAbs(dir) {
		codex = filepath.Clean(dir)
	}
	out := []homeSource{
		{base: claude, rel: "settings.json"},
		{base: claude, rel: "CLAUDE.md"},
		{base: claude, rel: "agents", folder: true},
		{base: claude, rel: "skills", folder: true},
		{base: claude, rel: "commands", folder: true},
		{base: claude, rel: "hooks", folder: true},
		{base: codex, rel: "config.toml"},
		{base: codex, rel: "AGENTS.md"},
		{base: home, rel: ".gitconfig"},
		{base: home, rel: ".config/git/config"},
		{base: home, rel: ".npmrc", metadata: true},
		{base: home, rel: ".ssh", folder: true, metadata: true},
	}
	for _, name := range []string{".profile", ".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".zshenv", ".zprofile", ".zshrc", ".zlogin", ".config/fish/config.fish"} {
		out = append(out, homeSource{base: home, rel: name})
	}
	if root, err := openPlainFolder(claude); err == nil {
		projects, _ := readDirCapped(root, "projects", maxMemoryDirs)
		_ = root.Close()
		for _, p := range projects {
			if p.IsDir() && fs.ValidPath(p.Name()) && !strings.Contains(p.Name(), "/") {
				out = append(out, homeSource{base: claude, rel: path.Join("projects", p.Name(), "memory"), folder: true})
			}
		}
	}
	return out
}

func (w *homeWalk) add(name, kind, hash string) {
	if w.seen[name] {
		return
	}
	if len(w.entries) >= session.MaxHomeEntries {
		if !w.stopped {
			w.stopped = true
			w.entries = append(w.entries, homeEntryJSON{Name: fmt.Sprintf("(stopped after %d files)", session.MaxHomeEntries), Kind: session.HomeKindStop})
		}
		return
	}
	w.seen[name] = true
	w.entries = append(w.entries, homeEntryJSON{Name: name, Kind: kind, Hash: hash})
}

func shownName(home, base, rel string) string {
	return cleanName(homeShown(home, filepath.Join(base, filepath.FromSlash(rel))), rel)
}

func cleanName(name, salt string) string {
	name = strings.ToValidUTF8(name, "?")
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			b.WriteString("?")
			continue
		}
		b.WriteRune(r)
	}
	name = b.String()
	if len(name) > session.MaxHomeName {
		name = name[:session.MaxHomeName-16]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
		name += "…" + sum(salt)[:8]
	}
	return name
}

func metadataHash(info fs.FileInfo) string {
	return sum(info.Mode().String(), fmt.Sprint(info.Size()), fmt.Sprint(info.ModTime().UnixNano()))
}

func (w *homeWalk) file(root *os.Root, base, rel string, info fs.FileInfo, metadata bool) {
	name := shownName(w.home, base, rel)
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
	if metadata || !oneLink(info) || info.Size() > maxHomeFile || w.read+info.Size() > maxHomeRead {
		w.add(name, session.HomeKindMetadata, metadataHash(info))
		return
	}
	data, _, err := readPlain(root, rel, maxHomeFile)
	if err != nil {
		w.add(name, session.HomeKindUnread, "")
		return
	}
	w.read += int64(len(data))
	w.add(name, session.HomeKindContent, sum("content", session.MaskSecrets(string(data))))
}

func (w *homeWalk) folder(root *os.Root, base, rel string, depth int, metadata bool) {
	entries, err := readDirCapped(root, rel, maxHomeDir+1)
	if err != nil {
		w.add(shownName(w.home, base, rel)+"/", session.HomeKindUnread, "")
		return
	}
	if len(entries) > maxHomeDir {
		w.add(shownName(w.home, base, rel)+"/", session.HomeKindStop, sum("over", fmt.Sprint(maxHomeDir)))
		entries = entries[:maxHomeDir]
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, e := range entries {
		child := path.Join(rel, e.Name())
		info, err := root.Lstat(child)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if slices.Contains(homeSkipped, e.Name()) {
				w.add(shownName(w.home, base, child)+"/", session.HomeKindMetadata, metadataHash(info))
				continue
			}
			if depth >= maxHomeDepth {
				w.add(shownName(w.home, base, child)+"/", session.HomeKindStop, sum("deep"))
				continue
			}
			w.folder(root, base, child, depth+1, metadata)
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
		w.add(shownName(w.home, src.base, ".")+"/", session.HomeKindUnread, "")
		return
	}
	defer func() { _ = root.Close() }()
	parent := path.Dir(src.rel)
	if parent != "." {
		if _, err := plainFolder(root, parent); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				w.add(shownName(w.home, src.base, src.rel), session.HomeKindUnread, "")
			}
			return
		}
	}
	info, err := root.Lstat(src.rel)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		w.add(shownName(w.home, src.base, src.rel), session.HomeKindUnread, "")
		return
	}
	if src.folder && info.IsDir() {
		w.folder(root, src.base, src.rel, 1, src.metadata)
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
		w.add(cleanName("MCP server "+name, name), session.HomeKindMCP, sum("mcp", session.MaskSecrets(rest)))
	}
	if len(other) > 0 {
		w.add(mcpOtherName, session.HomeKindMCP, sum(append([]string{"mcp-other"}, other...)...))
	}
	if !listed && len(other) == 0 {
		w.add(mcpEntryName, session.HomeKindMCP, sum("mcp-none"))
	}
}

func (s *Server) homeSettings(ctx context.Context, q homeJSON) (homeReply, *problem) {
	home := os.Getenv("HOME")
	if !filepath.IsAbs(home) {
		return homeReply{}, fail(http.StatusConflict, "home", "The container has no usable home folder.")
	}
	home = filepath.Clean(home)
	w := &homeWalk{home: home, seen: map[string]bool{}}
	for _, src := range homeSources(home) {
		w.source(src)
	}
	if q.MCP != nil {
		res, p := s.run(ctx, *q.MCP)
		if p != nil || res.Code != 0 || res.TimedOut {
			w.add(mcpEntryName, session.HomeKindUnread, "")
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
		entries = append(entries, session.HomeEntry{Name: e.Name, Kind: e.Kind, Hash: e.Hash})
	}
	return entries, nil
}

var _ session.HomeReader = (*Client)(nil)
