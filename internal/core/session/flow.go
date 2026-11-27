package session

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type FlowLook struct {
	Icon string
	Tone string
	Word string
}

type FlowNode struct {
	Name  string
	Phase string
	Look  FlowLook
	Aside bool
	Tags  []Said
}

type FlowRoute struct {
	Label   string
	Look    FlowLook
	Taken   bool
	Decided bool
	Note    string
}

type FlowZone struct {
	Kind      string
	Name      string
	Note      string
	Untrusted bool
}

type FlowWire struct {
	Label string
	Phase string
	Tone  string
}

type FlowKey struct {
	Swatch string
	Words  string
}

type FlowModel struct {
	Commit        string
	Reviews       []FlowNode
	Runs          []FlowNode
	Gate          FlowNode
	Routes        []FlowRoute
	RoutesLabel   string
	Wires         []FlowWire
	Zones         []FlowZone
	Key           []FlowKey
	Status        FlowLook
	Detail        string
	Expires       time.Time
	ExpiredNote   string
	ExpiredStatus FlowLook
}

type flowReviewer struct {
	name     string
	state    string
	blocking int
	advisory bool
	required bool
	tags     []string
}

type flowRun struct {
	kind  string
	state string
}

type flowGate struct {
	state    string
	decision string
}

type flowInput struct {
	commit    string
	reviewers []flowReviewer
	looked    bool
	runs      []flowRun
	gate      flowGate
	outcome   string
	note      string
}

type PushLook struct {
	Icon  string
	Tone  string
	Label string
	Lead  string
}

var pushLooks = map[string]PushLook{
	"pushed":     {"check", "ok", "Pushed", "Pushed to"},
	"up-to-date": {"check", "ok", "Pushed", "Pushed to"},
	"held":       {"warning", "warn", "Push held", "Push held for"},
	"denied":     {"error", "bad", "Push denied", "Push denied for"},
	"refused":    {"error", "bad", "Not pushed", "Not pushed to"},
	"unclear":    {"warning", "warn", "Push outcome unclear", "Push outcome unclear for"},
}

func PushResultLook(result string) PushLook {
	if l, ok := pushLooks[result]; ok {
		return l
	}
	return PushLook{"warning", "warn", "Push", "Push to"}
}

var flowRoutes = []string{"pushed", "held", "denied", "refused"}

var flowDone = []string{"pass", "block", "overridden", "set-aside", "unknown"}

var flowZones = []FlowZone{
	{"session", "Session's container", "untrusted: the agent's branch", true},
	{"review", "Reviewer containers", "one per run, on a read-only snapshot", false},
	{"verify", "Verification containers", "untrusted: no network; only the exit code counts", true},
	{"engine", "Engine", "decides; never takes the agent's word", false},
	{"git", "Git host", "only the engine pushes; held or denied, nothing is pushed", false},
}

var flowKey = []FlowKey{
	{"solid", "Solid: done or taken"},
	{"dashed", "Dashed: waiting, not taken or not counted"},
	{"moving", "Moving dashes: running"},
	{"ok", "Green: passed, allowed or pushed"},
	{"warn", "Amber: held, overridden or no verdict"},
	{"bad", "Red: blocked, failed, denied or refused"},
	{"untrusted", "Dashed zone: untrusted, where the agent's code runs"},
}

func countWord(n int, one, many string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + one
	}
	return strconv.Itoa(n) + " " + many
}

func reviewerLook(r flowReviewer) FlowLook {
	switch r.state {
	case "pending":
		return FlowLook{"more", "", "waiting"}
	case "running":
		return FlowLook{"", "", "running"}
	case "pass":
		return FlowLook{"check", "ok", "passed"}
	case "block":
		tone := "bad"
		if r.advisory || !r.required {
			tone = "warn"
		}
		return FlowLook{"error", tone, "blocked"}
	case "overridden":
		return FlowLook{"warning", "warn", "overridden"}
	case "set-aside":
		return FlowLook{"warning", "warn", "set aside"}
	case "unknown":
		return FlowLook{"warning", "warn", "no verdict"}
	case "failed":
		return FlowLook{"error", "bad", "didn't finish"}
	}
	return FlowLook{"info", "", "no review"}
}

