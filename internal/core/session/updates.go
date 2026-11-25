package session

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	UpdateEvery    = 24 * time.Hour
	updateCheckFor = time.Minute
	updateTries    = 3
	maxReleaseTags = 500

	UpdateNotYet     = "not-yet"
	UpdateOffByEnv   = "off-by-variable"
	UpdateUnreadable = "settings-unreadable"
	UpdateNewer      = "newer"
	UpdateCurrent    = "current"
	UpdateNoRelease  = "no-release"
	UpdateOwnVersion = "own-version-unparsable"
	UpdateOffline    = "offline"
	UpdateTimeout    = "timeout"
	UpdateRefused    = "refused"
	UpdateMalformed  = "malformed"
	UpdateUntrusted  = "untrusted"
)

var (
	releaseTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
	failureKinds      = []string{UpdateOffline, UpdateTimeout, UpdateRefused, UpdateMalformed, UpdateUntrusted}
	checkedKinds      = []string{UpdateNewer, UpdateCurrent, UpdateNoRelease, UpdateOwnVersion, UpdateOffline, UpdateTimeout, UpdateRefused, UpdateMalformed, UpdateUntrusted}

	ErrUpdateForced     = errors.New("SF_NO_UPDATE_CHECK=1 turns the update check off for this engine, so the page can't change it")
	ErrUpdateUnreadable = errors.New("the engine can't read its update settings (update.json), so it doesn't change them")
	ErrUpdateNotShown   = errors.New("that isn't the release the page shows")
)

type ReleaseFailure struct {
	Kind string
}

func (f ReleaseFailure) Error() string { return "the release check failed: " + f.Kind }

type ReleaseNotes struct {
	Found      bool
	Tag        string
	PreRelease bool
	Draft      bool
}

type ReleaseSource interface {
	Tags(ctx context.Context) ([]string, error)
	Release(ctx context.Context, tag string) (ReleaseNotes, error)
}

type UpdateLast struct {
	At      time.Time
	Kind    string
	Version string
}

type UpdateSettings struct {
	Later string
	Last  UpdateLast
}

func CleanUpdateLast(l UpdateLast) (UpdateLast, bool) {
	if l.At.IsZero() || !slices.Contains(checkedKinds, l.Kind) {
		return UpdateLast{}, false
	}
	if l.Kind != UpdateNewer {
		return UpdateLast{At: l.At, Kind: l.Kind}, l.Version == ""
	}
	version, ok := ParseReleaseVersion(l.Version)
	if !ok || version != l.Version {
		return UpdateLast{}, false
	}
	return l, true
}

type UpdateStore interface {
	Load() (UpdateSettings, error)
	Save(UpdateSettings) error
}

type UpdateView struct {
	Running     string
	Forced      bool
	Unreadable  bool
	Development bool
	Result      string
	Checked     time.Time
	Version     string
}

type UpdatesPage interface {
	View() UpdateView
	Later(version string) error
}

type UpdatesConfig struct {
	Version     string
	Source      ReleaseSource
	Store       UpdateStore
	Forced      bool
	Development bool
	Every       time.Duration
	Reuse       time.Duration
	Now         func() time.Time
	Logger      shared.Logger
}

type Updates struct {
	cfg     UpdatesConfig
	mu      sync.Mutex
	set     UpdateSettings
	broken  bool
	result  string
	checked time.Time
	found   string
}

var _ UpdatesPage = (*Updates)(nil)

type releaseVersion [3]int

func (v releaseVersion) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

func (v releaseVersion) Tag() string { return "v" + v.String() }

func parseReleaseTag(tag string) (releaseVersion, bool) {
	m := releaseTagPattern.FindStringSubmatch(tag)
	if m == nil {
		return releaseVersion{}, false
	}
	var v releaseVersion
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return releaseVersion{}, false
		}
		v[i] = n
	}
	return v, true
}

func ParseReleaseVersion(version string) (string, bool) {
	v, ok := parseReleaseTag("v" + version)
	if !ok {
		return "", false
	}
	return v.String(), true
}

func compareReleases(a, b releaseVersion) int {
	return slices.Compare(a[:], b[:])
}

func releaseKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return UpdateTimeout
	}
	if f, ok := errors.AsType[ReleaseFailure](err); ok && slices.Contains(failureKinds, f.Kind) {
		return f.Kind
	}
	return UpdateOffline
}

func NewUpdates(cfg UpdatesConfig) *Updates {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Every <= 0 {
		cfg.Every = UpdateEvery
	}
	if cfg.Reuse <= 0 || cfg.Reuse > cfg.Every {
		cfg.Reuse = cfg.Every
	}
	u := &Updates{cfg: cfg, result: UpdateNotYet}
	if cfg.Store != nil {
		set, err := cfg.Store.Load()
		if err != nil {
			u.broken = true
			u.log(UpdateUnreadable)
		} else {
			u.set = set
			u.adopt(set.Last)
		}
	}
	return u
}

