package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	versionFile = ".sf-fake-version"
	passAllFile = ".sf-fake-pass-all"
	fakeVersion = "sf-fake-claude"

	slopReviewer = "knackpack:slop-reviewer"
)

func version(args []string) bool {
	if len(args) != 1 || args[0] != "--version" {
		return false
	}
	v := fakeVersion
	if data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), versionFile)); err == nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n"); line != "" {
			v = strings.TrimSpace(line)
		}
	}
	fmt.Println(v + " (Claude Code)")
	return true
}

func switched(name string) bool {
	_, err := os.Lstat(filepath.Join(os.Getenv("HOME"), name))
	return err == nil
}
