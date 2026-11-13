package page

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

//go:embed all:dist
var dist embed.FS

const (
	prefsFile  = "prefs.json"
	maxPrefs   = 4096
	indexMarks = `data-theme="solarized-dark" data-scale="100" data-bar="left"`
)

var (
	assetName  = regexp.MustCompile(`^[a-z][a-z0-9-]*\.(js|css|svg)$`)
	themeRule  = regexp.MustCompile(`(?m)^\[data-theme="([a-z0-9-]+)"\] \{`)
	scaleRule  = regexp.MustCompile(`(?m)^:root\[data-scale="(\d+)"\]`)
	assetTypes = map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml"}
	folders    = []string{"", "components/", "lib/", "styles/"}
)

type asset struct {
	body []byte
	kind string
}

type prefs struct {
	Theme string `json:"theme,omitempty"`
	Scale int    `json:"scale,omitempty"`
	Bar   string `json:"bar,omitempty"`
}

type assets struct {
	prefix string
	files  map[string]asset
	index  string
	themes []string
	scales []int
	dir    string
	mu     sync.Mutex
	saved  prefs
}

func loadAssets(stateDir string) *assets {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	a := &assets{prefix: "/s/" + hex.EncodeToString(b) + "/", files: map[string]asset{}, dir: stateDir}
	for _, folder := range folders {
		entries, err := fs.ReadDir(dist, path.Join("dist", folder))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !assetName.MatchString(e.Name()) {
				continue
			}
			body, err := fs.ReadFile(dist, path.Join("dist", folder, e.Name()))
			if err == nil {
				a.files[folder+e.Name()] = asset{body: body, kind: assetTypes[path.Ext(e.Name())]}
			}
		}
	}
	if index, err := fs.ReadFile(dist, "dist/index.html"); err == nil {
		a.index = prefixed(string(index), a.prefix)
	}
	for _, m := range themeRule.FindAllStringSubmatch(string(a.files["styles/themes.css"].body), -1) {
		a.themes = append(a.themes, m[1])
	}
	for _, m := range scaleRule.FindAllStringSubmatch(string(a.files["styles/app.css"].body), -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			a.scales = append(a.scales, n)
		}
	}
	if data, ok := readOwned(stateDir, prefsFile, maxPrefs); ok {
		var p prefs
		if json.Unmarshal(data, &p) == nil {
			a.saved = a.clean(p)
		}
	}
	return a
}

func prefixed(html, prefix string) string {
	var out strings.Builder
	rest := html
	for {
		i := strings.Index(rest, `="`)
		if i < 0 {
			out.WriteString(rest)
			return out.String()
		}
		attr := rest[:i]
		out.WriteString(rest[:i+2])
		rest = rest[i+2:]
		if !strings.HasSuffix(attr, " href") && !strings.HasSuffix(attr, " src") {
			continue
		}
		if rest != "" && rest[0] != '/' && rest[0] != '.' && !hasScheme(rest) {
			out.WriteString(prefix)
		}
	}
}

func hasScheme(s string) bool {
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c == ':' && i > 0:
			return true
		default:
			return false
		}
	}
	return false
}

func (a *assets) clean(p prefs) prefs {
	out := prefs{}
	if slices.Contains(a.themes, p.Theme) {
		out.Theme = p.Theme
	}
	if slices.Contains(a.scales, p.Scale) {
		out.Scale = p.Scale
	}
	if p.Bar == "left" || p.Bar == "right" {
		out.Bar = p.Bar
	}
	return out
}

func (a *assets) page() (string, bool) {
	if a.index == "" {
		return "", false
	}
	a.mu.Lock()
	p := a.saved
	a.mu.Unlock()
	theme, scale, bar := "solarized-dark", 100, "left"
	if p.Theme != "" {
		theme = p.Theme
	}
	if p.Scale != 0 {
		scale = p.Scale
	}
	if p.Bar != "" {
		bar = p.Bar
	}
	marks := `data-theme="` + theme + `" data-scale="` + strconv.Itoa(scale) + `" data-bar="` + bar + `"`
	return strings.Replace(a.index, indexMarks, marks, 1), true
}

func (a *assets) save(update map[string]any) bool {
	if len(update) == 0 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.saved
	for k, v := range update {
		switch k {
		case "theme":
			s, ok := v.(string)
			if !ok || !slices.Contains(a.themes, s) {
				return false
			}
			next.Theme = s
		case "scale":
			f, ok := v.(float64)
			n := int(f)
			if !ok || float64(n) != f || !slices.Contains(a.scales, n) {
				return false
			}
			next.Scale = n
		case "bar":
			s, ok := v.(string)
			if !ok || (s != "left" && s != "right") {
				return false
			}
			next.Bar = s
		default:
			return false
		}
	}
	data, err := json.Marshal(next)
	if err != nil || writeAtomic(a.dir, prefsFile, data) != nil {
		return false
	}
	a.saved = next
	return true
}

func guide() string {
	b, err := dist.ReadFile("dist/docs.md")
	if err != nil {
		return ""
	}
	return string(b)
}
