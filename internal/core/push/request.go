package push

import "context"

type Decision string

const (
	DecisionPushed  Decision = "pushed"
	DecisionHeld    Decision = "held"
	DecisionDenied  Decision = "denied"
	DecisionRefused Decision = "refused"
)

type Reply struct {
	Decision Decision
	Reason   string
}

type Requests interface {
	Request(ctx context.Context, sid string) Reply
}

const NotWiredReason = "pushing isn't wired yet"

type notWired struct{}

func NotWired() Requests { return notWired{} }

func (notWired) Request(context.Context, string) Reply {
	return Reply{Decision: DecisionHeld, Reason: NotWiredReason}
}
