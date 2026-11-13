package session

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
	Needs   *Needs
}

func (e *Error) Error() string { return e.Reason }

func invalid(reason string) *Error  { return &Error{Kind: Invalid, Reason: reason} }
func conflict(reason string) *Error { return &Error{Kind: Conflict, Reason: reason} }
func notFound(reason string) *Error { return &Error{Kind: NotFound, Reason: reason} }
func limited(reason string) *Error  { return &Error{Kind: Limited, Reason: reason} }
func full(reason string) *Error     { return &Error{Kind: Full, Reason: reason} }
func unauthorized() error           { return &Error{Kind: Unauthorized, Reason: "connection"} }
func explained(e *Error, message string) *Error {
	return &Error{Kind: e.Kind, Reason: e.Reason, Message: message}
}
