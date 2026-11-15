package session

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxChangeNotes = 200
	maxFileKinds   = 100
	maxNamesShown  = 8
	binarySniff    = 8000
)

type changeScan struct {
	problems []string
	tests    []ChangeNote
	kinds    []FileKind
	deps     []DependencyChange
	moreDeps int
}

type lineCount struct {
	what string
	re   *regexp.Regexp
}

var (
	testPath     = regexp.MustCompile(`(?i)(_test\.go$|\.(test|spec)\.[cm]?[jt]sx?$|(^|/)test_[^/]*\.py$|_test\.py$|(^|/)(__tests__|tests?|spec|specs)/|(^|/)src/test/|Tests?\.(java|kt|cs)$|_spec\.rb$)`)
	goTestFunc   = regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]*|Benchmark[A-Za-z0-9_]*|Fuzz[A-Za-z0-9_]*)\s*\(`)
	pyTestFunc   = regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+(test[A-Za-z0-9_]*)\s*\(`)
	jsTestName   = regexp.MustCompile("(?m)\\b(?:it|test)\\s*\\(\\s*['\"`]([^'\"`\\n]{1,160})['\"`]")
	annotatedRun = regexp.MustCompile(`(?m)^\s*(?:#\[test\]|#\[tokio::test|@Test\b|@ParameterizedTest\b|\[Fact\]|\[Test\]|\[TestMethod\])`)
	skipMarks    = []lineCount{
		{"skip markers", regexp.MustCompile(`\bt\.Skip(?:Now|f)?\(|\b(?:it|test|describe)\.skip\b|\bx(?:it|test|describe)\s*\(|@pytest\.mark\.(?:skip|xfail)\b|\bpytest\.skip\(|@unittest\.skip|#\[ignore\]|@Disabled\b|@Ignore\b|\bskip\(\s*\)`)},
		{"focus markers (.only), which skip every other test", regexp.MustCompile(`\b(?:it|test|describe)\.only\b|\bf(?:it|describe)\s*\(`)},
	}
	assertLine  = regexp.MustCompile(`\bassert|\bexpect\s*\(|\brequire\.[A-Z]|\bt\.(?:Error|Errorf|Fatal|Fatalf|Fail|FailNow)\b|\.should\b|\bshould\.|\bverify\s*\(`)
	negatedLine = regexp.MustCompile(`\.not\.|\bassert\.?Not|\bassertNot|\brequire\.Not|\bassert\s+not\b|\bassertFalse\b|\bassert\.False\b|toBeFalsy\b`)
	suppressed  = []lineCount{
		{"eslint-disable", regexp.MustCompile(`eslint-disable`)},
		{"nolint", regexp.MustCompile(`//\s*nolint\b`)},
		{"ts-ignore or ts-expect-error", regexp.MustCompile(`@ts-(?:ignore|expect-error|nocheck)\b`)},
		{"# type: ignore", regexp.MustCompile(`#\s*type:\s*ignore`)},
		{"# noqa", regexp.MustCompile(`#\s*noqa\b`)},
		{"pylint: disable", regexp.MustCompile(`pylint:\s*disable`)},
		{"#[allow(...)]", regexp.MustCompile(`#!?\[allow\(`)},
		{"@SuppressWarnings", regexp.MustCompile(`@SuppressWarnings|\[SuppressMessage|#pragma\s+warning\s+disable`)},
		{"coverage ignore", regexp.MustCompile(`(?:istanbul|c8|v8)\s+ignore|pragma:\s*no\s*cover`)},
		{"NOSONAR", regexp.MustCompile(`NOSONAR`)},
	}
	loggingCall = regexp.MustCompile(`\b(?:log|logger|logging|slog|klog|zap|zerolog|console|metrics|statsd|tracer|span|sentry|Sentry|otel|telemetry|audit)\s*\.\s*[A-Za-z_]+\s*\(|\blog(?:ger)?\.(?:Info|Warn|Error|Debug|Print)`)
	runLater    = regexp.MustCompile(`(?i)(^|/)(makefile|gnumakefile|[^/]+\.mk|justfile|taskfile\.ya?ml|\.pre-commit-config\.ya?ml)$|^\.github/workflows/|^\.husky/|^\.githooks/`)
)

func isTestPath(p string) bool { return testPath.MatchString(p) }