func runLook(state string) FlowLook {
	switch state {
	case "pending":
		return FlowLook{"more", "", "waiting"}
	case "running":
		return FlowLook{"", "", "running"}
	case "passed":
		return FlowLook{"check", "ok", "passed"}
	case "failed":
		return FlowLook{"error", "bad", "failed"}
	}
	return FlowLook{"warning", "warn", state}
}

func gateLook(g flowGate) FlowLook {
	or := func(fallback string) string { return cmpOr(g.decision, fallback) }
	switch g.state {
	case "waiting":
		return FlowLook{"more", "", or("waiting for reviews")}
	case "deciding":
		return FlowLook{"", "", or("deciding")}
	case "ready":
		return FlowLook{"check", "ok", or("would allow a push")}
	case "holding":
		return FlowLook{"warning", "warn", or("would hold the push")}
	}
	return FlowLook{"", "", g.decision}
}

func flowPhase(state string) string {
	if state == "pending" || state == "running" {
		return state
	}
	return ""
}

func taken(outcome string) string {
	if outcome == "up-to-date" {
		return "pushed"
	}
	return outcome
}

func routeLabel(key, outcome string) string {
	shown := key
	if key == taken(outcome) {
		shown = outcome
	}
	if l, ok := pushLooks[shown]; ok {
		return l.Label
	}
	return shown
}

func tally(words []string) string {
	var order []string
	seen := map[string]int{}
	for _, w := range words {
		if seen[w] == 0 {
			order = append(order, w)
		}
		seen[w]++
	}
	parts := make([]string, 0, len(order))
	for _, w := range order {
		parts = append(parts, strconv.Itoa(seen[w])+" "+w)
	}
	return strings.Join(parts, ", ")
}

func flowStatus(m flowInput, note string) FlowLook {
	if m.outcome == "" {
		g := gateLook(m.gate)
		return FlowLook{g.Icon, g.Tone, upperFirst(g.Word) + "; no push decided yet."}
	}
	l := PushResultLook(m.outcome)
	extra := ""
	if note != "" {
		extra = ", " + note
	}
	return FlowLook{l.Icon, l.Tone, routeLabel(taken(m.outcome), m.outcome) + extra + "."}
}

func flowDetail(m flowInput) string {
	var said []string
	if m.commit != "" {
		said = append(said, "Commit "+shortSHA7(m.commit)+".")
	}
	if !m.looked {
		said = append(said, "The engine decided before it looked at the reviews.")
	} else {
		var counted, others []string
		for _, r := range m.reviewers {
			if r.required {
				counted = append(counted, reviewerLook(r).Word)
			} else {
				others = append(others, reviewerLook(r).Word)
			}
		}
		if len(counted) > 0 {
			said = append(said, countWord(len(counted), "required reviewer", "required reviewers")+": "+tally(counted)+".")
		}
		if len(others) > 0 {
			said = append(said, countWord(len(others), "reviewer that doesn't count", "reviewers that don't count")+": "+tally(others)+".")
		}
		if len(m.reviewers) == 0 {
			said = append(said, "No reviewer.")
		}
	}
	if len(m.runs) > 0 {
		words := make([]string, 0, len(m.runs))
		for _, r := range m.runs {
			words = append(words, runLook(r.state).Word)
		}
		said = append(said, countWord(len(m.runs), "verification run", "verification runs")+": "+tally(words)+".")
	}
	return strings.Join(said, " ")
}

