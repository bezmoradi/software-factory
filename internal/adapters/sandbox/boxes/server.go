package boxes

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	WireVersion    = "1"
	KeyHeader      = "X-SF-Helper-Key"
	MaxRequest     = 1 << 10
	maxKeyFile     = 256
	startFor       = 3 * time.Minute
	otherFor       = 2 * time.Minute
	routeHealth    = "health"
	routeList      = "list"
	routeBox       = "box"
	routeStart     = "start"
	routeStop      = "stop"
	routeReviewers = "reviewers"
	routeReviewer  = "reviewer"
	routeReview    = "review"
	actionStartSeg = "start"
	actionStopSeg  = "stop"
)

type ServerConfig struct {
	Boxes     session.Boxes
	Reviewers ReviewerMaker
	KeyFile   string
	Base      context.Context
	Logger    shared.Logger
	About     func() About
}

type server struct {
	cfg ServerConfig
}

type boxJSON struct {
	Box     string   `json:"box"`
	State   string   `json:"state"`
	Recipe  string   `json:"recipe"`
	Host    string   `json:"host"`
	Engine  string   `json:"engine"`
	Agent   string   `json:"agent"`
	Gateway string   `json:"gateway"`
	Subnet  string   `json:"subnet"`
	Facts   []string `json:"facts"`
}

type reviewersJSON struct {
	Version   string    `json:"version"`
	Reviewers []boxJSON `json:"reviewers"`
}

type listJSON struct {
	Version string    `json:"version"`
	Boxes   []boxJSON `json:"boxes"`
	About   *About    `json:"about,omitempty"`
}

type answerJSON struct {
	Version string   `json:"version"`
	Box     *boxJSON `json:"box,omitempty"`
}

type errorJSON struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func Handler(cfg ServerConfig) http.Handler {
	if cfg.Base == nil {
		cfg.Base = context.Background()
	}
	return &server{cfg: cfg}
}

type routed struct {
	name, id, snapshot string
}

func route(path string) (routed, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "healthz":
		return routed{name: routeHealth}, true
	case len(parts) < 2 || parts[0] != "v1":
		return routed{}, false
	case parts[1] == "reviewers":
		return reviewerRoute(parts[2:])
	case parts[1] != "boxes":
		return routed{}, false
	case len(parts) == 2:
		return routed{name: routeList}, true
	case len(parts) == 3:
		return routed{name: routeBox, id: parts[2]}, true
	case len(parts) == 4 && parts[3] == actionStartSeg:
		return routed{name: routeStart, id: parts[2]}, true
	case len(parts) == 4 && parts[3] == actionStopSeg:
		return routed{name: routeStop, id: parts[2]}, true
	}
	return routed{}, false
}

func reviewerRoute(rest []string) (routed, bool) {
	switch len(rest) {
	case 0:
		return routed{name: routeReviewers}, true
	case 1:
		return routed{name: routeReviewer, id: rest[0]}, true
	case 2:
		return routed{name: routeReview, id: rest[0], snapshot: rest[1]}, true
	}
	return routed{}, false
}

func allowed(r string) []string {
	switch r {
	case routeHealth, routeList, routeReviewers:
		return []string{http.MethodGet}
	case routeBox, routeReviewer:
		return []string{http.MethodPut, http.MethodDelete}
	case routeReview:
		return []string{http.MethodPut}
	}
	return []string{http.MethodPost}
}

