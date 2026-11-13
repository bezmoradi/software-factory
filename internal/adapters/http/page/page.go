package page

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxPageBody     = 64 << 10
	MaxRegisterBody = 1 << 20
	MaxEventsBody   = 4 << 20
	MaxAgentBody    = 64 << 10
	PageConns       = 16
	AgentConns      = 256
	PollFor         = 20 * time.Second
	KeepAlive       = 15 * time.Second
	writeFor        = 20 * time.Second
	readFor         = 15 * time.Second
	signinBurst     = 30
)

type Config struct {
	PublicPort     int
	StateDir       string
	Version        string
	Out            io.Writer
	Now            func() time.Time
	Starter        session.Starter
	Reviews        session.ReviewStarter
	Repos          session.RepoAdmin
	Push           push.Requests
	Pushes         session.PushStarter
	ReviewerSignIn func() (session.SignIn, bool)
	Work           session.WorkPage
	Workspace      string
	Audit          session.AuditPage
	Logins         session.LoginPage
	Forge          session.ForgeService
}

type Server struct {
	cfg      Config
	reg      *session.Registry
	logger   shared.Logger
	keys     *keys
	assets   *assets
	host     string
	origin   string
	mu       sync.Mutex
	attempts []time.Time
	lists    fileLists
}

func New(cfg Config, reg *session.Registry, logger shared.Logger) (*Server, error) {
	if cfg.PublicPort < 1 || cfg.PublicPort > 65535 {
		return nil, fmt.Errorf("page: the public port must be between 1 and 65535")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Push == nil {
		cfg.Push = push.NotWired()
	}
	host := "127.0.0.1:" + strconv.Itoa(cfg.PublicPort)
	return &Server{
		cfg:    cfg,
		reg:    reg,
		logger: logger,
		keys:   newKeys(cfg.Now),
		assets: loadAssets(cfg.StateDir),
		host:   host,
		origin: "http://" + host,
	}, nil
}

func (s *Server) Built() bool {
	return s.assets.index != ""
}

func (s *Server) PrintLink() {
	_, _ = fmt.Fprintf(s.cfg.Out, "Software Factory: open http://%s\n", s.host)
}

func (s *Server) Tick() {
	s.reg.Tick()
	if s.keys.expire(s.reg.Streams()) {
		s.reg.DetachAll()
		s.logger.Info("page signed out", "why", "expired")
	}
}

func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

func (s *Server) Page() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("/", chain(
		http.HandlerFunc(s.page),
		recoverPanics(s.logger, "page", s.pageRoute),
		requestLogger(s.logger, "page", s.pageRoute),
	))
	return pageHeaders(mux)
}

const signinRoute = "/api/session"

var pageRoutes = map[string]bool{
	"/api/events": true, "/api/replay": true, "/api/message": true, "/api/interrupt": true, "/api/answer": true,
	"/api/control": true, "/api/release": true, "/api/disconnect": true, "/api/prefs": true,
	"/api/projects": true, "/api/changes": true, "/api/docs": true, "/api/audit": true,
	"/api/dispatch": true, "/api/wake": true, "/api/stop-session": true, "/api/remove-session": true,
	"/api/add-repo": true, "/api/remove-repo": true, "/api/save-token": true, "/api/remove-token": true,
	reviewRoute: true, pushPageRoute: true, answerRoute: true, expectRoute: true, loginRoute: true, trustRoute: true, overrideRoute: true, handoffRoute: true, forgeAnswerRoute: true, findingsRoute: true,
	uploadRoute: true, testListRoute: true, "/api/files": true, "/api/file": true, "/api/file-check": true, "/api/file-match": true,
}

func (s *Server) pageRoute(r *http.Request) string {
	p := r.URL.Path
	switch {
	case p == "/" || p == signinRoute || pageRoutes[p] || workflowRoutes[p]:
		return p
	case strings.HasPrefix(p, s.assets.prefix):
		return "asset"
	}
	return "other"
}

func (s *Server) reject(w http.ResponseWriter, status int, why string) {
	s.logger.Warn("request refused", "side", "page", "why", why)
	refuse(w, status, why)
}