func flowRoutesOf(m flowInput, note string) []FlowRoute {
	keys := flowRoutes
	if m.outcome != "" && !slices.Contains(flowRoutes, taken(m.outcome)) {
		keys = append(slices.Clone(flowRoutes), taken(m.outcome))
	}
	decided := m.outcome != ""
	out := make([]FlowRoute, 0, len(keys))
	for _, k := range keys {
		on := decided && k == taken(m.outcome)
		lookKey := k
		if on {
			lookKey = m.outcome
		}
		l, ok := pushLooks[lookKey]
		if !ok {
			l = PushLook{Icon: "warning", Tone: "warn"}
		}
		look := FlowLook{Icon: l.Icon}
		if on {
			look.Tone = l.Tone
		}
		switch {
		case on:
			look.Word = "taken"
		case decided:
			look.Word = "not taken"
		}
		r := FlowRoute{Label: routeLabel(k, m.outcome), Look: look, Taken: on, Decided: decided}
		if on {
			r.Note = note
		}
		out = append(out, r)
	}
	return out
}

func flowOutLabel(m flowInput) string {
	var said []string
	switch {
	case len(m.reviewers) == 1:
		said = append(said, "verdict")
	case len(m.reviewers) > 1:
		said = append(said, "verdicts")
	}
	switch {
	case len(m.runs) == 1:
		said = append(said, "exit code")
	case len(m.runs) > 1:
		said = append(said, "exit codes")
	}
	return strings.Join(said, ", ")
}

func waiting(state string) bool {
	return state == "pending" || state == "running"
}

func verdictTone(m flowInput) string {
	tone := ""
	for _, r := range m.reviewers {
		if !r.required {
			continue
		}
		switch r.state {
		case "block", "failed":
			return "bad"
		case "pass":
			tone = cmpOr(tone, "ok")
		default:
			tone = "warn"
		}
	}
	for _, r := range m.runs {
		switch r.state {
		case "failed":
			return "bad"
		case "passed":
			tone = cmpOr(tone, "ok")
		default:
			tone = "warn"
		}
	}
	return tone
}

func flowWires(m flowInput) []FlowWire {
	snapshot := FlowWire{}
	started := false
	for _, r := range m.reviewers {
		started = started || r.state != "pending"
	}
	for _, r := range m.runs {
		started = started || r.state != "pending"
	}
	if len(m.reviewers)+len(m.runs) > 0 {
		snapshot.Label = "snapshot"
		if !started {
			snapshot.Phase = "pending"
		}
	}
	verdicts := FlowWire{Label: flowOutLabel(m)}
	open := slices.ContainsFunc(m.reviewers, func(r flowReviewer) bool { return r.required && waiting(r.state) }) || slices.ContainsFunc(m.runs, func(r flowRun) bool { return waiting(r.state) })
	switch {
	case m.gate.state == "deciding":
		verdicts.Phase = "running"
	case open:
		verdicts.Phase = "pending"
	default:
		verdicts.Tone = verdictTone(m)
	}
	decision := FlowWire{Label: "decision", Phase: "pending"}
	if m.outcome != "" {
		decision.Phase, decision.Tone = "", PushResultLook(m.outcome).Tone
	}
	return []FlowWire{snapshot, verdicts, decision}
}

