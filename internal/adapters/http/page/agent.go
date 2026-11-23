package page

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

var cardParam = regexp.MustCompile(`^[0-9a-f]{32}$`)

const (
	MaxBatchEvents = 2000
	MaxEventBytes  = 2 << 20
	MaxDecodes     = 2
	eventsWait     = readFor
	eventsBodyFor  = 10 * time.Second
	retryAfter     = 2
)

var agentRoutes = map[string]string{
	"/register":   http.MethodPost,
	"/poll":       http.MethodGet,
	"/events":     http.MethodPost,
	"/wait":       http.MethodGet,
	"/wake":       http.MethodPost,
	"/image":      http.MethodGet,
	"/check":      http.MethodPost,
	pushRoute:     http.MethodPost,
	forgeRoute:    http.MethodPost,
	checksRoute:   http.MethodPost,
	commentsRoute: http.MethodPost,
	splitRoute:    http.MethodPost,
	workTypeRoute: http.MethodPost,

	turnCheckRoute:    http.MethodPost,
	sessionModelRoute: http.MethodGet,
}

func (s *Server) agentRoute(r *http.Request) string {
	if _, ok := agentRoutes[r.URL.Path]; ok {
		return r.URL.Path
	}
	return "other"
}

func (s *Server) Agent() http.Handler {
	return agentHeaders(chain(
		http.HandlerFunc(s.agent),
		recoverPanics(s.logger, "agent", s.agentRoute),
		requestLogger(s.logger, "agent", s.agentRoute),
	))
}

func (s *Server) agentReject(w http.ResponseWriter, r *http.Request, status int, why string) {
	s.agentRefused(r, why)
	agentRefuse(w, status, why)
}

func (s *Server) agentRefused(r *http.Request, why string) {
	shared.For(r.Context(), s.logger).Warn("request refused", boxed(r, "side", "agent", "route", s.agentRoute(r), "why", why)...)
}

func boxed(r *http.Request, args ...any) []any {
	if box := ConnBox(r.Context()); box != "" {
		args = append(args, "box", box)
	}
	return args
}

