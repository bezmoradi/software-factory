package session

import (
	"maps"
	"slices"
	"strconv"
	"sync"
)

const (
	AuditPush        = "push"
	AuditOverride    = "override"
	AuditAllowFile   = "allow-file"
	AuditAllowCommit = "allow-commit"
	MaxAuditShown    = 50
	maxRefusalKind   = 1_000_000
)

const (
	AuditReview              = "review"
	AuditFindings            = "findings"
	AuditPRComment           = "pr-comment"
	AuditApproveStartup      = "approve-startup"
	AuditApproveRequirements = "approve-requirements"
	AuditApprovePlan         = "approve-plan"
	AuditApproveTeam         = "approve-team"
	AuditForgetTeam          = "forget-team"
)

const (
	ResultSending   = "sending"
	ResultUnclear   = "unclear"
	ResultAbandoned = "abandoned"
	ResultExpired   = "expired"
	ResultPosting   = "posting"
	ResultCommented = "commented"
	ResultSent      = "sent"
	ResultWithheld  = "withheld"
	ResultNotSent   = "not-sent"
)

const (
	RefusedPush      = "push-refused"
	RefusedRemote    = "remote-held"
	RefusedProtected = "protected-write"
	RefusedCloud     = "cloud-refused"
)

type maskedAudit struct {
	Audit
}

func masking(a Audit) Audit {
	if _, done := a.(maskedAudit); a == nil || done {
		return a
	}
	return maskedAudit{a}
}

func (m maskedAudit) Append(e AuditEntry) error {
	return m.Audit.Append(e.masked())
}

const (
	maxAuditItems     = 100
	maxAuditItemBytes = 64 << 10
	maxAuditReason    = 16 << 10
)

func boundItems(items []string) []string {
	if items == nil {
		return nil
	}
	out := make([]string, 0, min(len(items), maxAuditItems))
	used := 0
	for i, m := range items {
		if len(out) >= maxAuditItems-1 && i < len(items)-1 || used+len(m) > maxAuditItemBytes {
			return append(out, "and "+strconv.Itoa(len(items)-i)+" more items, which the page's gate panel lists")
		}
		used += len(m)
		out = append(out, m)
	}
	return out
}

func boundReason(s string) string {
	if len(s) <= maxAuditReason {
		return s
	}
	return clip(s, maxAuditReason-60) + " … (cut here; the page's gate panel lists every item)"
}

func (e AuditEntry) masked() AuditEntry {
	e.Reason, e.PersonReason, e.Path = maskCut(boundReason(e.Reason)), maskCut(e.PersonReason), maskSecrets(e.Path)
	if e.Missing != nil {
		bounded := boundItems(e.Missing)
		missing := make([]string, len(bounded))
		for i, m := range bounded {
			missing[i] = maskSecrets(m)
		}
		e.Missing = missing
	}
	return e
}

func ProjectLevelAudit(kind string) bool {
	return slices.Contains([]string{AuditReviewerAgent, AuditApproveTeam, AuditForgetTeam, AuditApproveStartup}, kind)
}

type AuditReader interface {
	Read(repository, branch string, limit int) ([]AuditEntry, error)
}

type AuditView struct {
	Entries  []AuditEntry
	Refusals map[string]int
}

type AuditPage interface {
	View(sid string) (AuditView, error)
	Refused(sid, kind string)
}

type AuditLog struct {
	reader  AuditReader
	targets func(sid string) (PushTarget, bool)
	mu      sync.Mutex
	counts  map[string]map[string]int
}

var _ AuditPage = (*AuditLog)(nil)

func NewAuditLog(reader AuditReader, targets func(sid string) (PushTarget, bool)) *AuditLog {
	return &AuditLog{reader: reader, targets: targets, counts: map[string]map[string]int{}}
}

func (a *AuditLog) View(sid string) (AuditView, error) {
	t, ok := a.targets(sid)
	if !ok {
		return AuditView{}, explained(conflict("not started here"), "Only a session this engine started has an audit view here.")
	}
	entries, err := a.reader.Read(t.Name, t.Branch, MaxAuditShown)
	if err != nil {
		return AuditView{}, explained(conflict("audit"), "The engine couldn't read its audit file.")
	}
	a.mu.Lock()
	counts := map[string]int{}
	maps.Copy(counts, a.counts[t.Name])
	a.mu.Unlock()
	return AuditView{Entries: entries, Refusals: counts}, nil
}

func (a *AuditLog) Refused(sid, kind string) {
	if !slices.Contains([]string{RefusedPush, RefusedRemote, RefusedProtected, RefusedCloud}, kind) {
		return
	}
	t, ok := a.targets(sid)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m := a.counts[t.Name]
	if m == nil {
		m = map[string]int{}
		a.counts[t.Name] = m
	}
	m[kind] = min(m[kind]+1, maxRefusalKind)
}
