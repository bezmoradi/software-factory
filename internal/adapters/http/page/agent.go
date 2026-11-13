package page

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var cardParam = regexp.MustCompile(`^[0-9a-f]{32}$`)

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
	s.logger.Warn("request refused", "side", "agent", "route", s.agentRoute(r), "why", why)
	refuse(w, status, why)
}

func connSecret(r *http.Request) string {
	values := r.Header.Values("X-SF-Conn")
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(writeFor))
	if r.Method == http.MethodPost {
		_ = rc.SetReadDeadline(time.Now().Add(2 * readFor))
	}
	if browserHeaders(r) {
		s.agentReject(w, r, http.StatusForbidden, "browser")
		return
	}
	want, ok := agentRoutes[r.URL.Path]
	if !ok {
		refuse(w, http.StatusNotFound, "route")
		return
	}
	if r.Method != want {
		refuse(w, http.StatusMethodNotAllowed, "method")
		return
	}
	if r.Method == http.MethodPost && !isJSON(r) {
		refuse(w, http.StatusUnsupportedMediaType, "type")
		return
	}
	card := ""
	switch r.URL.Path {
	case "/wait", "/wake":
		q := r.URL.Query()
		card = q.Get("card")
		if len(q) != 1 || len(q["card"]) != 1 || !cardParam.MatchString(card) {
			refuse(w, http.StatusBadRequest, "card")
			return
		}
	case "/image":
	default:
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			refuse(w, http.StatusBadRequest, "query")
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
	case "/check":
		s.check(w, r)
		return
	}
	given := connSecret(r)
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
		body, status := readObject(w, r, MaxEventsBody)
		if status != 0 {
			refuse(w, status, "body")
			return
		}
		events, ok := body["events"].([]any)
		if !ok {
			refuse(w, http.StatusBadRequest, "events")
			return
		}
		stream, _ := body["stream"].(string)
		if err := s.reg.Report(given, session.Batch{Stream: clip(stream, 40), Reset: body["reset"] == true, Events: events}); err != nil {
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
		if _, status := readObject(w, r, MaxAgentBody); status != 0 {
			refuse(w, status, "body")
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
			refuse(w, http.StatusNotFound, "route")
		}
	}
}

func (s *Server) agentError(w http.ResponseWriter, r *http.Request, err error) {
	code, body := status(err)
	if code == http.StatusConflict || code == http.StatusUnauthorized || code >= 500 {
		s.logger.Warn("request refused", "side", "agent", "route", s.agentRoute(r), "why", body.Error)
	}
	send(w, code, body)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	body, bad := readObject(w, r, MaxRegisterBody)
	if bad != 0 {
		refuse(w, bad, "body")
		return
	}
	protocol, _ := integer(body["protocol"])
	lastSeq, _ := integer(body["lastSeq"])
	cards, _ := body["cards"].([]any)
	if protocol < 0 || protocol > 1000 {
		protocol = 0
	}
	got, err := s.reg.Register(session.Registration{
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
			s.logger.Warn("registration limited", "side", "agent")
		}
		send(w, code, why)
		return
	}
	s.logger.Info("session registered", "side", "agent")
	out := map[string]any{
		"secret": got.Secret, "restart": false, "page": got.Page, "push": got.Push, "control": got.Control,
	}
	if got.Background != nil {
		out["background"] = *got.Background
	}
	send(w, http.StatusOK, out)
}