func pushToken(r *http.Request) string {
	values := r.Header.Values("X-SF-Push")
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func connSecret(r *http.Request) string {
	values := r.Header.Values("X-SF-Conn")
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

type markedBody struct {
	io.ReadCloser
	read atomic.Bool
}

func (b *markedBody) Read(p []byte) (int, error) {
	b.read.Store(true)
	return b.ReadCloser.Read(p)
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(writeFor))
	if r.ContentLength != 0 && r.Body != nil && r.Body != http.NoBody {
		body := &markedBody{ReadCloser: r.Body}
		r.Body = body
		defer func() {
			if !body.read.Load() {
				_ = rc.SetReadDeadline(time.Now())
			}
		}()
	}
	if r.URL.Path == "/poll" || r.URL.Path == "/wait" {
		_ = rc.SetReadDeadline(time.Now().Add(PollFor + writeFor))
	} else {
		_ = rc.SetReadDeadline(time.Now().Add(2 * readFor))
	}
	if browserHeaders(r) {
		s.agentReject(w, r, http.StatusForbidden, "browser")
		return
	}
	want, ok := agentRoutes[r.URL.Path]
	if !ok {
		agentRefuse(w, http.StatusNotFound, "route")
		return
	}
	if r.Method != want {
		agentRefuse(w, http.StatusMethodNotAllowed, "method")
		return
	}
	if r.Method == http.MethodPost && !isJSON(r) {
		agentRefuse(w, http.StatusUnsupportedMediaType, "type")
		return
	}
	box := ConnBox(r.Context())
	if box == "" || s.reg.FromBox(connSecret(r), pushToken(r), box) != nil {
		s.agentReject(w, r, http.StatusUnauthorized, "box")
		return
	}
	card := ""
	switch r.URL.Path {
	case "/wait", "/wake":
		q := r.URL.Query()
		card = q.Get("card")
		if len(q) != 1 || len(q["card"]) != 1 || !cardParam.MatchString(card) {
			agentRefuse(w, http.StatusBadRequest, "card")
			return
		}
	case "/image":
	default:
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			agentRefuse(w, http.StatusBadRequest, "query")
			return
		}
	}
	switch r.URL.Path {
	case "/register":
		s.register(w, r)
		return
	case pushRoute:
		s.pushRequest(w, r)
		return
	case forgeRoute:
		s.forgeRequest(w, r)
		return
	case checksRoute:
		s.checksRequest(w, r)
		return
	case commentsRoute:
		s.commentsRequest(w, r)
		return
	case splitRoute:
		s.splitRequest(w, r)
		return
	case workTypeRoute:
		s.workTypeRequest(w, r)
		return
	case "/check":
		s.check(w, r)
		return
	case turnCheckRoute:
		s.turnCheck(w, r)
		return
	case sessionModelRoute:
		s.sessionModel(w, r)
		return
	}
	given := connSecret(r)
	if (r.URL.Path == "/events" || r.URL.Path == "/wake") && !s.reg.Known(given) {
		s.agentReject(w, r, http.StatusUnauthorized, "connection")
		return
	}
	switch r.URL.Path {
	case "/poll":
		_ = rc.SetWriteDeadline(time.Now().Add(PollFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), PollFor)
		defer cancel()
		messages, err := s.reg.Poll(ctx, given)
		switch {
		case err != nil:
			s.agentError(w, r, err)
		case messages == nil:
			w.WriteHeader(http.StatusNoContent)
		default:
			send(w, http.StatusOK, map[string]any{"messages": messages})
		}
	case "/events":
		leave, entered := s.boxTurn(r)
		if !entered {
			s.busy(w, r)
			return
		}
		defer leave()
		_ = rc.SetReadDeadline(time.Now().Add(eventsBodyFor))
		data, status := readBody(w, r, MaxEventsBody)
		if status != 0 {
			agentRefuse(w, status, "body")
			return
		}
		_ = rc.SetReadDeadline(time.Now().Add(eventsWait + writeFor))
		done, decoding := s.decodeTurn(r)
		if !decoding {
			s.busy(w, r)
			return
		}
		batch, status, why := decodeEvents(json.NewDecoder(bytes.NewReader(data)))
		if status != 0 {
			done()
			agentRefuse(w, status, why)
			return
		}
		err := s.reg.Report(given, batch)
		done()
		if err != nil {
			s.agentError(w, r, err)
			return
		}
		send(w, http.StatusOK, map[string]any{})
	case "/wait":
		_ = rc.SetWriteDeadline(time.Now().Add(PollFor + writeFor))
		ctx, cancel := context.WithTimeout(r.Context(), PollFor)
		defer cancel()
		answer, found, err := s.reg.Wait(ctx, given, card)
		switch {
		case err != nil:
			s.agentError(w, r, err)
		case !found:
			w.WriteHeader(http.StatusNoContent)
		default:
			send(w, http.StatusOK, map[string]any{"answer": answer})
		}
	case "/wake":
		leave, entered := s.boxTurn(r)
		if !entered {
			s.busy(w, r)
			return
		}
		defer leave()
		if _, status := readObject(w, r, MaxAgentBody); status != 0 {
			agentRefuse(w, status, "body")
			return
		}
		if err := s.reg.Wake(given, card); err != nil {
			s.agentError(w, r, err)
			return
		}
		send(w, http.StatusOK, map[string]any{})
	default:
		if !s.reg.Known(given) {
			s.agentReject(w, r, http.StatusUnauthorized, "connection")
			return
		}
		switch r.URL.Path {
		case "/image":
			s.image(w, r)
		default:
			agentRefuse(w, http.StatusNotFound, "route")
		}
	}
}

func (s *Server) agentError(w http.ResponseWriter, r *http.Request, err error) {
	code, body := status(err)
	if code == http.StatusConflict || code == http.StatusUnauthorized || code >= 500 {
		s.agentRefused(r, body.Error)
	}
	send(w, code, body)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxRegisterBody)
	if bad != 0 {
		agentRefuse(w, bad, "body")
		return
	}
	protocol, _ := integer(body["protocol"])
	lastSeq, _ := integer(body["lastSeq"])
	cards, _ := body["cards"].([]any)
	if protocol < 0 || protocol > 1000 {
		protocol = 0
	}
	got, err := s.reg.Register(session.Registration{
		Box:        ConnBox(r.Context()),
		SID:        str(body["sid"], 80),
		Cwd:        str(body["cwd"], session.MaxCwd+1),
		Version:    str(body["version"], 40),
		Epoch:      str(body["epoch"], 17),
		Mode:       str(body["mode"], 40),
		Protocol:   int(protocol),
		LastSeq:    lastSeq,
		Controlled: body["controlled"] == true,
		Restored:   body["restored"] == true,
		GateKnown:  body["gateKnown"] != false,
		Busy:       body["busy"] == true,
		Login:      body["login"] == true,
		Cards:      cards,
	})
	if err != nil {
		code, why := status(err)
		if code == http.StatusTooManyRequests {
			shared.For(r.Context(), s.logger).Warn("registration limited", boxed(r, "side", "agent")...)
		} else {
			s.agentRefused(r, why.Error)
		}
		send(w, code, why)
		return
	}
	shared.For(r.Context(), s.logger).Info("session registered", "side", "agent")
	out := map[string]any{
		"secret": got.Secret, "restart": false, "page": got.Page, "push": got.Push, "control": got.Control, "compose": session.Compose(),
	}
	if got.Background != nil {
		out["background"] = *got.Background
	}
	send(w, http.StatusOK, out)
}

