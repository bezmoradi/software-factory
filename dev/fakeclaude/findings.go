package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	fakeHeading  = regexp.MustCompile(`^#{2,4} \[(Critical|High|Medium|Low)\] (.+)$`)
	fakeLocation = regexp.MustCompile("^\\*\\*Location:\\*\\* `([^`]+)`$")
)

type fakeFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Blocking bool   `json:"blocking"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Title    string `json:"title"`
}

func (f *fakeFinding) at(loc string) {
	f.Path, f.Line = loc, 0
	if i := strings.LastIndex(loc, ":"); i > 0 {
		if n, err := strconv.Atoi(loc[i+1:]); err == nil {
			f.Path, f.Line = loc[:i], n
		}
	}
}

func findingLines(text string) string {
	data, err := os.ReadFile(filepath.Join("head", "FINDING-LINES"))
	if err != nil {
		return text
	}
	spot, prose, contradict := "", false, false
	for w := range strings.FieldsSeq(string(data)) {
		switch {
		case w == "prose":
			prose = true
		case w == "contradict":
			contradict = true
		case spot == "" && strings.Contains(w, ":"):
			spot = w
		}
	}
	lines := strings.Split(text, "\n")
	var found []*fakeFinding
	for i, l := range lines {
		if m := fakeHeading.FindStringSubmatch(l); m != nil {
			found = append(found, &fakeFinding{ID: "F" + strconv.Itoa(len(found)+1), Severity: strings.ToLower(m[1]), Blocking: m[1] == "Critical" || m[1] == "High", Title: m[2]})
			continue
		}
		if m := fakeLocation.FindStringSubmatch(l); m != nil && len(found) > 0 {
			loc := m[1]
			if spot != "" {
				loc = spot
				if prose {
					lines[i] = "**Location:** `" + spot + "`"
				}
			}
			found[len(found)-1].at(loc)
		}
	}
	if contradict {
		f := &fakeFinding{ID: "F99", Severity: "critical", Blocking: true, Title: "head/FINDING-LINES asks for a finding line that contradicts the verdict"}
		f.at("README.md:1")
		found = append(found, f)
	}
	if len(found) == 0 {
		return text
	}
	at := len(lines)
	for i, line := range slices.Backward(lines) {
		if strings.HasPrefix(strings.TrimSpace(line), "knackpack-verdict/1:") {
			at = i
			if i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "knackpack-findings/1:") {
				at = i - 1
			}
			break
		}
	}
	var made []string
	for _, f := range found {
		b, err := json.Marshal(f)
		if err == nil {
			made = append(made, "knackpack-finding/1 "+string(b))
		}
	}
	out := append(append(append([]string{}, lines[:at]...), made...), lines[at:]...)
	return strings.Join(out, "\n")
}
