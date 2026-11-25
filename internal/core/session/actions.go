package session

import (
	"context"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxActions       = 32
	MaxActionArgs    = 32
	MaxActionArg     = 512
	MaxActionSlots   = 8
	MaxSlotPattern   = 200
	MaxSlotValue     = 512
	MaxActionProgram = 200
	ActionMinutes    = 10
	MaxActionMinutes = 60
	ActionWork       = "/work"
	ActionHome       = "/home/action"
	ActionAgent      = ActionHome + "/.ssh/agent.sock"
	ActionReadMark   = "software-factory action: read its command"
)

type ActionSlot struct {
	Name    string
	Pattern string
	Max     int
}

type Action struct {
	ID         string
	Name       string
	Program    string
	Args       []string
	Credential string
	Target     string
	Slots      []ActionSlot
	Files      bool
	Minutes    int
	Saved      time.Time
}

type ActionStore interface {
	Load() ([]Action, error)
	Save([]Action) error
}

type ActionDraft struct {
	ID         string
	Name       string
	Command    []string
	Credential string
	Target     string
	Slots      []ActionSlot
	Files      bool
	Minutes    int
}

type ActionView struct {
	ID         string
	Name       string
	Command    string
	Argv       []string
	Credential string
	Target     string
	Slots      []ActionSlot
	Files      bool
	Minutes    int
	Saved      time.Time
	Level      string
	Label      string
	Broken     string
}

var (
	slotName       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
	slotRef        = regexp.MustCompile(`\{([a-z][a-z0-9_]{0,23})\}`)
	programPath    = regexp.MustCompile(`^(\./|/)?[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+){0,15}$`)
	programVersion = regexp.MustCompile(`^([a-z][a-z-]*?)[0-9.]+$`)
	shellSafe      = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
)

var programsRefused = map[string]string{
	"git": "Software Factory never runs git: it reads and pushes repositories itself, and git runs programs a repository's configuration names",
}

var refusedKinds = []struct {
	why   string
	names []string
}{
	{"a shell runs its arguments as commands", []string{"sh", "bash", "dash", "ash", "zsh", "ksh", "mksh", "pdksh", "csh", "tcsh", "fish", "rbash", "busybox", "toybox"}},
	{"it runs another program its arguments name", []string{"env", "xargs", "nice", "nohup", "timeout", "stdbuf", "setsid", "sudo", "su", "doas", "chroot", "flock", "script", "watch", "time", "strace", "ltrace", "unshare", "nsenter", "runuser", "ionice", "taskset", "chrt", "sshpass", "parallel", "exec", "command", "builtin", "eval", "find"}},
	{"an interpreter runs its arguments as code", []string{"python", "pypy", "perl", "ruby", "irb", "node", "nodejs", "deno", "bun", "bunx", "php", "lua", "luajit", "tclsh", "wish", "expect", "rscript", "r", "awk", "gawk", "mawk", "nawk", "sed", "osascript", "pwsh", "powershell", "guile", "racket", "ocaml", "ghci", "runghc", "scala", "groovy", "julia", "swift", "elixir", "erl", "escript", "iex", "jshell", "gdb", "vim", "vi", "emacs", "ed", "ex"}},
	{"a task runner runs code from files its arguments name", []string{"make", "gmake", "npm", "npx", "pnpm", "yarn", "pip", "pipx", "uv", "uvx", "rake", "just", "task"}},
}

var actionReserved = []string{ActionWork, ActionHome, "/in", "/tmp", "/proc", "/sys", "/dev", "/run", "/var/run", "/etc"}

func RefusedProgram(program string) string {
	name := strings.ToLower(path.Base(program))
	if m := programVersion.FindStringSubmatch(name); m != nil {
		name = strings.TrimSuffix(m[1], "-")
	}
	if why, ok := programsRefused[name]; ok {
		return why
	}
	for _, k := range refusedKinds {
		if slices.Contains(k.names, name) {
			return k.why
		}
	}
	return ""
}

func WorktreeProgram(program string) bool {
	return strings.HasPrefix(program, "./")
}

func checkProgram(raw string, files bool) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" || len(p) > MaxActionProgram || !programPath.MatchString(p) || strings.Contains(p, "//") {
		return "", onField("command", invalid("command", "Line 1 is a program, like rsync or /usr/bin/kubectl."))
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/"), "/")
	if slices.Contains(parts, "..") || slices.Contains(parts, ".") {
		return "", onField("command", invalid("command", "The program's path has no . or .. part."))
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "./") && strings.Contains(p, "/") {
		return "", onField("command", invalid("command", withName("From the session's files, give ./", p, ".")))
	}
	if why := RefusedProgram(p); why != "" {
		return "", onField("command", conflict("program", withName("Can't run ", path.Base(p), "; give a program by its path.")))
	}
	if strings.HasPrefix(p, "/") {
		for _, r := range actionReserved {
			if p == r || strings.HasPrefix(p, r+"/") {
				return "", onField("command", invalid("command", withName("Needs a program in the image, not under ", r, ".")))
			}
		}
	}
	if WorktreeProgram(p) && !files {
		return "", onField("files", invalid("files", "A ./ program needs the session's files."))
	}
	return p, nil
}

