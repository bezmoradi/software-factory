package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	knackpackRepo   = "knackpack.git"
	knackpackPublic = "/public/" + knackpackRepo
	knackpackSeeded = "2026-11-01T00:00:00Z"
	knackpackFirst  = "0.26.0"
)

var (
	knackpackAgents = []string{"adjudicator", "adversarial-reviewer", "architecture-reviewer", "css-code-reviewer", "database-reviewer", "design-pattern-reviewer", "docker-reviewer",
		"go-code-reviewer", "kubernetes-reviewer", "opentelemetry-reviewer", "python-code-reviewer", "react-code-reviewer", "security-reviewer", "slop-reviewer", "terraform-reviewer",
		"typescript-code-reviewer", "ui-code-reviewer", "web-components-code-reviewer"}
	knackpackWord         = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,64}$`)
	knackpackExtras       = []string{"mcp-key", "mcp-file", "lsp-file", "monitors-key", "experimental-monitors", "monitors-file", "skill-shell", "placeholder-1", "placeholder-2", "placeholder-3", "placeholder-4", "placeholder-5", "placeholder-6", "placeholder-7", "placeholder-8", "placeholder-9", "placeholder-10", "placeholder-11", "placeholder-12", "placeholder-13", "placeholder-14", "placeholder-15", "placeholder-16"}
	knackpackPlaceholders = map[string]string{
		"skill-shell":    "The time: !`date`",
		"placeholder-1":  " $ARGUMENTS!`id`",
		"placeholder-2":  "Run: $ARGUMENTS!`id`",
		"placeholder-3":  "Do this: !$ARGUMENTS",
		"placeholder-4":  "x !`$ARGUMENTS and more text",
		"placeholder-5":  "see $1!`id`",
		"placeholder-6":  "${CLAUDE_SKILL_DIR}!`id`",
		"placeholder-7":  "```$ARGUMENTS!\nid\n```",
		"placeholder-8":  "``$0!\nid\n```",
		"placeholder-9":  "x $a:b!`id`",
		"placeholder-10": "x $out/dir!`id`",
		"placeholder-11": "x $a+b!`id`",
		"placeholder-12": "\x00---!`id`",
		"placeholder-13": "\x00description: ok---!`id`",
		"placeholder-14": "${``${CLAUDE_EFFORT}`!\nid\n```",
		"placeholder-15": "see ${CLAUDE_DEV_MODS_DIR}/x",
		"placeholder-16": "\x00? !!binary YXJndW1lbnRz\n: [a]\n---\n``$a`!\nid\n```",
	}
	errKnackpackArgs = errors.New("usage: sf-gitserver knackpack <version> [<name> [" + strings.Join(knackpackExtras, "|") + "]]")
)

