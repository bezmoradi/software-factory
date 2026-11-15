package session

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	maxFindingItems = 20
	maxFindingText  = 6000
)

var (
	findingHeading  = regexp.MustCompile(`(?i)^\s{0,3}#{2,4}\s*\[(medium|low)\]\s*(.*)$`)
	findingEnd      = regexp.MustCompile(`(?i)^\s{0,3}#{1,4}\s|^\s*knackpack-(verdict|findings)/`)
	findingLocation = regexp.MustCompile(`(?i)^[\s*_>-]*(location|where|file|files|path)[\s*_]*:[\s*_]*(.+?)[\s*_]*$`)
	findingPath     = regexp.MustCompile("`([^`\\s]+:\\d+(?:-\\d+)?)`")
)

type FindingItem struct {
	Severity string
	Title    string
	Location string
	Text     string
	Reviewer string
	Agent    string
}

type FindingOffer struct {
	Commit string
	Branch string
	Items  []FindingItem
	Held   int
	Opened bool
}

func NonBlockingFindings(message string) []FindingItem {
	var out []FindingItem
	var cur *FindingItem
	var lines []string
	flush := func() {
		if cur == nil {
			return
		}
		var kept []string
		for _, l := range lines {
			if m := findingLocation.FindStringSubmatch(l); m != nil && cur.Location == "" {
				cur.Location = CleanLine(strings.Trim(m[2], "`* "), 300)
				continue
			}
			kept = append(kept, CleanLine(l, 2000))
		}
		cur.Text = strings.TrimSpace(strings.Join(kept, "\n"))
		if utf8.RuneCountInString(cur.Text) > maxFindingText {
			cur.Text = string([]rune(cur.Text)[:maxFindingText]) + "\n[cut by Software Factory]"
		}
		if cur.Location == "" {
			if m := findingPath.FindStringSubmatch(cur.Title); m != nil {
				cur.Location = CleanLine(m[1], 300)
			}
		}
		if cur.Title != "" && len(out) < maxFindingItems {
			out = append(out, *cur)
		}
		cur, lines = nil, nil
	}
	for l := range strings.SplitSeq(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if m := findingHeading.FindStringSubmatch(l); m != nil {
			flush()
			sev := strings.ToUpper(m[1][:1]) + strings.ToLower(m[1][1:])
			cur = &FindingItem{Severity: sev, Title: CleanLine(strings.TrimSpace(m[2]), 200)}
			continue
		}
		if cur != nil && findingEnd.MatchString(l) {
			flush()
			continue
		}
		if cur != nil {
			lines = append(lines, l)
		}
	}
	flush()
	return out
}

func (fl *Follows) findingsAt(sid string) (PushTarget, string, []FindingItem, error) {
	t, ok := fl.cfg.Forges.cfg.Targets(sid)
	if !ok {
		return PushTarget{}, "", nil, explained(conflict("not started here"), "Only a session this engine started can open issues from its findings.")
	}
	if fl.cfg.Reviews == nil {
		return t, "", nil, explained(conflict("no reviews"), "This engine runs no reviews.")
	}
	commit := ""
	for _, i := range fl.cfg.Sessions() {
		if i.SID == sid && i.Review != nil && !i.Review.Running {
			commit = i.Review.Commit
		}
	}
	if !commitSHA.MatchString(commit) {
		return t, "", nil, nil
	}
	entries, err := fl.cfg.Reviews()
	if err != nil {
		return t, commit, nil, explained(conflict("records"), "The engine couldn't read its review records.")
	}
	var keys []review.Key
	for k, e := range entries {
		if k.Commit == commit && k.Branch == t.Branch && e.Record.State == review.Pass && e.Record.Override == "" {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b review.Key) int { return strings.Compare(a.Agent, b.Agent) })
	var items []FindingItem
	for _, k := range keys {
		e := entries[k]
		name := ReviewerName(workflow.Requirement{Agent: k.Agent, Role: e.Role})
		text := e.Message
		if fl.cfg.Replies != nil {
			text, _ = fl.cfg.Replies(sid, e)
		}
		for _, it := range NonBlockingFindings(text) {
			it.Reviewer, it.Agent = CleanLine(name, 100), CleanLine(k.Agent, 120)
			if len(items) < maxFindingItems {
				items = append(items, it)
			}
		}
	}
	return t, commit, items, nil
}

