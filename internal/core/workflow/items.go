package workflow

type Flag struct {
	Path    string
	Why     string
	Allowed bool
	Fixed   bool
}

type Progress struct {
	Chosen          Profile
	Effective       WorkType
	Adapter         AdapterID
	Commands        map[Step]string
	FilesListed     bool
	FilesNote       string
	FlagsRead       bool
	Flagged         []Flag
	ReqOk           bool
	ReqWasApproved  bool
	PlanFile        string
	PlanOk          bool
	PlanWasApproved bool
	PlanBase        string
	Built           bool
}

func Items(p Progress) ([]string, bool) {
	var out []string
	unapproved := false
	say := func(s string) { out = append(out, s) }
	ask := func(s string) {
		say(s)
		unapproved = true
	}
	feature := p.Effective.Profile() == Feature
	if !p.FilesListed {
		note := ""
		if p.FilesNote != "" {
			note = " (" + p.FilesNote + ")"
		}
		say("the list of files this branch changes, which the gate couldn't read in full" + note + ", so the developer has to check which reviewers they call for and allow the push")
	}
	if !p.FlagsRead {
		say("a check of this branch's files for secrets, which the gate couldn't finish, so the developer has to check them and allow the push")
	}
	for _, f := range p.Flagged {
		switch {
		case f.Allowed:
		case f.Fixed:
			say(clip(f.Path, 200) + " out of every commit on the branch that holds it, because " + f.Why + "; removing it in a new commit still pushes the earlier ones")
		default:
			say("the developer's decision on " + clip(f.Path, 200) + ", because " + f.Why + ": they allow it on the page, or it comes out of every commit on the branch that holds it, since removing it in a new commit still pushes the earlier ones")
		}
	}
	if p.Chosen == Feature && !p.ReqOk {
		if p.ReqWasApproved {
			ask("the developer's approval of the requirements again, since they changed after they approved them")
		} else {
			ask("the developer's approval of the requirements, on the page")
		}
	}
	if p.PlanBase != "" && !p.Built {
		say("the work committed on top of commit " + p.PlanBase[:min(len(p.PlanBase), 7)] + ", where the plan was approved")
	}
	commands := Adapters[Knackpack].Commands
	if a, ok := Adapters[p.Adapter]; ok {
		commands = a.Commands
	}
	if p.Commands != nil {
		commands = p.Commands
	}
	switch {
	case feature && p.PlanFile == "":
		from := commands[Plan]
		if from == "" {
			from = "the Plan step"
		}
		say("a plan from " + from + ", which only the developer can start")
	case feature && !p.PlanOk:
		switch {
		case p.PlanWasApproved:
			ask("the developer's approval of the plan again, since it changed after they approved it")
		case p.Chosen == Feature && !p.ReqOk:
			ask("the developer's approval of the plan, after they approve the requirements")
		default:
			ask("the developer's approval of the plan, on the page")
		}
	}
	return out, unapproved
}
