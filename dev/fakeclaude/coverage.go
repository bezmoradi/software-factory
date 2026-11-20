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

var fakeRequirementID = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-[0-9]{1,4}\b`)

type fakeCoverage struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Evidence string `json:"evidence,omitempty"`
	Note     string `json:"note,omitempty"`
}

func coverageIDs() []string {
	data, err := os.ReadFile(filepath.Join("approved", "requirements.md"))
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range fakeRequirementID.FindAllString(string(data), -1) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func coverageLine(c fakeCoverage) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return "knackpack-coverage/1 " + string(b)
}

func coverageLines(agent, text string) string {
	data, err := os.ReadFile(filepath.Join("head", "COVERAGE-LINES"))
	if err != nil {
		return text
	}
	words := strings.Fields(string(data))
	on := func(w string) bool { return slices.Contains(words, w) }
	if on("one-role") && agent != "knackpack:adversarial-reviewer" {
		return text
	}
	ids := coverageIDs()
	if len(ids) == 0 {
		ids = []string{"REQ-01", "REQ-02"}
	}
	var made []string
	for i, id := range ids {
		c := fakeCoverage{ID: id}
		switch i % 3 {
		case 0:
			c.Status, c.Evidence = "verified", "README.md:1"
			if on("head") {
				c.Evidence = "head/README.md:1"
			}
			if on("phrase") {
				c.Note = "needs verification; changes required"
			}
		case 1:
			c.Status, c.Evidence, c.Note = "not-verified", "README.md:2", "head/COVERAGE-LINES asks for a not verified line with evidence"
		default:
			c.Status, c.Evidence = "violated", "README.md:1-2"
		}
		made = append(made, coverageLine(c))
	}
	bad := map[string]fakeCoverage{
		"unknown-id":  {ID: "REQ-999", Status: "verified", Evidence: "README.md:1"},
		"bad-status":  {ID: ids[0], Status: "passed", Evidence: "README.md:1"},
		"no-evidence": {ID: ids[0], Status: "verified"},
		"bad-path":    {ID: ids[0], Status: "verified", Evidence: "../etc/passwd:1"},
		"abs-path":    {ID: ids[0], Status: "verified", Evidence: "/etc/passwd:1"},
		"control":     {ID: ids[0], Status: "verified", Evidence: "READ\u0007ME.md:1"},
		"duplicate":   {ID: ids[0], Status: "violated", Evidence: "README.md:1"},
		"long":        {ID: ids[0], Status: "verified", Evidence: "README.md:1", Note: strings.Repeat("x", 1100)},
	}
	for _, w := range []string{"unknown-id", "bad-status", "no-evidence", "bad-path", "abs-path", "control", "duplicate", "long"} {
		if on(w) {
			made = append(made, coverageLine(bad[w]))
		}
	}
	if on("bad-json") {
		made = append(made, `knackpack-coverage/1 {"id":"`+ids[0]+`","status":"verified",`)
	}
	if on("filler") {
		for n := 1; n <= 40; n++ {
			made = append(made, coverageLine(fakeCoverage{ID: "FILL-" + strconv.Itoa(n), Status: "verified", Evidence: "README.md:1", Note: strings.Repeat("f", 1100)}))
		}
	}
	lines := strings.Split(text, "\n")
	at := len(lines)
	for i, line := range slices.Backward(lines) {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "knackpack-finding/1 ") && !strings.HasPrefix(t, "knackpack-findings/1:") && !strings.HasPrefix(t, "knackpack-verdict/1:") {
			break
		}
		if t != "" {
			at = i
		}
	}
	out := append(append(append([]string{}, lines[:at]...), made...), lines[at:]...)
	return strings.Join(out, "\n")
}