func requestSize(r *http.Request) int {
	n := len(r.Method) + len(r.RequestURI) + len(r.Proto) + 4
	for k, vs := range r.Header {
		for _, v := range vs {
			n += len(k) + len(v) + 4
		}
	}
	return n + len(r.Host)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if requestSize(r) > MaxRequest {
		s.refuse(w, r, refuse(http.StatusRequestHeaderFieldsTooLarge, ReasonTooLarge, "the request is larger than the box helper reads (1 KiB)"))
		return
	}
	rt, ok := route(r.URL.Path)
	name, box := rt.name, rt.id
	if !ok || r.URL.RawPath != "" {
		s.refuse(w, r, refuse(http.StatusNotFound, ReasonRoute, "the box helper has no such route"))
		return
	}
	methods := allowed(name)
	if !slices.Contains(methods, r.Method) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		s.refuse(w, r, refuse(http.StatusMethodNotAllowed, ReasonMethod, "the route takes only %s", strings.Join(methods, " or ")))
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		s.refuse(w, r, refuse(http.StatusBadRequest, ReasonInput, "the box helper takes no query"))
		return
	}
	if hasBody(w, r) {
		s.refuse(w, r, refuse(http.StatusBadRequest, ReasonInput, "the box helper takes no body"))
		return
	}
	if name == routeHealth {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if refusal := keyed(r, s.cfg.KeyFile); refusal != nil {
		s.refuse(w, r, refusal)
		return
	}
	base := s.cfg.Base
	if ids := r.Header.Values(shared.RequestHeader); len(ids) == 1 {
		base = shared.WithRequest(base, ids[0])
	}
	switch name {
	case routeReviewers, routeReviewer, routeReview:
		s.reviewers(base, w, r, rt)
		return
	}
	if name != routeList && !session.ValidBox(box) {
		s.refuse(w, r, refuse(http.StatusBadRequest, ReasonInput, "a box is 16 lowercase hexadecimal digits"))
		return
	}
	limit := otherFor
	if name == routeStart {
		limit = startFor
	}
	ctx, cancel := context.WithTimeout(base, limit)
	defer cancel()
	var err error
	switch {
	case name == routeList:
		var list []session.BoxInfo
		if list, err = s.cfg.Boxes.List(ctx); err == nil {
			out := listJSON{Version: WireVersion, Boxes: []boxJSON{}}
			if s.cfg.About != nil {
				a := s.cfg.About()
				out.About = &a
			}
			for _, b := range list {
				out.Boxes = append(out.Boxes, boxOf(b))
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
	case name == routeBox && r.Method == http.MethodPut:
		if err = s.cfg.Boxes.Create(ctx, box); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case name == routeBox:
		if err = s.cfg.Boxes.Remove(ctx, box); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case name == routeStart:
		var info session.BoxInfo
		if info, err = s.cfg.Boxes.Start(ctx, box); err == nil {
			b := boxOf(info)
			writeJSON(w, http.StatusOK, answerJSON{Version: WireVersion, Box: &b})
			return
		}
	default:
		if err = s.cfg.Boxes.Stop(ctx, box); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.failed(w, r, err)
}

func (s *server) failed(w http.ResponseWriter, r *http.Request, err error) {
	var rf *Refusal
	if !errors.As(err, &rf) {
		rf = refuse(http.StatusBadGateway, ReasonDocker, "%s", oneLine(err.Error()))
	}
	s.refuse(w, r, rf)
}

func (s *server) reviewers(base context.Context, w http.ResponseWriter, r *http.Request, rt routed) {
	if s.cfg.Reviewers == nil {
		s.refuse(w, r, refuse(http.StatusNotFound, ReasonRoute, "the box helper has no such route"))
		return
	}
	if rt.name != routeReviewers {
		if err := badRun(rt.id, rt.snapshot); err != nil {
			s.failed(w, r, err)
			return
		}
	}
	limit := otherFor
	if r.Method == http.MethodPut {
		limit = startFor
	}
	ctx, cancel := context.WithTimeout(base, limit)
	defer cancel()
	switch {
	case rt.name == routeReviewers:
		list, err := s.cfg.Reviewers.ListReviewers(ctx)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		out := reviewersJSON{Version: WireVersion, Reviewers: []boxJSON{}}
		for _, b := range list {
			out.Reviewers = append(out.Reviewers, boxOf(b))
		}
		writeJSON(w, http.StatusOK, out)
	case r.Method == http.MethodDelete:
		if err := s.cfg.Reviewers.RemoveReviewer(ctx, rt.id); err != nil {
			s.failed(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		info, err := s.cfg.Reviewers.StartReviewer(ctx, rt.id, rt.snapshot)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		b := boxOf(info)
		writeJSON(w, http.StatusOK, answerJSON{Version: WireVersion, Box: &b})
	}
}

func hasBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		return true
	}
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	n, _ := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, MaxRequest))
	return n > 0
}

func (s *server) refuse(w http.ResponseWriter, r *http.Request, rf *Refusal) {
	switch rf.Reason {
	case ReasonKey, ReasonNoKey, ReasonRoute, ReasonMethod, ReasonInput, ReasonTooLarge:
		s.cfg.Logger.Warn("box helper refused a request", "reason", rf.Reason, "method", r.Method, "path", clip(r.URL.Path), "remote", r.RemoteAddr)
	}
	writeJSON(w, rf.Status, errorJSON{Version: WireVersion, Reason: rf.Reason, Message: rf.Message})
}

func clip(s string) string {
	s = oneLine(s)
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func boxOf(b session.BoxInfo) boxJSON {
	out := boxJSON{Box: b.Box, State: b.State, Recipe: b.Recipe, Host: b.Host, Facts: b.Facts}
	if b.Engine.IsValid() {
		out.Engine = b.Engine.String()
	}
	if b.Agent.IsValid() {
		out.Agent = b.Agent.String()
	}
	if b.Gateway.IsValid() {
		out.Gateway = b.Gateway.String()
	}
	if b.Subnet.IsValid() {
		out.Subnet = b.Subnet.String()
	}
	if out.Facts == nil {
		out.Facts = []string{}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"version":"1","reason":"answer","message":"the answer couldn't be written"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func keyed(r *http.Request, file string) *Refusal {
	want := keyHash(file)
	if want == nil {
		return refuse(http.StatusServiceUnavailable, ReasonNoKey, "the box helper has no key from the engine yet")
	}
	given := r.Header.Values(KeyHeader)
	if len(given) != 1 || len(given[0]) != 64 {
		return refuse(http.StatusUnauthorized, ReasonKey, "the helper key is missing")
	}
	sum := sha256.Sum256([]byte(given[0]))
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return refuse(http.StatusUnauthorized, ReasonKey, "the helper key is wrong")
	}
	return nil
}

func keyHash(file string) []byte {
	if file == "" {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFile))
	if err != nil {
		return nil
	}
	sum, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(sum) != sha256.Size {
		return nil
	}
	return sum
}
