package review

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const slopReviewer = "knackpack:slop-reviewer"

var simplerSaid = regexp.MustCompile(`(?i)^simpler change available\b`)

const (
	BelowMinimum Channel = "below-minimum"
	Unchecked    Channel = "unchecked"
	Checking     Channel = "checking"
	CheckFailed  Channel = "check-failed"

	ApprovalChanged Channel = "approval-changed"
)

type Versions struct {
	ClaudeCode string
	Plugin     string
	Models     []string
	Model      string
	Effort     string
}

func (v Versions) Covers(w Versions) bool {
	if v.ClaudeCode == "" || v.ClaudeCode != w.ClaudeCode || v.Plugin != w.Plugin || v.Model != w.Model || v.Effort != w.Effort {
		return false
	}
	for _, m := range w.Models {
		if !slices.Contains(v.Models, m) {
			return false
		}
	}
	return true
}

func (v Versions) Words() string {
	parts := []string{"Claude Code " + orUnknown(v.ClaudeCode)}
	if v.Plugin != "" {
		parts = append(parts, "plugin or agent file "+short(v.Plugin))
	}
	if len(v.Models) > 0 {
		parts = append(parts, "model "+strings.Join(v.Models, ", "))
	}
	if v.Model != "" {
		parts = append(parts, "started with --model="+v.Model)
	}
	if v.Effort != "" {
		parts = append(parts, "started with --effort="+v.Effort)
	}
	return strings.Join(parts, ", ")
}

type CheckResult string

const (
	CheckRunning   CheckResult = "running"
	CheckPassed    CheckResult = "passed"
	CheckFailure   CheckResult = "failed"
	CheckWarned    CheckResult = "warned"
	CheckUnsettled CheckResult = "unsettled"
)

func (c CheckResult) Counts() bool {
	return c == CheckPassed || c == CheckWarned || c == CheckUnsettled
}

func CheckOutcome(caught bool, defect, clean State) CheckResult {
	switch {
	case caught && clean == Pass:
		return CheckPassed
	case caught && clean == Block:
		return CheckWarned
	case caught:
		return CheckUnsettled
	case defect == Pass:
		return CheckFailure
	}
	return CheckUnsettled
}

func DefectCaught(role Role, agent string, defect State, channel Channel, verdict, reply string) bool {
	if defect == Block {
		return true
	}
	if role != Simplicity || (defect != Pass && (defect != Unknown || channel != SeverePass)) {
		return false
	}
	return AnyCount(reply) || (agent == slopReviewer && (simplerVerdict(verdict) || slices.ContainsFunc(strings.Split(reply, "\n"), simplerVerdict)))
}

func simplerVerdict(line string) bool {
	return simplerSaid.MatchString(verdictPrefix.ReplaceAllString(plainLine(line), ""))
}

type Minimum struct {
	ClaudeCode string
	Plugin     string
	Pinned     string
}

func (m Minimum) Below(v Versions, plugin bool) string {
	var parts []string
	if m.ClaudeCode != "" && Older(v.ClaudeCode, m.ClaudeCode) {
		parts = append(parts, "its Claude Code "+orUnknown(v.ClaudeCode)+" is older than the minimum "+m.ClaudeCode)
	}
	if plugin && m.Plugin != "" && v.Plugin != m.Plugin && v.Plugin != m.Pinned {
		parts = append(parts, "its plugin commit "+orUnknown(short(v.Plugin))+" is neither the minimum "+short(m.Plugin)+" nor the pinned "+short(m.Pinned))
	}
	return strings.Join(parts, ", and ")
}

func Older(version, minimum string) bool {
	if minimum == "" {
		return false
	}
	have, ok := versionParts(version)
	want, wok := versionParts(minimum)
	if !ok || !wok {
		return true
	}
	for i := range max(len(have), len(want)) {
		h, w := at(have, i), at(want, i)
		if h != w {
			return h < w
		}
	}
	return false
}

func versionParts(s string) ([]int, bool) {
	core, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(s), "v"), "-")
	core, _, _ = strings.Cut(core, "+")
	if core == "" {
		return nil, false
	}
	var out []int
	for p := range strings.SplitSeq(core, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func at(list []int, i int) int {
	if i < len(list) {
		return list[i]
	}
	return 0
}

func orUnknown(s string) string {
	if s == "" {
		return "(not recorded)"
	}
	return s
}