func scanChange(files ChangeBlobs, repo, base, head string) changeScan {
	var out changeScan
	list, err := files.ChangedFiles(context.Background(), repo, base, head)
	if err != nil {
		out.problems = append(out.problems, clip("the engine couldn't list the change's files: "+err.Error(), 300))
		return out
	}
	if len(list) > maxExtraFiles {
		out.problems = append(out.problems, "the change has more than "+strconv.Itoa(maxExtraFiles)+" files; only the first ones were read")
		list = list[:maxExtraFiles]
	}
	var small, large []string
	for _, c := range list {
		if c.NewMode == push.ModeSubmodule || c.OldMode == push.ModeSubmodule {
			continue
		}
		into := &small
		if manifestKind(c.Path) != "" && lockfile(c.Path) {
			into = &large
		}
		if c.OldSHA != "" {
			*into = append(*into, c.OldSHA)
		}
		if c.NewSHA != "" {
			*into = append(*into, c.NewSHA)
		}
	}
	blobs := map[string]BlobData{}
	if len(small) > 0 {
		got, err := files.Blobs(repo, slices.Compact(slices.Sorted(slices.Values(small))), maxScannedBlob, maxScannedBytes)
		if err != nil {
			out.problems = append(out.problems, clip("the engine couldn't read the changed files: "+err.Error(), 300))
		}
		maps.Copy(blobs, got)
	}
	if len(large) > 0 {
		got, err := files.Blobs(repo, slices.Compact(slices.Sorted(slices.Values(large))), maxLockBlob, maxScannedBytes)
		if err != nil {
			out.problems = append(out.problems, clip("the engine couldn't read the changed lockfiles: "+err.Error(), 300))
		}
		maps.Copy(blobs, got)
	}
	cut := false
	text := func(sha string) (string, bool) {
		if sha == "" {
			return "", true
		}
		b, ok := blobs[sha]
		if !ok || b.Cut {
			cut = true
			return "", false
		}
		return string(b.Data), true
	}
	var deps []DependencyChange
	for _, c := range list {
		if c.NewMode == push.ModeSubmodule || c.OldMode == push.ModeSubmodule {
			out.kinds = append(out.kinds, pointerMove(c))
			continue
		}
		out.kinds = append(out.kinds, kindsOf(c, blobs)...)
		if c.NewMode == push.ModeSymlink || c.OldMode == push.ModeSymlink {
			continue
		}
		before, okBefore := text(c.OldSHA)
		after, okAfter := text(c.NewSHA)
		if !okBefore || !okAfter || isBinaryText(before) || isBinaryText(after) {
			continue
		}
		out.tests = append(out.tests, testNotes(c, before, after)...)
		if kind := manifestKind(c.Path); kind != "" {
			deps = append(deps, dependencyChanges(c.Path, kind, before, after)...)
		}
	}
	if cut {
		out.problems = append(out.problems, "some files were too large to read, so they weren't checked")
	}
	if len(out.tests) > maxChangeNotes {
		out.tests = out.tests[:maxChangeNotes]
	}
	if len(out.kinds) > maxFileKinds {
		out.kinds = out.kinds[:maxFileKinds]
	}
	out.deps, out.moreDeps = capDependencies(deps)
	return out
}

func isBinaryText(s string) bool {
	if len(s) > binarySniff {
		s = s[:binarySniff]
	}
	return strings.IndexByte(s, 0) >= 0
}

func pointerMove(c push.Change) FileKind {
	was, now := c.OldMode == push.ModeSubmodule, c.NewMode == push.ModeSubmodule
	switch {
	case was && now:
		return FileKind{Path: c.Path, Kind: "submodule pointer moved from commit " + c.OldSHA + " to commit " + c.NewSHA}
	case now:
		return FileKind{Path: c.Path, Kind: "submodule pointer added at commit " + c.NewSHA}
	}
	return FileKind{Path: c.Path, Kind: "submodule pointer removed; it was at commit " + c.OldSHA}
}

func kindsOf(c push.Change, blobs map[string]BlobData) []FileKind {
	if c.Status == push.Deleted {
		return nil
	}
	var out []FileKind
	switch {
	case c.NewMode == push.ModeSymlink && (c.OldMode != push.ModeSymlink || c.OldSHA != c.NewSHA):
		target := "(not read)"
		if b, ok := blobs[c.NewSHA]; ok && !b.Cut {
			target = CleanLine(string(b.Data), 300)
		}
		out = append(out, FileKind{Path: c.Path, Kind: "symlink", Target: target})
	case c.NewMode == push.ModeExecutable && c.OldMode != push.ModeExecutable:
		out = append(out, FileKind{Path: c.Path, Kind: "executable"})
	}
	if c.NewMode != push.ModeSymlink {
		if b, ok := blobs[c.NewSHA]; ok && isBinaryText(string(b.Data[:min(len(b.Data), binarySniff)])) {
			old, had := blobs[c.OldSHA]
			if !had || !isBinaryText(string(old.Data[:min(len(old.Data), binarySniff)])) || c.Status == push.Added {
				out = append(out, FileKind{Path: c.Path, Kind: "binary"})
			} else {
				out = append(out, FileKind{Path: c.Path, Kind: "binary (changed)"})
			}
		}
	}
	return out
}

