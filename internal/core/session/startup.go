package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type StartupPlace string

const (
	InWorktree StartupPlace = "worktree"
	InClone    StartupPlace = "clone"
)

type StartupEntry struct {
	Path   string
	Folder bool
	Place  StartupPlace
	Why    string
	Source string
}

var StartupLists = map[string][]StartupEntry{
	"claude": {
		{Path: ".claude/settings.json", Place: InWorktree, Why: "permissions, hooks, env (ANTHROPIC_BASE_URL and the like), apiKeyHelper, enabledPlugins, extraKnownMarketplaces, MCP server approval",
			Source: "code.claude.com/docs/en/settings, \"Settings files and who they affect\" (Claude Code 2.1.322)"},
		{Path: ".claude/settings.local.json", Place: InWorktree, Why: "the same keys as settings.json, applied over it; versions before 2.1.211 kept it in the starting folder, and 2.1.322 still reads one left there",
			Source: "code.claude.com/docs/en/settings, \"Where Claude Code keeps the local file in a git repository\""},
		{Path: ".claude/settings.local.json", Place: InClone, Why: "in a worktree Claude Code reads and writes the local settings file at the main checkout's root, the clone's folder",
			Source: "code.claude.com/docs/en/settings: \"In a worktree, it uses the file at the main checkout's root\""},
		{Path: ".mcp.json", Place: InWorktree, Why: "the project's MCP servers: commands Claude Code starts",
			Source: "code.claude.com/docs/en/claude-directory, file reference: \".mcp.json, Team-shared MCP servers\""},
		{Path: ".claude/agents", Folder: true, Place: InWorktree, Why: "subagents, with their own tools, permissionMode, hooks and mcpServers in frontmatter",
			Source: "code.claude.com/docs/en/claude-directory, \"agents/*.md\" and its frontmatter fields"},
		{Path: ".claude/commands", Folder: true, Place: InWorktree, Why: "slash commands, with allowed-tools and hooks in frontmatter",
			Source: "code.claude.com/docs/en/claude-directory, \"commands/*.md\""},
		{Path: ".claude/skills", Folder: true, Place: InWorktree, Why: "skills, with allowed-tools, hooks and shell in frontmatter, and the scripts they run",
			Source: "code.claude.com/docs/en/claude-directory, \"skills/<name>/SKILL.md\""},
		{Path: ".claude/workflows", Folder: true, Place: InWorktree, Why: "dynamic workflow scripts that start subagents, each a /<name> command",
			Source: "code.claude.com/docs/en/claude-directory, \"workflows/*.js\""},
		{Path: ".claude/hooks", Folder: true, Place: InWorktree, Why: "the conventional folder for the scripts settings.json hooks run",
			Source: "code.claude.com/docs/en/hooks (scripts referenced from settings hooks); read when it exists"},
	},
	"codex": {
		{Path: ".codex", Folder: true, Place: InWorktree, Why: "project config.toml (approval and sandbox settings, MCP servers, profiles), hooks.json, rules/*.rules (exec-policy allow rules), agents/ and skills/; app-server marks the folder trusted itself, so they load",
			Source: "learn.chatgpt.com/docs/config-file/config-reference: \"Untrusted projects skip project-scoped .codex/ layers, including project-local config, hooks, and rules\"; .codex/config.toml, .codex/hooks.json, .codex/agents and .codex/skills named in the codex 0.167.1 binary"},
		{Path: ".agents/skills", Folder: true, Place: InWorktree, Why: "repository skills Codex discovers and may run",
			Source: "\".agents/skills\" named in the codex 0.167.1 binary's skill discovery"},
		{Path: ".agents/plugins", Folder: true, Place: InWorktree, Why: "a repository plugin marketplace (.agents/plugins/marketplace.json)",
			Source: "learn.chatgpt.com/docs/config-file/config-reference, marketplaces; \".agents/plugins/marketplace.json\" in the codex 0.167.1 binary"},
	},
}

const (
	MaxStartupFiles   = 400
	MaxStartupFile    = 1 << 20
	MaxStartupBytes   = 8 << 20
	MaxStartupDepth   = 8
	ShownStartupFile  = 128 << 10
	ShownStartupBytes = 2 << 20
)

type StartupFile struct {
	Path      string
	Place     StartupPlace
	Link      bool
	Target    string
	Data      []byte
	Committed bool
}

type StartupRead struct {
	Commit string
	Files  []StartupFile
}

type StartupReader interface {
	Read(clone, worktree, branch string, entries []StartupEntry) (StartupRead, error)
}

type Approvals interface {
	Load() ([]Approval, error)
	Save([]Approval) error
}

type Approval struct {
	Repo       string
	Agent      string
	Hash       string
	ApprovedAt time.Time
}

const keptApprovals = 1000

type ShownFile struct {
	Path        string
	Place       StartupPlace
	Link        bool
	Target      string
	Size        int
	Hash        string
	Content     string
	Clipped     bool
	Uncommitted bool
}

type ApprovalView struct {
	Agent  string
	Repo   string
	Commit string
	Hash   string
	Files  []ShownFile
}

type Needs struct {
	Approval *ApprovalView
	SignIn   *SignIn
	Expected Expect
	Step     string
	Cost     bool
	Trust    string
}

type Consent struct {
	Approve string
	CostOK  bool
}

func hexHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func ValidApproval(s string) bool { return s == "" || hexHash(s) }

func fileHash(f StartupFile) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00", f.Place, f.Path)
	if f.Link {
		_, _ = fmt.Fprintf(h, "link\x00%s\x00", f.Target)
	} else {
		_, _ = fmt.Fprintf(h, "file\x00")
	}
	_, _ = h.Write(f.Data)
	return hex.EncodeToString(h.Sum(nil))
}

