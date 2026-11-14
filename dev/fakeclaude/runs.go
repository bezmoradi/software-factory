package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	runSentence = regexp.MustCompile(`This is run (\d) of (\d) of your role`)
	lessonID    = regexp.MustCompile(`^## (L\d{1,2})$`)
)

const (
	realCVE = "CVE-2021-44228"
	madeCVE = "CVE-2042-424242"
)

func runOf(prompt string) int {
	if m := runSentence.FindStringSubmatch(prompt); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 1
}

func blockRun() int {
	data, err := os.ReadFile(filepath.Join("head", "BLOCK-RUN"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func lessonsReply() reply {
	data, err := os.ReadFile(filepath.Join("head", "findings.md"))
	if err != nil {
		return reply{text: "head/findings.md is missing, so the fake reviewer proposes no line.", turns: 2}
	}
	var out strings.Builder
	out.WriteString("## Suggested lines (from the fake reviewer)\n\n")
	id := ""
	count := 0
	for l := range strings.SplitSeq(string(data), "\n") {
		if m := lessonID.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			id = m[1]
			continue
		}
		if title, ok := strings.CutPrefix(l, "Title: "); ok && id != "" {
			out.WriteString("- [" + id + "] Before committing, check the change for this repeated finding: " + strings.TrimSpace(title) + "\n")
			id = ""
			count++
		}
	}
	if count == 0 {
		out.WriteString("No finding IDs were found in head/findings.md.\n")
	}
	if comments, err := os.ReadFile(filepath.Join("head", "comments.md")); err == nil {
		n := strings.Count(string(comments), "\n## Comment ")
		out.WriteString("\nhead/comments.md holds " + strconv.Itoa(n) + " of the person's comments.\n")
	}
	return reply{text: out.String(), turns: 2}
}
