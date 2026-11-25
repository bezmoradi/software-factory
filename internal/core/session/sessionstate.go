package session

import (
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/level"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	StallAfter  = 10 * time.Minute
	DoubledNote = "Read-only: another session on this engine holds this session's branch. Its changes, gate, recap and audit still open here; delete it to offer its unpushed commits, or keep working in the other session."

	ToneOff  = "off"
	ToneNeed = "need"
	ToneBusy = "busy"
	ToneDone = "done"

	ControlRelease  = "release"
	ControlWake     = "wake"
	ControlTake     = "control"
	ControlReadOnly = "read-only"

	ActionControl    = "control"
	ActionSteps      = "steps"
	ActionStop       = "stop"
	ActionWake       = "wake"
	ActionManage     = "manage"
	ActionModel      = "model"
	ActionBringMain  = "bringMain"
	ActionFork       = "fork"
	ActionCopy       = "copy"
	ActionRestore    = "restore"
	ActionCheckpoint = "checkpoint"
	ActionReview     = "review"
	ActionRules      = "rules"
	bypassPermission = "bypassPermissions"
)

type Status struct {
	Word string
	Tone string
}

type Control struct {
	Text        string
	Action      string
	Label       string
	Confirm     string
	ConfirmText string
	Tone        string
	Alert       bool
}

func AgentWord(agent string) string {
	switch agent {
	case "", "claude":
		return "Claude"
	case DevAgentID:
		return DevelopmentAgent("").Name
	}
	for _, a := range Agents() {
		if a.ID == agent {
			return a.Name
		}
	}
	return "The agent"
}

func Controllable(i Info) bool {
	return i.Controlled && i.Verified && i.Running && i.Live && !i.Disputed && !i.Outdated
}

func Sleeping(i Info) bool {
	return i.Kind == "background" && i.Verified && !i.Doubled && !i.Running && !i.Live && claudeSessionID(i.ID)
}

func loginNeeded(i Info) bool {
	return i.Running && i.Live && i.Login
}

func messages(n int) string {
	if n == 1 {
		return "1 message"
	}
	return strconv.Itoa(n) + " messages"
}

func SessionStatus(i Info) Status {
	switch {
	case i.Disputed:
		return Status{"two connections claim this session", ToneOff}
	case !i.Verified:
		return Status{"not started by this engine", ""}
	case i.Doubled:
		return Status{"read-only: another session holds its branch", ToneOff}
	case loginNeeded(i):
		return Status{"needs you to sign in again", ToneOff}
	case i.SignInWait:
		return Status{"Waiting for sign-in", ToneNeed}
	case i.HomeHeld:
		return Status{"settings changed: Wake to review them", ToneNeed}
	case !i.Running && i.Queued > 0:
		return Status{"waking, " + messages(i.Queued) + " waiting", ToneBusy}
	case !i.Running && i.LevelLost:
		return Status{"stopped: process lost", ToneOff}
	case !i.Running && i.StopReason != "":
		return Status{"stopped: " + i.StopReason, ToneOff}
	case !i.Running && i.State == "done":
		return Status{"done", ToneDone}
	case !i.Running:
		return Status{"stopped", ""}
	case !i.Live && i.WaitingFor != "":
		return Status{"waiting for " + i.WaitingFor, ""}
	case !i.Live:
		return Status{firstOf(i.Status, i.State, "running"), ""}
	case i.Outdated:
		return Status{"older plugin version", ToneOff}
	case i.Needs > 0:
		return Status{"needs you", ToneNeed}
	case i.Busy || i.Compacting || i.Subagents:
		return Status{"working", ToneBusy}
	}
	return Status{"idle", ""}
}

func firstOf(words ...string) string {
	for _, w := range words {
		if w != "" {
			return w
		}
	}
	return ""
}

func SessionWarnings(i Info) []string {
	out := []string{}
	if i.Verified && i.Level == level.Locked && !i.Sandboxed {
		out = append(out, "Locked, not sandboxed: every check since this run started passed, but a check failed or couldn't run in an earlier run, so the session's work isn't counted as sandboxed and its pushes are held on the sandbox item.")
	}
	if i.Verified && i.Level == level.Held {
		dropped := ""
		if i.LevelDropped {
			dropped = " This dropped from Locked after the fact."
		}
		out = append(out, "Held: every push from this session is held and no new session starts, because "+firstOf(i.LevelReason, "the sandbox check hasn't passed")+"."+dropped)
	}
	if loginNeeded(i) {
		out = append(out, "Claude Code couldn't use this session's login: its last request failed authentication. Sign in again inside the session's container ("+CommandsFor(i.Agent, i.ID, i.Box).SignIn+"); this page never signs in for you.")
	}
	if i.SignInWait {
		out = append(out, "Waiting for sign-in: "+AgentWord(i.Agent)+" in this session's container isn't signed in. Press Wake to open the session's sign-in panel, sign in there, then continue.")
	}
	if i.HomeHeld {
		out = append(out, "Its settings changed since its last start: "+AgentWord(i.Agent)+"'s user settings in this session's container changed, so it didn't wake. Press Wake to review them.")
	}
	return out
}

