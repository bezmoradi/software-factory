package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	debugIncident = "/knackpack:debug-incident"
	fixIncident   = "/knackpack:fix-incident"
	incidentTask  = "fake-incident"
	rootCauseFile = "ROOT-CAUSE.md"
)

var (
	approvedHash = regexp.MustCompile(`KNACKPACK_INCIDENT_APPROVED_SHA256=([0-9a-f]{64})`)
	nextStatus   = map[string]string{"": "TRIAGED", "TRIAGED": "REPRODUCED", "REPRODUCED": "CONFIRMED", "CONFIRMED": "CONFIRMED"}
)

func incidentLine(text string) (string, bool) {
	switch {
	case strings.HasPrefix(text, debugIncident):
		return incidentStep(), true
	case strings.HasPrefix(text, fixIncident):
		return incidentFix(text), true
	}
	return "", false
}

func fileStatus(text string) string {
	under := false
	for l := range strings.SplitSeq(text, "\n") {
		switch {
		case !under:
			under = strings.TrimSpace(l) == "## Status"
		case strings.HasPrefix(l, "#"):
			return ""
		case strings.TrimSpace(l) != "":
			return strings.Fields(l)[0]
		}
	}
	return ""
}

func incidentStep() string {
	abs, ok := docsFile(rootCauseFile)
	if !ok {
		return "The fake agent has no documents folder: " + docsVariable + " isn't an absolute path, so it ran no incident step."
	}
	old, _ := os.ReadFile(abs)
	status := nextStatus[fileStatus(string(old))]
	if status == "" {
		return "The fake agent can't tell the next incident step from " + abs + "."
	}
	text := "# Root cause: the fake export fails\n\n## Status\n\n" + status + "\n\n## Incident\n\n- Task: " + incidentTask + "\n- Symptom: the fake export answers 500\n\n## Evidence\n\n## Missing evidence\n\n## Reproduction\n\n## Hypotheses\n\n## Cause\n\n"
	if status == "CONFIRMED" {
		text += "The fake cause, confirmed by the fake reproduction.\n\n"
	}
	text += "## Remaining uncertainty\n"
	return "The fake agent ran one incident step, Status " + status + ": " + writeDoc(rootCauseFile, "root cause", text)
}

func incidentFix(text string) string {
	abs, ok := docsFile(rootCauseFile)
	if !ok {
		return "refused: " + docsVariable + " isn't an absolute path, so there's no diagnosis to read"
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "refused: " + abs + " doesn't exist, so there's no diagnosis to fix"
	}
	if s := fileStatus(string(data)); s != "CONFIRMED" {
		return "refused: its Status is " + s + ", not CONFIRMED"
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	m := approvedHash.FindStringSubmatch(text)
	switch {
	case m == nil:
		return "refused: no approval was passed in KNACKPACK_INCIDENT_APPROVED_SHA256, and this session has no one to ask"
	case m[1] != got:
		return "refused: the approved SHA-256 is " + m[1] + ", but the diagnosis now hashes to " + got
	}
	fix := filepath.Join("src", "incident-fix.txt")
	if os.MkdirAll(filepath.Join(worktreeTop(), "src"), 0o755) != nil || os.WriteFile(filepath.Join(worktreeTop(), fix), []byte("the fake fix\n"), 0o644) != nil {
		return "approved " + got + ", and the fake agent couldn't write " + fix
	}
	commit, ok := commitFiles("Fix the fake export's failure", fix)
	if !ok {
		return "approved " + got + ", and the fake agent couldn't commit " + fix
	}
	return "approved " + got + ": the fake agent committed its fix to " + fix + " as " + commit + "."
}

func commitFiles(message string, paths ...string) (string, bool) {
	top := worktreeTop()
	git := func(args ...string) (string, bool) {
		cmd := exec.Command("git", append([]string{"-C", top, "-c", "user.name=Fake Agent", "-c", "user.email=fake-agent@example.invalid"}, args...)...)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	if _, ok := git(append([]string{"add", "--"}, paths...)...); !ok {
		return "", false
	}
	if _, ok := git("commit", "-q", "-m", message); !ok {
		return "", false
	}
	return git("rev-parse", "--short", "HEAD")
}
