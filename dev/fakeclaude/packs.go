package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const noVerdictFile = "NO-VERDICT"

func reviewerView(plugin string) (string, error) {
	root, err := os.OpenRoot(plugin)
	if err != nil {
		return "", errors.New("--plugin-dir doesn't name a folder")
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(".claude-plugin/plugin.json")
	if err != nil {
		return "", errors.New("--plugin-dir doesn't name an installed plugin")
	}
	var m struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &m) != nil || m.Name == "" {
		return "", errors.New("--plugin-dir's plugin.json names no plugin")
	}
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case rel == "." || rel == ".claude-plugin" || rel == "agents":
			return nil
		case rel == ".claude-plugin/plugin.json":
			return nil
		case strings.HasPrefix(rel, "agents/") && strings.HasSuffix(rel, ".md") && !strings.Contains(strings.TrimPrefix(rel, "agents/"), "/") && d.Type().IsRegular():
			return nil
		}
		return fmt.Errorf("--plugin-dir holds %s, and a reviewer's copy of a plugin holds only .claude-plugin/plugin.json and agents/*.md", rel)
	})
	if err != nil {
		return "", err
	}
	return m.Name, nil
}

func pluginAgent(plugin, agent string) (string, error) {
	name, err := reviewerView(plugin)
	if err != nil {
		return "", err
	}
	prefix, bare, _ := strings.Cut(agent, ":")
	if prefix != name {
		return "", fmt.Errorf("--agent %s isn't one of the plugin %s's agents", agent, name)
	}
	info, err := os.Lstat(filepath.Join(plugin, "agents", bare+".md"))
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("--plugin-dir holds no agents/%s.md for --agent %s", bare, agent)
	}
	return name, nil
}

func packReply(text string) string {
	text = strings.Replace(text, "knackpack-verdict/1: ", "Pack agent from the reviewer's copy of the plugin\nVerdict: ", 1)
	if !has(noVerdictFile) {
		return text
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "Verdict: ") {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\nhead/" + noVerdictFile + " asks the fake reviewer to end without a verdict line."
}
