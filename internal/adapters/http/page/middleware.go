package page

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const contentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"

func chain(h http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for _, m := range slices.Backward(middleware) {
		h = m(h)
	}
	return h
}

func recoverPanics(logger shared.Logger, side string, route func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				logger.Error("panic recovered", "side", side, "route", route(r), "kind", fmt.Sprintf("%T", v))
				if rec.written {
					panic(http.ErrAbortHandler)
				}
				w.WriteHeader(http.StatusInternalServerError)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

func requestLogger(logger shared.Logger, side string, route func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Info("request",
				"side", side,
				"method", method(r),
				"route", route(r),
				"status", rec.status,
				"duration", time.Since(start).Round(time.Millisecond).String(),
			)
		})
	}
}

func method(r *http.Request) string {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return r.Method
	}
	return "other"
}

type recorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