func (s *Server) busy(w http.ResponseWriter, r *http.Request) {
	s.agentRefused(r, "events-busy")
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	send(w, http.StatusTooManyRequests, map[string]any{"error": "events-busy", "retryAfter": retryAfter})
}

func (s *Server) boxTurn(r *http.Request) (func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), eventsWait)
	defer cancel()
	return s.inflight.enter(ctx, ConnBox(r.Context()))
}

func (s *Server) decodeTurn(r *http.Request) (func(), bool) {
	t := time.NewTimer(eventsWait)
	defer t.Stop()
	select {
	case s.decodes <- struct{}{}:
		return func() { <-s.decodes }, true
	case <-t.C:
	case <-r.Context().Done():
	}
	return nil, false
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, int) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, http.StatusRequestEntityTooLarge
		}
		return nil, http.StatusBadRequest
	}
	return data, 0
}

func decodeEvents(dec *json.Decoder) (session.Batch, int, string) {
	bad := func(why string) (session.Batch, int, string) { return session.Batch{}, http.StatusBadRequest, why }
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return bad("body")
	}
	var batch session.Batch
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok || seen[key] {
			return bad("body")
		}
		seen[key] = true
		switch key {
		case "events":
			events, status, why := decodeList(dec)
			if status != 0 {
				return session.Batch{}, status, why
			}
			batch.Events = events
		case "stream":
			var stream string
			if dec.Decode(&stream) != nil {
				return bad("body")
			}
			batch.Stream = clip(stream, 40)
		case "reset":
			if dec.Decode(&batch.Reset) != nil {
				return bad("body")
			}
		default:
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return bad("body")
			}
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return bad("body")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return bad("body")
	}
	if batch.Events == nil {
		return bad("events")
	}
	return batch, 0, ""
}

func decodeList(dec *json.Decoder) ([]any, int, string) {
	t, err := dec.Token()
	if err != nil || t != json.Delim('[') {
		return nil, http.StatusBadRequest, "events"
	}
	events := []any{}
	for dec.More() {
		if len(events) >= MaxBatchEvents {
			return nil, http.StatusRequestEntityTooLarge, "events"
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return nil, http.StatusBadRequest, "event"
		}
		if len(raw) > MaxEventBytes {
			return nil, http.StatusRequestEntityTooLarge, "event"
		}
		if raw[0] != '{' {
			events = append(events, nil)
			continue
		}
		var ev map[string]any
		if json.Unmarshal(raw, &ev) != nil {
			return nil, http.StatusBadRequest, "event"
		}
		events = append(events, ev)
	}
	if t, err := dec.Token(); err != nil || t != json.Delim(']') {
		return nil, http.StatusBadRequest, "events"
	}
	return events, 0, ""
}

type oneAtATime struct {
	mu   sync.Mutex
	held map[string]*turnstile
}

type turnstile struct {
	ch    chan struct{}
	users int
}

func newOneAtATime() *oneAtATime {
	return &oneAtATime{held: map[string]*turnstile{}}
}

func (o *oneAtATime) enter(ctx context.Context, key string) (func(), bool) {
	o.mu.Lock()
	t := o.held[key]
	if t == nil {
		t = &turnstile{ch: make(chan struct{}, 1)}
		o.held[key] = t
	}
	t.users++
	o.mu.Unlock()
	leave := func() {
		o.mu.Lock()
		t.users--
		if t.users == 0 {
			delete(o.held, key)
		}
		o.mu.Unlock()
	}
	select {
	case t.ch <- struct{}{}:
		return func() {
			<-t.ch
			leave()
		}, true
	case <-ctx.Done():
		leave()
		return nil, false
	}
}
