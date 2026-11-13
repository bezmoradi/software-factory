package review

type Key struct {
	Commit string
	Branch string
	Agent  string
}

type Record struct {
	Provider string
	State    State
	Verdict  string
	Channel  Channel
	Override string
	Shaped   string
	Held     string
}

func Problem(records map[Key]Record, k Key, offer *Offer) string {
	r, ok := records[k]
	if !ok {
		return "a review by " + k.Agent + " of commit " + short(k.Commit) + " on " + k.Branch
	}
	if r.State == Running {
		return k.Agent + " to finish"
	}
	if why := RoleProblem(k.Agent, spawned(k.Agent, r.Provider, offer)); why != "" {
		return "a review by " + k.Agent + " started from a provider the gate trusts: " + why
	}
	if r.Override != "" {
		return ""
	}
	switch r.State {
	case Pass:
		return ""
	case Block:
		return "an override or a fix for the verdict \"" + r.Verdict + "\" from " + k.Agent
	case Unknown:
		if versionHeld[r.Channel] {
			return "a review by " + k.Agent + " that counts: " + r.Held
		}
		if r.Channel == Conflict {
			return "a review by " + k.Agent + " whose verdict lines agree: its reply gave both pass and block, so it counts as no verdict"
		}
		if r.Channel == ShapedPass {
			why := r.Shaped
			if why == "" {
				why = "the change adds a line shaped like a reviewer's verdict"
			}
			return "a review by " + k.Agent + " that counts: " + why + ", so its pass counts as no verdict; check the change yourself, then allow the push with your reason, or have the agent change it and review again"
		}
		if why, ok := heldBy[r.Channel]; ok {
			return "a review by " + k.Agent + " that counts (" + why + ")"
		}
		if RuleFor(k.Agent) != nil {
			return "a verdict the gate recognizes from " + k.Agent + ": it finished, but its reply had no verdict the gate knows"
		}
		return "a line \"Verdict: pass\" or \"Verdict: block\" among the last three of " + k.Agent + "'s reply: it finished without one"
	}
	return "a review by " + k.Agent + " that finishes: the last one failed or timed out, so it counts as not passed"
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

var versionHeld = map[Channel]bool{BelowMinimum: true, Unchecked: true, Checking: true, CheckFailed: true}

var heldBy = map[Channel]string{
	Shallow:    "it passed in one turn without reading a file, which counts as no verdict: press Review again",
	DeniedRead: "it tried to read outside its review folder, so the code may have steered it, which counts as no verdict: read its reply, then press Review again or decide yourself",
	SeverePass: "it passed while its own report lists a Critical, High or blocking finding, which counts as no verdict: read the finding, then have the agent fix it, press Review again or allow the push with your reason",
	Withheld:   "its reply held a token-shaped string, so the engine didn't keep it, which counts as no verdict: press Review again",
}
