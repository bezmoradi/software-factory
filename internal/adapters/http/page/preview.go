package page

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	previewRoute    = "/api/preview"
	previewCodePath = "/.sf-preview"
	previewCookie   = "sf-preview"
	PreviewConns    = 64

	saidPreviewOpen    = "Open the preview from Software Factory's page."
	saidPreviewStopped = "The session is stopped."
)

type previewKey struct{}

func (s *Server) UsePreviews(p *session.Previews, publicPort int) {
	s.mu.Lock()
	s.previews = p
	s.previewPort = publicPort
	s.mu.Unlock()
}

func (s *Server) previewParts() (*session.Previews, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previewPort < 1 || s.previewPort > 65535 {
		return nil, 0
	}
	return s.previews, s.previewPort
}

func previewOrigin(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

func (s *Server) pageCSP() string {
	if p, port := s.previewParts(); p != nil {
		return PageCSP + "; frame-src " + previewOrigin(port)
	}
	return PageCSP
}

func previewPortOf(body map[string]any) (int, bool) {
	v, given := body["port"]
	if !given {
		return 0, true
	}
	n, ok := v.(float64)
	if !ok || n != math.Trunc(n) || n < 1 || n > 65535 {
		return 0, false
	}
	return int(n), true
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request, body map[string]any) {
	sid, okSID := viewSession(body)
	port, okPort := previewPortOf(body)
	if !keysWithin(body, "sid", "port") || !okSID || !okPort {
		refuseFields(w)
		return
	}
	p, public := s.previewParts()
	if p == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine serves no preview."})
		return
	}
	offer, err := p.Open(sid, port)
	shared.For(r.Context(), s.logger).Info("preview", "event", "open", "ok", err == nil)
	if err != nil {
		code, body := status(err)
		send(w, code, body)
		return
	}
	origin := previewOrigin(public)
	send(w, http.StatusOK, map[string]any{
		"url":   origin + previewCodePath + "?code=" + offer.Code,
		"tab":   origin + "/",
		"port":  offer.Port,
		"ports": offer.Ports,
	})
}

func previewSay(w http.ResponseWriter, frame string, status int, text string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors "+frame)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text+"\n")
}

func (s *Server) Preview() http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: previewRewrite,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     time.Minute,
			DisableCompression:  true,
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Add("Content-Security-Policy", "frame-ancestors "+s.origin)
			return nil
		},
		ErrorHandler: s.previewFailed,
	}
	return chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.previewServe(w, r, proxy) }),
		recoverPanics(s.logger, "preview", func(*http.Request) string { return "preview" }),
	)
}

func (s *Server) previewServe(w http.ResponseWriter, r *http.Request, proxy *httputil.ReverseProxy) {
	p, public := s.previewParts()
	if p == nil {
		previewSay(w, s.origin, http.StatusNotFound, "This engine serves no preview.")
		return
	}
	if r.Host != "127.0.0.1:"+strconv.Itoa(public) {
		previewSay(w, s.origin, http.StatusMisdirectedRequest, "The preview answers only at "+previewOrigin(public)+".")
		return
	}
	if r.URL.Path == previewCodePath {
		cookie, ok := p.Redeem(r.URL.Query().Get("code"))
		shared.For(r.Context(), s.logger).Info("preview", "event", "code", "ok", ok)
		if !ok {
			previewSay(w, s.origin, http.StatusForbidden, saidPreviewOpen)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: previewCookie, Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	given := []string{}
	for _, c := range r.CookiesNamed(previewCookie) {
		given = append(given, c.Value)
	}
	target := p.Target(given)
	switch {
	case !target.Allowed:
		previewSay(w, s.origin, http.StatusForbidden, saidPreviewOpen)
		return
	case target.Stopped:
		previewSay(w, s.origin, http.StatusBadGateway, saidPreviewStopped)
		return
	}
	proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), previewKey{}, target.Addr)))
}

func previewRewrite(pr *httputil.ProxyRequest) {
	addr, _ := pr.In.Context().Value(previewKey{}).(netip.AddrPort)
	pr.SetURL(&url.URL{Scheme: "http", Host: addr.String()})
	pr.Out.Host = pr.In.Host
	pr.Out.Header.Del("Cookie")
	kept := []string{}
	for _, line := range pr.In.Header.Values("Cookie") {
		for part := range strings.SplitSeq(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name != previewCookie && strings.TrimSpace(part) != "" {
				kept = append(kept, strings.TrimSpace(part))
			}
		}
	}
	if len(kept) > 0 {
		pr.Out.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

func (s *Server) previewFailed(w http.ResponseWriter, r *http.Request, err error) {
	addr, _ := r.Context().Value(previewKey{}).(netip.AddrPort)
	if op, ok := errors.AsType[*net.OpError](err); ok && op.Op == "dial" {
		previewSay(w, s.origin, http.StatusBadGateway, "Nothing answers on port "+strconv.Itoa(int(addr.Port()))+" in the session. Start the app on that port and listen on all addresses (for Vite: --host).")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	previewSay(w, s.origin, http.StatusBadGateway, "The app on port "+strconv.Itoa(int(addr.Port()))+" in the session stopped answering.")
}
