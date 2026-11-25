package session

import (
	"cmp"
	"slices"
	"strconv"
	"time"
)

const (
	maxHistoryShown  = 8
	maxFrictionTips  = 3
	minFrictionCount = 2
)

var frictionWords = map[string]string{
	"edit-held":         "Claude tried to edit before the plan was approved",
	"define-held":       "Define was started with uncommitted files",
	"plan-held":         "Plan was started before the requirements were approved",
	"reviewer-early":    "a reviewer was started before Implement was done",
	"reviewer-fence":    "a reviewer tried to change the repository",
	"reviewer-capped":   "a reviewer was started past the round limit",
	"reviewer-workflow": "a reviewer was started from a workflow",
	"reviewer-resumed":  "Claude tried to continue a finished reviewer",
	"push-held":         "a push was held for what it was missing",
	RefusedPush:         "the agent ran a push-shaped command, which the gate turned away",
	RefusedRemote:       "a remote tool was held for you",
	RefusedProtected:    "a write by Claude's file tools was refused as protected",
	"reviewer-provider": "a reviewer came from a provider the gate doesn't trust",
	"implement-held":    "Implement was started before the plan was approved",
	RefusedCloud:        "the agent ran a command that hands work to the vendor's cloud, which was refused",
}

func HistoryShown(entries []AuditEntry) []AuditEntry {
	out := make([]AuditEntry, 0, maxHistoryShown)
	for _, e := range entries {
		if len(out) == maxHistoryShown {
			break
		}
		if e.Kind == "push" && e.Result == "allowed" || e.Result == ResultSending || e.Result == ResultPosting {
			continue
		}
		out = append(out, e)
	}
	return out
}

func FrictionTips(refusals map[string]int) []string {
	type count struct {
		kind string
		n    int
	}
	var counts []count
	for kind, n := range refusals {
		if _, known := frictionWords[kind]; known && n >= minFrictionCount {
			counts = append(counts, count{kind, n})
		}
	}
	slices.SortStableFunc(counts, func(a, b count) int {
		return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.kind, b.kind))
	})
	out := []string{}
	for _, c := range counts[:min(len(counts), maxFrictionTips)] {
		out = append(out, strconv.Itoa(c.n)+" times in this project, "+frictionWords[c.kind]+". If it keeps happening, say what to do instead in this project's CLAUDE.md.")
	}
	return out
}

type HistoryLine struct {
	Time time.Time
	Text string
	Tone string
}

var lifecycleWords = map[string]string{"session-start": "Session started", "session-wake": "Session woken", "session-stop": "Session stopped", "session-fork": "New session", "session-delete": "Session deleted"}

var startFromWords = map[string]string{"main": "from main", "branch": "from branch", "pull-request": "from pull request #", "fork": "from a commit of session", "pinned": "from a pinned commit"}

var historyResult = map[string]string{"pushed": "Pushed", "up-to-date": "Already on the remote", "held": "Held", "denied": "Denied", "refused": "Refused", "unclear": "Push outcome unclear"}

var historyException = map[string]string{
	"allow-push": "Push allowed with items missing", "override": "Verdict overridden", "allow-file": "Flagged file allowed", "allow-commit": "Flagged commit allowed",
	"issue": "Issue", "pull-request": "Pull request", "review": "Review", "findings": "Blocking findings for the agent", "pr-comment": "Pull request comment",
	"approve-startup": "Start-up files approved", "approve-requirements": "Requirements approved", "approve-plan": "Plan approved", "approve-team": "Team approved",
	"forget-team": "Team approval forgotten", "lock-tests": "Tests locked", "unlock-tests": "Tests unlocked", "reviewer-agent": "Reviewer agent file", "pack": "Reviewer pack",
	"after-review": "After review", "home-settings": "Changed agent settings", "permission-answer": "Permission card answered", "pr-follow": "Pull request followed",
	"pr-ready": "Pull request marked ready for review", "assign": "Assigned to the token's user", "away": "Away mode",
}

var historyOutcome = map[string]string{"unclear": "outcome unclear", "abandoned": "abandoned", "expired": "expired", "commented": "posted", "sent": "sent", "withheld": "withheld", "not-sent": "not sent", "ci-failure": "a CI failure", "review-comment": "a review comment"}

var lifecycleBy = map[string]string{"engine": ", by the engine", "engine, at reset": ", by the engine at the usage reset", "page": ", from the page"}