func plainArg(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !hostReasonControl.MatchString(s)
}

func anchored(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

func checkSlots(slots []ActionSlot, args []string) ([]ActionSlot, error) {
	if len(slots) > MaxActionSlots {
		return nil, onField("slots", invalid("slots", "At most 8 arguments the agent fills."))
	}
	out := make([]ActionSlot, 0, len(slots))
	seen := map[string]bool{}
	for _, s := range slots {
		s.Name = strings.TrimSpace(s.Name)
		if !slotName.MatchString(s.Name) {
			return nil, onField("slots", invalid("slots", "A name like tag: small letters, digits and _."))
		}
		if seen[s.Name] {
			return nil, onField("slots", invalid("slots", withName("Name each once; ", s.Name, " is twice.")))
		}
		seen[s.Name] = true
		if s.Pattern == "" || len(s.Pattern) > MaxSlotPattern || !utf8.ValidString(s.Pattern) || hostReasonControl.MatchString(s.Pattern) {
			return nil, onField("slots", invalid("slots", withName("A pattern of 1 to 200 bytes for ", s.Name, ".")))
		}
		if _, err := anchored(s.Pattern); err != nil {
			return nil, onField("slots", invalid("slots", withName("Go's regexp refuses the pattern of ", s.Name, ".")))
		}
		if s.Max < 1 || s.Max > MaxSlotValue {
			return nil, onField("slots", invalid("slots", withName("A length from 1 to 512 for ", s.Name, ".")))
		}
		out = append(out, s)
	}
	used := map[string]int{}
	for _, a := range args {
		refs := slotRef.FindAllStringSubmatch(a, -1)
		if len(refs) > 1 {
			return nil, onField("command", invalid("command", "Each line holds at most one {name}."))
		}
		for _, r := range refs {
			if !seen[r[1]] {
				return nil, onField("command", invalid("command", withName("No argument named ", r[1], " to fill.")))
			}
			used[r[1]]++
		}
	}
	for _, s := range out {
		if used[s.Name] != 1 {
			return nil, onField("slots", invalid("slots", withName("Use {", s.Name, "} on exactly one line.")))
		}
	}
	return out, nil
}

func (g *Grants) actionTarget(raw string, c Credential) (string, error) {
	name := HostName(raw)
	if name == "" {
		return "", onField("target", invalid("target", "A host like api.example.com, or host:port."))
	}
	var target, host string
	if h, p, ok := SplitPortTarget(name); ok || strings.Contains(name, ":") && !strings.HasPrefix(name, "[") && strings.Count(name, ":") == 1 {
		if !ok {
			return "", onField("target", invalid("target", "A port from 1 to 65535 after the colon."))
		}
		got, err := g.portRule(h, p)
		if err != nil {
			return "", onField("target", err)
		}
		host, target = got, PortTarget(got, p)
	} else {
		if strings.HasPrefix(name, ".") {
			return "", onField("target", invalid("target", "One host, not every name under a domain."))
		}
		got, err := g.hostRule(name)
		if err != nil {
			if _, aerr := netip.ParseAddr(strings.Trim(name, "[]")); aerr == nil {
				return "", onField("target", invalid("target", "An address needs its port, like :22."))
			}
			return "", onField("target", err)
		}
		host, target = got, got
	}
	if git := g.actionGitHost(target); git == host {
		return "", onField("target", conflict("git host", "A git host; an action never reaches one."))
	} else if git != "" {
		return "", onField("target", conflict("git host", withName("An address of ", git, ", a git host.")))
	}
	if c.Type == CredentialSSH && !portEntry(target) {
		return "", onField("target", invalid("target", "SSH needs host:port, like 192.168.1.20:22."))
	}
	if !boundTo(c, target) {
		return "", onField("target", conflict("bound", withName("Bind ", c.Name, " to it first.")))
	}
	return target, nil
}

func (g *Grants) actionGitHost(target string) string {
	host := targetHost(target)
	if a, err := netip.ParseAddr(host); err == nil {
		return g.cfg.GitAddress(a.Unmap())
	}
	if g.cfg.Allow.gitEntry(host) {
		return host
	}
	return ""
}

func actionGitWords(target, git string) string {
	if git == targetHost(target) {
		return "its target " + target + " is a git host now, and an action never reaches one"
	}
	return "its target " + target + " is an address of " + git + ", a git host now, and an action never reaches one"
}

func boundTo(c Credential, target string) bool {
	return slices.ContainsFunc(c.Bound, func(b string) bool {
		if portEntry(target) {
			return b == target
		}
		return !portEntry(b) && (b == target || EntryCovers(b, target))
	})
}

func (g *Grants) checkAction(d ActionDraft) (Action, error) {
	a := Action{ID: d.ID, Name: strings.TrimSpace(d.Name), Files: d.Files, Minutes: d.Minutes}
	if !credentialName.MatchString(a.Name) {
		return Action{}, onField("name", invalid("name", "Needs small letters, digits, _ and -, like deploy-pi."))
	}
	if len(d.Command) == 0 {
		return Action{}, onField("command", invalid("command", "Line 1 is a program, like rsync."))
	}
	program, err := checkProgram(d.Command[0], d.Files)
	if err != nil {
		return Action{}, err
	}
	a.Program = program
	if len(d.Command)-1 > MaxActionArgs {
		return Action{}, onField("command", invalid("command", "At most 32 arguments after the program."))
	}
	a.Args = make([]string, 0, len(d.Command)-1)
	for _, arg := range d.Command[1:] {
		if !plainArg(arg, MaxActionArg) {
			return Action{}, onField("command", invalid("command", "Each argument is one line of at most 512 bytes."))
		}
		if secretText(arg) {
			return Action{}, onField("command", invalid("command", "An argument looks like a secret; use a credential."))
		}
		a.Args = append(a.Args, arg)
	}
	if a.Slots, err = checkSlots(d.Slots, a.Args); err != nil {
		return Action{}, err
	}
	if a.Minutes == 0 {
		a.Minutes = ActionMinutes
	}
	if a.Minutes < 1 || a.Minutes > MaxActionMinutes {
		return Action{}, onField("minutes", invalid("minutes", "From 1 to 60 minutes."))
	}
	c, err := g.credentialRule(d.Credential)
	if err != nil {
		return Action{}, err
	}
	a.Credential = c.ID
	if a.Target, err = g.actionTarget(d.Target, c); err != nil {
		return Action{}, err
	}
	return a, nil
}

func QuotedCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if shellSafe.MatchString(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

func (a Action) argv(values map[string]string) []string {
	out := make([]string, 0, len(a.Args)+1)
	out = append(out, a.Program)
	for _, arg := range a.Args {
		out = append(out, slotRef.ReplaceAllStringFunc(arg, func(ref string) string {
			if v, ok := values[ref[1:len(ref)-1]]; ok {
				return v
			}
			return ref
		}))
	}
	return out
}

func actionsUnread() error {
	return conflict("actions", "The engine couldn't read actions.json, so it saves, deletes and runs no action. Move the file out of the engine's state folder and restart the engine.")
}

func (g *Grants) actionByID(id string) (Action, bool) {
	for _, a := range g.actions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

func (g *Grants) actionByName(name string) (Action, bool) {
	for _, a := range g.actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

func (g *Grants) actionViewLocked(a Action) ActionView {
	v := ActionView{ID: a.ID, Name: a.Name, Argv: a.argv(nil), Target: a.Target, Slots: slices.Clone(a.Slots), Files: a.Files, Minutes: a.Minutes, Saved: a.Saved}
	v.Command = QuotedCommand(v.Argv)
	c, ok := g.credentialByID(a.Credential)
	if !ok || g.credsBroken {
		v.Broken = "Its credential is gone, so it can't run. Delete it, or save it again with another credential."
		v.Level, v.Label = RiskHigh, riskLabels[RiskHigh]
		return v
	}
	v.Credential = c.Name
	if !boundTo(c, a.Target) {
		v.Broken = c.Name + " isn't bound to " + a.Target + " any more, so it can't run. Bind it again on its credential, or save the action with another target."
	}
	if git := g.actionGitHost(a.Target); git != "" {
		v.Broken = upperFirst(actionGitWords(a.Target, git)) + ", so it can't run. Save it with another target."
	}
	r := actionRisk(a, c, ActionOnce)
	v.Level, v.Label = r.Level, r.Label
	return v
}

func (g *Grants) actionViewsLocked() []ActionView {
	out := []ActionView{}
	if g.actionsBroken {
		return out
	}
	for _, a := range g.actions {
		out = append(out, g.actionViewLocked(a))
	}
	return out
}

func (g *Grants) saveActionsLocked(next []Action) error {
	if err := g.cfg.Actions.Store.Save(slices.Clone(next)); err != nil {
		g.cfg.Logger.Warn("actions", "event", "not-saved")
		return conflict("state", "The engine couldn't save its actions, so nothing changed.")
	}
	g.actions = next
	return nil
}

func (g *Grants) actionsOff() error {
	if g.cfg.Actions.Store == nil {
		return conflict("actions off", "This engine runs no actions: it has no actions volume (SF_ACTIONS) or no box helper.")
	}
	return nil
}

func (g *Grants) SaveAction(ctx context.Context, d ActionDraft) (string, error) {
	if err := g.actionsOff(); err != nil {
		return "", err
	}
	a, err := g.checkAction(d)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	if g.actionsBroken {
		g.mu.Unlock()
		return "", actionsUnread()
	}
	at := -1
	if a.ID != "" {
		if at = slices.IndexFunc(g.actions, func(x Action) bool { return x.ID == a.ID }); at < 0 {
			g.mu.Unlock()
			return "", notFound("action", "That action isn't there any more. Reload the page.")
		}
	}
	if slices.ContainsFunc(g.actions, func(x Action) bool { return x.Name == a.Name && x.ID != a.ID }) {
		g.mu.Unlock()
		return "", onField("name", conflict("name", "An action of that name is saved."))
	}
	if at < 0 && len(g.actions) >= MaxActions {
		g.mu.Unlock()
		return "", limited("actions", "Software Factory keeps at most "+strconv.Itoa(MaxActions)+" actions. Delete one first.")
	}
	a.Saved = g.cfg.Now().Truncate(time.Second)
	next := slices.Clone(g.actions)
	if at >= 0 {
		next[at] = a
	} else {
		a.ID = newGrantID()
		next = append(next, a)
	}
	err = g.saveActionsLocked(next)
	g.mu.Unlock()
	if err != nil {
		return "", err
	}
	stopped := 0
	if at >= 0 {
		stopped = g.endActionWork(ctx, func(x actionKey) bool { return x.action == a.ID }, "the action was replaced")
	}
	shared.For(ctx, g.cfg.Logger).Info("actions", "event", "saved", "replaced", at >= 0)
	said := "Saved the action " + a.Name + "."
	if at >= 0 {
		said = "Replaced the action " + a.Name + "." + endedWords(stopped)
	}
	return said, nil
}

func (g *Grants) DeleteAction(ctx context.Context, id string) (string, error) {
	if err := g.actionsOff(); err != nil {
		return "", err
	}
	g.mu.Lock()
	if g.actionsBroken {
		g.mu.Unlock()
		return "", actionsUnread()
	}
	a, ok := g.actionByID(id)
	if !ok {
		g.mu.Unlock()
		return "", notFound("action", "That action isn't there any more. Reload the page.")
	}
	err := g.saveActionsLocked(slices.DeleteFunc(slices.Clone(g.actions), func(x Action) bool { return x.ID == id }))
	g.mu.Unlock()
	if err != nil {
		return "", err
	}
	stopped := g.endActionWork(ctx, func(x actionKey) bool { return x.action == id }, "the action was deleted")
	shared.For(ctx, g.cfg.Logger).Info("actions", "event", "deleted")
	return "Deleted the action " + a.Name + "." + endedWords(stopped), nil
}

func (g *Grants) dropActionsOfLocked(credential string) ([]Action, error) {
	if g.actionsBroken || g.cfg.Actions.Store == nil {
		return nil, nil
	}
	var gone []Action
	next := slices.DeleteFunc(slices.Clone(g.actions), func(a Action) bool {
		if a.Credential == credential {
			gone = append(gone, a)
			return true
		}
		return false
	})
	if len(gone) == 0 {
		return nil, nil
	}
	return gone, g.saveActionsLocked(next)
}

func endedWords(stopped int) string {
	switch stopped {
	case 0:
		return ""
	case 1:
		return " Its grant or run that was still going is ended, and a running command is stopped."
	}
	return " Its " + strconv.Itoa(stopped) + " grants and runs that were still going are ended, and running commands are stopped."
}

func SavedActionShape(a Action) bool {
	if !credentialName.MatchString(a.Name) || len(a.Args) > MaxActionArgs || a.Minutes < 1 || a.Minutes > MaxActionMinutes {
		return false
	}
	if p, err := checkProgram(a.Program, a.Files); err != nil || p != a.Program {
		return false
	}
	for _, arg := range a.Args {
		if !plainArg(arg, MaxActionArg) {
			return false
		}
	}
	slots, err := checkSlots(a.Slots, a.Args)
	if err != nil || len(slots) != len(a.Slots) {
		return false
	}
	t := HostName(a.Target)
	return t == a.Target && (ValidPortEntry(t) || ValidAllowEntry(t) && !strings.HasPrefix(t, "."))
}