func (u *Updates) adopt(l UpdateLast) {
	l, ok := CleanUpdateLast(l)
	if ok && l.Kind == UpdateNewer {
		ok = u.newerThanRunning(l.Version)
	}
	if _, own := ParseReleaseVersion(u.cfg.Version); !ok || !own {
		u.set.Last = UpdateLast{}
		return
	}
	u.result, u.checked = l.Kind, l.At
	u.found = l.Version
}

func (u *Updates) firstWait() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.checked.IsZero() {
		return 0
	}
	age := u.cfg.Now().Sub(u.checked)
	if age < 0 || age >= u.cfg.Reuse {
		return 0
	}
	return u.cfg.Reuse - age
}

func (u *Updates) newerThanRunning(version string) bool {
	own, ok := parseReleaseTag("v" + u.cfg.Version)
	found, okFound := parseReleaseTag("v" + version)
	return ok && okFound && compareReleases(found, own) > 0
}

func (u *Updates) log(kind string) {
	if u.cfg.Logger == nil {
		return
	}
	if kind == UpdateUntrusted {
		u.cfg.Logger.Warn("update check", "event", kind)
		return
	}
	u.cfg.Logger.Info("update check", "event", kind)
}

func (u *Updates) Watch(ctx context.Context) {
	if u.cfg.Forced {
		u.log(UpdateOffByEnv)
		return
	}
	if wait := u.firstWait(); wait > 0 {
		u.log("reused")
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	for {
		u.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(u.cfg.Every):
		}
	}
}

func (u *Updates) active() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return !u.broken && u.cfg.Source != nil
}

func (u *Updates) check(ctx context.Context) {
	if !u.active() {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, updateCheckFor)
	found, kind := u.lookup(cctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	u.mu.Lock()
	u.result = kind
	u.checked = u.cfg.Now()
	u.found = found
	saved := true
	if !u.broken && u.cfg.Store != nil {
		next := u.set
		next.Last = UpdateLast{At: u.checked, Kind: kind, Version: found}
		if u.cfg.Store.Save(next) == nil {
			u.set = next
		} else {
			saved = false
		}
	}
	u.mu.Unlock()
	u.log(kind)
	if !saved {
		u.log("not-saved")
	}
}

func (u *Updates) lookup(ctx context.Context) (string, string) {
	own, ok := parseReleaseTag("v" + u.cfg.Version)
	if !ok {
		return "", UpdateOwnVersion
	}
	tags, err := u.cfg.Source.Tags(ctx)
	if err != nil {
		return "", releaseKind(err)
	}
	if len(tags) > maxReleaseTags {
		tags = tags[:maxReleaseTags]
	}
	parsed := 0
	var newer []releaseVersion
	for _, t := range tags {
		v, ok := parseReleaseTag(t)
		if !ok {
			continue
		}
		parsed++
		if compareReleases(v, own) > 0 && !slices.Contains(newer, v) {
			newer = append(newer, v)
		}
	}
	if len(newer) == 0 {
		if parsed == 0 {
			return "", UpdateNoRelease
		}
		return "", UpdateCurrent
	}
	slices.SortFunc(newer, func(a, b releaseVersion) int { return compareReleases(b, a) })
	for _, v := range newer[:min(len(newer), updateTries)] {
		notes, err := u.cfg.Source.Release(ctx, v.Tag())
		if err != nil {
			return "", releaseKind(err)
		}
		if !notes.Found || notes.Draft || notes.PreRelease || notes.Tag != v.Tag() {
			continue
		}
		return v.String(), UpdateNewer
	}
	return "", UpdateNoRelease
}

func (u *Updates) shownLocked() bool {
	if u.cfg.Forced || u.broken || u.found == "" || !u.newerThanRunning(u.found) {
		return false
	}
	if u.set.Later == "" {
		return true
	}
	later, ok := parseReleaseTag("v" + u.set.Later)
	found, _ := parseReleaseTag("v" + u.found)
	return !ok || compareReleases(found, later) > 0
}

func (u *Updates) resultLocked() string {
	switch {
	case u.cfg.Forced:
		return UpdateOffByEnv
	case u.broken:
		return UpdateUnreadable
	}
	return u.result
}

func (u *Updates) View() UpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := UpdateView{Running: u.cfg.Version, Forced: u.cfg.Forced, Unreadable: u.broken, Development: u.cfg.Development, Result: u.resultLocked(), Checked: u.checked}
	if u.shownLocked() {
		v.Version = u.found
	}
	return v
}

func (u *Updates) save(next UpdateSettings) error {
	if u.cfg.Forced {
		return ErrUpdateForced
	}
	if u.broken {
		return ErrUpdateUnreadable
	}
	if u.cfg.Store != nil {
		if err := u.cfg.Store.Save(next); err != nil {
			return err
		}
	}
	u.set = next
	return nil
}

func (u *Updates) Later(version string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.shownLocked() || version != u.found {
		return ErrUpdateNotShown
	}
	next := u.set
	next.Later = version
	return u.save(next)
}