func siteAllowed(site []string, visit bool) bool {
	if len(site) != 1 {
		return false
	}
	return site[0] == "same-origin" || visit && site[0] == "none"
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(writeFor))
	if r.Method == http.MethodPost {
		_ = rc.SetReadDeadline(time.Now().Add(readFor))
	}
	if r.Host != s.host {
		s.reject(w, http.StatusMisdirectedRequest, "host")
		return
	}
	origin, hasOrigin := r.Header["Origin"]
	if hasOrigin && (len(origin) != 1 || origin[0] != s.origin) {
		s.reject(w, http.StatusForbidden, "origin")
		return
	}
	visit := r.Method == http.MethodGet && r.URL.Path == "/" && r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
	if site, ok := r.Header["Sec-Fetch-Site"]; ok && !siteAllowed(site, visit) {
		s.reject(w, http.StatusForbidden, "site")
		return
	}
	if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, s.assets.prefix) {
		s.static(w, r)
		return
	}
	known := r.URL.Path == signinRoute || pageRoutes[r.URL.Path] || workflowRoutes[r.URL.Path]
	if !known || !strings.HasPrefix(r.URL.Path, "/api/") {
		refuse(w, http.StatusNotFound, "route")
		return
	}
	want := http.MethodPost
	if r.URL.Path == "/api/events" {
		want = http.MethodGet
	}
	if r.Method != want {
		refuse(w, http.StatusMethodNotAllowed, "method")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		s.reject(w, http.StatusBadRequest, "query")
		return
	}
	if r.Method == http.MethodPost && !hasOrigin {
		s.reject(w, http.StatusForbidden, "origin")
		return
	}
	if r.URL.Path == signinRoute {
		if !isJSON(r) {
			refuse(w, http.StatusUnsupportedMediaType, "type")
			return
		}
		s.signin(w, r)
		return
	}
	if !s.authorized(r) {
		s.reject(w, http.StatusUnauthorized, "token")
		return
	}
	if r.URL.Path == "/api/events" {
		s.events(w, r)
		return
	}
	if r.URL.Path == uploadRoute {
		s.upload(w, r)
		return
	}
	if !isJSON(r) {
		refuse(w, http.StatusUnsupportedMediaType, "type")
		return
	}
	limit := int64(MaxPageBody)
	if approveRoutes[r.URL.Path] {
		limit = MaxApproveBody
	}
	body, bad := readObject(w, r, limit)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	if workflowRoutes[r.URL.Path] {
		s.workflowAPI(w, r, r.URL.Path, body)
		return
	}
	if starting[r.URL.Path] {
		s.manage(w, r, body)
		return
	}
	if r.URL.Path == reviewRoute || r.URL.Path == overrideRoute {
		s.review(w, r, body)
		return
	}
	if r.URL.Path == testListRoute {
		s.testList(w, r, body)
		return
	}
	if r.URL.Path == expectRoute {
		s.expect(w, body)
		return
	}
	if r.URL.Path == loginRoute {
		s.claudeLogin(w, r, body)
		return
	}
	if r.URL.Path == trustRoute {
		s.claudeTrust(w, r, body)
		return
	}
	if r.URL.Path == pushPageRoute || r.URL.Path == answerRoute || r.URL.Path == handoffRoute || r.URL.Path == findingsRoute || r.URL.Path == "/api/changes" {
		s.pushPage(w, r, body)
		return
	}
	if r.URL.Path == forgeAnswerRoute {
		s.forgeAnswer(w, body)
		return
	}
	if repoRoutes[r.URL.Path] {
		s.repos(w, r.URL.Path, body)
		return
	}
	if fileRoutes[r.URL.Path] {
		s.files(w, r.URL.Path, body)
		return
	}
	if r.URL.Path == "/api/message" {
		s.message(w, r, body)
		return
	}
	s.api(w, r.URL.Path, body)
}

func (s *Server) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, given, ok := strings.Cut(values[0], " ")
	return ok && strings.EqualFold(scheme, "Bearer") && s.keys.check(strings.TrimLeft(given, " "))
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		refuse(w, http.StatusMethodNotAllowed, "method")
		return
	}
	if r.URL.Path == "/" {
		html, ok := s.assets.page()
		if !ok {
			send(w, http.StatusServiceUnavailable, problem{Error: "not built", Message: "The page wasn't built into this engine. Run npm --prefix web run build, then build the engine again."})
			return
		}
		w.Header().Set("Clear-Site-Data", `"storage"`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, html)
		return
	}
	a, ok := s.assets.files[strings.TrimPrefix(r.URL.Path, s.assets.prefix)]
	if !ok {
		refuse(w, http.StatusNotFound, "missing")
		return
	}
	w.Header().Set("Content-Type", a.kind)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.body)
}

func (s *Server) allowSignin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	kept := s.attempts[:0]
	for _, t := range s.attempts {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	s.attempts = kept
	if len(kept) >= signinBurst {
		return false
	}
	s.attempts = append(s.attempts, now)
	return true
}

