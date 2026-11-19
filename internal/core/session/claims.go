package session

import (
	"context"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxMarkedTurns = 20
	maxTurnTools   = 500
	turnSlack      = 2 * time.Second
	turnStartAge   = 10 * time.Minute
	turnHeadWait   = 5 * time.Second
)

type TurnFiles interface {
	Touched(dir string, names []string, since time.Time) (map[string]bool, error)
}

type ClaimCommits interface {
	ChangedFiles(ctx context.Context, repo, from, to string) ([]push.Change, error)
}

type ClaimChecks struct {
	Targets func(sid string) (PushTarget, bool)
	Heads   Heads
	Commits ClaimCommits
	Files   TurnFiles
}

type ClaimMark struct {
	Kind  ClaimKind
	Quote string
	File  string
	Why   string
}

type TurnMarks struct {
	TurnID string
	Epoch  string
	Marks  []ClaimMark
	Notes  []string
	serial uint64
}

type turnTool struct {
	shell      bool
	unknown    bool
	command    string
	order      int
	finished   bool
	ok         bool
	background bool
}

type turnCommand struct {
	command    string
	extra      bool
	finished   bool
	ok         bool
	background bool
}

type turnHead struct {
	done chan struct{}
	sha  string
}

type turnLog struct {
	serial  uint64
	open    bool
	whole   bool
	turnID  string
	epoch   string
	start   time.Time
	lastSeq int64
	head    *turnHead
	tools   map[string]*turnTool
	started int
	extra   []string
	edits   []string
	early   []string
	unknown bool
	message string
}

type endedTurn struct {
	sid      string
	serial   uint64
	turnID   string
	epoch    string
	cwd      string
	start    time.Time
	known    bool
	commands []string
	ran      []turnCommand
	edits    []string
	message  string
	head     *turnHead
}

var (
	shellTools = map[string]bool{"Bash": true, "PowerShell": true, "Monitor": true}
	editTools  = map[string]bool{"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true}
	quietTools = map[string]bool{
		"Read": true, "Grep": true, "Glob": true, "LS": true, "WebFetch": true, "WebSearch": true, "TodoWrite": true, "TodoRead": true,
		"ExitPlanMode": true, "EnterPlanMode": true, "AskUserQuestion": true, "BashOutput": true, "KillShell": true, "KillBash": true,
		"TaskOutput": true, "TaskStop": true, "ToolSearch": true, "ListMcpResourcesTool": true, "ReadMcpResourceTool": true, "LSP": true,
		"read": true, "search": true, "fetch": true, "think": true, "switch_mode": true, "edit": true, "delete": true, "move": true,
	}
)

func (r *Registry) CheckClaims(c ClaimChecks) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimChecks = &c
}

func (r *Registry) trackTurn(c *conn, kind string, ev map[string]any, seq int64, now time.Time) {
	a := r.admitted[c.sid]
	if a == nil {
		return
	}
	log := a.turnLog
	if log == nil {
		log = &turnLog{}
		a.turnLog = log
	}
	if log.lastSeq != 0 && seq != log.lastSeq+1 {
		log.whole = false
	}
	log.lastSeq = seq
	switch kind {
	case "busy":
		r.claimSerial++
		turnID, _ := ev["turnId"].(string)
		start := now
		if ts, ok := ev["ts"].(float64); ok {
			if at := time.UnixMilli(int64(ts)); at.Before(now) && now.Sub(at) < turnStartAge {
				start = at
			}
		}
		early := log.early
		*log = turnLog{serial: r.claimSerial, open: true, whole: true, turnID: clip(turnID, maxToolID), epoch: c.epoch, start: start, lastSeq: seq, tools: map[string]*turnTool{}, extra: early}
		if r.claimChecks != nil {
			log.head = &turnHead{done: make(chan struct{})}
			go readTurnHead(c.sid, log.head, *r.claimChecks)
		}
	case "tool":
		r.trackTool(log, ev)
	case "tool_done":
		id, _ := ev["id"].(string)
		command, _ := ev["command"].(string)
		t := log.tools[id]
		if !log.open || t == nil {
			return
		}
		if command != "" {
			t.shell, t.unknown, t.command = true, false, command
		}
		if ok, reported := ev["ok"].(bool); reported {
			t.finished, t.ok = true, ok
		}
		if background, _ := ev["background"].(bool); background {
			t.background = true
		}
	case "text":
		if log.open {
			text, _ := ev["text"].(string)
			log.message = keepTail(log.message+text, maxClaimMessage)
		}
	case "idle":
		if !log.open {
			return
		}
		log.open = false
		reason, _ := ev["reason"].(string)
		if reason == "aborted" || reason == "error" || log.message == "" {
			return
		}
		a.lastMessage = log.message
		if r.claimChecks != nil {
			r.endTurn(c.sid, a, log, *r.claimChecks)
		}
	}
}

