package session

import (
	"errors"
	"slices"
	"unicode/utf8"
)

type Kind int

const (
	Invalid Kind = iota + 1
	Conflict
	NotFound
	Limited
	Full
	Unauthorized
	Gone
)

type Error struct {
	Kind    Kind
	Reason  string
	Message string
	Field   string
	Needs   *Needs
	Line    string
}

func (e *Error) Error() string { return e.Reason }

func invalid(reason, message string) *Error {
	return &Error{Kind: Invalid, Reason: reason, Message: message}
}
func conflict(reason, message string) *Error {
	return &Error{Kind: Conflict, Reason: reason, Message: message}
}
func notFound(reason, message string) *Error {
	return &Error{Kind: NotFound, Reason: reason, Message: message}
}
func limited(reason, message string) *Error {
	return &Error{Kind: Limited, Reason: reason, Message: message}
}
func full(reason, message string) *Error { return &Error{Kind: Full, Reason: reason, Message: message} }

func unauthorized() error                     { return &Error{Kind: Unauthorized, Reason: "connection"} }
func toAgent(kind Kind, reason string) *Error { return &Error{Kind: kind, Reason: reason} }
func explained(e *Error, message string) *Error {
	return &Error{Kind: e.Kind, Reason: e.Reason, Message: message}
}

const maxFieldMessage = 60

func withName(before, name, after string) string {
	room := maxFieldMessage - utf8.RuneCountInString(before) - utf8.RuneCountInString(after)
	if r := []rune(name); len(r) > room {
		name = string(r[:max(room-1, 0)]) + "…"
	}
	return before + name + after
}

func onField(field string, err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	named := *e
	named.Field = field
	return &named
}

var startFields = []string{"name", "from", "issue"}

func onStartField(err error) error {
	var e *Error
	if !errors.As(err, &e) || e.Field != "" || !slices.Contains(startFields, e.Reason) {
		return err
	}
	return onField(e.Reason, err)
}