func countMatches(re *regexp.Regexp, text string) int {
	n := 0
	for line := range strings.SplitSeq(text, "\n") {
		if re.MatchString(line) {
			n++
		}
	}
	return n
}

func names(re *regexp.Regexp, text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out[strings.TrimSpace(m[1])] = true
	}
	return out
}

func shownNames(list []string) string {
	slices.Sort(list)
	more := ""
	if len(list) > maxNamesShown {
		more = " and " + strconv.Itoa(len(list)-maxNamesShown) + " more"
		list = list[:maxNamesShown]
	}
	for i, n := range list {
		list[i] = CleanLine(n, 120)
	}
	return strings.Join(list, ", ") + more
}

func testNotes(c push.Change, before, after string) []ChangeNote {
	var out []ChangeNote
	note := func(what string) { out = append(out, ChangeNote{Path: c.Path, What: what}) }
	test := isTestPath(c.Path)
	if test && c.Status == push.Deleted {
		note("test file deleted")
	}
	if test && c.Status != push.Deleted {
		for _, re := range []*regexp.Regexp{goTestFunc, pyTestFunc, jsTestName} {
			old, now := names(re, before), names(re, after)
			var gone []string
			for n := range old {
				if !now[n] {
					gone = append(gone, n)
				}
			}
			if len(gone) > 0 {
				note("tests removed: " + shownNames(gone))
			}
		}
		if d := countMatches(annotatedRun, before) - countMatches(annotatedRun, after); d > 0 {
			note(strconv.Itoa(d) + " fewer annotated tests")
		}
		for _, m := range skipMarks {
			if d := countMatches(m.re, after) - countMatches(m.re, before); d > 0 {
				note(strconv.Itoa(d) + " new " + m.what)
			}
		}
		oldAsserts, newAsserts := countMatches(assertLine, before), countMatches(assertLine, after)
		oldNeg, newNeg := countMatches(negatedLine, before), countMatches(negatedLine, after)
		if d := oldAsserts - newAsserts; d > 0 {
			note(strconv.Itoa(d) + " fewer assertion lines")
		}
		if newNeg > oldNeg && oldAsserts-oldNeg > newAsserts-newNeg {
			note(strconv.Itoa(newNeg-oldNeg) + " more negated assertions while plain ones went down: check for inverted assertions")
		}
	}
	if c.Status != push.Deleted {
		for _, m := range suppressed {
			if d := countMatches(m.re, after) - countMatches(m.re, before); d > 0 {
				note(strconv.Itoa(d) + " new " + m.what)
			}
		}
	}
	if !test && c.Status != push.Added {
		if d := countMatches(loggingCall, before) - countMatches(loggingCall, after); d > 0 {
			note(strconv.Itoa(d) + " fewer logging or monitoring calls")
		}
	}
	if runLater.MatchString(c.Path) {
		note("a file a person or CI runs later was " + string(c.Status))
	}
	if path.Base(c.Path) == "package.json" {
		if what := scriptChanges(before, after); what != "" {
			note(what)
		}
	}
	return out
}

func scriptChanges(before, after string) string {
	read := func(s string) map[string]string {
		out := map[string]string{}
		if s == "" {
			return out
		}
		var doc map[string]any
		if json.NewDecoder(bytes.NewReader([]byte(s))).Decode(&doc) != nil {
			return out
		}
		scripts, _ := doc["scripts"].(map[string]any)
		for k, v := range scripts {
			text, _ := v.(string)
			out[k] = text
		}
		return out
	}
	old, now := read(before), read(after)
	var changed []string
	for k, v := range now {
		if w, ok := old[k]; !ok || w != v {
			changed = append(changed, k)
		}
	}
	for k := range old {
		if _, ok := now[k]; !ok {
			changed = append(changed, k)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	return "package.json scripts added, changed or removed: " + shownNames(changed)
}
