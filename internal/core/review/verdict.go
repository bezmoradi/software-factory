package review

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

type State string

const (
	Running State = "running"
	Pass    State = "pass"
	Block   State = "block"
	Unknown State = "unknown"
	Failed  State = "failed"
)

type Channel string

const (
	Machine  Channel = "machine"
	Phrase   Channel = "phrase"
	Conflict Channel = "conflict"

	Shallow    Channel = "shallow"
	DeniedRead Channel = "denied-read"
	SeverePass Channel = "severe-pass"
	Withheld   Channel = "withheld"
)

type Verdict struct {
	State   State
	Verdict string
	Channel Channel
}

type Rule struct {
	block      *regexp.Regexp
	notAfterNo bool
	pass       *regexp.Regexp
}

const space = `\s\x0b\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	knackpackRule = &Rule{block: regexp.MustCompile(`(?i)\b(changes required|needs verification)\b`), notAfterNo: true, pass: regexp.MustCompile(`(?i)\bno blocking issues found\b`)}
	rules         = map[string]*Rule{
		"knackpack:slop-reviewer":           {block: regexp.MustCompile(`(?i)\b(changes required|needs context)\b`), notAfterNo: true, pass: regexp.MustCompile(`(?i)\b(simpler change available|nothing to remove in scope)\b`)},
		"knackpack:security-reviewer":       {block: regexp.MustCompile(`(?i)\b(blocked|ready for required human review)\b`), pass: regexp.MustCompile(`(?i)\b(no blocking security issues found|ready to build)\b`)},
		"knackpack:architecture-reviewer":   {block: regexp.MustCompile(`(?i)\b(revise before implementation|insufficient evidence)\b`), pass: regexp.MustCompile(`(?i)\bproceed\b`)},
		"knackpack:design-pattern-reviewer": {block: regexp.MustCompile(`(?i)\b(behaviorally incorrect|unsuitable for the stated need|insufficient evidence)\b`), pass: regexp.MustCompile(`(?i)\b(appropriate as implemented|correct but could be simpler)\b`)},
	}
	knackpackReviewer = regexp.MustCompile(`^knackpack:[\w-]+-reviewer$`)
	afterNo           = regexp.MustCompile(`(?i)\bno[` + space + `]$`)
	machineLine       = regexp.MustCompile(`(?i)^knackpack-verdict/1:[` + space + `]*(pass|block)$`)
	verdictLine       = regexp.MustCompile(`(?i)^verdict:[` + space + `]*(pass|block)[` + space + `]*\.?$`)
	verdictPrefix     = regexp.MustCompile(`(?i)^verdict:[` + space + `]*`)
	markup            = regexp.MustCompile("[*_`#>]")
)

func RuleFor(agent string) *Rule {
	if r, ok := rules[agent]; ok {
		return r
	}
	if knackpackReviewer.MatchString(agent) {
		return knackpackRule
	}
	return nil
}

func (r *Rule) blocks(line string) bool {
	if !r.notAfterNo {
		return r.block.MatchString(line)
	}
	for _, m := range r.block.FindAllStringIndex(line, -1) {
		if !afterNo.MatchString(line[:m[0]]) {
			return true
		}
	}
	return false
}

func (r *Rule) passesAtStart(line string) bool {
	m := r.pass.FindStringIndex(verdictPrefix.ReplaceAllString(line, ""))
	return m != nil && m[0] == 0
}

func plainLine(l string) string {
	return strings.TrimFunc(markup.ReplaceAllString(l, ""), func(c rune) bool { return unicode.IsSpace(c) || c == 0xfeff })
}

func verdictsIn(re *regexp.Regexp, lines []string) []string {
	var out []string
	for _, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			out = append(out, strings.ToLower(m[1]))
		}
	}
	return out
}

func stateOf(word string) State {
	if strings.EqualFold(word, "pass") {
		return Pass
	}
	return Block
}

func ReplyVerdict(r *Rule, answer string) Verdict {
	var lines []string
	for l := range strings.SplitSeq(answer, "\n") {
		if p := plainLine(l); p != "" {
			lines = append(lines, p)
		}
	}
	last := ""
	if len(lines) > 0 {
		last = lines[len(lines)-1]
	}
	tail := lines[max(0, len(lines)-3):]
	machines := verdictsIn(machineLine, lines)
	if r == nil || len(machines) > 0 {
		seen := map[string]bool{}
		for _, v := range append(append([]string{}, machines...), verdictsIn(verdictLine, tail)...) {
			seen[v] = true
		}
		if len(seen) > 1 {
			return Verdict{State: Unknown, Channel: Conflict}
		}
	}
	if m := machineLine.FindStringSubmatch(last); m != nil {
		said := last
		for i := len(lines) - 2; i >= 0; i-- {
			if !strings.HasPrefix(lines[i], "/") && !machineLine.MatchString(lines[i]) {
				said = lines[i]
				break
			}
		}
		return Verdict{State: stateOf(m[1]), Verdict: clip(said, 200), Channel: Machine}
	}
	if r == nil {
		for _, t := range slices.Backward(tail) {
			if m := verdictLine.FindStringSubmatch(t); m != nil {
				return Verdict{State: stateOf(m[1]), Verdict: clip(t, 200), Channel: Phrase}
			}
		}
		return Verdict{State: Unknown}
	}
	read := func(l string) State {
		if r.blocks(l) {
			return Block
		}
		if r.passesAtStart(l) {
			return Pass
		}
		return ""
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "/") {
			continue
		}
		state := read(l)
		if state == "" {
			break
		}
		if len(machines) > 0 && State(machines[0]) != state {
			return Verdict{State: Unknown, Channel: Conflict}
		}
		return Verdict{State: state, Verdict: clip(l, 200), Channel: Phrase}
	}
	for _, l := range lines {
		if machineLine.MatchString(l) {
			return Verdict{State: stateOf(machines[0]), Verdict: clip(l, 200), Channel: Machine}
		}
	}
	return Verdict{State: Unknown}
}
