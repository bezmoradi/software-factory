package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	packRepo   = "acme-pack.git"
	packPublic = "/public/" + packRepo
	packSeeded = "2026-11-02T00:00:00Z"
)

type packVariant struct {
	branch string
	files  map[string]string
}

const (
	packManifest = `{
  "format": "software-factory-pack/1",
  "name": "acme",
  "version": "1.0.0",
  "roles": { "bug-hunter": "bug-finder", "simplicity": "simplicity-checker" },
  "steps": { "define": "/acme:requirements", "plan": "/acme:plan", "review": "/acme:handoff" },
  "documents": { "folder": ".acme", "requirements": "REQUIREMENTS.md", "plan": "PLAN.md" }
}
`
	packPlugin    = "{\n  \"name\": \"acme\",\n  \"version\": \"1.0.0\",\n  \"description\": \"The dev git host's test pack\"\n}\n"
	packBugFinder = "---\nname: bug-finder\ndescription: Finds bugs in the change\ntools: Read, Grep, Glob, Bash\nmodel: sonnet\nomitClaudeMd: true\n---\nRead change.diff and the files it touches. Report each bug with its location.\n"
	packSimpler   = "---\nname: simplicity-checker\ndescription: Finds what the change could drop\ndisallowedTools: Bash\n---\nRead change.diff. Name what could be simpler.\n"
	packHooks     = "{\n  \"hooks\": {\n    \"SessionStart\": [{ \"hooks\": [{ \"type\": \"command\", \"command\": \"sh ${CLAUDE_PLUGIN_ROOT}/hooks/start.sh\" }] }]\n  }\n}\n"
	packSkill     = "---\nname: notes\ndescription: Keeps notes on the task\n---\nWrite what you learned to .acme/NOTES.md.\n"
)

func packBase() map[string]string {
	return map[string]string{
		"software-factory-pack.json":   packManifest,
		".claude-plugin/plugin.json":   packPlugin,
		"agents/bug-finder.md":         packBugFinder,
		"agents/simplicity-checker.md": packSimpler,
		"hooks/hooks.json":             packHooks,
		"hooks/start.sh":               "#!/bin/sh\necho acme pack session start\n",
		"skills/notes/SKILL.md":        packSkill,
		"commands/requirements.md":     "---\ndescription: Agree the requirements\n---\nWrite .acme/REQUIREMENTS.md.\n",
	}
}

var packVariants = []packVariant{
	{branch: "simplicity-only", files: map[string]string{"software-factory-pack.json": strings.Replace(packManifest, `"bug-hunter": "bug-finder", `, "", 1)}},
	{branch: "refused-key", files: map[string]string{"software-factory-pack.json": strings.Replace(packManifest, `"version": "1.0.0",`, `"version": "1.0.0",`+"\n  \"tools\": [\"Bash\"],", 1)}},
	{branch: "bad-step", files: map[string]string{"software-factory-pack.json": strings.Replace(packManifest, `"/acme:requirements"`, `"/knackpack:challenge-requirements"`, 1)}},
	{branch: "missing-agent", files: map[string]string{"software-factory-pack.json": strings.Replace(packManifest, `"bug-finder"`, `"ghost"`, 1)}},
	{branch: "name-knackpack", files: map[string]string{"software-factory-pack.json": strings.ReplaceAll(packManifest, "acme", "knackpack"), ".claude-plugin/plugin.json": strings.Replace(packPlugin, `"acme"`, `"knackpack"`, 1)}},
	{branch: "agent-hooks", files: map[string]string{"agents/bug-finder.md": strings.Replace(packBugFinder, "model: sonnet\n", "model: sonnet\nhooks:\n  PreToolUse: []\n", 1)}},
	{branch: "agent-skills", files: map[string]string{"agents/bug-finder.md": strings.Replace(packBugFinder, "model: sonnet\n", "model: sonnet\nskills: [acme:notes]\n", 1)}},
	{branch: "mcp-file", files: map[string]string{".mcp.json": "{}\n"}},
	{branch: "plugin-hooks-key", files: map[string]string{".claude-plugin/plugin.json": strings.Replace(packPlugin, `"version": "1.0.0",`, `"version": "1.0.0",`+"\n  \"hooks\": \"./hooks/hooks.json\",", 1)}},
	{branch: "skill-shell", files: map[string]string{"skills/notes/SKILL.md": strings.Replace(packSkill, "Write what", "The time: !`date`\nWrite what", 1)}},
	{branch: "v2", files: map[string]string{"software-factory-pack.json": strings.Replace(packManifest, `"1.0.0"`, `"1.1.0"`, 1), "agents/bug-finder.md": packBugFinder + "Also read the tests.\n"}},
}

func writePackFiles(work string, files map[string]string) error {
	for rel, text := range files {
		path := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			return err
		}
	}
	return nil
}

func seedPack(root string) error {
	bare := filepath.Join(root, packRepo)
	if _, err := os.Stat(bare); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	work, err := os.MkdirTemp("", "gitserver-pack")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if _, err := git(root, "init", "-q", "--bare", "-b", "main", packRepo); err != nil {
		return err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", bare}} {
		if _, err := git(work, args...); err != nil {
			return err
		}
	}
	if err := writePackFiles(work, packBase()); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "acme 1.0.0"}, {"push", "-q", "origin", "HEAD:main"}} {
		if _, err := gitAt(work, packSeeded, args...); err != nil {
			return err
		}
	}
	for _, v := range packVariants {
		if _, err := gitAt(work, packSeeded, "checkout", "-q", "-B", v.branch, "main"); err != nil {
			return err
		}
		if err := writePackFiles(work, v.files); err != nil {
			return err
		}
		for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "acme " + v.branch}, {"push", "-q", "origin", "HEAD:" + v.branch}} {
			if _, err := gitAt(work, packSeeded, args...); err != nil {
				return err
			}
		}
	}
	heads, err := git(bare, "show-ref", "--heads")
	if err != nil {
		return err
	}
	log.Printf("seeded %s:\n%s", packRepo, heads)
	return nil
}

func packHeads(root string) error {
	heads, err := git(filepath.Join(root, packRepo), "show-ref", "--heads")
	if err != nil {
		return err
	}
	fmt.Println(heads)
	return nil
}
