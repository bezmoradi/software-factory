package session

import (
	"bytes"
	"context"
	"errors"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	MaxCommandOutput    = 4 << 20
	CommandOutputHead   = 1 << 20
	CommandOutputTail   = MaxCommandOutput - CommandOutputHead
	CommandOutputTop    = "/tmp"
	CommandOutputFolder = "software-factory/commands"
	OutputGone          = "gone"
	maxOutputShown      = 1 << 20
	outputShownHead     = 256 << 10
	outputShownTail     = maxOutputShown - outputShownHead
)

var outputRun = regexp.MustCompile(`^[0-9a-f]{16}$`)

type KeptOutput struct {
	Head []byte
	Tail []byte
	Left int64
}

type CommandOutput struct {
	Kind   string
	Text   string
	Lead   string
	Size   int64
	Cut    bool
	Masked bool
}

func ValidOutputRun(run string) bool {
	return outputRun.MatchString(run)
}

func CommandOutputName(kind, run string) string {
	return kind + "-" + run + ".log"
}

func CommandOutputPath(kind, run string) string {
	return path.Join(CommandOutputTop, CommandOutputFolder, CommandOutputName(kind, run))
}

func OmittedLine(n int64) string {
	return "[Software Factory left out " + grouped(n) + " bytes here.]\n"
}

func JoinEnds(head, tail []byte, left int64) []byte {
	if left <= 0 {
		return slices.Concat(head, tail)
	}
	if i := bytes.LastIndexByte(head, '\n'); i >= 0 {
		left += int64(len(head) - i - 1)
		head = head[:i+1]
	} else {
		left += int64(len(head))
		head = nil
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		left += int64(i + 1)
		tail = tail[i+1:]
	}
	return slices.Concat(head, []byte(OmittedLine(left)), tail)
}

func shownEnds(head, tail []byte, left int64) ([]byte, bool) {
	if left <= 0 {
		head, tail = slices.Concat(head, tail), nil
		if len(head) <= maxOutputShown {
			return head, false
		}
		head, tail = head[:outputShownHead], head[outputShownHead:]
	}
	h := head[:min(len(head), outputShownHead)]
	t := tail[len(tail)-min(len(tail), outputShownTail):]
	return JoinEnds(h, t, left+int64(len(head)-len(h)+len(tail)-len(t))), true
}

func mib(n int) string {
	if n >= 1<<20 {
		return strconv.Itoa(n>>20) + " MiB"
	}
	return strconv.Itoa(n>>10) + " KiB"
}

func outputLead(size int64, over, cut, masked bool) string {
	s := "The command printed " + byteWords(size) + "."
	switch {
	case over:
		s += " The session's container keeps its first " + mib(CommandOutputHead) + " and its last " + mib(CommandOutputTail) + " for the agent to read. This shows the first " + mib(outputShownHead) + " and the last " + mib(outputShownTail) + "."
	case cut:
		s += " This shows the first " + mib(outputShownHead) + " and the last " + mib(outputShownTail) + ". The agent can read all of it in the session's container."
	default:
		s += " This is all of it."
	}
	if masked {
		s += " Text that looks like a token is hidden."
	}
	return s
}

func (w *SessionWork) CommandOutput(ctx context.Context, sid, kind string) (CommandOutput, error) {
	if _, ok := w.reg.workInfo(sid); !ok {
		return CommandOutput{}, notFound("session", "The engine has no session of that id.")
	}
	check, setup := w.Runs(sid)
	run, name := check, "check"
	if kind == CommandSetup {
		run, name = setup, "setup command"
	}
	switch {
	case run == nil:
		return CommandOutput{}, conflict("no-output", "The "+name+" hasn't run in this session, so there is no output to show.")
	case run.State == RunRunning:
		return CommandOutput{}, conflict("running", "The "+name+" is still running. Its output shows once it ends.")
	case run.Output == "":
		return CommandOutput{}, conflict("no-output", "The session's container kept no output of this run of the "+name+".")
	}
	unreached := conflict("container", "The engine can't reach the session's container, so it can't read the output. A container that stopped keeps none.")
	host, err := w.commandHost(ctx, sid)
	if err != nil {
		return CommandOutput{}, unreached
	}
	kept, err := host.CommandOutput(ctx, kind, run.Output)
	if h, ok := errors.AsType[*HostError](err); ok && h.Code == OutputGone {
		return CommandOutput{}, conflict(OutputGone, "The session's container no longer keeps this output: a newer run replaced it, or the container restarted.")
	}
	if err != nil {
		return CommandOutput{}, unreached
	}
	size := int64(len(kept.Head)) + max(kept.Left, 0) + int64(len(kept.Tail))
	shown, cut := shownEnds(kept.Head, kept.Tail, kept.Left)
	text := ansi.ReplaceAllString(strings.ToValidUTF8(string(shown), "�"), "")
	masked := maskLines(text, true)
	return CommandOutput{Kind: kind, Text: masked, Size: size, Cut: cut, Masked: masked != text, Lead: outputLead(size, kept.Left > 0, cut, masked != text)}, nil
}
