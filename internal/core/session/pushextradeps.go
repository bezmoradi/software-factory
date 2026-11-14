package session

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	EcosystemGo    = "go"
	EcosystemNPM   = "npm"
	EcosystemPyPI  = "pypi"
	EcosystemCrate = "crates"

	DependencyAdded   = "added"
	DependencyRemoved = "removed"
	DependencyChanged = "changed"

	maxDependencies   = 200
	maxLookups        = 20
	youngPackage      = 30 * 24 * time.Hour
	maxDependencyName = 214
)

type DependencyChange struct {
	Ecosystem string
	Name      string
	From      string
	To        string
	Change    string
	File      string
	Lockfile  bool
	Looked    bool
	Found     bool
	Published time.Time
	Young     bool
	Note      string
}

type PackageFacts struct {
	Found     bool
	Published time.Time
}

type PackageRegistry interface {
	Lookup(ctx context.Context, ecosystem, name, version string) (PackageFacts, error)
}

var (
	goRequire    = regexp.MustCompile(`^\s*(?:require\s+)?([^\s()"]+)\s+(v[^\s/]+)`)
	pyRequire    = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*(?:(==|===|>=|<=|~=|!=|>|<)\s*([^\s;,#]+))?`)
	tomlKey      = regexp.MustCompile(`^\s*["']?([A-Za-z0-9][A-Za-z0-9._-]*)["']?\s*=\s*(.*)$`)
	tomlVersion  = regexp.MustCompile(`version\s*=\s*["']([^"']*)["']`)
	tomlString   = regexp.MustCompile(`^["']([^"']*)["']`)
	tomlTable    = regexp.MustCompile(`^\s*\[\s*([^\]]+?)\s*\]\s*$`)
	yarnEntry    = regexp.MustCompile(`^"?((?:@[^@/\s"]+/)?[^@\s",]+)@`)
	yarnVersion  = regexp.MustCompile(`^\s+version:?\s+"?([^"\s]+)"?`)
	pnpmPackage  = regexp.MustCompile(`^\s{2}['"]?/?((?:@[^@/\s'"]+/)?[^@\s'":(]+)@([^\s'":(]+)`)
	pyListItem   = regexp.MustCompile(`["']([^"']+)["']`)
	pythonNormal = regexp.MustCompile(`[-_.]+`)
)

func manifestKind(p string) string {
	base := path.Base(p)
	switch {
	case base == "go.mod":
		return EcosystemGo
	case base == "package.json", base == "package-lock.json", base == "npm-shrinkwrap.json", base == "yarn.lock", base == "pnpm-lock.yaml":
		return EcosystemNPM
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"), base == "pyproject.toml":
		return EcosystemPyPI
	case base == "Cargo.toml":
		return EcosystemCrate
	}
	return ""
}

func lockfile(p string) bool {
	switch path.Base(p) {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml":
		return true
	}
	return false
}

func dependencyChanges(file, kind, before, after string) []DependencyChange {
	read := func(s string) map[string]string {
		if s == "" {
			return map[string]string{}
		}
		switch path.Base(file) {
		case "go.mod":
			return goModules(s)
		case "package.json":
			return npmManifest(s)
		case "package-lock.json", "npm-shrinkwrap.json":
			return npmLock(s)
		case "yarn.lock":
			return yarnLock(s)
		case "pnpm-lock.yaml":
			return pnpmLock(s)
		case "pyproject.toml":
			return pyproject(s)
		case "Cargo.toml":
			return cargoManifest(s)
		}
		return pyRequirements(s)
	}
	old, now := read(before), read(after)
	lock := lockfile(file)
	var out []DependencyChange
	add := func(name, from, to, change string) {
		out = append(out, DependencyChange{Ecosystem: kind, Name: CleanLine(name, maxDependencyName), From: CleanLine(from, 100), To: CleanLine(to, 100), Change: change, File: file, Lockfile: lock})
	}
	for name, v := range now {
		w, ok := old[name]
		switch {
		case !ok:
			add(name, "", v, DependencyAdded)
		case w != v:
			add(name, w, v, DependencyChanged)
		}
	}
	for name, w := range old {
		if _, ok := now[name]; !ok {
			add(name, w, "", DependencyRemoved)
		}
	}
	return out
}

func capDependencies(list []DependencyChange) ([]DependencyChange, int) {
	slices.SortFunc(list, func(a, b DependencyChange) int {
		if a.Lockfile != b.Lockfile {
			if a.Lockfile {
				return 1
			}
			return -1
		}
		rank := map[string]int{DependencyAdded: 0, DependencyChanged: 1, DependencyRemoved: 2}
		if rank[a.Change] != rank[b.Change] {
			return rank[a.Change] - rank[b.Change]
		}
		return strings.Compare(a.File+" "+a.Name, b.File+" "+b.Name)
	})
	if len(list) > maxDependencies {
		return list[:maxDependencies], len(list) - maxDependencies
	}
	return list, 0
}

func goModules(s string) map[string]string {
	out := map[string]string{}
	block := false
	for line := range strings.SplitSeq(s, "\n") {
		code, _, _ := strings.Cut(line, "//")
		trimmed := strings.TrimSpace(code)
		switch {
		case strings.HasPrefix(trimmed, "require") && strings.HasSuffix(trimmed, "("):
			block = true
			continue
		case block && trimmed == ")":
			block = false
			continue
		case !block && !strings.HasPrefix(trimmed, "require "):
			continue
		}
		if m := goRequire.FindStringSubmatch(code); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

func decodeJSON(s string) map[string]any {
	var doc map[string]any
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	if d.Decode(&doc) != nil {
		return nil
	}
	return doc
}

func npmManifest(s string) map[string]string {
	out := map[string]string{}
	doc := decodeJSON(s)
	for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		deps, _ := doc[key].(map[string]any)
		for name, v := range deps {
			text, _ := v.(string)
			out[name] = text
		}
	}
	return out
}

func npmLock(s string) map[string]string {
	out := map[string]string{}
	doc := decodeJSON(s)
	if pkgs, ok := doc["packages"].(map[string]any); ok {
		for key, v := range pkgs {
			i := strings.LastIndex(key, "node_modules/")
			if i < 0 {
				continue
			}
			entry, _ := v.(map[string]any)
			version, _ := entry["version"].(string)
			name := key[i+len("node_modules/"):]
			if old, seen := out[name]; seen && old != version {
				version = old + ", " + version
			}
			out[name] = version
		}
		return out
	}
	var walk func(deps map[string]any, depth int)
	walk = func(deps map[string]any, depth int) {
		if depth > 50 {
			return
		}
		for name, v := range deps {
			entry, _ := v.(map[string]any)
			version, _ := entry["version"].(string)
			out[name] = version
			if sub, ok := entry["dependencies"].(map[string]any); ok {
				walk(sub, depth+1)
			}
		}
	}
	if deps, ok := doc["dependencies"].(map[string]any); ok {
		walk(deps, 0)
	}
	return out
}

func yarnLock(s string) map[string]string {
	out := map[string]string{}
	var current []string
	for line := range strings.SplitSeq(s, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			current = current[:0]
			for part := range strings.SplitSeq(strings.TrimSuffix(line, ":"), ",") {
				if m := yarnEntry.FindStringSubmatch(strings.TrimSpace(part)); m != nil && !slices.Contains(current, m[1]) {
					current = append(current, m[1])
				}
			}
			continue
		}
		if m := yarnVersion.FindStringSubmatch(line); m != nil && len(current) > 0 && strings.Count(line, " ") <= 3 {
			for _, name := range current {
				if old, seen := out[name]; seen && old != m[1] && !strings.Contains(old, m[1]) {
					out[name] = old + ", " + m[1]
				} else if !seen {
					out[name] = m[1]
				}
			}
			current = current[:0]
		}
	}
	return out
}

func pnpmLock(s string) map[string]string {
	out := map[string]string{}
	in := false
	for line := range strings.SplitSeq(s, "\n") {
		if !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" {
			in = strings.TrimSpace(line) == "packages:"
			continue
		}
		if !in {
			continue
		}
		if m := pnpmPackage.FindStringSubmatch(line); m != nil {
			if old, seen := out[m[1]]; seen && old != m[2] {
				out[m[1]] = old + ", " + m[2]
			} else {
				out[m[1]] = m[2]
			}
		}
	}
	return out
}

func pythonName(name string) string {
	return pythonNormal.ReplaceAllString(strings.ToLower(name), "-")
}

func pyRequirement(spec string) (string, string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasPrefix(spec, "#") || strings.HasPrefix(spec, "-") || strings.Contains(spec, "://") {
		return "", "", false
	}
	m := pyRequire.FindStringSubmatch(spec)
	if m == nil {
		return "", "", false
	}
	return pythonName(m[1]), strings.TrimSpace(m[2] + m[3]), true
}

func pyRequirements(s string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		if name, version, ok := pyRequirement(line); ok {
			out[name] = version
		}
	}
	return out
}

func pyproject(s string) map[string]string {
	out := map[string]string{}
	table := ""
	inList := false
	for line := range strings.SplitSeq(s, "\n") {
		code := strings.TrimSpace(line)
		if m := tomlTable.FindStringSubmatch(code); m != nil && !inList {
			table = strings.Trim(m[1], `"' `)
			continue
		}
		poetry := table == "tool.poetry.dependencies" || table == "tool.poetry.dev-dependencies" || strings.HasPrefix(table, "tool.poetry.group.") && strings.HasSuffix(table, ".dependencies")
		listed := table == "project" || table == "project.optional-dependencies" || table == "dependency-groups"
		switch {
		case inList:
			for _, m := range pyListItem.FindAllStringSubmatch(code, -1) {
				if name, version, ok := pyRequirement(m[1]); ok {
					out[name] = version
				}
			}
			if strings.Contains(code, "]") {
				inList = false
			}
		case listed:
			m := tomlKey.FindStringSubmatch(code)
			if m == nil || table == "project" && m[1] != "dependencies" {
				continue
			}
			rest := strings.TrimSpace(m[2])
			if !strings.HasPrefix(rest, "[") {
				continue
			}
			for _, item := range pyListItem.FindAllStringSubmatch(rest, -1) {
				if name, version, ok := pyRequirement(item[1]); ok {
					out[name] = version
				}
			}
			inList = !strings.Contains(rest, "]")
		case poetry:
			if m := tomlKey.FindStringSubmatch(code); m != nil && m[1] != "python" {
				out[pythonName(m[1])] = tomlValue(m[2])
			}
		}
	}
	return out
}

func tomlValue(v string) string {
	v = strings.TrimSpace(v)
	if m := tomlString.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	if m := tomlVersion.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return CleanLine(v, 100)
}

func cargoManifest(s string) map[string]string {
	out := map[string]string{}
	table := ""
	for line := range strings.SplitSeq(s, "\n") {
		code := strings.TrimSpace(line)
		if m := tomlTable.FindStringSubmatch(code); m != nil {
			table = strings.Trim(m[1], `"' `)
			continue
		}
		parts := strings.Split(table, ".")
		last := parts[len(parts)-1]
		switch {
		case cargoSection(last):
			if m := tomlKey.FindStringSubmatch(code); m != nil {
				out[m[1]] = tomlValue(m[2])
			}
		case len(parts) >= 2 && cargoSection(parts[len(parts)-2]):
			if _, ok := out[last]; !ok {
				out[last] = ""
			}
			if m := tomlKey.FindStringSubmatch(code); m != nil && m[1] == "version" {
				out[last] = tomlValue(m[2])
			}
		}
	}
	return out
}

func cargoSection(s string) bool {
	return s == "dependencies" || s == "dev-dependencies" || s == "build-dependencies"
}

func lookupPackages(ctx context.Context, reg PackageRegistry, deps []DependencyChange, now time.Time) string {
	asked := 0
	skipped := 0
	for i := range deps {
		d := &deps[i]
		if d.Change != DependencyAdded || d.Lockfile {
			continue
		}
		if asked >= maxLookups {
			skipped++
			continue
		}
		if ctx.Err() != nil {
			skipped++
			continue
		}
		asked++
		facts, err := reg.Lookup(ctx, d.Ecosystem, d.Name, d.To)
		if err != nil {
			d.Note = clip(maskSecrets(err.Error()), 200)
			continue
		}
		d.Looked, d.Found, d.Published = true, facts.Found, facts.Published
		d.Young = facts.Found && !facts.Published.IsZero() && now.Sub(facts.Published) < youngPackage
	}
	switch {
	case asked == 0 && skipped == 0:
		return "on"
	case skipped > 0:
		return "on; " + strconv.Itoa(skipped) + " new packages weren't looked up (at most " + strconv.Itoa(maxLookups) + " per push)"
	}
	return "on"
}