func (r *Registry) lastMessage(sid string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.admitted[sid]; a != nil {
		return a.lastMessage
	}
	return ""
}

func (r *Registry) trackTool(log *turnLog, ev map[string]any) {
	tool, _ := ev["tool"].(string)
	summary, _ := ev["summary"].(string)
	id, _ := ev["id"].(string)
	_, sub := ev["agentId"].(string)
	t := classifyTool(tool, summary, ev)
	if editTools[tool] && summary != "" && !strings.HasSuffix(summary, "…") {
		log.edits = appendCapped(log.edits, summary)
	}
	if !log.open {
		switch {
		case !sub:
		case t.command != "":
			log.early = appendCapped(log.early, t.command)
		case t.unknown:
			log.early = appendCapped(log.early, "")
		}
		return
	}
	if !sub {
		log.message = ""
	}
	if id == "" || len(log.tools) >= maxTurnTools {
		log.unknown = true
		return
	}
	t.order = log.started
	log.started++
	log.tools[id] = &t
}

func classifyTool(tool, summary string, ev map[string]any) turnTool {
	switch {
	case shellTools[tool]:
		if summary == "" || strings.HasSuffix(summary, "…") || utf8.RuneCountInString(summary) >= 300 {
			return turnTool{unknown: true}
		}
		return turnTool{shell: true, command: summary, background: tool == "Monitor"}
	case tool == "execute":
		if command, _ := ev["command"].(string); command != "" {
			return turnTool{shell: true, command: command}
		}
		return turnTool{unknown: true}
	case editTools[tool], quietTools[tool]:
		return turnTool{}
	case strings.Contains(summary, "factory_") || strings.Contains(tool, "factory_"):
		return turnTool{}
	}
	return turnTool{unknown: true}
}

func appendCapped(list []string, s string) []string {
	if len(list) >= maxTurnTools {
		return list
	}
	return append(list, s)
}

func keepTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

func readTurnHead(sid string, h *turnHead, checks ClaimChecks) {
	defer close(h.done)
	if checks.Targets == nil || checks.Heads == nil {
		return
	}
	t, ok := checks.Targets(sid)
	if !ok {
		return
	}
	if head, err := checks.Heads.Head(t.Repo, t.Branch); err == nil {
		h.sha = head
	}
}

func (r *Registry) TurnBase(sid string) string {
	r.mu.Lock()
	var h *turnHead
	if a := r.admitted[sid]; a != nil && a.turnLog != nil {
		h = a.turnLog.head
	}
	r.mu.Unlock()
	return h.wait()
}

func (h *turnHead) wait() string {
	if h == nil {
		return ""
	}
	select {
	case <-h.done:
		return h.sha
	case <-time.After(turnHeadWait):
		return ""
	}
}

func (r *Registry) endTurn(sid string, a *admitted, log *turnLog, checks ClaimChecks) {
	known := log.whole && !log.unknown
	var ran []turnCommand
	for _, c := range log.extra {
		if c == "" {
			known = false
			continue
		}
		ran = append(ran, turnCommand{command: c, extra: true})
	}
	tools := make([]*turnTool, 0, len(log.tools))
	for _, t := range log.tools {
		if t.unknown || (t.shell && t.command == "") {
			known = false
		}
		if t.command != "" {
			tools = append(tools, t)
		}
	}
	slices.SortFunc(tools, func(a, b *turnTool) int { return a.order - b.order })
	for _, t := range tools {
		ran = append(ran, turnCommand{command: t.command, finished: t.finished, ok: t.ok, background: t.background})
	}
	commands := make([]string, 0, len(ran))
	for _, c := range ran {
		commands = append(commands, c.command)
	}
	ended := endedTurn{sid: sid, serial: log.serial, turnID: log.turnID, epoch: log.epoch, cwd: a.Cwd, start: log.start, known: known, commands: commands, ran: ran, edits: slices.Clone(log.edits), message: log.message, head: log.head}
	go r.checkTurn(ended, checks)
}

