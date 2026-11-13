package page

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	MaxFileShown  = 512 << 10
	maxFilePath   = 1000
	maxListed     = 20000
	maxFolderRead = 5000
	folderBatch   = 500
	listFor       = 250 * time.Millisecond
	listDepth     = 12
	listKept      = 20 * time.Second
	maxMatches    = 15
	maxChecked    = 20
	maxMatchTries = 20
	maxQuery      = 200
)

var (
	secretFile   = regexp.MustCompile(`(?i)^(\.env(\..*)?|\.envrc|\.npmrc|\.pypirc|\.netrc|\.git-credentials|\.htpasswd|\.pgpass|id_(rsa|dsa|ecdsa|ed25519)(\.pub)?|credentials(\.[a-z0-9]+)*|settings\.local\.json|.*\.tfstate(\.backup)?|.*\.(pem|key|p12|pfx|keystore|jks|kdbx|ppk|asc|gpg))$`)
	plainDotfile = regexp.MustCompile(`(?i)^\.(gitignore|gitattributes|gitmodules|gitkeep|editorconfig|dockerignore|prettierignore|eslintignore|(prettier|eslint|stylelint)rc(\.[a-z]+)?|markdownlint[\w.-]*|nvmrc|node-version|python-version|ruby-version|tool-versions|browserslistrc|shellcheckrc|yamllint(\.ya?ml)?)$`)
	claudeParts  = map[string]bool{"CLAUDE.md": true, "rules": true, "agents": true, "commands": true, "skills": true}
	skipFolders  = map[string]bool{"node_modules": true, "dist": true, "build": true, "out": true, "target": true, "vendor": true, "coverage": true, "__pycache__": true, "venv": true}
	shownDots    = map[string]bool{".github": true, ".claude": true}
	worktreeName = regexp.MustCompile(`^sf-[0-9a-f]{8}$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var (
	errOutside = errors.New("outside")
	errSecret  = errors.New("secret")
	errLarge   = errors.New("large")
	errBinary  = errors.New("binary")
	errMissing = errors.New("missing")
	errLink    = errors.New("link")
)

func hiddenPath(parts []string) bool {
	name := parts[len(parts)-1]
	if secretFile.MatchString(name) {
		return true
	}
	for i := 0; i < len(parts)-1; i++ {
		p := parts[i]
		if !strings.HasPrefix(p, ".") || p == ".github" {
			continue
		}
		if p == ".claude" && claudeParts[parts[i+1]] {
			continue
		}
		return true
	}
	return strings.HasPrefix(name, ".") && !plainDotfile.MatchString(name)
}

type checkout struct {
	dir  string
	root *os.Root
}

func (c *checkout) Close() { _ = c.root.Close() }

func (s *Server) checkout(sid string) (*checkout, error) {
	dir, ok := s.reg.Worktree(sid)
	if !ok || s.cfg.Workspace == "" {
		return nil, errOutside
	}
	worktrees := filepath.Join(s.cfg.Workspace, "worktrees")
	if filepath.Dir(dir) != worktrees || !worktreeName.MatchString(filepath.Base(dir)) {
		return nil, errOutside
	}
	ws, err := os.OpenRoot(s.cfg.Workspace)
	if err != nil {
		return nil, errOutside
	}
	defer func() { _ = ws.Close() }()
	rel := path.Join("worktrees", filepath.Base(dir))
	for _, p := range []string{"worktrees", rel} {
		info, err := ws.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return nil, errOutside
		}
	}
	root, err := ws.OpenRoot(rel)
	if err != nil {
		return nil, errOutside
	}
	return &checkout{dir: dir, root: root}, nil
}

func (c *checkout) relative(given string) (string, error) {
	if given == "" || len(given) > maxFilePath || strings.ContainsAny(given, "\x00\r\n") || !utf8.ValidString(given) {
		return "", errMissing
	}
	if strings.HasPrefix(given, "/") {
		rest, ok := strings.CutPrefix(given, c.dir+"/")
		if !ok {
			return "", errOutside
		}
		given = rest
	}
	rel := path.Clean(given)
	if !filepath.IsLocal(rel) || rel == "." {
		return "", errOutside
	}
	if hiddenPath(strings.Split(rel, "/")) {
		return "", errSecret
	}
	return rel, nil
}

func (c *checkout) lstat(rel string) (fs.FileInfo, error) {
	parts := strings.Split(rel, "/")
	var info fs.FileInfo
	for i := range parts {
		at := strings.Join(parts[:i+1], "/")
		got, err := c.root.Lstat(at)
		if err != nil {
			return nil, errMissing
		}
		if got.Mode()&fs.ModeSymlink != 0 {
			return nil, errLink
		}
		if i < len(parts)-1 && !got.IsDir() {
			return nil, errMissing
		}
		info = got
	}
	if !info.Mode().IsRegular() {
		return nil, errMissing
	}
	return info, nil
}

func (c *checkout) read(rel string, limit int64) ([]byte, error) {
	seen, err := c.lstat(rel)
	if err != nil {
		return nil, err
	}
	if seen.Size() > limit {
		return nil, errLarge
	}
	f, err := c.root.OpenFile(rel, openFlags, 0)
	if err != nil {
		return nil, errMissing
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(seen, info) {
		return nil, errMissing
	}
	if info.Size() > limit {
		return nil, errLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, errMissing
	}
	if int64(len(data)) > limit {
		return nil, errLarge
	}
	return data, nil
}

func (c *checkout) text(given string) (string, string, error) {
	rel, err := c.relative(given)
	if err != nil {
		return "", "", err
	}
	data, err := c.read(rel, MaxFileShown)
	if err != nil {
		return "", "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", "", errBinary
	}
	return rel, strings.ToValidUTF8(string(data), "�"), nil
}

func (c *checkout) exists(given string) bool {
	rel, err := c.relative(given)
	if err != nil {
		return false
	}
	_, err = c.lstat(rel)
	return err == nil
}

type listing struct {
	at   time.Time
	list []string
	cut  bool
}

type fileLists struct {
	mu   sync.Mutex
	kept map[string]listing
}

func (l *fileLists) of(c *checkout) listing {
	l.mu.Lock()
	defer l.mu.Unlock()
	if hit, ok := l.kept[c.dir]; ok && time.Since(hit.at) < listKept {
		return hit
	}
	list, cut := c.walk()
	if l.kept == nil || len(l.kept) > 50 {
		l.kept = map[string]listing{}
	}
	got := listing{at: time.Now(), list: list, cut: cut}
	l.kept[c.dir] = got
	return got
}

func folderEntries(f *os.File) ([]fs.DirEntry, bool) {
	var entries []fs.DirEntry
	for len(entries) < maxFolderRead {
		batch, err := f.ReadDir(folderBatch)
		entries = append(entries, batch...)
		if err != nil {
			return entries, false
		}
	}
	more, _ := f.ReadDir(1)
	return entries, len(more) > 0
}

func (c *checkout) walk() ([]string, bool) {
	type dir struct {
		rel   string
		depth int
	}
	var list []string
	cut := false
	started := time.Now()
	stack := []dir{{".", 0}}
	for len(stack) > 0 {
		if len(list) >= maxListed || time.Since(started) >= listFor {
			cut = true
			break
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f, err := c.root.Open(d.rel)
		if err != nil {
			continue
		}
		entries, partial := folderEntries(f)
		_ = f.Close()
		cut = cut || partial
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") && !shownDots[name] {
				continue
			}
			rel := name
			if d.rel != "." {
				rel = d.rel + "/" + name
			}
			switch {
			case e.Type()&fs.ModeSymlink != 0:
			case e.IsDir():
				if !skipFolders[name] && d.depth < listDepth {
					stack = append(stack, dir{rel, d.depth + 1})
				}
			case e.Type().IsRegular():
				if !hiddenPath(strings.Split(rel, "/")) {
					list = append(list, rel)
				}
			}
		}
	}
	return list, cut
}

func inOrder(text, query string) bool {
	q := []rune(query)
	i := 0
	for _, ch := range text {
		if i < len(q) && ch == q[i] {
			i++
		}
	}
	return i == len(q)
}

func matchFiles(list []string, q string) []string {
	query := strings.ToLower(q)
	type hit struct {
		rank, depth int
		path        string
	}
	var found []hit
	for _, p := range list {
		lower := strings.ToLower(p)
		base := lower[strings.LastIndex(lower, "/")+1:]
		rank := -1
		switch {
		case query == "":
			rank = 3
		case strings.HasPrefix(base, query):
			rank = 0
		case strings.HasPrefix(lower, query):
			rank = 1
		case strings.Contains(lower, query):
			rank = 2
		case inOrder(lower, query):
			rank = 3
		}
		if rank >= 0 {
			found = append(found, hit{rank, strings.Count(p, "/"), p})
		}
	}
	slices.SortFunc(found, func(a, b hit) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.depth != b.depth {
			return a.depth - b.depth
		}
		return strings.Compare(a.path, b.path)
	})
	out := make([]string, 0, maxMatches)
	for i := 0; i < len(found) && i < maxMatches; i++ {
		out = append(out, found[i].path)
	}
	return out
}

func (c *checkout) same(list []string, name string, size int64, sum string) string {
	var named []string
	for _, p := range list {
		if p == name || strings.HasSuffix(p, "/"+name) {
			named = append(named, p)
		}
	}
	slices.SortFunc(named, func(a, b string) int {
		if d := strings.Count(a, "/") - strings.Count(b, "/"); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for i, p := range named {
		if i >= maxMatchTries {
			break
		}
		info, err := c.lstat(p)
		if err != nil || info.Size() != size {
			continue
		}
		data, err := c.read(p, session.MaxDrop)
		if err != nil {
			continue
		}
		got := sha256.Sum256(data)
		if hex.EncodeToString(got[:]) == sum {
			return p
		}
	}
	return ""
}

func fileError(err error) string {
	for _, e := range []error{errOutside, errSecret, errLarge, errBinary, errLink} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return errMissing.Error()
}

func (s *Server) files(w http.ResponseWriter, route string, body map[string]any) {
	sid := str(body["sid"], 80)
	allowed := map[string][]string{
		"/api/file":       {"sid", "path"},
		"/api/file-check": {"sid", "paths"},
		"/api/files":      {"sid", "q"},
		"/api/file-match": {"sid", "name", "size", "sha256"},
	}
	if !keysWithin(body, allowed[route]...) || sid == "" {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	c, err := s.checkout(sid)
	if err != nil {
		if route == "/api/file" {
			send(w, http.StatusUnprocessableEntity, problem{Error: fileError(err)})
			return
		}
		refuse(w, http.StatusConflict, "unverified")
		return
	}
	defer c.Close()
	switch route {
	case "/api/file":
		given, ok := body["path"].(string)
		if !ok || given == "" || len(given) > maxFilePath {
			refuse(w, http.StatusBadRequest, "path")
			return
		}
		rel, text, err := c.text(given)
		s.logger.Info("file read", "ok", err == nil)
		if err != nil {
			send(w, http.StatusUnprocessableEntity, problem{Error: fileError(err)})
			return
		}
		send(w, http.StatusOK, map[string]string{"path": rel, "text": text})
	case "/api/file-check":
		list, ok := body["paths"].([]any)
		if !ok || len(list) == 0 || len(list) > maxChecked {
			refuse(w, http.StatusBadRequest, "paths")
			return
		}
		found := []string{}
		for _, v := range list {
			p, ok := v.(string)
			if !ok || p == "" || len(p) > maxFilePath {
				refuse(w, http.StatusBadRequest, "paths")
				return
			}
			if c.exists(p) {
				found = append(found, p)
			}
		}
		send(w, http.StatusOK, map[string]any{"found": found})
	case "/api/files":
		q, ok := body["q"].(string)
		if !ok || len(q) > maxQuery {
			refuse(w, http.StatusBadRequest, "query")
			return
		}
		listed := s.lists.of(c)
		send(w, http.StatusOK, map[string]any{"files": matchFiles(listed.list, q), "cut": listed.cut})
	case "/api/file-match":
		name, _ := body["name"].(string)
		sum, _ := body["sha256"].(string)
		size, ok := integer(body["size"])
		if !session.UploadName(name) || !ok || size < 0 || size > session.MaxDrop || !sha256Hex.MatchString(sum) {
			refuse(w, http.StatusBadRequest, "match")
			return
		}
		s.logger.Info("file match")
		send(w, http.StatusOK, map[string]string{"path": c.same(s.lists.of(c).list, name, size, sum)})
	}
}
