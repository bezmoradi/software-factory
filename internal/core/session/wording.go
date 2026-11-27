package session

import (
	"strconv"
	"strings"
	"time"
)

type Said struct {
	Label string
	Tone  string
}

func saidOf(table map[string]Said, key string, fallback Said) Said {
	if s, ok := table[key]; ok {
		return s
	}
	return fallback
}

var roleSaid = map[string]Said{
	"running": {"running", ""},
	"pass":    {"pass", "ok"},
	"block":   {"block", "bad"},
	"unknown": {"no verdict", "warn"},
	"failed":  {"failed", "bad"},
}

func RoleSaid(state string, overridden, advisory bool) Said {
	switch {
	case state == "block" && overridden:
		return Said{"overridden", "warn"}
	case state == "block" && advisory:
		return Said{"warning", "warn"}
	}
	return saidOf(roleSaid, state, Said{state, "warn"})
}

var forgeSaid = map[string]Said{
	"waiting":   {"waiting for you", "warn"},
	"sending":   {"opening", "warn"},
	"opened":    {"opened", "ok"},
	"exists":    {"already open", "ok"},
	"denied":    {"denied", "bad"},
	"failed":    {"failed", "bad"},
	"posting":   {"posting", "warn"},
	"commented": {"posted", "ok"},
	"unclear":   {"check on the host", "warn"},
}

func ForgeSaid(state string) Said {
	return saidOf(forgeSaid, state, Said{state, "warn"})
}

func ForgeKindLabel(kind string) string {
	switch kind {
	case "pull-request":
		return "Pull request"
	case "comment":
		return "Comment"
	}
	return "Issue"
}

var forgeCommentNote = map[string]string{
	"waiting":   "Software Factory held this comment after the push instead of posting it under your token, as this project's settings ask, because it looks like it holds a secret.",
	"posting":   "Software Factory is posting this under your token after the push, as this project's settings ask.",
	"denied":    "You denied posting this comment.",
	"failed":    "Software Factory couldn't post this comment.",
	"commented": "Software Factory posted this under your token after the push, as this project's settings ask.",
}

var forgeFromNote = map[string]string{
	"":         "The agent asked for this. Its text is shown exactly as written; read it before you approve, since the engine posts it under your token.",
	"page":     "You asked for this on the page, and the engine wrote it from its records: the branch, its commit messages, which the agent wrote, and the approved requirements' title. Read it before you approve, since the engine posts it under your token.",
	"findings": "You asked for this from a finding that didn't block a passing review. The finding's text is the reviewer's, as it wrote it; read it before you approve, since the engine posts it under your token.",
}

func ForgeNote(kind, state, from string) string {
	if kind == "comment" {
		return forgeCommentNote[state]
	}
	if state != "waiting" && state != "sending" {
		return ""
	}
	if n, ok := forgeFromNote[from]; ok {
		return n
	}
	return forgeFromNote[""]
}

var splitSaid = map[string]Said{
	"waiting":   {"waiting for you", "warn"},
	"queued":    {"waits for a free slot", "warn"},
	"starting":  {"starting", "warn"},
	"started":   {"started", "ok"},
	"dismissed": {"dismissed", ""},
	"adding":    {"adding its project", "warn"},
}

func SplitSaid(state string) Said {
	return saidOf(splitSaid, state, Said{state, "warn"})
}

var verifyWord = map[string]string{
	"running":     "running",
	"passed":      "passed",
	"failed":      "failed",
	"timed-out":   "ran out of time",
	"oom":         "ran out of memory",
	"error":       "couldn’t run",
	"interrupted": "cut off by an engine restart",
	"stopped":     "stopped",
}

func VerifySaid(state string) Said {
	tone := "warn"
	switch state {
	case "passed":
		tone = "ok"
	case "running":
		tone = ""
	}
	if w, ok := verifyWord[state]; ok {
		return Said{w, tone}
	}
	return Said{state, tone}
}

var workspaceSaid = map[string]Said{
	"merged":   {"on main", "ok"},
	"pushed":   {"pushed", "ok"},
	"unpushed": {"unpushed commits", "warn"},
	"unknown":  {"not known", ""},
}

