package session

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

const (
	AuditAfterReview  = "after-review"
	ResultCIFailure   = "ci-failure"
	ResultLateComment = "review-comment"
	MaxFollowReviewed = 20
	MaxFollowCounted  = 500
	maxAfterRoles     = maxMetricRoles
)

type ReviewedPush struct {
	Commit string
	At     time.Time
	Passed []string
	All    bool
}

type afterEvent struct {
	key    string
	result string
	commit string
	reason string
	passed []string
	all    bool
}

func ValidRole(s string) bool { return metricRole.MatchString(s) }

func reviewedOf(p PushedFacts, now time.Time) (ReviewedPush, bool) {
	if !commitSHA.MatchString(p.Commit) {
		return ReviewedPush{}, false
	}
	at := p.At
	if at.IsZero() {
		at = now
	}
	out := ReviewedPush{Commit: p.Commit, At: at, All: len(p.Roles) > 0}
	for _, r := range p.Roles {
		ok := r.Passed && !r.Overridden && ValidRole(r.Agent)
		out.All = out.All && ok
		if ok && !slices.Contains(out.Passed, r.Agent) && len(out.Passed) < maxAfterRoles {
			out.Passed = append(out.Passed, r.Agent)
		}
	}
	return out, len(out.Passed) > 0
}

func addReviewed(list []ReviewedPush, r ReviewedPush) []ReviewedPush {
	out := slices.DeleteFunc(slices.Clone(list), func(x ReviewedPush) bool { return x.Commit == r.Commit })
	out = append(out, r)
	if len(out) > MaxFollowReviewed {
		out = out[len(out)-MaxFollowReviewed:]
	}
	return out
}

func addCounted(list []string, keys []string) []string {
	out := slices.Clone(list)
	for _, k := range keys {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	if len(out) > MaxFollowCounted {
		out = out[len(out)-MaxFollowCounted:]
	}
	return out
}

func afterKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func lateComment(c PullComment) (string, bool) {
	switch {
	case c.Kind == CommentReview && c.State == "CHANGES_REQUESTED":
		return "a review asking for changes", true
	case c.Kind == CommentReview && c.State == "COMMENTED" && strings.TrimSpace(c.Body) != "":
		return "a review with comments", true
	case c.Kind == CommentInline:
		return "an inline review comment", true
	}
	return "", false
}

func afterEvents(reviewed []ReviewedPush, counted []string, head string, report CheckReport, checksRead bool, comments []PullComment) []afterEvent {
	var out []afterEvent
	seen := func(key string) bool {
		return slices.Contains(counted, key) || slices.ContainsFunc(out, func(e afterEvent) bool { return e.key == key })
	}
	find := func(commit string) (ReviewedPush, bool) {
		if !commitSHA.MatchString(commit) {
			return ReviewedPush{}, false
		}
		i := slices.IndexFunc(reviewed, func(r ReviewedPush) bool { return r.Commit == commit })
		if i < 0 {
			return ReviewedPush{}, false
		}
		return reviewed[i], true
	}
	passedText := func(rp ReviewedPush) string {
		if rp.All {
			return "every required review"
		}
		return "some of its required reviews"
	}
	if rp, ok := find(head); ok && checksRead {
		add := func(kind, name, result string) {
			key := afterKey("ci", head, kind, name)
			if name == "" || seen(key) {
				return
			}
			out = append(out, afterEvent{key: key, result: ResultCIFailure, commit: head, passed: slices.Clone(rp.Passed), all: rp.All,
				reason: "CI check \"" + CleanLine(name, 200) + "\" ended " + CleanLine(result, 40) + " on commit " + shortSHA(head) + ", pushed once " + passedText(rp) + " passed"})
		}
		for _, r := range report.Runs {
			if r.Status == "completed" && failedRun(r) {
				add("run", r.Name, r.Conclusion)
			}
		}
		for _, s := range report.Statuses {
			if s.State == "failure" || s.State == "error" {
				add("status", s.Context, s.State)
			}
		}
	}
	for _, c := range comments {
		what, ok := lateComment(c)
		if !ok || c.URL == "" || strings.HasPrefix(strings.TrimSpace(c.Body), PushNoteMarker) {
			continue
		}
		rp, ok := find(c.Commit)
		if !ok || !c.Created.After(rp.At) {
			continue
		}
		key := afterKey("comment", c.URL)
		if seen(key) {
			continue
		}
		out = append(out, afterEvent{key: key, result: ResultLateComment, commit: rp.Commit, passed: slices.Clone(rp.Passed), all: rp.All,
			reason: what + " on commit " + shortSHA(rp.Commit) + " came after it was pushed once " + passedText(rp) + " passed: " + CleanLine(c.URL, 300)})
	}
	return out
}

func (fl *Follows) countAfterReview(sid string, x *follow, ft forgeTarget, head string, report CheckReport, checksRead bool, comments []PullComment) {
	fl.mu.Lock()
	if fl.list[sid] != x {
		fl.mu.Unlock()
		return
	}
	events := afterEvents(x.saved.Reviewed, x.saved.Counted, head, report, checksRead, comments)
	fl.mu.Unlock()
	if len(events) == 0 || fl.cfg.Audit == nil {
		return
	}
	var written []string
	for _, ev := range events {
		e := AuditEntry{Time: fl.cfg.Now(), Session: sid, Repository: ft.t.Name, Branch: ft.t.Branch, Commit: ev.commit, Remote: ft.repo.Host + "/" + ft.repo.Slug(),
			By: ByEngine, Result: ev.result, Reason: fl.cfg.Forges.clean(ft.repo.Token, ev.reason), Kind: AuditAfterReview, Hash: ev.key, Passed: ev.passed, AllPassed: ev.all}
		if err := fl.cfg.Audit.Append(e); err != nil {
			fl.cfg.Logger.Warn("pull follow", "event", "audit-not-written")
			break
		}
		written = append(written, ev.key)
	}
	if len(written) == 0 {
		return
	}
	fl.mu.Lock()
	if fl.list[sid] == x {
		x.saved.Counted = addCounted(x.saved.Counted, written)
	}
	fl.mu.Unlock()
	fl.save()
	fl.cfg.Logger.Info("pull follow", "event", "after-review", "count", len(written))
}
