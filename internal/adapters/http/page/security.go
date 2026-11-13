package page

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

const PageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"

func pageHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", PageCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func agentHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func send(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

type problem struct {
	Error   string     `json:"error"`
	Message string     `json:"message,omitempty"`
	Wait    int        `json:"wait,omitempty"`
	Needs   *needsJSON `json:"needs,omitempty"`
}

func refuse(w http.ResponseWriter, status int, reason string) {
	send(w, status, problem{Error: reason})
}

func isJSON(r *http.Request) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && kind == "application/json"
}

var errBody = errors.New("body")

func readObject(w http.ResponseWriter, r *http.Request, limit int64) (map[string]any, int) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return nil, http.StatusRequestEntityTooLarge
		}
		return nil, http.StatusBadRequest
	}
	var v any
	if err := strictDecode(data, &v); err != nil {
		return nil, http.StatusBadRequest
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, http.StatusBadRequest
	}
	return obj, 0
}

func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errBody
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errBody
	}
	return nil
}

func str(v any, max int) string {
	s, _ := v.(string)
	return clip(s, max)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func integer(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) || f < -(1<<53) || f > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

func browserHeaders(r *http.Request) bool {
	for _, h := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Referer"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}
