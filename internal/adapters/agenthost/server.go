package agenthost

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	Upgrade         = "sf-stdio"
	ProcessHeader   = "X-SF-Process"
	DefaultSocket   = "/run/sf/plugin.sock"
	SocketVariable  = "SF_PLUGIN_SOCKET"
	MaxControlBody  = 64 << 10
	MaxArgs         = 64
	MaxArg          = 32 << 10
	MaxEnvValue     = 4096
	MaxNames        = 32
	DefaultMaxProcs = 32
	MaxRuns         = 8
	keptEnded       = 100
)

type Config struct {
	Workspace    string
	AgentsBin    string
	PluginSocket string
	Git          string
	MaxProcesses int
	Review       bool
	KeyFile      string
	Environ      []string
	Logger       shared.Logger
}

const ReviewRunTimeout = 25 * time.Minute

const KeyHeader = "X-SF-Host-Key"

var reviewRoutes = map[string]bool{"/healthz": true, "/v1/run": true, "/v1/sign-in": true, "/v1/login": true, "/v1/api-key": true}

type Server struct {
	cfg   Config
	log   shared.Logger
	mu    sync.Mutex
	procs map[string]*process
	ended []string
	runs  int
	login *login
	wtMu  sync.Mutex
}

type commandJSON struct {
	Argv      []string          `json:"argv"`
	Cwd       string            `json:"cwd"`
	Pass      []string          `json:"pass,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
}

type runReply struct {
	Code     int    `json:"code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

type worktreeJSON struct {
	Repo   string `json:"repo"`
	Name   string `json:"name"`
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
	Force  bool   `json:"force,omitempty"`
}

type worktreeReply struct {
	Path string `json:"path"`
}

type stopJSON struct {
	ID string `json:"id"`
}

type processJSON struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	PID       int    `json:"pid"`
	Running   bool   `json:"running"`
	Exit      *int   `json:"exit,omitempty"`
	StartedAt int64  `json:"startedAt"`
}

type problemJSON struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

type problem struct {
	status int
	code   string
	msg    string
}

func fail(status int, code, msg string) *problem {
	return &problem{status: status, code: code, msg: msg}
}

func New(cfg Config) (*Server, error) {
	if !filepath.IsAbs(cfg.Workspace) {
		return nil, errors.New("agent-host: the workspace must be an absolute path")
	}
	cfg.Workspace = filepath.Clean(cfg.Workspace)
	if cfg.AgentsBin != "" && (!filepath.IsAbs(cfg.AgentsBin) || strings.ContainsAny(cfg.AgentsBin, ":\x00")) {
		return nil, errors.New("agent-host: the agents folder must be an absolute path without a colon")
	}
	info, err := os.Stat(cfg.Workspace)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("agent-host: the workspace %s isn't a folder", cfg.Workspace)
	}
	if cfg.MaxProcesses <= 0 {
		cfg.MaxProcesses = DefaultMaxProcs
	}
	if cfg.Git == "" {
		cfg.Git = "git"
	}
	log := cfg.Logger
	if log == nil {
		log = quiet{}
	}
	return &Server{cfg: cfg, log: log, procs: map[string]*process{}}, nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func browserHeaders(r *http.Request) bool {
	for _, h := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Referer"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func send(w http.ResponseWriter, status int, body any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, p *problem) {
	if a, ok := w.(*answer); ok {
		a.why = p.code
	}
	send(w, p.status, problemJSON{Error: p.code, Message: p.msg})
}

type answer struct {
	http.ResponseWriter
	status int
	why    string
}

func (a *answer) WriteHeader(status int) {
	if a.status == 0 {
		a.status = status
	}
	a.ResponseWriter.WriteHeader(status)
}

func (a *answer) Unwrap() http.ResponseWriter {
	return a.ResponseWriter
}

func (a *answer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(a.ResponseWriter).Hijack()
}

func (s *Server) logRefused(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := &answer{ResponseWriter: w}
		next(a, r)
		if a.why == "" || a.why == "browser" || a.why == "key" || a.why == "process" {
			return
		}
		path := r.URL.Path
		if _, known := routes[path]; !known {
			path = "other"
		}
		s.log.Warn("agent-host request refused", "path", path, "status", a.status, "why", a.why)
	}
}

