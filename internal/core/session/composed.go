package session

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxComments        = 50
	MaxNote            = 2000
	MaxComment         = 2000
	ReviseRequirements = "requirements"
	RevisePlan         = "plan"
	ReviseRootCause    = "root-cause"
)

type LineComment struct {
	File    string
	Line    int
	Removed bool
	Text    string
}

func ValidReviseKind(kind string) bool {
	return kind == ReviseRequirements || kind == RevisePlan || kind == ReviseRootCause
}

func pageSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0xfeff, 0x2028, 0x2029:
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

func pageTrim(s string) string { return strings.TrimFunc(s, pageSpace) }

func oneLine(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if !pageSpace(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j, broken := i, false
		for j < len(rs) && pageSpace(rs[j]) {
			broken = broken || rs[j] == '\n'
			j++
		}
		if broken {
			b.WriteByte(' ')
		} else {
			b.WriteString(string(rs[i:j]))
		}
		i = j
	}
	return pageTrim(b.String())
}

func ReviseMessage(kind, note string, docPath func() (string, error)) (string, error) {
	note = pageTrim(note)
	if note == "" {
		return "", onField("note", invalid("note", "Type what should change first."))
	}
	if utf16Len(note) > MaxNote {
		return "", onField("note", invalid("note", "At most 2,000 characters."))
	}
	var text string
	switch kind {
	case ReviseRequirements:
		text = "Before going on, change the agreed requirements in this session's intent record: " + note
	case RevisePlan, ReviseRootCause:
		path, err := docPath()
		if err != nil {
			return "", err
		}
		if path == "" {
			what := "the plan's file"
			if kind == ReviseRootCause {
				what = "the root cause's file"
			}
			return "", conflict("no file", "The engine doesn't know "+what+" for this session, so it can't ask for the change. Ask for it in the conversation instead.")
		}
		if kind == RevisePlan {
			text = "Before going on, change the plan in " + path + ": " + note
		} else {
			text = "/knackpack:debug-incident Correct the diagnosis in " + path + ": " + note
		}
	default:
		return "", invalid("text", "The engine asks for a change to the requirements, the plan or the root cause only.")
	}
	return text, nil
}

func CommentsMessage(list []LineComment) (string, error) {
	const fewer = " Remove some, send these, then add the rest."
	if len(list) == 0 {
		return "", invalid("comments", "Add a comment first.")
	}
	if len(list) > MaxComments {
		return "", invalid("comments", "At most 50 comments go in one message."+fewer)
	}
	count := strconv.Itoa(len(list))
	if len(list) == 1 {
		count = "one"
	}
	lines := []string{"My comments on lines of the change, " + count + " in all:", ""}
	for i, c := range list {
		text := oneLine(c.Text)
		which := "Comment " + strconv.Itoa(i+1) + ", on " + c.File + " line " + strconv.Itoa(c.Line)
		if text == "" {
			return "", invalid("comments", which+", is empty: write it, or remove it.")
		}
		if utf16Len(pageTrim(c.Text)) > MaxComment {
			return "", invalid("comments", which+", is over 2,000 characters: shorten it.")
		}
		removed := ""
		if c.Removed {
			removed = " (a removed line, numbered in the old file)"
		}
		lines = append(lines, "- "+c.File+" line "+strconv.Itoa(c.Line)+removed+": "+text)
	}
	text := strings.Join(lines, "\n")
	if utf16Len(text) > MaxText {
		return "", invalid("comments", "These comments would make a message over 20,000 characters."+fewer)
	}
	return text, nil
}

const (
	CheckpointCommand = "/knackpack:checkpoint"
	CommitAsk         = "Commit every uncommitted change on this branch, new files included, so a push sends all of it. If a file shouldn't be committed, such as a secret, a local setting or a scratch file, don't commit it: ask me whether to delete it or add it to .gitignore. Each commit message says what changed and why."

	AskDefine         = "define"
	AskPlan           = "plan"
	AskTriage         = "triage"
	AskReproduce      = "reproduce"
	AskCause          = "cause"
	AskFix            = "fix"
	AskCommit         = "commit"
	AskIssues         = "issues"
	AskHandoff        = "handoff"
	AskIncidentReview = "incident-review"
	AskCheckpoint     = "checkpoint"
)

var gateFree = map[string]string{AskCommit: CommitAsk, AskCheckpoint: CheckpointCommand}

func ValidAsk(ask string) bool {
	switch ask {
	case AskDefine, AskPlan, AskTriage, AskReproduce, AskCause, AskFix, AskCommit, AskIssues, AskHandoff, AskIncidentReview, AskCheckpoint:
		return true
	}
	return false
}

func AskNeedsGate(ask string) bool {
	_, free := gateFree[ask]
	return !free
}

func fixIncidentMessage(d WorkDoc) string {
	return "/knackpack:fix-incident The developer approved " + d.Path + " on the Software Factory page: its committed text has the SHA-256 " + d.Hash + ". Run incident-check.sh with KNACKPACK_INCIDENT_APPROVED_SHA256=" + d.Hash + " in its environment, as in KNACKPACK_INCIDENT_APPROVED_SHA256=" + d.Hash + " sh \"${CLAUDE_PLUGIN_ROOT}/scripts/incident-check.sh\" followed by that file's absolute path."
}

func stageCommands(v GateView) map[string]string {
	out := map[string]string{}
	for _, step := range []workflow.Step{workflow.Define, workflow.Plan} {
		if c := v.Commands[step]; c != "" {
			out[string(step)] = c
		}
	}
	incident := v.Effective == workflow.Incident
	if incident {
		for _, ask := range []string{AskTriage, AskReproduce, AskCause} {
			out[ask] = "/knackpack:debug-incident"
		}
		if v.Plan != nil && v.Plan.Approved {
			out[AskFix] = fixIncidentMessage(*v.Plan)
		}
	}
	if v.Adapter == workflow.Knackpack {
		out[AskIssues], out[AskHandoff] = "/knackpack:create-issues", "/knackpack:handoff"
		if incident {
			out[AskIncidentReview] = "/knackpack:incident-review"
		}
	}
	return out
}

func StageCommands(v GateView) map[string]string {
	return stageCommands(v)
}

func AskMessage(ask string, view func() (GateView, error)) (string, error) {
	if text, ok := gateFree[ask]; ok {
		return text, nil
	}
	v, err := view()
	if err != nil {
		return "", err
	}
	if text := stageCommands(v)[ask]; text != "" {
		return text, nil
	}
	return "", conflict("step", "This session's workflow doesn't offer that step now.")
}