func findingCard(it FindingItem, commit, branch string) (string, string) {
	title := "[" + it.Severity + "] " + it.Title
	if utf8.RuneCountInString(title) > MaxForgeTitle {
		title = string([]rune(title)[:MaxForgeTitle])
	}
	var b strings.Builder
	if it.Text != "" {
		b.WriteString(it.Text + "\n\n")
	}
	if it.Location != "" {
		b.WriteString("Location: " + it.Location + "\n\n")
	}
	b.WriteString("Found by " + it.Reviewer + " (`" + strings.ReplaceAll(it.Agent, "`", "") + "`) as a " + strings.ToLower(it.Severity) + " finding, which didn't block, in a passing review of commit `" + shortSHA(commit) + "` on `" + strings.ReplaceAll(CleanLine(branch, 200), "`", "") + "`. Opened from Software Factory's page; the finding's text is the reviewer's, as it wrote it.")
	body := b.String()
	if utf8.RuneCountInString(body) > MaxForgeBody {
		body = string([]rune(body)[:MaxForgeBody])
	}
	return strings.TrimSpace(CleanLine(title, MaxForgeTitle)), body
}

func (fl *Follows) Findings(sid string) (FindingOffer, error) {
	t, commit, items, err := fl.findingsAt(sid)
	if err != nil {
		return FindingOffer{}, err
	}
	out := FindingOffer{Commit: commit, Branch: t.Branch}
	for _, it := range items {
		title, body := findingCard(it, commit, t.Branch)
		if where, _ := ForgeSecret(title, body); where != "" {
			out.Held++
			continue
		}
		out.Items = append(out.Items, it)
	}
	fl.mu.Lock()
	out.Opened = commit != "" && fl.offered[sid] == commit
	fl.mu.Unlock()
	return out, nil
}

func (fl *Follows) OpenFindings(ctx context.Context, sid, commit string) (int, error) {
	t, at, items, err := fl.findingsAt(sid)
	if err != nil {
		return 0, err
	}
	if at == "" || at != commit {
		return 0, explained(conflict("commit"), "The review those findings came from isn't the session's latest any more. Read them again.")
	}
	fl.mu.Lock()
	if fl.offered[sid] == commit {
		fl.mu.Unlock()
		return 0, explained(conflict("offered"), "Issue cards for these findings were already offered. Answer them under Issues and pull requests.")
	}
	fl.mu.Unlock()
	made, skipped := 0, 0
	var last error
	for _, it := range items {
		title, body := findingCard(it, commit, t.Branch)
		if where, _ := ForgeSecret(title, body); where != "" {
			skipped++
			continue
		}
		if err := fl.cfg.Forges.PageRequest(ctx, sid, ForgeIssue, title, body, ForgeFromFindings); err != nil {
			last = err
			continue
		}
		made++
	}
	if made == 0 {
		if last != nil {
			return 0, last
		}
		return 0, explained(conflict("no findings"), "No finding that didn't block waits to become an issue.")
	}
	fl.mu.Lock()
	if len(fl.offered) >= MaxFollowed {
		clear(fl.offered)
	}
	fl.offered[sid] = commit
	fl.mu.Unlock()
	fl.cfg.Logger.Info("pull follow", "event", "finding-cards", "made", made, "skipped", skipped)
	if made < len(items)-skipped && last != nil {
		return made, explained(conflict("partly"), strconv.Itoa(made)+" issue cards wait for you; the rest weren't made: "+errorText(last))
	}
	return made, nil
}

func errorText(err error) string {
	if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
		return e.Message
	}
	return "the engine couldn't make them"
}