func WorkspaceSaid(state string) Said {
	return saidOf(workspaceSaid, state, workspaceSaid["unknown"])
}

var coverageSaid = map[string]Said{
	"verified":     {"verified", "ok"},
	"violated":     {"violated", "bad"},
	"not-verified": {"not verified", ""},
}

func CoverageSaid(status string) (Said, bool) {
	s, ok := coverageSaid[status]
	return s, ok
}

var readinessLabel = map[string]string{
	"instructions": "Instruction file",
	"tests":        "Tests",
	"ci":           "CI",
	"lint":         "Linter",
	"check":        "Check command",
	"verified":     "Check on main",
}

var readinessWord = map[string]string{"found": "Found", "missing": "Missing", "unknown": "Unknown"}

func ReadinessWord(state string) string {
	return readinessWord[state]
}

func ReadinessSaid(kind, state string) Said {
	label, ok := readinessLabel[kind]
	if !ok {
		label = kind
	}
	tone := ""
	switch state {
	case "found":
		tone = "ok"
	case "missing":
		tone = "warn"
	}
	return Said{label, tone}
}

var egressReason = map[string]string{
	"not_allowlisted": "not on the allowlist", "git_host": "a git host, always refused", "port": "a port other than 443", "method": "plain HTTP, not HTTPS",
	"ip_literal": "an IP address, not a name", "sni_mismatch": "a TLS name sent through another host", "private_address": "a private address",
	"resolve_failed": "the name didn’t resolve", "dial_failed": "the host didn’t answer", "no_sni": "no TLS name", "ech": "encrypted TLS hello", "not_tls": "not TLS", "invalid_host": "not a host name",
}

func EgressReasonLabel(reason string) string {
	if w, ok := egressReason[reason]; ok {
		return w
	}
	return reason
}

var exceptionKind = map[string]string{
	"allow-push":   "Push allowed with items missing",
	"override":     "Verdict overridden",
	"allow-file":   "Flagged file allowed",
	"allow-commit": "Flagged commit allowed",
	"cap-reached":  "Cap reached",
	"cap-answer":   "Cap card answered",
	"usage-answer": "Automatic work kept running near a usage limit",
}

var capWord = map[string]string{"money": "spending cap", "time": "time cap", "turns": "turn cap", "disk": "disk cap"}
var capAnswerWord = map[string]string{"raised": "raised", "stopped": "stopped"}

func ExceptionLabel(kind, result, verdict, reviewer, path string, missing int) string {
	if kind == "cap-answer" && verdict == "freed" {
		return "Disk cap cleared by the engine’s measurement"
	}
	what, ok := exceptionKind[kind]
	if !ok {
		what = kind
	}
	switch {
	case kind == "override":
		if reviewer != "" {
			what += " (" + reviewer + ")"
		}
	case kind == "allow-file" && path != "":
		what += " (" + path + ")"
	case kind == "allow-push":
		what += " (" + strconv.Itoa(missing) + " missing)"
	case strings.HasPrefix(kind, "cap-") && (capWord[result] != "" || capAnswerWord[verdict] != ""):
		parts := []string{}
		for _, w := range []string{capWord[result], capAnswerWord[verdict]} {
			if w != "" {
				parts = append(parts, w)
			}
		}
		what += " (" + strings.Join(parts, ", ") + ")"
	}
	return what
}

func ExceptionNote(kind, verdict, reason string) string {
	switch {
	case kind == "cap-answer" && verdict == "freed":
		return "The engine counted the whole session folder under its disk cap, so the session went on. Nobody answered the card."
	case reason != "":
		return "Your reason: “" + reason + "”"
	}
	return "No reason recorded."
}

var placeWord = map[string]string{"worktree": "a session worktree", "clone": "the clone folder", "user": "the agents’ user settings"}

func PlaceLabel(place string) string {
	if w, ok := placeWord[place]; ok {
		return w
	}
	return place
}