var historyBy = map[string]string{"agent": ", asked by the agent", "agent, after reviews": ", asked by the agent, after its reviews", "page": ", from the page", "engine": ", by the engine", "engine, at start": ", by the engine at start"}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func lifecycleText(e AuditEntry) string {
	what, ok := lifecycleWords[e.Kind]
	if !ok {
		what = e.Kind
	}
	ref := ""
	if e.From == "pull-request" {
		ref = startFromWords[e.From] + e.FromRef
	} else if w, known := startFromWords[e.From]; known && e.From != "" {
		ref = w
		if e.FromRef != "" {
			ref += " " + e.FromRef
		}
		if e.Source != "" {
			ref += " " + firstRunes(e.Source, 8)
		}
	}
	line := what
	if e.Kind == "session-delete" || e.Kind == "session-fork" {
		line += " (" + e.Branch + ")"
	}
	if ref != "" {
		line += " " + ref
	}
	return line
}

func historyText(e AuditEntry) (string, string) {
	if _, ok := lifecycleWords[e.Kind]; ok {
		line := lifecycleText(e)
		if e.Commit != "" {
			line += " at " + shortSHA7(e.Commit)
		}
		line += lifecycleBy[e.By]
		if e.Reason != "" {
			line += ": " + e.Reason
		}
		return line, ""
	}
	if e.Kind == AuditAccess {
		return accessHistory(e), "warn"
	}
	if e.Kind == AuditAction {
		return actionHistory(e), "warn"
	}
	kind := e.Kind
	if kind == "push" {
		kind = ""
		if e.PersonReason != "" && e.Result == "pushed" {
			kind = "allow-push"
		}
	}
	var what, said string
	if kind != "" {
		what = kind
		if w, ok := historyException[kind]; ok {
			what = w
		}
		if o, ok := historyOutcome[e.Result]; ok {
			what += ", " + o
		}
		switch {
		case e.Reviewer != "":
			what += " (" + e.Reviewer
			if e.Verdict != "" {
				what += ": " + e.Verdict
			}
			what += ")"
		case e.Path != "":
			what += " (" + e.Path + ")"
		}
		said = e.PersonReason
		if said == "" {
			said = e.Reason
		}
	} else {
		what = "Push " + e.Result
		if w, ok := historyResult[e.Result]; ok {
			what = w
		}
		switch {
		case e.Result == "held":
			said = strconv.Itoa(len(e.Missing)) + " missing"
		case e.Result == "pushed" || e.Result == "up-to-date":
		case e.Result == "denied" && e.PersonReason != "":
			said = "“" + e.PersonReason + "”"
		default:
			said = e.Reason
		}
	}
	line := what
	if e.Commit != "" {
		line += " " + shortSHA7(e.Commit)
	}
	if kind == "" {
		line += historyBy[e.By]
	}
	if said != "" {
		line += ": " + said
	}
	if kind != "" {
		return line, "warn"
	}
	return line, ""
}

var accessResults = map[string]string{ResultGranted: "Network access allowed", ResultNotGranted: "Network access not allowed", ResultExpired: "Network access expired", ResultGrantEnded: "Network access ended", ResultTakenBack: "Network access taken back"}

var actionResults = map[string]string{ResultGranted: "Action allowed again", ResultNotGranted: "Action not run", ResultExpired: "Action grant expired", ResultGrantEnded: "Action grant ended", ResultTakenBack: "Action grant taken back", "ran": "Action ran"}

func actionHistory(e AuditEntry) string {
	line, ok := actionResults[e.Result]
	if !ok {
		line = "Action " + e.Result
	}
	line += historyBy[e.By]
	if e.Reason != "" {
		line += ": " + e.Reason
	}
	return line
}

func accessHistory(e AuditEntry) string {
	line, ok := accessResults[e.Result]
	if !ok {
		line = "Network access " + e.Result
	}
	line += historyBy[e.By]
	if e.Reason != "" {
		line += ": " + e.Reason
	}
	return line
}

func HistoryLines(entries []AuditEntry) []HistoryLine {
	shown := HistoryShown(entries)
	out := make([]HistoryLine, 0, len(shown))
	for _, e := range shown {
		text, tone := historyText(e)
		out = append(out, HistoryLine{Time: e.Time, Text: text, Tone: tone})
	}
	return out
}
