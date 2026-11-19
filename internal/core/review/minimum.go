package review

import (
	"strconv"
	"strings"
)

const (
	BelowMinimum Channel = "below-minimum"
	Unchecked    Channel = "unchecked"

	ApprovalChanged Channel = "approval-changed"
)

type Versions struct {
	ClaudeCode string
	Plugin     string
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
