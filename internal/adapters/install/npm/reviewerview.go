package npm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
)

const (
	PacksFolder      = "packs"
	reviewersView    = "reviewers"
	sessionView      = "session"
	maxViewAgent     = 256 << 10
	maxViewAgents    = 100
	maxViewMatter    = 200
	viewAgentsFolder = "agents"
	rolesEntry       = "software-factory-pack.json#roles"
)

var (
	matterKey       = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)[ \t]*:(.*)$`)
	refusedAgentKey = map[string]string{
		"hooks":          "hooks would run commands in the trusted reviewer container",
		"mcpservers":     "mcpServers would start programs in the trusted reviewer container",
		"permissionmode": "permissionMode changes what the reviewer may do without asking",
		"skills":         "skills loads instructions and scripts reviews don't allow",
		"isolation":      "isolation changes where the reviewer runs",
		"memory":         "memory gives the reviewer a folder it writes across runs",
	}
)

type viewFile struct {
	path string
	data []byte
}

func closingLine(line string) bool {
	return strings.TrimRight(line, " \t\r") == "---"
}

func reviewerAgentProblem(name string, data []byte) string {
	switch {
	case !utf8.Valid(data):
		return "isn't UTF-8"
	case slices.Contains(data, 0):
		return "holds a NUL byte"
	case len(data) > maxViewAgent:
		return fmt.Sprintf("is over %d KiB", maxViewAgent>>10)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || !closingLine(lines[0]) {
		return "doesn't start with front matter between two --- lines"
	}
	end := -1
	for i := 1; i < len(lines) && i <= maxViewMatter; i++ {
		if closingLine(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return fmt.Sprintf("has no --- line closing its front matter within %d lines", maxViewMatter)
	}
	seen := map[string]bool{}
	named := ""
	for _, line := range lines[1:end] {
		bare := strings.TrimRight(line, " \t\r")
		switch {
		case bare == "", strings.HasPrefix(bare, "#"):
			continue
		case strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"):
			continue
		case strings.HasPrefix(bare, "---") || strings.HasPrefix(bare, "..."):
			return "has a line starting with --- or ... inside its front matter, which Claude Code may read as its end"
		}
		m := matterKey.FindStringSubmatch(bare)
		if m == nil {
			return "has a front matter line the engine can't read as a plain key: " + shown(bare)
		}
		key := strings.ToLower(m[1])
		if seen[key] {
			return "names the front matter key " + shown(m[1]) + " twice"
		}
		seen[key] = true
		if why, refused := refusedAgentKey[key]; refused {
			return "has the front matter key " + m[1] + ", which a reviewer pack can't hold: " + why
		}
		if key == "name" {
			named = strings.Trim(strings.TrimSpace(m[2]), `"'`)
		}
	}
	if named != name {
		return "names itself " + shown(named) + " in its front matter, not " + shown(name) + ", its file's name"
	}
	return ""
}

func agentsIn(dir string) (map[string][]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("the plugin's folder couldn't be opened to read its agents")
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(viewAgentsFolder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return map[string][]byte{}, nil
	case err != nil:
		return nil, errors.New("its agents folder couldn't be read")
	case !info.IsDir():
		return nil, errors.New("its agents entry isn't a plain folder")
	}
	entries, err := fs.ReadDir(root.FS(), viewAgentsFolder)
	if err != nil {
		return nil, errors.New("its agents folder couldn't be read")
	}
	out := map[string][]byte{}
	for _, e := range entries {
		name, isAgent := strings.CutSuffix(e.Name(), ".md")
		if !isAgent {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 || !e.Type().IsRegular() {
			return nil, fmt.Errorf("its agent %s isn't a regular file", shown(e.Name()))
		}
		if !pluginID.MatchString(name) {
			return nil, fmt.Errorf("its agent file %s isn't named with lowercase letters, digits and hyphens", shown(e.Name()))
		}
		if len(out) >= maxViewAgents {
			return nil, fmt.Errorf("it has more than %d agents", maxViewAgents)
		}
		data, err := readInside(dir, viewAgentsFolder+"/"+e.Name(), maxViewAgent)
		if err != nil {
			return nil, fmt.Errorf("its agent %s %s", e.Name(), err.Error())
		}
		out[name] = data
	}
	return out, nil
}

func reviewerViewOf(name, version string, agents map[string][]byte) ([]viewFile, []string, error) {
	manifest, err := json.MarshalIndent(map[string]string{"name": name, "version": version, "description": "The reviewer agents of " + name + ", as Software Factory writes them for its reviewers"}, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	files := []viewFile{{path: PluginManifest, data: append(manifest, '\n')}}
	names := make([]string, 0, len(agents))
	for a := range agents {
		names = append(names, a)
	}
	slices.Sort(names)
	for _, a := range names {
		if why := reviewerAgentProblem(a, agents[a]); why != "" {
			return nil, nil, fmt.Errorf("its agent agents/%s.md %s", a, why)
		}
		files = append(files, viewFile{path: viewAgentsFolder + "/" + a + ".md", data: agents[a]})
	}
	return files, names, nil
}

func digestLine(path, mode string, data []byte) string {
	sum := sha256.Sum256(data)
	return path + "\t" + mode + "\t" + hex.EncodeToString(sum[:])
}

func digestOf(lines []string) string {
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

func rolesLine(roles map[review.Role]string) string {
	var b strings.Builder
	for _, r := range review.Roles {
		b.WriteString(string(r) + "=" + roles[r] + "\n")
	}
	return digestLine(rolesEntry, "-", []byte(b.String()))
}

func viewDigest(files []viewFile, roles map[review.Role]string) string {
	lines := make([]string, 0, len(files)+1)
	for _, f := range files {
		lines = append(lines, digestLine(f.path, "100644", f.data))
	}
	return digestOf(append(lines, rolesLine(roles)))
}

func writeView(dir string, files []viewFile) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, f := range files {
		if err := root.MkdirAll(filepath.ToSlash(filepath.Dir(f.path)), 0o755); err != nil {
			return err
		}
		if err := root.WriteFile(f.path, f.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func treeDigest(dir string) (string, error) {
	lines, err := treeLines(dir)
	if err != nil {
		return "", err
	}
	return digestOf(lines), nil
}

func treeLines(dir string) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var lines []string
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := root.Readlink(rel)
			if err != nil {
				return err
			}
			lines = append(lines, digestLine(rel, "120000", []byte(target)))
		case info.Mode().IsRegular():
			data, err := root.ReadFile(rel)
			if err != nil {
				return err
			}
			mode := "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			lines = append(lines, digestLine(rel, mode, data))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return lines, nil
}
