package review

import "strconv"

type Key struct {
	Commit string
	Branch string
	Agent  string
	Run    int
}

const MaxRuns = 3

func (k Key) Number() int {
	return max(k.Run, 1)
}

func (k Key) AtRun(n int) Key {
	k.Run = 0
	if n > 1 {
		k.Run = n
	}
	return k
}

func (k Key) Runs(n int) []Key {
	n = min(max(n, 1), MaxRuns)
	out := make([]Key, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, k.AtRun(i))
	}
	return out
}

func (k Key) SameRole(o Key) bool {
	return k.Commit == o.Commit && k.Branch == o.Branch && k.Agent == o.Agent
}

type Record struct {
	Provider string
	State    State
	Verdict  string
	Channel  Channel
	Override string
	Shaped   string
	Held     string
	Advisory bool
}

func Pooled(records map[Key]Record, k Key, runs int, offer *Offer) (string, Key) {
	return pooled(records, k, runs, offer, Problem)
}

func PooledForAgent(records map[Key]Record, k Key, runs int, offer *Offer) (string, Key) {
	return pooled(records, k, runs, offer, AgentProblem)
}

func pooled(records map[Key]Record, k Key, runs int, offer *Offer, problem func(map[Key]Record, Key, *Offer) string) (string, Key) {
	n := min(max(runs, 1), MaxRuns)
	for run := 1; run <= MaxRuns; run++ {
		rk := k.AtRun(run)
		if run > n {
			r, ok := records[rk]
			if !ok || r.State != Block || r.Override != "" {
				continue
			}
		}
		why := problem(records, rk, offer)
		if why == "" {
			if r, ok := records[rk]; ok && r.Advisory && r.Override == "" {
				why = "a review by " + k.Agent + " that counts: its review of commit " + short(k.Commit) + " ran as an advisory role, which never counts for the gate; press Review"
			}
		}
		if why != "" {
			if n > 1 || run > 1 {
				why += " (run " + strconv.Itoa(run) + " of " + strconv.Itoa(max(n, run)) + ")"
			}
			return why, rk
		}
	}
	return "", k.AtRun(1)
}

func Outcome(records map[Key]Record, k Key, runs int) (recorded, passed, overridden bool) {
	recorded, passed = true, true
	for _, rk := range k.Runs(runs) {
		r, ok := records[rk]
		if !ok {
			recorded, passed = false, false
			continue
		}
		if r.State != Pass {
			passed = false
		}
		if r.Override != "" {
			overridden = true
		}
	}
	return recorded, passed, overridden && !passed
}

func Problem(records map[Key]Record, k Key, offer *Offer) string {
	return missingItem(records, k, offer, true)
}

func AgentProblem(records map[Key]Record, k Key, offer *Offer) string {
	return missingItem(records, k, offer, false)
}

func missingItem(records map[Key]Record, k Key, offer *Offer, withVerdict bool) string {
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
		if !withVerdict {
			return "an override or a fix for the block from " + k.Agent
		}
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

var versionHeld = map[Channel]bool{BelowMinimum: true, Unchecked: true, ApprovalChanged: true}

var heldBy = map[Channel]string{
	Shallow:    "it passed in one turn without reading a file, which counts as no verdict: press Review again",
	DeniedRead: "it tried to read outside its review folder, so the code may have steered it, which counts as no verdict: read its reply, then press Review again or decide yourself",
	SeverePass: "it passed while its own report lists a Critical, High or blocking finding, which counts as no verdict: read the finding, then have the agent fix it, press Review again or allow the push with your reason",
	Withheld:   "its reply held a token-shaped string, so the engine didn't keep it, which counts as no verdict: press Review again",
}
