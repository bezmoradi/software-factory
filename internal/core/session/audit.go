package session

import (
	"maps"
	"slices"
	"sync"
)

const (
	AuditPush      = "push"
	AuditOverride  = "override"
	AuditAllowFile = "allow-file"
	MaxAuditShown  = 50
	maxRefusalKind = 1_000_000
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
)

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
	if !slices.Contains([]string{RefusedPush, RefusedRemote, RefusedProtected}, kind) {
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