func (r *Registry) checkTurn(t endedTurn, checks ClaimChecks) {
	claims := findClaims(t.message)
	if len(claims) == 0 {
		return
	}
	build := false
	for _, c := range t.commands {
		_, cb := commandRuns(c)
		build = build || cb
	}
	var marks []ClaimMark
	var notes []string
	var files []claim
	for _, c := range claims {
		switch c.kind {
		case ClaimTests:
			if why := t.testsUnbacked(); why != "" {
				marks = append(marks, ClaimMark{Kind: c.kind, Quote: claimQuote(c.sentence), Why: why})
			}
		case ClaimBuild:
			if t.known && !build {
				marks = append(marks, ClaimMark{Kind: c.kind, Quote: claimQuote(c.sentence), Why: "No build or test command ran in this turn."})
			}
		case ClaimFile:
			if rel, ok := worktreeRelative(t.cwd, c.file); ok && !t.namedByTurn(rel) {
				c.file = rel
				files = append(files, c)
			}
		}
	}
	if len(files) > 0 {
		fileMarks, fileNotes := r.checkFiles(t, files, checks)
		marks = append(marks, fileMarks...)
		notes = append(notes, fileNotes...)
	}
	if len(marks) == 0 && len(notes) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[t.sid]
	if a == nil {
		return
	}
	entry := TurnMarks{TurnID: t.turnID, Epoch: t.epoch, Marks: marks, Notes: notes, serial: t.serial}
	at := len(a.unbacked)
	for at > 0 && a.unbacked[at-1].serial > t.serial {
		at--
	}
	a.unbacked = slices.Insert(a.unbacked, at, entry)
	if len(a.unbacked) > maxMarkedTurns {
		a.unbacked = slices.Clone(a.unbacked[len(a.unbacked)-maxMarkedTurns:])
	}
	r.announce(r.now())
}

func (t endedTurn) testsUnbacked() string {
	if !t.known {
		return ""
	}
	ran, background, last := false, false, -1
	for i, c := range t.ran {
		narrow := runsTestCheck(c.command)
		tests, _ := commandRuns(c.command)
		if (tests || narrow) && c.background || narrow && statusHiddenBy(c.command, isTestSegment) == HiddenByBackground {
			background = true
			continue
		}
		if tests || narrow {
			ran = true
		}
		if narrow {
			last = i
		}
	}
	switch {
	case !ran && background:
		return "The test commands in this turn ran in the background, so the engine didn't see whether they passed."
	case !ran:
		return "No test command ran in this turn."
	}
	if last < 0 {
		return ""
	}
	c := t.ran[last]
	if c.extra || !c.finished || c.ok || statusHiddenBy(c.command, isCheckSegment) != "" || !testDecides(c.command) {
		return ""
	}
	return "The last test command in this turn failed."
}

func (t endedTurn) namedByTurn(rel string) bool {
	base := path.Base(rel)
	for _, e := range t.edits {
		if e == rel || strings.HasSuffix(e, "/"+rel) {
			return true
		}
	}
	for _, c := range t.commands {
		if strings.Contains(c, base) {
			return true
		}
	}
	return false
}

func worktreeRelative(cwd, name string) (string, bool) {
	if strings.HasPrefix(name, "/") {
		if cwd == "" {
			return "", false
		}
		rest, ok := strings.CutPrefix(name, strings.TrimSuffix(cwd, "/")+"/")
		if !ok {
			return "", false
		}
		name = rest
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", false
	}
	return clean, true
}

func (r *Registry) checkFiles(t endedTurn, files []claim, checks ClaimChecks) ([]ClaimMark, []string) {
	unchecked := func() []string {
		var notes []string
		for _, c := range files {
			notes = append(notes, "Not checked: the engine can't tell whether "+claimQuote(c.file)+" changed in this turn.")
		}
		return notes
	}
	start := t.head.wait()
	if t.cwd == "" || start == "" || checks.Targets == nil || checks.Heads == nil || checks.Commits == nil || checks.Files == nil {
		return nil, unchecked()
	}
	target, ok := checks.Targets(t.sid)
	if !ok {
		return nil, unchecked()
	}
	head, err := checks.Heads.Head(target.Repo, target.Branch)
	if err != nil {
		return nil, unchecked()
	}
	var committed []push.Change
	if head != start {
		if committed, err = checks.Commits.ChangedFiles(context.Background(), target.Repo, start, head); err != nil {
			return nil, unchecked()
		}
	}
	names := make([]string, 0, len(files))
	for _, c := range files {
		names = append(names, c.file)
	}
	touched, err := checks.Files.Touched(t.cwd, names, t.start.Add(-turnSlack))
	if err != nil {
		return nil, unchecked()
	}
	var marks []ClaimMark
	var notes []string
	for _, c := range files {
		if inCommits(committed, c.file) {
			continue
		}
		changed, known := touched[c.file]
		switch {
		case !known:
			notes = append(notes, "Not checked: the engine can't tell whether "+claimQuote(c.file)+" changed in this turn.")
		case !changed:
			marks = append(marks, ClaimMark{Kind: c.kind, Quote: claimQuote(c.sentence), File: claimQuote(c.file), Why: "This turn's edits, commands and commits don't touch " + claimQuote(c.file) + "."})
		}
	}
	return marks, notes
}

func inCommits(list []push.Change, rel string) bool {
	bare := !strings.Contains(rel, "/")
	for _, c := range list {
		if c.Path == rel || bare && path.Base(c.Path) == rel {
			return true
		}
	}
	return false
}

func claimQuote(s string) string {
	q := maskCut(CleanLine(maskSecrets(s), maxClaimQuote))
	if secretText(q) {
		return secretMask
	}
	return q
}