func (s *Server) signin(w http.ResponseWriter, r *http.Request) {
	if !s.allowSignin() {
		s.reject(w, http.StatusTooManyRequests, "signin-rate")
		return
	}
	body, bad := readObject(w, r, MaxPageBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	if len(body) != 0 {
		s.reject(w, http.StatusBadRequest, "body")
		return
	}
	key := s.keys.issue()
	s.reg.DetachAll()
	s.logger.Info("page signed in")
	send(w, http.StatusOK, map[string]any{"token": key})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	sink := newStream(MaxStreamQueue)
	id, err := s.reg.Attach(sink)
	if err != nil {
		s.reject(w, http.StatusTooManyRequests, "streams")
		return
	}
	defer s.reg.Detach(id)
	s.logger.Info("page stream opened")
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	keep := time.NewTicker(KeepAlive)
	defer keep.Stop()
	for {
		lines, closed := sink.take()
		for _, line := range lines {
			_ = rc.SetWriteDeadline(time.Now().Add(writeFor))
			if _, err := w.Write(line); err != nil {
				return
			}
		}
		if len(lines) > 0 {
			_ = rc.Flush()
		}
		if closed {
			return
		}
		select {
		case <-sink.notify:
		case <-keep.C:
			_ = rc.SetWriteDeadline(time.Now().Add(writeFor))
			if _, err := io.WriteString(w, "\n"); err != nil {
				return
			}
			_ = rc.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func status(err error) (int, problem) {
	var e *session.Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError, problem{Error: "server"}
	}
	codes := map[session.Kind]int{
		session.Invalid:      http.StatusBadRequest,
		session.Conflict:     http.StatusConflict,
		session.NotFound:     http.StatusNotFound,
		session.Limited:      http.StatusTooManyRequests,
		session.Full:         http.StatusServiceUnavailable,
		session.Unauthorized: http.StatusUnauthorized,
		session.Gone:         http.StatusGone,
	}
	code, ok := codes[e.Kind]
	if !ok {
		code = http.StatusInternalServerError
	}
	return code, problem{Error: e.Reason, Message: e.Message, Needs: needsOf(e.Needs)}
}

func reply(w http.ResponseWriter, err error, okStatus int) {
	if err != nil {
		code, body := status(err)
		send(w, code, body)
		return
	}
	send(w, okStatus, map[string]bool{"ok": true})
}

func keysWithin(body map[string]any, allowed ...string) bool {
	for k := range body {
		found := false
		for _, a := range allowed {
			if a == k {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func present(v any) bool {
	if v == nil {
		return false
	}
	if list, ok := v.([]any); ok {
		return len(list) > 0
	}
	return true
}

var apiFields = map[string][]string{
	"/api/disconnect": {},
	"/api/replay":     {"stream", "sid", "epoch", "since"},
	"/api/interrupt":  {"sid"},
	"/api/answer":     {"sid", "id", "answer"},
	"/api/control":    {"sid", "on", "confirm"},
	"/api/release":    {"sid"},
	"/api/projects":   {},
	"/api/docs":       {},
}

func (s *Server) api(w http.ResponseWriter, path string, body map[string]any) {
	if fields, ok := apiFields[path]; ok && !keysWithin(body, fields...) {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	for _, k := range []string{"sid", "stream", "epoch", "id"} {
		if v, present := body[k]; present {
			if _, ok := v.(string); !ok {
				refuse(w, http.StatusBadRequest, "fields")
				return
			}
		}
	}
	sid := str(body["sid"], 80)
	switch path {
	case "/api/disconnect":
		if s.keys.revoke() {
			s.logger.Info("page signed out", "why", "disconnect")
		}
		s.reg.DetachAll()
		send(w, http.StatusOK, map[string]bool{"ok": true})
	case "/api/prefs":
		if !s.assets.save(body) {
			refuse(w, http.StatusBadRequest, "prefs")
			return
		}
		send(w, http.StatusOK, map[string]bool{"ok": true})
	case "/api/replay":
		since, ok := integer(body["since"])
		if !ok {
			since = 0
		}
		reply(w, s.reg.Replay(str(body["stream"], 40), sid, str(body["epoch"], 16), since), http.StatusAccepted)
	case "/api/interrupt":
		reply(w, s.reg.Interrupt(sid), http.StatusAccepted)
	case "/api/answer":
		err := s.reg.Answer(sid, str(body["id"], 40), body["answer"])
		if err == nil {
			s.logger.Info("card answered")
		}
		reply(w, err, http.StatusOK)
	case "/api/control":
		on, ok := body["on"].(bool)
		if body["confirm"] != true || !ok {
			refuse(w, http.StatusBadRequest, "confirm")
			return
		}
		reply(w, s.reg.Control(sid, on), http.StatusAccepted)
	case "/api/release":
		err := s.reg.Release(sid)
		if err == nil {
			s.logger.Warn("disputed session released")
		}
		reply(w, err, http.StatusOK)
	case "/api/projects":
		s.projects(w)
	case "/api/docs":
		send(w, http.StatusOK, map[string]any{"text": guide()})
	case "/api/audit":
		s.audit(w, body)
	default:
		refuse(w, http.StatusNotFound, "route")
	}
}