func SetHash(list string, files []StartupFile) string {
	hashes := make([]string, 0, len(files))
	for _, f := range files {
		hashes = append(hashes, string(f.Place)+"\x00"+f.Path+"\x00"+fileHash(f))
	}
	slices.Sort(hashes)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "software-factory start-up set v1\x00%s\n", list)
	for _, x := range hashes {
		_, _ = fmt.Fprintf(h, "%s\n", x)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func escaped(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		return true
	case r == 0xad || r == 0x34f || r == 0x61c || r == 0x115f || r == 0x1160 || r == 0x17b4 || r == 0x17b5 || r == 0x180e:
		return true
	case r >= 0x180b && r <= 0x180f:
		return true
	case r >= 0x200b && r <= 0x200f, r >= 0x2028 && r <= 0x202e, r >= 0x2060 && r <= 0x206f:
		return true
	case r == 0x3164 || r == 0xfeff || r == 0xffa0 || (r >= 0xfff0 && r <= 0xfffb):
		return true
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0000 && r <= 0xe0fff, r >= 0x1d173 && r <= 0x1d17a:
		return true
	}
	return false
}

func Escape(data []byte) string {
	var b strings.Builder
	b.Grow(len(data))
	for len(data) > 0 {
		r, n := utf8.DecodeRune(data)
		switch {
		case r == utf8.RuneError && n <= 1:
			fmt.Fprintf(&b, "⟨0x%02X⟩", data[0])
		case escaped(r):
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
		default:
			b.Write(data[:n])
		}
		data = data[n:]
	}
	return b.String()
}

func approvalView(agent, repo string, read StartupRead, hash string) *ApprovalView {
	v := &ApprovalView{Agent: agent, Repo: repo, Commit: read.Commit, Hash: hash, Files: make([]ShownFile, 0, len(read.Files))}
	room := ShownStartupBytes
	for _, f := range read.Files {
		s := ShownFile{Path: Escape([]byte(f.Path)), Place: f.Place, Link: f.Link, Target: Escape([]byte(f.Target)), Size: len(f.Data), Hash: fileHash(f), Uncommitted: !f.Committed}
		{
			show := min(len(f.Data), ShownStartupFile, max(room, 0))
			s.Content = Escape(f.Data[:show])
			s.Clipped = show < len(f.Data)
			room -= show
		}
		v.Files = append(v.Files, s)
	}
	return v
}

func (d *Dispatcher) approved(repo, agent, hash string) (bool, error) {
	if d.cfg.Approvals == nil {
		return false, nil
	}
	d.approvalMu.Lock()
	defer d.approvalMu.Unlock()
	list, err := d.cfg.Approvals.Load()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(list, func(a Approval) bool { return a.Repo == repo && a.Agent == agent && a.Hash == hash }), nil
}

func (d *Dispatcher) approve(repo, agent, hash string) error {
	if d.cfg.Approvals == nil {
		return errNoApprovals
	}
	d.approvalMu.Lock()
	defer d.approvalMu.Unlock()
	list, err := d.cfg.Approvals.Load()
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(a Approval) bool { return a.Repo == repo && a.Agent == agent })
	list = append(list, Approval{Repo: repo, Agent: agent, Hash: hash, ApprovedAt: d.reg.now()})
	if len(list) > keptApprovals {
		list = list[len(list)-keptApprovals:]
	}
	return d.cfg.Approvals.Save(list)
}

var errNoApprovals = fmt.Errorf("this engine keeps no approvals")

func (d *Dispatcher) startup(r *run, consent Consent) error {
	list := r.spec.Startup
	entries, ok := StartupLists[list]
	if !ok {
		return explained(conflict("no start-up list"), "Software Factory has no list of the files "+r.spec.Name+" loads at start, so it can't show them for approval and doesn't start it.")
	}
	if d.cfg.Startup == nil {
		return explained(conflict("no start-up reader"), "This engine can't read the worktree's start-up files, so it doesn't start agents.")
	}
	read, err := d.cfg.Startup.Read(r.repoPath, r.cwd, r.branch, entries)
	if err != nil {
		d.cfg.Logger.Warn("start-up files", "event", "unreadable")
		return explained(conflict("start-up files"), clip("The engine couldn't read "+r.spec.Name+"'s start-up files in the worktree, so it can't show them and doesn't start it: "+err.Error(), maxOutput))
	}
	if len(read.Files) == 0 {
		return nil
	}
	hash := SetHash(list, read.Files)
	ok, err = d.approved(r.repo, r.spec.ID, hash)
	if err != nil {
		return explained(conflict("approvals"), "The engine couldn't read its approvals of start-up files, so it doesn't start the agent.")
	}
	if ok {
		return nil
	}
	if consent.Approve != "" && consent.Approve == hash {
		if err := d.approve(r.repo, r.spec.ID, hash); err != nil {
			return explained(conflict("approvals"), "The engine couldn't save the approval, so it doesn't start the agent.")
		}
		d.startupAudit(r, hash)
		d.cfg.Logger.Info("start-up files", "event", "approved")
		return nil
	}
	msg := r.spec.Name + " loads these files from the repository at start. They can change what it may do without asking. Read them, then approve to start."
	if consent.Approve != "" {
		msg = "The start-up files changed since you looked at them. Read them again, then approve to start."
	}
	d.cfg.Logger.Info("start-up files", "event", "needs-approval")
	return &Error{Kind: Conflict, Reason: "needs approval", Message: msg, Needs: &Needs{Approval: approvalView(r.spec.ID, r.repo, read, hash)}}
}
