package page

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
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
	Field   string     `json:"field,omitempty"`
	Message string     `json:"message,omitempty"`
	Wait    int        `json:"wait,omitempty"`
	Needs   *needsJSON `json:"needs,omitempty"`
	Line    string     `json:"line,omitempty"`
}

func refuse(w http.ResponseWriter, status int, reason, message string) {
	send(w, status, problem{Error: reason, Message: message})
}

func agentRefuse(w http.ResponseWriter, status int, reason string) {
	send(w, status, problem{Error: reason})
}

const (
	saidRoute   = "The engine has no such route."
	saidMethod  = "The engine doesn't take that method on this route."
	saidType    = "The engine takes this request as JSON only, sent with Content-Type application/json."
	saidHost    = "The engine answers only at the address it serves the page on. Open the page at that address."
	saidOrigin  = "The engine takes requests only from its own page. Open the page at its own address."
	saidSite    = "The engine takes requests only from its own page, not from another site."
	saidQuery   = "The engine's routes take no query string."
	saidFields  = "The request holds a field this route doesn't take, or a field of the wrong type. Reload the page and try again."
	saidServer  = "Something went wrong in the engine, and its log has the details. Try again."
	saidKey     = "The page's key is missing or isn't one the engine issued. Reload the page to sign in."
	saidOp      = "That op isn't one this route takes. Reload the page and try again."
	saidOffer   = "That isn't a choice the engine offered for this session's folder. Reload the page and try again."
	saidMessage = "The message's parts aren't ones the engine reads. Reload the page and try again."
)

func bodyRefused(route string, status int, limit int64) string {
	if status == http.StatusRequestEntityTooLarge {
		switch route {
		case packsRoute:
			return "The engine refused the upload: it is over 2 MiB in all, the most a request to it carries."
		case reviewerAgentsRoute:
			return "The engine refused this agent file: it is over 1 MiB, the most a request to it carries."
		}
	}
	return bodySaid(status, limit)
}

func bodySaid(status int, limit int64) string {
	if status == http.StatusRequestEntityTooLarge {
		return "The request is over " + sizeWords(limit) + ", the most the engine takes on this route."
	}
	return "The request's body isn't one JSON object. Reload the page and try again."
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func refuseFields(w http.ResponseWriter) {
	refuse(w, http.StatusBadRequest, "fields", saidFields)
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

func keyEnded(why string) string {
	if why == "replaced" {
		return "The page signed in again in another tab or browser, which ended this tab's key. Reload the page to use it here."
	}
	return "The page's key ended after a time without use. Reload the page to sign in again."
}
