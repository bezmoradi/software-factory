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
	Nested bool
}

var StartupLists = map[string][]StartupEntry{
	"claude": {
		{Path: ".claude/settings.json", Place: InWorktree, Why: "permissions, hooks, env (ANTHROPIC_BASE_URL and the like), apiKeyHelper, enabledPlugins, extraKnownMarketplaces, MCP server approval",
			Source: "code.claude.com/docs/en/settings, \"Settings files and who they affect\" (Claude Code 2.1.322)"},
		{Path: ".claude/settings.local.json", Place: InWorktree, Why: "the same keys as settings.json, applied over it; versions before 2.1.211 kept it in the starting folder, and 2.1.322 still reads one left there",
			Source: "code.claude.com/docs/en/settings, \"Where Claude Code keeps the local file in a git repository\""},
		{Path: ".mcp.json", Place: InWorktree, Why: "the project's MCP servers: commands Claude Code starts",
			Source: "code.claude.com/docs/en/claude-directory, file reference: \".mcp.json, Team-shared MCP servers\""},
		{Path: ".claude/agents", Folder: true, Place: InWorktree, Why: "subagents, with their own tools, permissionMode, hooks and mcpServers in frontmatter",
			Source: "code.claude.com/docs/en/claude-directory, \"agents/*.md\" and its frontmatter fields"},
		{Path: ".claude/commands", Folder: true, Place: InWorktree, Why: "slash commands, with allowed-tools and hooks in frontmatter",
			Source: "code.claude.com/docs/en/claude-directory, \"commands/*.md\""},
		{Path: ".claude/skills", Folder: true, Place: InWorktree, Nested: true, Why: "skills, with allowed-tools, hooks and shell in frontmatter, and the scripts they run; also <subfolder>/.claude/skills, which loads once Claude works on files there",
			Source: "code.claude.com/docs/en/skills, \"Choose where skills load\" (Nested: <subdir>/.claude/skills/<skill-name>/SKILL.md) and \"Load skills in monorepos and subdirectories\"; the 2.1.322 binary names skills \"from <folder>/.claude/skills\" applying to files under that folder"},
		{Path: ".claude/skills", Folder: true, Place: InClone, Why: "in a worktree with no .claude/skills folder at its root, Claude Code loads the main checkout's project skills, the clone's folder",
			Source: "code.claude.com/docs/en/skills, \"Load skills in monorepos and subdirectories\": \"when the worktree checkout has no .claude/skills directory at its root, Claude Code loads the main checkout's project skills instead\" (2.1.277 and later)"},
		{Path: ".claude/workflows", Folder: true, Place: InWorktree, Why: "dynamic workflow scripts that start subagents, each a /<name> command",
			Source: "code.claude.com/docs/en/claude-directory, \"workflows/*.js\""},
		{Path: ".claude/hooks", Folder: true, Place: InWorktree, Why: "the conventional folder for the scripts settings.json hooks run",
			Source: "code.claude.com/docs/en/hooks (scripts referenced from settings hooks); read when it exists"},
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
	Commit       string
	Files        []StartupFile
	Instructions []StartupFile
	Walk         []string
	Notes        []string
}

type StartupReader interface {
	Read(clone, worktree, branch string, scan StartupScan) (StartupRead, error)
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
	Seen       []string
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
	Hidden      bool
	Marks       []Mark
}

type ApprovalView struct {
	Agent        string
	Repo         string
	Commit       string
	Hash         string
	Files        []ShownFile
	Instructions []ShownFile
	Unchanged    int
	Walk         []string
	Notes        []string
	Skipped      []string
}

type Needs struct {
	Approval *ApprovalView
	SignIn   *SignIn
	Expected Expect
	Step     string
	Cost     bool
	Trust    string
	Box      string
	Agent    string
	Code     bool
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

func shownFiles(files []StartupFile) []ShownFile {
	out := make([]ShownFile, 0, len(files))
	room := ShownStartupBytes
	for _, f := range files {
		s := ShownFile{Path: Escape([]byte(f.Path)), Place: f.Place, Link: f.Link, Target: Escape([]byte(f.Target)), Size: len(f.Data), Hash: fileHash(f), Uncommitted: !f.Committed,
			Hidden: Hidden([]byte(f.Path)) || Hidden([]byte(f.Target)) || Hidden(f.Data), Marks: Marks(f.Path, f.Data, f.Link)}
		show := min(len(f.Data), ShownStartupFile, max(room, 0))
		s.Content = Escape(f.Data[:show])
		s.Clipped = show < len(f.Data)
		room -= show
		out = append(out, s)
	}
	return out
}

func approvalView(agent, repo string, read StartupRead, hash string, seen []string) *ApprovalView {
	v := &ApprovalView{Agent: agent, Repo: repo, Commit: read.Commit, Hash: hash, Files: shownFiles(read.Files), Walk: read.Walk, Notes: read.Notes}
	changed := make([]StartupFile, 0, len(read.Instructions))
	for _, f := range read.Instructions {
		if slices.Contains(seen, fileHash(f)) {
			v.Unchanged++
			continue
		}
		changed = append(changed, f)
	}
	v.Instructions = shownFiles(changed)
	if len(read.Instructions) > 0 || len(read.Notes) > 0 || len(read.Walk) > 0 {
		v.Skipped = WalkSkipped
	}
	return v
}

func instructionHashes(files []StartupFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, fileHash(f))
	}
	return out
}