func gitAt(dir, date string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Dev Git Server", "GIT_AUTHOR_EMAIL=dev@gitserver.test", "GIT_COMMITTER_NAME=Dev Git Server", "GIT_COMMITTER_EMAIL=dev@gitserver.test",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func knackpackManifest(name, version, extra string) []byte {
	m := map[string]any{"name": name, "version": version, "description": "The dev git host's stand-in for knackpack: a manifest and empty agent files"}
	switch extra {
	case "mcp-key":
		m["mcpServers"] = map[string]any{"stand-in": map[string]any{"command": "true"}}
	case "monitors-key":
		m["monitors"] = []any{map[string]any{"name": "stand-in", "command": "true"}}
	case "experimental-monitors":
		m["experimental"] = map[string]any{"monitors": "./m.json"}
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	return append(data, '\n')
}

func knackpackServerFiles(work, extra string) error {
	for file, mode := range map[string]string{".mcp.json": "mcp-file", ".lsp.json": "lsp-file", "monitors/monitors.json": "monitors-file"} {
		top, _, _ := strings.Cut(file, "/")
		if extra != mode {
			if err := os.RemoveAll(filepath.Join(work, top)); err != nil {
				return err
			}
			continue
		}
		path := filepath.Join(work, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func knackpackSkillShell(work, line string) error {
	agent, skill := filepath.Join(work, "agents", "stand-in-preloader.md"), filepath.Join(work, "skills", "stand-in-shell")
	if line == "" {
		if err := os.Remove(agent); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.RemoveAll(skill)
	}
	if err := os.MkdirAll(skill, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(agent, []byte("---\nname: stand-in-preloader\ndescription: preloads a skill with inline shell\nskills: [knackpack:stand-in-shell]\n---\nReview the change.\n"), 0o644); err != nil {
		return err
	}
	text := "---\nname: stand-in-shell\ndescription: runs a command as it loads\nallowed-tools: Bash\n---\n" + line + "\n"
	if tail, ok := strings.CutPrefix(line, "\x00"); ok {
		text = "---\nname: stand-in-shell\n" + tail + "\n"
	}
	return os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(text), 0o644)
}

func knackpackCommit(root, name, version, extra, date string) (string, error) {
	bare := filepath.Join(root, knackpackRepo)
	work, err := os.MkdirTemp("", "gitserver-knackpack")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if _, err := os.Stat(bare); errors.Is(err, os.ErrNotExist) {
		if _, err := git(root, "init", "-q", "--bare", "-b", "main", knackpackRepo); err != nil {
			return "", err
		}
		if _, err := git(work, "init", "-q", "-b", "main"); err != nil {
			return "", err
		}
		if _, err := git(work, "remote", "add", "origin", bare); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Join(work, "agents"), 0o755); err != nil {
			return "", err
		}
		for _, a := range knackpackAgents {
			if err := os.WriteFile(filepath.Join(work, "agents", a+".md"), []byte("# "+a+"\n"), 0o644); err != nil {
				return "", err
			}
		}
	} else if _, err := git(work, "clone", "-q", bare, "."); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(work, ".claude-plugin"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, ".claude-plugin", "plugin.json"), knackpackManifest(name, version, extra), 0o644); err != nil {
		return "", err
	}
	if err := knackpackServerFiles(work, extra); err != nil {
		return "", err
	}
	if err := knackpackSkillShell(work, knackpackPlaceholders[extra]); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "--allow-empty", "-m", name + " " + version}, {"push", "-q", "origin", "HEAD:main"}} {
		if _, err := gitAt(work, date, args...); err != nil {
			return "", err
		}
	}
	return git(work, "rev-parse", "HEAD")
}

func seedKnackpack(root string) error {
	if _, err := os.Stat(filepath.Join(root, knackpackRepo)); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	head, err := knackpackCommit(root, "knackpack", knackpackFirst, "", knackpackSeeded)
	if err != nil {
		return err
	}
	log.Printf("seeded %s: knackpack %s at %s", knackpackRepo, knackpackFirst, head)
	return nil
}

func knackpackCommand(root string, args []string) error {
	if len(args) < 2 || len(args) > 4 || !knackpackWord.MatchString(args[1]) {
		return errKnackpackArgs
	}
	name, extra := "knackpack", ""
	if len(args) >= 3 {
		if !knackpackWord.MatchString(args[2]) {
			return errKnackpackArgs
		}
		name = args[2]
	}
	if len(args) == 4 {
		if !slices.Contains(knackpackExtras, args[3]) {
			return errKnackpackArgs
		}
		extra = args[3]
	}
	head, err := knackpackCommit(root, name, args[1], extra, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	fmt.Println(head)
	return nil
}

func knackpackHandler(backend http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, knackpackPublic)
		listing := r.Method == http.MethodGet && rest == "/info/refs" && r.URL.Query().Get("service") == "git-upload-pack"
		fetching := r.Method == http.MethodPost && rest == "/git-upload-pack"
		if !listing && !fetching {
			http.NotFound(w, r)
			return
		}
		log.Printf("knackpack %s %s", r.Method, rest)
		r.URL.Path = "/" + knackpackRepo + rest
		r.URL.RawPath = ""
		backend.ServeHTTP(w, r)
	}
}