func flowOf(m flowInput) FlowModel {
	out := FlowModel{Commit: shortSHA7(m.commit), Zones: flowZones, Key: flowKey, Reviews: []FlowNode{}, Runs: []FlowNode{}}
	if !m.looked {
		out.Reviews = append(out.Reviews, FlowNode{Name: "Reviews", Look: FlowLook{"info", "", "not looked at"}, Aside: true, Tags: []Said{}})
	}
	for _, r := range m.reviewers {
		n := FlowNode{Name: r.name, Phase: flowPhase(r.state), Look: reviewerLook(r), Aside: !r.required, Tags: []Said{}}
		if r.blocking > 0 && slices.Contains(flowDone, r.state) {
			tone := "warn"
			if r.required && r.state == "block" {
				tone = "bad"
			}
			n.Tags = append(n.Tags, Said{strconv.Itoa(r.blocking) + " blocking", tone})
		}
		if !r.required {
			word := "not required"
			if r.advisory {
				word = "advisory"
			}
			n.Tags = append(n.Tags, Said{word, ""})
		}
		for _, t := range r.tags {
			n.Tags = append(n.Tags, Said{t, "warn"})
		}
		out.Reviews = append(out.Reviews, n)
	}
	if len(out.Reviews) == 0 {
		out.Reviews = append(out.Reviews, FlowNode{Name: "No reviewer", Look: FlowLook{"info", "", "none required"}, Aside: true, Tags: []Said{}})
	}
	for _, r := range m.runs {
		out.Runs = append(out.Runs, FlowNode{Name: "Verification run (" + r.kind + ")", Phase: flowPhase(r.state), Look: runLook(r.state), Tags: []Said{}})
	}
	gatePhase := ""
	switch m.gate.state {
	case "waiting":
		gatePhase = "pending"
	case "deciding":
		gatePhase = "running"
	}
	out.Gate = FlowNode{Name: "Gate", Phase: gatePhase, Look: gateLook(m.gate), Tags: []Said{}}
	out.Routes = flowRoutesOf(m, m.note)
	out.RoutesLabel = "The gate's routes, none taken yet"
	if m.outcome != "" {
		out.RoutesLabel = "The gate's routes"
		l := PushResultLook(m.outcome)
		out.Gate.Look.Icon, out.Gate.Look.Tone = l.Icon, l.Tone
	}
	out.Wires = flowWires(m)
	out.Status = flowStatus(m, m.note)
	out.Detail = flowDetail(m)
	return out
}

func lowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

func CardFlow(p *PushView) *FlowModel {
	if p == nil || p.Running || p.Result == "" {
		return nil
	}
	label := PushResultLook(p.Result).Label
	if _, ok := pushLooks[p.Result]; !ok {
		label = p.Result
	}
	in := flowInput{commit: p.Commit, gate: flowGate{state: "decided", decision: lowerFirst(label)}, outcome: p.Result}
	if c := p.CheckedBy; c != nil {
		in.looked = true
		for _, r := range c.Reviewers {
			state := "none"
			switch r.State {
			case "pass", "failed", "running":
				state = r.State
			case "block":
				state = "block"
				if r.Overridden {
					state = "overridden"
				}
			case "unknown":
				state = "unknown"
				if r.SetAside != "" {
					state = "set-aside"
				}
			}
			var tags []string
			if r.Required && !r.Counted && r.State == "pass" {
				tags = append(tags, "doesn't count")
			}
			if r.Unchecked {
				tags = append(tags, "not rechecked")
			}
			in.reviewers = append(in.reviewers, flowReviewer{name: cmpOr(r.Name, r.Agent), state: state, blocking: max(0, r.Blocking), advisory: r.Advisory, required: r.Required, tags: tags})
		}
		for _, r := range c.Runs {
			in.runs = append(in.runs, flowRun{kind: r.Kind, state: r.State})
		}
	}
	switch {
	case p.Result == "up-to-date":
		in.note = "already on the remote"
	case p.Result == "held" && p.Card != "":
		in.note = "waiting for you"
	}
	m := flowOf(in)
	if p.Result == "held" && p.Card != "" && !p.Expires.IsZero() {
		m.Expires = p.Expires
		m.ExpiredNote = "card expired"
		m.ExpiredStatus = flowStatus(in, m.ExpiredNote)
	}
	return &m
}