func isJSON(r *http.Request) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && kind == "application/json"
}

func decode(w http.ResponseWriter, r *http.Request, v any) *problem {
	if !isJSON(r) {
		return fail(http.StatusUnsupportedMediaType, "type", "agent-host takes only application/json.")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxControlBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return fail(http.StatusRequestEntityTooLarge, "body", fmt.Sprintf("The request is over agent-host's limit of %d KiB.", MaxControlBody>>10))
		}
		return fail(http.StatusBadRequest, "body", "agent-host couldn't read the request.")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		return fail(http.StatusBadRequest, "body", "agent-host couldn't read the request: it isn't the JSON this route takes.")
	}
	return nil
}

var routes = map[string]string{
	"/healthz":             http.MethodGet,
	"/v1/processes":        "",
	"/v1/processes/stop":   http.MethodPost,
	"/v1/run":              http.MethodPost,
	"/v1/sign-in":          http.MethodPost,
	"/v1/login":            http.MethodPost,
	"/v1/api-key":          http.MethodPost,
	"/v1/trust":            http.MethodPost,
	"/v1/worktrees":        http.MethodPost,
	"/v1/worktrees/remove": http.MethodPost,
}

func (s *Server) keyed(r *http.Request) bool {
	data, err := os.ReadFile(s.cfg.KeyFile)
	key := strings.TrimSpace(string(data))
	if err != nil || len(key) < 32 {
		return false
	}
	values := r.Header.Values(KeyHeader)
	return len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte(key)) == 1
}

func (s *Server) Handler() http.Handler {
	return s.logRefused(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if browserHeaders(r) {
		s.log.Warn("agent-host request refused", "why", "browser")
		refuse(w, fail(http.StatusForbidden, "browser", ""))
		return
	}
	want, ok := routes[r.URL.Path]
	if ok && s.cfg.Review && !reviewRoutes[r.URL.Path] {
		ok = false
	}
	if ok && s.cfg.KeyFile != "" && r.URL.Path != "/healthz" && !s.keyed(r) {
		s.log.Warn("agent-host request refused", "why", "key")
		refuse(w, fail(http.StatusUnauthorized, "key", ""))
		return
	}
	if !ok {
		refuse(w, fail(http.StatusNotFound, "route", ""))
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		refuse(w, fail(http.StatusBadRequest, "query", ""))
		return
	}
	if r.URL.Path == "/v1/processes" {
		switch r.Method {
		case http.MethodGet:
			send(w, http.StatusOK, map[string]any{"processes": s.list()})
		case http.MethodPost:
			s.start(w, r)
		default:
			refuse(w, fail(http.StatusMethodNotAllowed, "method", ""))
		}
		return
	}
	if r.Method != want {
		refuse(w, fail(http.StatusMethodNotAllowed, "method", ""))
		return
	}
	switch r.URL.Path {
	case "/healthz":
		send(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/v1/processes/stop":
		var q stopJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		exited, p := s.stop(q.ID)
		if p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, map[string]bool{"ok": true, "exited": exited})
	case "/v1/run":
		var q commandJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		out, p := s.run(r.Context(), q)
		if p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, out)
	case "/v1/sign-in":
		var q signInJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		out, p := s.signIn(r.Context(), q)
		if p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, out)
	case "/v1/trust":
		var q commandJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		out, p := s.trustStep(r.Context(), q)
		if p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, out)
	case "/v1/login":
		var q loginJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		out, p := s.loginStep(r.Context(), q)
		if p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, out)
	case "/v1/api-key":
		var q apiKeyJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		if p := s.apiKeyStep(q); p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, map[string]bool{"ok": true})
	case "/v1/worktrees":
		var q worktreeJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		path, p := s.addWorktree(r.Context(), q)
		if p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, worktreeReply{Path: path})
	case "/v1/worktrees/remove":
		var q worktreeJSON
		if p := decode(w, r, &q); p != nil {
			refuse(w, p)
			return
		}
		if p := s.removeWorktree(r.Context(), q); p != nil {
			refuse(w, p)
			return
		}
		send(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func upgrading(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), Upgrade) {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
				return true
			}
		}
	}
	return false
}

type quiet struct{}

func (quiet) Error(string, ...any) {}
func (quiet) Fatal(error)          {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}
