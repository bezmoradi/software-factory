package push

import "context"

type Target struct {
	URL    string
	Token  string
	Branch string
}

type RemoteState struct {
	Empty        bool
	Main         string
	MainCommit   string
	BranchCommit string
}

type Outcome string

const (
	Pushed   Outcome = "pushed"
	Rejected Outcome = "rejected"
	Failed   Outcome = "failed"
	Unclear  Outcome = "unclear"
)

type Request struct {
	Repo     string
	Target   Target
	Commit   string
	Expected string
	Force    bool
}

type Result struct {
	Outcome  Outcome
	Reason   string
	Previous string
}

type Pusher interface {
	Remote(ctx context.Context, target Target) (RemoteState, error)
	Push(ctx context.Context, req Request) Result
}