var sessionPlaceWord = map[string]string{"clone": "the clone folder, shared by the project’s sessions", "worktree": "this session’s worktree", "user": "the agent’s user settings in this session’s container"}

func SessionPlaceLabel(place string) string {
	if w, ok := sessionPlaceWord[place]; ok {
		return w
	}
	return place
}

var decisionKind = map[string]string{
	"requirements": "Requirements approved",
	"plan":         "Plan approved",
	"override":     "Verdict overridden",
	"allow":        "Push allowed",
	"denied":       "Push denied",
	"merged":       "Pull request merged",
	"note":         "Your entry",
}

func DecisionLabel(kind string) string {
	if w, ok := decisionKind[kind]; ok {
		return w
	}
	return kind
}

var checkKindLabel = map[string]string{"test": "Tests", "lint": "Lint", "type-check": "Type check", "build": "Build"}

func CheckKindLabel(kind string) string {
	if w, ok := checkKindLabel[kind]; ok {
		return w
	}
	return kind
}

func ChecksBefore(all []AgentCheck, at time.Time) []AgentCheck {
	out := []AgentCheck{}
	for _, c := range all {
		if at.IsZero() || !c.At.After(at) {
			out = append(out, c)
		}
	}
	return out
}

func ChecksNotReported(ran []string) string {
	missing := []string{}
	for _, k := range checkKinds {
		found := false
		for _, r := range ran {
			found = found || r == string(k)
		}
		if !found {
			missing = append(missing, checkKindLabel[string(k)])
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "Not reported: " + strings.Join(missing, ", ")
}

var hiddenResult = map[string]string{
	"pipe":       "result unknown: its exit status is that of the command its output was piped into, and it doesn't set pipefail",
	"or":         "result unknown: it ends in || and a command that isn't a check, such as || true or || echo failed, so its exit status is that command's",
	"background": "result unknown: it ran in the background, so the call returned before the check finished",
}

func CheckResult(ok bool, hidden string) string {
	if hidden != "" {
		if w, found := hiddenResult[hidden]; found {
			return w
		}
		return "result unknown: its exit status isn't the check's own"
	}
	if ok {
		return "finished without an error"
	}
	return "failed or was refused"
}

var ecosystemLabel = map[string]string{"go": "Go", "npm": "npm", "pypi": "PyPI", "crates": "crates.io"}

func EcosystemLabel(eco string) string {
	if w, ok := ecosystemLabel[eco]; ok {
		return w
	}
	return eco
}

var pinSaid = map[string]Said{
	"verified":  {"Provenance verified", "ok"},
	"none":      {"No provenance", "warn"},
	"unpinned":  {"Provenance not checked against a source", "warn"},
	"failed":    {"Provenance failed", "bad"},
	"unchecked": {"Provenance not checked yet", "muted"},
}

var pluginSaid = map[string]Said{
	PluginUpToDate: {"Up to date", "ok"},
	PluginUpdated:  {"Updated", "ok"},
	PluginKept:     {"Not updated", "warn"},
	PluginMissing:  {"Not installed", "bad"},
	PluginChecking: {"Checking", "muted"},
}

func PluginSaid(check string) Said {
	return saidOf(pluginSaid, check, Said{"Not checked yet", "muted"})
}

func PinSaid(state string) Said {
	return saidOf(pinSaid, state, Said{state, "muted"})
}

func PluginCheckLine(check, branch, from, problem string, kept bool) (line, tone string, stamped bool) {
	keptWords := ""
	if kept {
		keptWords = ", so reviews and new sessions use the installed commit"
	}
	switch check {
	case "up-to-date":
		return "up to date.", "ok", true
	case "updated":
		if from != "" {
			return "updated from commit " + shortCommit(from) + ".", "ok", true
		}
		return "installed.", "ok", true
	case "kept":
		return "the head of " + branch + " wasn't installed" + keptWords + ". " + problem, "warn", true
	case "not-installed":
		return "nothing could be installed. " + problem, "bad", true
	case "checking":
		return "Checking the head of " + branch + " now.", "muted", false
	}
	return "Not checked yet at this start.", "muted", false
}

func shortCommit(sha string) string {
	if len(sha) == 40 && strings.Trim(sha, "0123456789abcdef") == "" {
		return sha[:7]
	}
	return sha
}

var runWord = map[string]string{"running": "is running", "passed": "passed", "failed": "failed", "timed-out": "didn't finish in time", "error": "couldn't run"}

func RunSaid(state string) Said {
	w, ok := runWord[state]
	if !ok {
		w = state
	}
	tone := "warn"
	switch state {
	case "passed":
		tone = "ok"
	case "running":
		tone = "muted"
	}
	return Said{w, tone}
}

var pushSaid = map[string]Said{"held": {"Held", "warn"}, "denied": {"Denied", "bad"}, "refused": {"Not pushed", "bad"}}

func PushResultSaid(result string) *Said {
	if s, ok := pushSaid[result]; ok {
		return &s
	}
	return nil
}

var fileError = map[string]string{
	"outside": "Only files inside this session's project open here.",
	"secret":  "This file can hold secrets, so the page doesn't show it.",
	"large":   "This file is over 512 KB, too large to show here.",
	"binary":  "This isn't a text file.",
	"missing": "This file isn't there anymore.",
	"link":    "This path goes through a symbolic link, which the page doesn't follow.",
	"commit":  "This commit isn't one this session's reviews read, so the page doesn't open files from it.",
}

func FileErrorMessage(code string) string {
	if w, ok := fileError[code]; ok {
		return w
	}
	return "The file couldn't be read."
}

var notDiffedWhy = map[string]string{"large": "over the 1 MiB a file may have to be diffed", "budget": "past the 8 MiB the view reads in all", "binary": "binary, its lines counted", "unreadable": "its stored object couldn’t be read"}

func NotDiffedWhy(code string) string {
	return notDiffedWhy[code]
}

var modeLabel = map[string]string{"default": "Manual", "manual": "Manual", "acceptEdits": "Accept edits", "plan": "Plan", "auto": "Auto", "dontAsk": "Don't ask", "bypassPermissions": "Bypass permissions"}

func ModeLabel(mode string) string {
	if w, ok := modeLabel[mode]; ok {
		return w
	}
	return mode
}

type MissingItem struct {
	Text  string
	Agent string
	Since string
}

const (
	SincePassed  = "passed"
	SinceBlocked = "blocked"
)

func PushMissing(p *PushView, r *ReviewView) []MissingItem {
	if p == nil {
		return nil
	}
	var roles []RoleReview
	if p.Result == "held" && r != nil && !r.Running && r.Commit == p.Commit {
		roles = r.Roles
	}
	out := make([]MissingItem, 0, len(p.Missing))
	for _, m := range p.Missing {
		item := MissingItem{Text: m}
		for _, role := range roles {
			if !strings.HasPrefix(m, "a review by "+role.Agent+" ") {
				continue
			}
			item.Agent = role.Agent
			switch {
			case string(role.State) == "pass":
				item.Since = SincePassed
			case string(role.State) == "block" && item.Since == "":
				item.Since = SinceBlocked
			}
		}
		out = append(out, item)
	}
	return out
}

func CloneWords(p CloneProgress, host string) string {
	switch p.Phase() {
	case CloneDownloading:
		if p.Total > 0 {
			return grouped(min(p.Objects, p.Total)) + " of " + grouped(p.Total) + " objects · " + byteWords(p.Received)
		}
		return byteWords(p.Received) + " received"
	case ClonePreparing:
		if p.Total > 0 && p.Checked > 0 {
			return "Preparing files · " + grouped(min(p.Checked, p.Total)) + " of " + grouped(p.Total) + " objects"
		}
		return "Preparing files…"
	}
	if host == "" {
		return "Waiting for the git host…"
	}
	return "Waiting for " + host + "…"
}

func grouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func byteWords(n int64) string {
	units := []string{"bytes", "KiB", "MiB", "GiB", "TiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + " " + units[0]
	}
	digits := 0
	if v < 10 {
		digits = 1
	}
	return strconv.FormatFloat(v, 'f', digits, 64) + " " + units[i]
}
