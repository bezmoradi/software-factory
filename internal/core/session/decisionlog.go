package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/gate"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	DecisionNoteSource  = "decisions"
	MaxDecisionNote     = 4096
	MaxDecisionName     = 120
	MaxDecisionText     = 500
	MaxDecisionEdits    = 300
	MaxDecisionAdded    = 50
	MaxDecisionProjects = 200
	AuditDecisionLog    = "decision-log"
	maxDecisionReason   = 300
	maxDecisionShown    = 300

	DecisionRequirements = "requirements"
	DecisionPlan         = "plan"
	DecisionOverride     = "override"
	DecisionAllow        = "allow"
	DecisionDenied       = "denied"
	DecisionMerged       = "merged"
	DecisionNote         = "note"

	decisionWithheld = "[withheld: it looks like it holds a token]"
	decisionHeader   = "Decision log for this project, built by Software Factory from its own records: the person's approvals by hash, overrides, Allows and denied pushes with the person's reasons, and merged pull requests, oldest first. Text in quotes is the person's own. No line here was written by an agent."
)

var (
	decisionID     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	decisionBranch = regexp.MustCompile(`^sf/[0-9a-f]{8}$`)
	decisionHex    = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

type DecisionEdit struct {
	ID     string
	Added  bool
	Name   string
	Text   string
	Hidden bool
	At     time.Time
}

type DecisionState struct {
	Projects     map[string][]DecisionEdit
	Removed      []RemovedProject[[]DecisionEdit]
	Spans        map[string][]ProjectSpan
	RemovedSpans []RemovedProject[[]ProjectSpan]
}

type DecisionStore interface {
	Load() (DecisionState, error)
	Save(DecisionState) (int, error)
	Dropped(n int)
}

type DecisionEntry struct {
	ID       string
	Kind     string
	Time     time.Time
	Line     string
	Reason   string
	Withheld bool
	Name     string
	Text     string
	Hidden   bool
	Added    bool
	Edited   bool
}

type DecisionLogView struct {
	Repo       string
	Entries    []DecisionEntry
	More       int
	Note       string
	Left       int
	AuditSince time.Time
	Orphans    int
	Problems   []string
	live       map[string]bool
}

type DecisionLogPage interface {
	DecisionLog(repo string) (DecisionLogView, error)
	SetDecision(repo, id, name, text string, hidden bool) (DecisionLogView, bool, error)
	AddDecision(repo, text string) (DecisionLogView, error)
	RemoveDecision(repo, id string) (DecisionLogView, error)
}

type DecisionLogConfig struct {
	Store   DecisionStore
	Audit   AuditAll
	Writer  Audit
	Follows func() (FollowRecord, error)
	Repos   Repositories
	Note    func(sid, text string)
	Now     func() time.Time
	Logger  shared.Logger
}

type DecisionLog struct {
	cfg     DecisionLogConfig
	mu      sync.Mutex
	dropped int
	pending pendingSteps
}

var (
	_ DecisionLogPage = (*DecisionLog)(nil)
	_ ProjectData     = (*DecisionLog)(nil)
)

func NewDecisionLog(cfg DecisionLogConfig) *DecisionLog {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	cfg.Writer = masking(cfg.Writer)
	return &DecisionLog{cfg: cfg}
}

func ValidDecisionID(id string) bool { return decisionID.MatchString(id) }

func decisionLine(s string, max int, multiline bool) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if r == '\n' && multiline {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

func ValidDecisionName(s string) bool { return decisionLine(s, MaxDecisionName, false) }

func ValidDecisionText(s string) bool { return decisionLine(s, MaxDecisionText, true) }

func ValidDecisionEdit(e DecisionEdit) bool {
	if !ValidDecisionID(e.ID) || !ValidDecisionName(e.Name) || !ValidDecisionText(e.Text) {
		return false
	}
	if e.Added {
		return e.Text != ""
	}
	return e.Name != "" || e.Text != "" || e.Hidden
}

func decisionKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func shownBranch(b string) string {
	if decisionBranch.MatchString(b) {
		return b
	}
	return "a session's branch"
}

func shownHex(s string, n int) string {
	if !decisionHex.MatchString(s) {
		return ""
	}
	return s[:min(len(s), n)]
}

func personText(s string, max int) (string, bool) {
	s = strings.TrimSpace(CleanLine(strings.Join(strings.Fields(s), " "), max))
	if s == "" {
		return "", false
	}
	if secretText(s) {
		return decisionWithheld, true
	}
	return s, false
}

func decisionOf(e AuditEntry) (DecisionEntry, bool) {
	d := DecisionEntry{Time: e.Time}
	at := ""
	if c := shownHex(e.Commit, 7); c != "" {
		at = " at " + c
	}
	switch e.Kind {
	case AuditApproveRequirements, AuditApprovePlan:
		hash := shownHex(e.Hash, 12)
		if hash == "" {
			return d, false
		}
		d.Kind, d.Line = DecisionRequirements, "Requirements approved on the page, approval "+hash
		if e.Kind == AuditApprovePlan {
			d.Kind, d.Line = DecisionPlan, "Plan approved on the page, approval "+hash
		}
		d.Line += " on " + shownBranch(e.Branch)
	case AuditOverride:
		who := "a reviewer"
		if metricRole.MatchString(e.Reviewer) {
			who = e.Reviewer
		}
		d.Kind, d.Line = DecisionOverride, "The person overrode the verdict of "+who+" on "+shownBranch(e.Branch)+at
	case "", AuditPush:
		switch {
		case e.Result == string(gate.Allowed) && e.PersonReason != "":
			n := len(e.Missing)
			items := "items"
			if n == 1 {
				items = "item"
			}
			d.Kind, d.Line = DecisionAllow, "The person allowed a push with "+strconv.Itoa(n)+" missing "+items+" from "+shownBranch(e.Branch)+at
		case e.Result == string(gate.Denied):
			d.Kind, d.Line = DecisionDenied, "The person denied a push from "+shownBranch(e.Branch)+at
		default:
			return d, false
		}
	default:
		return d, false
	}
	d.Reason, d.Withheld = personText(e.PersonReason, maxDecisionReason)
	d.ID = decisionKey(d.Kind, e.Time.UTC().Format(time.RFC3339Nano), e.Session, e.Commit, e.Hash, e.Reviewer)
	return d, true
}

func (l *DecisionLog) known(repo string) error {
	return knownRepo(l.cfg.Repos, repo)
}

func (l *DecisionLog) state() (DecisionState, error) {
	if l.cfg.Store == nil {
		return DecisionState{}, conflict("no store", "This engine keeps no decision log edits.")
	}
	st, err := l.load()
	if err != nil {
		l.cfg.Logger.Warn("decision log", "event", "edits-not-read")
		return DecisionState{}, conflict("unreadable", "The engine couldn't read the decision log's edits, so it changes none.")
	}
	return st, nil
}

func (l *DecisionLog) load() (DecisionState, error) {
	st, err := l.cfg.Store.Load()
	if err != nil {
		return DecisionState{}, err
	}
	if st.Projects == nil {
		st.Projects = map[string][]DecisionEdit{}
	}
	if st.Spans == nil {
		st.Spans = map[string][]ProjectSpan{}
	}
	steps := l.pending.list()
	if len(steps) == 0 {
		return st, nil
	}
	next := st
	next.Projects, next.Spans = maps.Clone(st.Projects), maps.Clone(st.Spans)
	next.Removed, next.RemovedSpans = slices.Clone(st.Removed), slices.Clone(st.RemovedSpans)
	dropped := 0
	for _, s := range steps {
		n, _ := l.step(&next, s)
		dropped += n
	}
	trimmed, err := l.cfg.Store.Save(next)
	if err != nil {
		projectDataNotSaved(l.cfg.Logger, "decision log", "", err)
		return st, nil
	}
	l.trimmed(trimmed)
	if dropped > 0 {
		l.cfg.Store.Dropped(dropped)
	}
	l.pending.done(len(steps))
	l.cfg.Logger.Info("decision log", "event", "pending-project-data-applied", "steps", len(steps))
	return next, nil
}

func (l *DecisionLog) step(st *DecisionState, s projectStep) (int, bool) {
	if s.restore {
		st.Removed, _ = restoreProject(st.Projects, st.Removed, s.id)
		l.restoreSpans(st, s.id, s.at)
		return 0, true
	}
	var parked bool
	var dropped []RemovedProject[[]DecisionEdit]
	st.Removed, dropped, parked = parkProject(st.Projects, st.Removed, s.id)
	spans := l.parkSpans(st, s.id, s.at)
	n := 0
	for _, r := range dropped {
		n += len(r.Data)
	}
	return n, parked || spans
}

func (l *DecisionLog) projectData(event string, step *projectStep, fn func(st *DecisionState) (int, bool)) {
	if l == nil || l.cfg.Store == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	failed := func(err error) {
		name := ""
		if step != nil {
			name = step.id.Name
			l.pending.remember(step.id, step.restore, step.at)
		}
		projectDataNotSaved(l.cfg.Logger, "decision log", name, err)
	}
	st, err := l.load()
	if err != nil {
		failed(err)
		return
	}
	dropped, changed := fn(&st)
	if !changed {
		return
	}
	trimmed, err := l.cfg.Store.Save(st)
	if err != nil {
		failed(err)
		return
	}
	l.trimmed(trimmed)
	if dropped > 0 {
		l.cfg.Store.Dropped(dropped)
	}
	l.cfg.Logger.Info("decision log", "event", event)
}

func (l *DecisionLog) trimmed(n int) {
	if n > 0 {
		l.cfg.Logger.Warn("decision log", "event", "removed-trimmed-to-fit", "edits", n)
	}
}

func (l *DecisionLog) ParkProject(id ProjectID) {
	s := projectStep{id: id, at: l.cfg.Now()}
	l.projectData("parked", &s, func(st *DecisionState) (int, bool) {
		return l.step(st, s)
	})
}

func (l *DecisionLog) RestoreProject(id ProjectID) {
	s := projectStep{id: id, restore: true, at: l.cfg.Now()}
	l.projectData("restored", &s, func(st *DecisionState) (int, bool) {
		return l.step(st, s)
	})
}

func (l *DecisionLog) KeepProjects(names []string) {
	l.projectData("unlisted-set-aside", nil, func(st *DecisionState) (int, bool) {
		var n int
		st.Removed, n = keepProjects(st.Projects, st.Removed, names)
		spans := l.keepSpans(st, names)
		return 0, n > 0 || spans
	})
}

func (l *DecisionLog) built(repo string, edits []DecisionEdit, own projectLines) DecisionLogView {
	v := DecisionLogView{Repo: repo}
	var list []DecisionEntry
	if l.cfg.Audit == nil {
		v.Problems = append(v.Problems, "This engine keeps no audit file.")
	} else if entries, err := l.cfg.Audit.ReadAll(MetricsAuditLines); err != nil {
		v.Problems = append(v.Problems, "The engine couldn't read its audit file, so the log holds only merged pull requests and your own entries.")
	} else {
		for _, e := range entries {
			if v.AuditSince.IsZero() || e.Time.Before(v.AuditSince) {
				v.AuditSince = e.Time
			}
			if !own.audit(e) {
				continue
			}
			if d, ok := decisionOf(e); ok {
				list = append(list, d)
			}
		}
	}
	if l.cfg.Follows != nil {
		if rec, err := l.cfg.Follows(); err != nil {
			v.Problems = append(v.Problems, "The engine couldn't read the pull requests it follows, so the log holds no merged pull requests.")
		} else {
			for _, f := range rec.Follows {
				if !own.at(f.Project, f.DoneAt) || f.Done != FollowMerged || f.Number <= 0 {
					continue
				}
				list = append(list, DecisionEntry{ID: decisionKey(DecisionMerged, f.SID, strconv.Itoa(f.Number)), Kind: DecisionMerged, Time: f.DoneAt,
					Line: "Pull request #" + strconv.Itoa(f.Number) + " from " + shownBranch(f.Branch) + " was merged on the git host"})
			}
		}
	}
	seen := map[string]bool{}
	list = slices.DeleteFunc(list, func(d DecisionEntry) bool {
		if seen[d.ID] {
			return true
		}
		seen[d.ID] = true
		return false
	})
	byID := map[string]int{}
	v.live = map[string]bool{}
	for i, d := range list {
		byID[d.ID] = i
		v.live[d.ID] = true
	}
	for _, e := range edits {
		if e.Added {
			list = append(list, DecisionEntry{ID: e.ID, Kind: DecisionNote, Time: e.At, Name: e.Name, Text: e.Text, Hidden: e.Hidden, Added: true})
			continue
		}
		i, ok := byID[e.ID]
		if !ok {
			v.Orphans++
			continue
		}
		list[i].Name, list[i].Text, list[i].Hidden, list[i].Edited = e.Name, e.Text, e.Hidden, true
	}
	slices.SortStableFunc(list, func(a, b DecisionEntry) int { return a.Time.Compare(b.Time) })
	v.Note, v.Left = decisionNote(list)
	slices.Reverse(list)
	if len(list) > maxDecisionShown {
		v.More = len(list) - maxDecisionShown
		list = list[:maxDecisionShown]
	}
	v.Entries = list
	return v
}

func inQuotes(s string) string {
	return "\"" + s + "\""
}

func noteLine(d DecisionEntry) string {
	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(d.Time.UTC().Format(time.DateOnly))
	b.WriteString(" ")
	name, _ := personText(d.Name, MaxDecisionName)
	text, _ := personText(d.Text, MaxDecisionText)
	if name != "" {
		b.WriteString(inQuotes(name))
		b.WriteString(": ")
	}
	switch {
	case text != "":
		b.WriteString(inQuotes(text))
		if !d.Added {
			b.WriteString(" (" + d.Kind + ")")
		}
	default:
		b.WriteString(d.Line)
		if d.Reason != "" {
			b.WriteString(", reason " + inQuotes(d.Reason))
		}
	}
	return b.String()
}

func decisionNote(list []DecisionEntry) (string, int) {
	var lines []string
	for _, d := range list {
		if !d.Hidden {
			lines = append(lines, noteLine(d))
		}
	}
	if len(lines) == 0 {
		return "", 0
	}
	left := 0
	for len(lines) > 0 {
		out := decisionHeader
		if left > 0 {
			out += "\n(" + strconv.Itoa(left) + " older entries left out for length.)"
		}
		out += "\n" + strings.Join(lines, "\n")
		if len(out) <= MaxDecisionNote {
			return out, left
		}
		lines, left = lines[1:], left+1
	}
	return "", left
}

func (l *DecisionLog) view(repo string) (DecisionLogView, error) {
	st, err := l.state()
	own := l.linesFor(st, repo)
	if err != nil {
		v := l.built(repo, nil, own)
		v.Problems = append(v.Problems, "The engine couldn't read your edits to the log, so this shows the log without them and sends no log to new sessions.")
		v.Note = ""
		return v, nil
	}
	return l.built(repo, st.Projects[repo], own), nil
}

func (l *DecisionLog) DecisionLog(repo string) (DecisionLogView, error) {
	if err := l.known(repo); err != nil {
		return DecisionLogView{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.view(repo)
}

func (l *DecisionLog) Offer(sid, repo string) {
	if l == nil || l.cfg.Note == nil || sid == "" {
		return
	}
	l.mu.Lock()
	v, err := l.view(repo)
	l.mu.Unlock()
	if err != nil || v.Note == "" {
		return
	}
	l.cfg.Note(sid, v.Note)
	l.cfg.Logger.Info("decision log", "event", "offered", "bytes", len(v.Note), "left", v.Left)
}

var errNoEdit = errors.New("the edit changes nothing")

func (l *DecisionLog) change(repo, result string, f func(list []DecisionEdit, current DecisionLogView) ([]DecisionEdit, error)) (DecisionLogView, bool, error) {
	if err := l.known(repo); err != nil {
		return DecisionLogView{}, false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := l.state()
	if err != nil {
		return DecisionLogView{}, false, err
	}
	all := st.Projects
	own := l.linesFor(st, repo)
	current := l.built(repo, all[repo], own)
	l.dropped = 0
	next, err := f(slices.Clone(all[repo]), current)
	if errors.Is(err, errNoEdit) {
		return current, false, nil
	}
	if err != nil {
		return DecisionLogView{}, false, err
	}
	if _, ok := all[repo]; !ok && len(next) > 0 && len(all) >= MaxDecisionProjects {
		return DecisionLogView{}, false, limited("projects", "The engine keeps decision log edits for at most 200 projects.")
	}
	if len(next) == 0 {
		delete(all, repo)
	} else {
		all[repo] = next
	}
	trimmed, err := l.cfg.Store.Save(st)
	if err != nil {
		l.cfg.Logger.Warn("decision log", "event", "edits-not-saved", "err", err.Error())
		return DecisionLogView{}, false, conflict("not saved", "The engine couldn't save the change, so nothing changed.")
	}
	l.trimmed(trimmed)
	if l.dropped > 0 {
		l.cfg.Store.Dropped(l.dropped)
	}
	if l.cfg.Writer != nil {
		e := AuditEntry{Time: l.cfg.Now(), Repository: repo, By: ByPage, Kind: AuditDecisionLog, Result: result, Reason: "the person " + result + " an entry of the project's decision log on the page"}
		if err := l.cfg.Writer.Append(e); err != nil {
			l.cfg.Logger.Warn("decision log", "event", "audit-not-written")
		}
	}
	l.cfg.Logger.Info("decision log", "event", result)
	return l.built(repo, next, own), true, nil
}

func checkPersonText(name, text string) error {
	switch {
	case !ValidDecisionName(name):
		return onField("name", invalid("name", "Needs one line of at most 120 characters."))
	case !ValidDecisionText(text):
		return onField("text", invalid("text", "At most 500 characters."))
	case secretText(name) || secretText(text):
		return invalid("token", "That looks like it holds a token, and the log goes to every new session's agent. Leave it out.")
	}
	return nil
}

func (l *DecisionLog) pruned(list []DecisionEdit, current DecisionLogView) []DecisionEdit {
	if len(list) < MaxDecisionEdits {
		return list
	}
	n := len(list)
	list = slices.DeleteFunc(list, func(e DecisionEdit) bool { return !e.Added && !current.live[e.ID] })
	l.dropped += n - len(list)
	return list
}

func (l *DecisionLog) SetDecision(repo, id, name, text string, hidden bool) (DecisionLogView, bool, error) {
	if !ValidDecisionID(id) {
		return DecisionLogView{}, false, invalid("id", "That isn't the id of an entry in this decision log. Reload the log and try again.")
	}
	name, text = strings.TrimSpace(name), strings.TrimSpace(text)
	if err := checkPersonText(name, text); err != nil {
		return DecisionLogView{}, false, err
	}
	same := func(e DecisionEdit) bool { return e.Name == name && e.Text == text && e.Hidden == hidden }
	return l.change(repo, "changed", func(list []DecisionEdit, current DecisionLogView) ([]DecisionEdit, error) {
		i := slices.IndexFunc(list, func(e DecisionEdit) bool { return e.ID == id })
		if i >= 0 && list[i].Added {
			if text == "" {
				return nil, onField("text", invalid("text", "An entry you added needs its text. Remove it instead."))
			}
			if same(list[i]) {
				return nil, errNoEdit
			}
			list[i].Name, list[i].Text, list[i].Hidden = name, text, hidden
			return list, nil
		}
		if !current.live[id] {
			return nil, conflict("gone", "That entry isn't in the log any more. Read it again.")
		}
		edit := DecisionEdit{ID: id, Name: name, Text: text, Hidden: hidden, At: l.cfg.Now()}
		if i >= 0 {
			if same(list[i]) {
				return nil, errNoEdit
			}
			if name == "" && text == "" && !hidden {
				return slices.Delete(list, i, i+1), nil
			}
			list[i] = edit
			return list, nil
		}
		if name == "" && text == "" && !hidden {
			return nil, errNoEdit
		}
		list = l.pruned(list, current)
		if len(list) >= MaxDecisionEdits {
			return nil, limited("edits", "A project's log keeps at most 300 of your edits and entries. Remove some first.")
		}
		return append(list, edit), nil
	})
}

func (l *DecisionLog) AddDecision(repo, text string) (DecisionLogView, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return DecisionLogView{}, onField("text", invalid("text", "Needs its text."))
	}
	if err := checkPersonText("", text); err != nil {
		return DecisionLogView{}, err
	}
	v, _, err := l.change(repo, "added", func(list []DecisionEdit, current DecisionLogView) ([]DecisionEdit, error) {
		added := 0
		for _, e := range list {
			if e.Added {
				added++
			}
		}
		if added >= MaxDecisionAdded {
			return nil, limited("added", "A project's log keeps at most 50 entries you added. Remove one first.")
		}
		list = l.pruned(list, current)
		if len(list) >= MaxDecisionEdits {
			return nil, limited("edits", "A project's log keeps at most 300 of your edits and entries. Remove some first.")
		}
		return append(list, DecisionEdit{ID: newPromptID(), Added: true, Text: text, At: l.cfg.Now()}), nil
	})
	return v, err
}

func (l *DecisionLog) RemoveDecision(repo, id string) (DecisionLogView, error) {
	if !ValidDecisionID(id) {
		return DecisionLogView{}, invalid("id", "That isn't the id of an entry in this decision log. Reload the log and try again.")
	}
	v, _, err := l.change(repo, "removed", func(list []DecisionEdit, _ DecisionLogView) ([]DecisionEdit, error) {
		next := slices.DeleteFunc(list, func(e DecisionEdit) bool { return e.ID == id })
		if len(next) == len(list) {
			return nil, conflict("gone", "Nothing of yours is saved for that entry. Read the log again.")
		}
		return next, nil
	})
	return v, err
}

func (d *Dispatcher) UseDecisionLog(l *DecisionLog) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.decisions = l
}

func (d *Dispatcher) offerDecisions(r *run, err error) {
	if err != nil || r.sid == "" || d.historyOnly(r.sid) != nil {
		return
	}
	d.mu.Lock()
	l := d.decisions
	d.mu.Unlock()
	l.Offer(r.sid, r.repo)
}
