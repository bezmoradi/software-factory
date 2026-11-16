package slog

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type logger struct {
	logger *slog.Logger
	ring   *ring
}

var _ shared.Recent = logger{}

func New(fileName ...string) shared.Logger {
	out := os.Stdout
	if len(fileName) > 0 {
		file, err := os.OpenFile(fileName[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			panic(err)
		}
		out = file
	}
	r := &ring{}
	return logger{logger: slog.New(kept{Handler: slog.NewTextHandler(out, nil), ring: r}), ring: r}
}

func (l logger) Info(message string, args ...any) {
	l.logger.Info(message, args...)
}

func (l logger) Warn(message string, args ...any) {
	l.logger.Warn(message, args...)
}

func (l logger) Error(message string, args ...any) {
	l.logger.Error(message, args...)
}

func (l logger) Fatal(err error) {
	l.logger.Error(err.Error())
	os.Exit(1)
}

func (l logger) Recent() []shared.Event {
	return l.ring.list()
}

type ring struct {
	mu     sync.Mutex
	events []shared.Event
	next   int
}

func (r *ring) add(e shared.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) < shared.MaxEvents {
		r.events = append(r.events, e)
		return
	}
	r.events[r.next] = e
	r.next = (r.next + 1) % shared.MaxEvents
}

func (r *ring) list() []shared.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]shared.Event, 0, len(r.events))
	out = append(out, r.events[r.next:]...)
	return append(out, r.events[:r.next]...)
}

type kept struct {
	slog.Handler
	ring *ring
}

func (k kept) Handle(ctx context.Context, rec slog.Record) error {
	if e, ok := eventOf(rec); ok {
		k.ring.add(e)
	}
	return k.Handler.Handle(ctx, rec)
}

func (k kept) WithAttrs(attrs []slog.Attr) slog.Handler {
	return kept{Handler: k.Handler.WithAttrs(attrs), ring: k.ring}
}

func (k kept) WithGroup(name string) slog.Handler {
	return kept{Handler: k.Handler.WithGroup(name), ring: k.ring}
}

var (
	partWord = regexp.MustCompile(`^[a-z][a-z0-9 -]{0,47}$`)
	kindWord = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
)

func eventOf(rec slog.Record) (shared.Event, bool) {
	e := shared.Event{Time: rec.Time.UTC(), Level: strings.ToLower(rec.Level.String()), Part: "other"}
	if partWord.MatchString(rec.Message) {
		e.Part = rec.Message
	}
	route, status := "", 0
	rec.Attrs(func(a slog.Attr) bool {
		v := a.Value.Resolve()
		switch a.Key {
		case "event":
			e.Kind = word(v.String())
		case "why", "reason":
			if e.Error == "" {
				e.Error = word(v.String())
			}
		case shared.RequestKey:
			if id := v.String(); shared.ValidRequest(id) {
				e.Req = id
			}
		case "route":
			route = v.String()
		case "status":
			if v.Kind() == slog.KindInt64 {
				status = int(v.Int64())
			}
		}
		return true
	})
	if rec.Message == "request" {
		if status > 0 && status < 400 && rec.Level < slog.LevelWarn {
			return shared.Event{}, false
		}
		if e.Kind == "" {
			e.Kind = word(strings.TrimPrefix(route, "/api/"))
		}
		if e.Error == "" && status >= 400 {
			e.Error = strconv.Itoa(status)
		}
	}
	return e, true
}

func word(s string) string {
	if kindWord.MatchString(s) {
		return s
	}
	if s == "" {
		return ""
	}
	return "other"
}