func SessionControl(i Info) Control {
	var c Control
	bypass := i.Mode == bypassPermission
	switch {
	case i.Disputed:
		lead := "Two connections claim to be this session. "
		if len(i.Claims) > 0 {
			lead = "Claimed by " + strconv.Itoa(len(i.Claims)) + " connections: " + strings.Join(i.Claims, ", ") + ". "
		}
		c = Control{Text: lead + "This page sends it nothing while they disagree. One of them may be a program pretending to be it.", Action: ControlRelease, Label: "End both connections", Alert: true}
	case !i.Verified:
		c.Text = "Read-only: this engine didn’t start this session, so the page only shows what it reports."
	case i.Doubled:
		c.Text = DoubledNote
	case !i.Running && i.State == "done":
		c.Text = "This session is done: its agent finished its work and ended by itself. A message wakes it again."
		if Sleeping(i) && !i.Controlled {
			c.Action, c.Label = ControlWake, "Wake it"
		}
	case Sleeping(i) && !i.Controlled:
		c = Control{Text: "This background session is stopped.", Action: ControlWake, Label: "Wake it"}
	case Sleeping(i) && bypass:
		c.Text = "This session is stopped, in bypass permissions mode: its tools run without asking."
	case Sleeping(i):
	case !i.Running:
		c.Text = "This session isn’t running."
	case !i.Live:
		c.Text = "Software Factory isn’t connected to this session, so this page can only list it."
	case i.Outdated:
		c.Text = "This session runs an older Software Factory. Restart it to control it from here."
	case !i.Controlled:
		c = Control{Text: "Read-only on this page. Use the button to let the page control it.", Action: ControlTake, Label: "Let this page control it",
			Confirm: "Confirm control", ConfirmText: "Press Confirm control to let this page send messages and answer prompts in this session."}
	default:
		c.Action, c.Label = ControlReadOnly, "Make it read-only"
		if bypass {
			c.Text = "Bypass permissions mode: tools run without asking."
		}
	}
	if bypass && (c.Action == "" || c.Action == ControlReadOnly) {
		c.Tone = "bad"
	}
	return c
}

func SessionActions(i Info) map[string]string {
	control := Controllable(i)
	owned := i.Verified && i.Kind == "background"
	finishes := AgentWord(i.Agent)
	out := map[string]string{
		ActionControl:   off(control, "Read-only on this page."),
		ActionStop:      off(control && i.Busy, "no turn is running that this page controls"),
		ActionWake:      off(Sleeping(i), "it isn’t a stopped Claude Code background session"),
		ActionManage:    off(owned && claudeSessionID(i.ID), "only a Claude Code background session this engine started is stopped or deleted here"),
		ActionModel:     off(i.Agent == "claude" && i.Controlled && !i.Doubled, "only a Claude Code session this page controls switches its model here"),
		ActionBringMain: off(owned && i.Project != "" && i.Running && i.Live && i.Controlled, "only a running background session this page controls brings in the latest main"),
		ActionFork:      off(owned && i.Project != "", "only a background session this engine started in a project of your list starts others from its commits"),
		ActionCopy:      off(i.Kind == "background" && (i.Running || Sleeping(i)), "only a running background session, or a stopped one Wake brings back, has these commands"),
		ActionReview:    off(i.Verified && i.Project != "", "reviews and pushes run for a session this engine started in a project of your list"),
		ActionRules:     off(i.Verified, "only a session this engine started has this view"),
	}
	switch {
	case i.Doubled:
		out[ActionSteps] = DoubledNote
	case !control:
		out[ActionSteps] = "Step buttons are off: this session is read-only on this page."
	case i.Busy:
		out[ActionSteps] = "Step buttons are available when " + finishes + " finishes."
	default:
		out[ActionSteps] = ""
	}
	switch {
	case !control:
		out[ActionCheckpoint] = "Read-only on this page."
	case i.Busy:
		out[ActionCheckpoint] = "Save a checkpoint once " + finishes + " finishes its turn."
	default:
		out[ActionCheckpoint] = ""
	}
	switch {
	case i.Doubled:
		out[ActionRestore] = DoubledNote
	case i.Busy:
		out[ActionRestore] = "Restore works once " + finishes + " finishes its turn."
	default:
		out[ActionRestore] = ""
	}
	return out
}

func off(allowed bool, why string) string {
	if allowed {
		return ""
	}
	return why
}

func ReadOnlyNote(i Info) string {
	if i.Doubled {
		return DoubledNote
	}
	return ""
}

type ReviewOffer struct {
	Reason  string
	Planned []WorkReviewer
}

func OfferReview(i Info, v GateView) ReviewOffer {
	var o ReviewOffer
	switch {
	case i.Doubled:
		o.Reason = DoubledNote
	case !v.Chosen:
		o.Reason = "Pick a work type first: it decides which reviews run."
	case v.Started == StartedNone:
		o.Reason = "Nothing to review or push yet: this session has no commits."
	}
	if !v.Chosen || v.Head == "" || i.Review != nil && i.Review.Commit == v.Head {
		return o
	}
	for _, r := range v.Reviewers {
		if r.State != "not-installed" {
			o.Planned = append(o.Planned, r)
		}
	}
	return o
}

func HeadPushed(i Info, head string) bool {
	p := i.Push
	return p != nil && !p.Running && (p.Result == "pushed" || p.Result == "up-to-date") && head != "" && p.Commit == head
}

func DocNote(d WorkDoc, clipped string, committed bool, head string) string {
	note := ""
	if d.Clipped && clipped != "" {
		note = clipped + " is longer than the page shows, so it can't be approved as a whole."
	}
	if committed && !d.Reported {
		note += " Read from commit " + shortSHA7(firstOf(d.Commit, head)) + " by the engine."
	}
	return strings.TrimSpace(note)
}

func PlanDocWord(p workflow.Profile) string {
	if p == workflow.Incident {
		return "The root cause"
	}
	return "The plan"
}