func (d *Dispatcher) lastApproval(repo, agent string) (Approval, error) {
	if d.cfg.Approvals == nil {
		return Approval{}, nil
	}
	d.approvalMu.Lock()
	defer d.approvalMu.Unlock()
	list, err := d.cfg.Approvals.Load()
	if err != nil {
		return Approval{}, err
	}
	if i := slices.IndexFunc(list, func(a Approval) bool { return a.Repo == repo && a.Agent == agent }); i >= 0 {
		return list[i], nil
	}
	return Approval{}, nil
}

func (d *Dispatcher) approve(repo, agent, hash string, seen []string) error {
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
	list = append(list, Approval{Repo: repo, Agent: agent, Hash: hash, ApprovedAt: d.reg.now(), Seen: seen})
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
	scan := StartupScan{Entries: entries, Instructions: InstructionLists[list], Walk: true}
	if len(scan.Instructions.Above) > 0 {
		scan.Above = foldersAbove(r.cwd, BoxFolder(d.cfg.Workspace, r.box))
	}
	if r.clone == "" {
		return oldLayout()
	}
	read, err := d.cfg.Startup.Read(r.clone, r.cwd, r.branch, scan)
	if err != nil {
		d.cfg.Logger.Warn("start-up files", "event", "unreadable")
		return explained(conflict("start-up files"), clip("The engine couldn't read "+r.spec.Name+"'s start-up files in the worktree, so it can't show them and doesn't start it: "+err.Error(), maxOutput))
	}
	if len(read.Files) == 0 && len(read.Walk) == 0 {
		return nil
	}
	hash := startupHash(list, read)
	last, err := d.lastApproval(r.repo, r.spec.ID)
	if err != nil {
		return explained(conflict("approvals"), "The engine couldn't read its approvals of start-up files, so it doesn't start the agent.")
	}
	if last.Hash == hash {
		return nil
	}
	if consent.Approve != "" && consent.Approve == hash {
		if err := d.approve(r.repo, r.spec.ID, hash, instructionHashes(read.Instructions)); err != nil {
			return explained(conflict("approvals"), "The engine couldn't save the approval, so it doesn't start the agent.")
		}
		d.startupAudit(r, hash)
		d.cfg.Logger.Info("start-up files", "event", "approved")
		return nil
	}
	msg := r.spec.Name + " loads these files from the repository at start. They can change what it may do without asking. Read them, then approve to start."
	if len(read.Files) == 0 {
		msg = "The search of the worktree's subfolders stopped early, so a skill folder past where it stopped wouldn't be shown or approved. Read where it stopped, then approve to start."
	}
	if consent.Approve != "" {
		msg = "The start-up files changed since you looked at them. Read them again, then approve to start."
	}
	d.cfg.Logger.Info("start-up files", "event", "needs-approval")
	return &Error{Kind: Conflict, Reason: "needs approval", Message: msg, Needs: &Needs{Approval: approvalView(r.spec.ID, r.repo, read, hash, last.Seen)}}
}
