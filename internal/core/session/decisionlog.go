package session

import (
	"crypto/sha256"
	"encoding/hex"
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

type DecisionStore interface {
	Load() (map[string][]DecisionEdit, error)
	Save(map[string][]DecisionEdit) error
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
	SetDecision(repo, id, name, text string, hidden bool) (DecisionLogView, error)
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
}

var _ DecisionLogPage = (*DecisionLog)(nil)

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

func decisionOf(e AuditEntry, repo string) (DecisionEntry, bool) {
	if e.Repository != repo {
		return DecisionEntry{}, false
	}
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

func (l *DecisionLog) edits() (map[string][]DecisionEdit, error) {
	if l.cfg.Store == nil {
		return nil, explained(conflict("no store"), "This engine keeps no decision log edits.")
	}
	all, err := l.cfg.Store.Load()
	if err != nil {
		l.cfg.Logger.Warn("decision log", "event", "edits-not-read")
		return nil, explained(conflict("unreadable"), "The engine couldn't read the decision log's edits, so it changes none.")
	}
	if all == nil {
		all = map[string][]DecisionEdit{}
	}
	return all, nil
}

func (l *DecisionLog) built(repo string, edits []DecisionEdit) DecisionLogView {
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
			if d, ok := decisionOf(e, repo); ok {
				list = append(list, d)
			}
		}
	}
	if l.cfg.Follows != nil {
		if rec, err := l.cfg.Follows(); err != nil {
			v.Problems = append(v.Problems, "The engine couldn't read the pull requests it follows, so the log holds no merged pull requests.")
		} else {
			for _, f := range rec.Follows {
				if f.Project != repo || f.Done != FollowMerged || f.Number <= 0 {
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
	all, err := l.edits()
	if err != nil {
		v := l.built(repo, nil)
		v.Problems = append(v.Problems, "The engine couldn't read your edits to the log, so this shows the log without them and sends no log to new sessions.")
		v.Note = ""
		return v, nil
	}
	return l.built(repo, all[repo]), nil
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

func (l *DecisionLog) change(repo, result string, f func(list []DecisionEdit, current DecisionLogView) ([]DecisionEdit, error)) (DecisionLogView, error) {
	if err := l.known(repo); err != nil {
		return DecisionLogView{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	all, err := l.edits()
	if err != nil {
		return DecisionLogView{}, err
	}
	current := l.built(repo, all[repo])
	l.dropped = 0
	next, err := f(slices.Clone(all[repo]), current)
	if err != nil {
		return DecisionLogView{}, err
	}
	if _, ok := all[repo]; !ok && len(next) > 0 && len(all) >= MaxDecisionProjects {
		return DecisionLogView{}, explained(limited("projects"), "The engine keeps decision log edits for at most 200 projects.")
	}
	if len(next) == 0 {
		delete(all, repo)
	} else {
		all[repo] = next
	}
	if err := l.cfg.Store.Save(all); err != nil {
		l.cfg.Logger.Warn("decision log", "event", "edits-not-saved")
		return DecisionLogView{}, explained(conflict("not saved"), "The engine couldn't save the change, so nothing changed.")
	}
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
	return l.built(repo, next), nil
}

func checkPersonText(name, text string) error {
	switch {
	case !ValidDecisionName(name):
		return explained(invalid("name"), "A name is one line of at most 120 characters, with no control characters.")
	case !ValidDecisionText(text):
		return explained(invalid("text"), "A text is at most 500 characters, with no control characters but line breaks.")
	case secretText(name) || secretText(text):
		return explained(invalid("token"), "That looks like it holds a token, and the log goes to every new session's agent. Leave it out.")
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

func (l *DecisionLog) SetDecision(repo, id, name, text string, hidden bool) (DecisionLogView, error) {
	if !ValidDecisionID(id) {
		return DecisionLogView{}, invalid("id")
	}
	if err := checkPersonText(name, text); err != nil {
		return DecisionLogView{}, err
	}
	return l.change(repo, "changed", func(list []DecisionEdit, current DecisionLogView) ([]DecisionEdit, error) {
		i := slices.IndexFunc(list, func(e DecisionEdit) bool { return e.ID == id })
		if i >= 0 && list[i].Added {
			if text == "" {
				return nil, explained(invalid("text"), "An entry you added needs its text. Remove it instead.")
			}
			list[i].Name, list[i].Text, list[i].Hidden = name, text, hidden
			return list, nil
		}
		if !current.live[id] {
			return nil, explained(conflict("gone"), "That entry isn't in the log any more. Read it again.")
		}
		edit := DecisionEdit{ID: id, Name: name, Text: text, Hidden: hidden, At: l.cfg.Now()}
		if i >= 0 {
			if name == "" && text == "" && !hidden {
				return slices.Delete(list, i, i+1), nil
			}
			list[i] = edit
			return list, nil
		}
		if name == "" && text == "" && !hidden {
			return list, nil
		}
		list = l.pruned(list, current)
		if len(list) >= MaxDecisionEdits {
			return nil, explained(limited("edits"), "A project's log keeps at most 300 of your edits and entries. Remove some first.")
		}
		return append(list, edit), nil
	})
}

func (l *DecisionLog) AddDecision(repo, text string) (DecisionLogView, error) {
	if text == "" {
		return DecisionLogView{}, explained(invalid("text"), "An entry needs its text.")
	}
	if err := checkPersonText("", text); err != nil {
		return DecisionLogView{}, err
	}
	return l.change(repo, "added", func(list []DecisionEdit, current DecisionLogView) ([]DecisionEdit, error) {
		added := 0
		for _, e := range list {
			if e.Added {
				added++
			}
		}
		if added >= MaxDecisionAdded {
			return nil, explained(limited("added"), "A project's log keeps at most 50 entries you added. Remove one first.")
		}
		list = l.pruned(list, current)
		if len(list) >= MaxDecisionEdits {
			return nil, explained(limited("edits"), "A project's log keeps at most 300 of your edits and entries. Remove some first.")
		}
		return append(list, DecisionEdit{ID: newPromptID(), Added: true, Text: text, At: l.cfg.Now()}), nil
	})
}

func (l *DecisionLog) RemoveDecision(repo, id string) (DecisionLogView, error) {
	if !ValidDecisionID(id) {
		return DecisionLogView{}, invalid("id")
	}
	return l.change(repo, "removed", func(list []DecisionEdit, _ DecisionLogView) ([]DecisionEdit, error) {
		next := slices.DeleteFunc(list, func(e DecisionEdit) bool { return e.ID == id })
		if len(next) == len(list) {
			return nil, explained(conflict("gone"), "Nothing of yours is saved for that entry. Read the log again.")
		}
		return next, nil
	})
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
