package session

const (
	NoticeIdentity = "identity"
	NoticeLogin    = "login"
)

type Notice struct {
	ID     string
	Text   string
	Action string
}

func ReadinessNotices(identity *CommitIdentity, reviewers *SignIn) []Notice {
	out := []Notice{}
	if identity != nil && !identity.Set() {
		out = append(out, Notice{ID: NoticeIdentity, Action: "Set name and email",
			Text: "Set the name and email agents commit with. No session starts until they are set: an agent with none may commit with the email of its sign-in, which a push makes public. For GitHub, use your noreply address."})
	}
	if reviewers != nil && !reviewers.Stale && reviewers.Method == SignInNone {
		out = append(out, Notice{ID: NoticeLogin, Action: "Sign Claude Code in",
			Text: "Sign the reviewers in. Every push waits for reviews, which run on Claude Code in short-lived reviewer containers of their own, signed in separately from the agents."})
	}
	return out
}

func TokenHostNotes() map[string]string {
	return map[string]string{"https://gitlab.com": "Pull requests and issues work only on GitHub."}
}