func severeTags(role *RoleReview) []string {
	if role == nil || role.State != "pass" && role.State != "block" && role.State != "unknown" {
		return nil
	}
	n := 0
	for _, f := range role.Findings {
		if f.Severity == "Critical" || f.Severity == "High" {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return []string{strconv.Itoa(n) + " critical or high"}
}

func roleFlowState(role RoleReview) string {
	if role.State == "block" && role.Override != "" {
		return "overridden"
	}
	return string(role.State)
}

func gateFlowState(g WorkReviewer, role *RoleReview) string {
	if role != nil && (g.State == "none" || g.State == "stale" || g.State == "running") {
		return roleFlowState(*role)
	}
	switch g.State {
	case "none", "stale":
		return "pending"
	case "unknown":
		if g.SetAside != "" {
			return "set-aside"
		}
		return "unknown"
	case "unclean", "untrusted":
		return "unknown"
	case "not-installed":
		return "none"
	}
	return g.State
}

func LiveFlow(i Info, v GateView, pushed bool) *FlowModel {
	r := i.Review
	if r != nil && r.Commit == "" {
		r = nil
	}
	head := v.Head
	if head == "" && r != nil {
		head = r.Commit
	}
	var roles []RoleReview
	if r != nil && r.Commit == head {
		roles = r.Roles
	}
	if head == "" || len(v.Reviewers) == 0 && len(roles) == 0 {
		return nil
	}
	used := make([]bool, len(roles))
	in := flowInput{commit: head, looked: true}
	for _, g := range v.Reviewers {
		var role *RoleReview
		for k := range roles {
			if roles[k].Agent == g.Agent && !used[k] {
				used[k], role = true, &roles[k]
				break
			}
		}
		tags := severeTags(role)
		switch g.State {
		case "not-installed":
			tags = append(tags, "not installed")
		case "unclean", "untrusted":
			tags = append(tags, g.State)
		}
		in.reviewers = append(in.reviewers, flowReviewer{name: cmpOr(g.Name, g.Agent), state: gateFlowState(g, role), required: true, tags: tags})
	}
	for k, role := range roles {
		if !used[k] {
			in.reviewers = append(in.reviewers, flowReviewer{name: cmpOr(role.Name, role.Agent), state: roleFlowState(role), advisory: role.Advisory, tags: severeTags(&roles[k])})
		}
	}
	open := slices.ContainsFunc(in.reviewers, func(x flowReviewer) bool { return x.required && (x.state == "pending" || x.state == "running") })
	pushing := i.Push != nil && i.Push.Running
	switch {
	case pushed:
		in.gate = flowGate{"decided", "pushed"}
		in.outcome, in.note = "pushed", "this commit is on the remote"
	case pushing:
		in.gate = flowGate{"deciding", "checking the push"}
	case open:
		in.gate = flowGate{"waiting", "waiting for reviews"}
	case v.Allowed:
		in.gate = flowGate{"ready", "would allow a push"}
	case len(v.Missing) > 0:
		in.gate = flowGate{"holding", "would hold: " + countWord(len(v.Missing), "item", "items") + " missing"}
	default:
		in.gate = flowGate{"holding", "would hold the push"}
	}
	m := flowOf(in)
	return &m
}

const heldUntil = "held until it has: "

func pushReasonText(p PushView) string {
	said := p.Reason
	if p.Result == "held" && len(p.Missing) > 0 {
		if at := strings.Index(p.Reason, heldUntil); at >= 0 {
			said = strings.TrimRightFunc(p.Reason[:at], func(r rune) bool { return r == ';' || r == ',' || unicode.IsSpace(r) })
		}
	}
	return upperFirst(said)
}

func pushHow(p PushView) string {
	who := ""
	if a := p.Audit; a != nil {
		switch a.By {
		case "agent", "agent, after reviews":
			who = "Asked by the agent"
		case "page":
			who = "From the page"
		}
	}
	n := p.Passed + p.Overridden
	after := ""
	switch {
	case len(p.Missing) > 0:
		items := strconv.Itoa(len(p.Missing)) + " items"
		if len(p.Missing) == 1 {
			items = "1 item"
		}
		after = "allowed with " + items + " missing"
	case n > 0 && p.Overridden == 0:
		switch n {
		case 1:
			after = "after its review passed"
		case 2:
			after = "after both reviews passed"
		default:
			after = "after all " + strconv.Itoa(n) + " reviews passed"
		}
	case n > 0:
		after = "after " + strconv.Itoa(p.Passed) + " of " + strconv.Itoa(n) + " reviews passed, " + strconv.Itoa(p.Overridden) + " overridden"
	}
	parts := []string{}
	for _, s := range []string{who, after} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

func PushOutcome(p PushView) string {
	switch p.Result {
	case "up-to-date":
		return "The remote already had this commit, so nothing was sent"
	case "pushed":
		return pushHow(p)
	}
	return pushReasonText(p)
}

func PushDecision(p PushView) string {
	alone := "Push"
	if l, ok := pushLooks[p.Result]; ok {
		alone = l.Label
	}
	if said := PushOutcome(p); said != "" {
		return alone + ". " + said
	}
	return alone + "."
}

type CheckedLine struct {
	Icon    string
	Tone    string
	Name    string
	Said    string
	Details string
	Tags    []Said
}

func CheckedReviewerLine(r PushReviewer) CheckedLine {
	l := CheckedLine{Name: cmpOr(r.Name, r.Agent), Tags: []Said{}}
	switch r.State {
	case "pass":
		l.Icon, l.Tone, l.Said = "check", "ok", "passed"
	case "block":
		switch {
		case r.Overridden:
			l.Icon, l.Tone, l.Said = "warning", "warn", "blocked, overridden by you"
		case r.Advisory:
			l.Icon, l.Tone, l.Said = "error", "warn", "blocked"
		default:
			l.Icon, l.Tone, l.Said = "error", "bad", "blocked"
		}
	case "unknown":
		l.Icon, l.Tone, l.Said = "warning", "warn", "no verdict the gate counts"
		if r.SetAside != "" {
			l.Said = "passed, but set aside"
		}
	case "failed":
		l.Icon, l.Tone, l.Said = "error", "bad", "didn't finish"
	case "running":
		l.Icon, l.Said = "info", "still running"
	default:
		l.Icon, l.Said = "info", "no review of this commit"
	}
	var parts []string
	if r.State == "pass" || r.State == "block" || r.State == "unknown" {
		if r.Blocking > 0 {
			parts = append(parts, countWord(r.Blocking, "blocking finding", "blocking findings"))
		} else {
			parts = append(parts, "no blocking findings")
		}
	}
	if asked := max(0, r.Requirements); asked > 0 {
		unreported := max(0, asked-max(0, r.Verified)-max(0, r.Violated)-max(0, r.NotVerified))
		line := "requirements: " + strconv.Itoa(max(0, r.Verified)) + " verified, " + strconv.Itoa(max(0, r.Violated)) + " violated, " + strconv.Itoa(max(0, r.NotVerified)) + " not verified"
		if unreported > 0 {
			line += ", " + strconv.Itoa(unreported) + " not reported"
		}
		parts = append(parts, line)
	}
	if r.Runs > 1 {
		parts = append(parts, countWord(r.Runs, "run", "runs"))
	}
	l.Details = strings.Join(parts, " · ")
	switch {
	case r.Required && r.Counted:
		l.Tags = append(l.Tags, Said{"counts for the push", "ok"})
	case r.Required:
		l.Tags = append(l.Tags, Said{"doesn't count for the push", "warn"})
	case r.Advisory:
		l.Tags = append(l.Tags, Said{"advisory, never counts", ""})
	default:
		l.Tags = append(l.Tags, Said{"not required for this push", ""})
	}
	if r.Unchecked {
		l.Tags = append(l.Tags, Said{"not rechecked since the engine restarted", "warn"})
	}
	return l
}

func CheckedRunLine(r PushRun) CheckedLine {
	l := CheckedLine{Name: "Verification run (" + r.Kind + ")", Tags: []Said{}}
	switch r.State {
	case "passed":
		l.Icon, l.Tone, l.Said = "check", "ok", "passed"
	case "running":
		l.Icon, l.Said = "info", "still running"
	case "failed":
		l.Icon, l.Tone, l.Said = "error", "bad", "failed, exit code "+strconv.Itoa(max(0, r.Code))
	default:
		l.Icon, l.Tone, l.Said = "warning", "warn", r.State
	}
	return l
}
