package session

const (
	FilterRunning = "running"
	FilterWaiting = "waiting"
	FilterHeld    = "held"
	FilterStopped = "stopped"
	FilterDone    = "done"
)

func SessionFilters(i Info) []string {
	running := i.Running || i.Live && !i.Verified
	out := []string{}
	if running {
		out = append(out, FilterRunning)
	}
	if i.Needs > 0 || i.SignInWait || i.HomeHeld {
		out = append(out, FilterWaiting)
	}
	if i.Push != nil && i.Push.Result == "held" && !i.Push.Running {
		out = append(out, FilterHeld)
	}
	switch {
	case running:
	case i.State == "done":
		out = append(out, FilterDone)
	default:
		out = append(out, FilterStopped)
	}
	return out
}

func StartPoint(i Info) bool {
	return i.Verified && i.Kind == "background"
}
