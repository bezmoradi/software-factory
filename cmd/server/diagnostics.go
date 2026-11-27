package server

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

type shownSetting struct {
	name  string
	shown func(v string) bool
}

var (
	shownSettings = []shownSetting{
		{"PORT", portWithin(0)},
		{"SF_PUBLIC_PORT", portWithin(1)},
		{"SF_OSV", oneOf("off")},
		{"SF_NO_UPDATE_CHECK", oneOf("1")},
		{"SF_SANDBOX_CHECK", oneOf("on", "off")},
		{"SF_UMASK", octalUmask},
		{"SF_DEV_SKIP_SIGNIN", oneOf("1")},
		{"SF_DEV_STATE_VERSION", oneOf("2")},
		{"SF_DEV_AUDIT_ROTATE", numberWithin(1024, 32<<20)},
		{devReviewMinutes, numberWithin(1, 19)},
	}
	namedSettings = []string{"SF_PAGE_HOST", "SF_PAGE_NAME", "SF_COMPOSE_PROJECT", "SF_REVIEWS", "SF_REVIEWER_PROXY", "SF_HELPER", "SF_BOX_SUBNETS", "SF_EGRESS_PROXY", "SF_EGRESS_STATUS",
		"SF_EGRESS_KEY_FILE", "EGRESS_ALLOW", "EGRESS_DENY", "SF_NPM_REGISTRY", "SF_DEV_ACP_AGENT", "SF_DEV_GIT_CA", "SF_DEV_OSV", "SF_DEV_ADVISORY_PIN", "SF_DEV_REVIEWER", devUpdateAPI, devKnackpackURL}
	digits     = regexp.MustCompile(`^[0-9]{1,9}$`)
	umaskDigit = regexp.MustCompile(`^0?[0-7]{3}$`)
)

func oneOf(words ...string) func(string) bool {
	return func(v string) bool { return slices.Contains(words, v) }
}

func numberWithin(low, high int) func(string) bool {
	return func(v string) bool {
		if !digits.MatchString(v) {
			return false
		}
		n, err := strconv.Atoi(v)
		return err == nil && n >= low && n <= high
	}
}

func portWithin(low int) func(string) bool {
	return numberWithin(low, 65535)
}

func octalUmask(v string) bool {
	if !umaskDigit.MatchString(v) {
		return false
	}
	n, err := strconv.ParseUint(v, 8, 32)
	return err == nil && n&^0o077 == 0 && n&0o070 <= 0o020
}

type aboutHelper interface {
	About() boxes.About
}

func settingsShown(getenv func(string) string) func() []session.DiagFact {
	return func() []session.DiagFact {
		var out []session.DiagFact
		for _, x := range shownSettings {
			v := getenv(x.name)
			switch {
			case v == "":
				v = "unset"
			case !x.shown(v):
				v = "set"
			}
			out = append(out, session.DiagFact{Name: strings.ToLower(x.name), Value: v})
		}
		for _, name := range namedSettings {
			v := "unset"
			if getenv(name) != "" {
				v = "set"
			}
			out = append(out, session.DiagFact{Name: strings.ToLower(name), Value: v})
		}
		return out
	}
}

func kernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func pluginFacts(pins *npm.Plugins) []session.DiagFact {
	if pins == nil {
		return nil
	}
	var out []session.DiagFact
	for _, p := range pins.List() {
		name := strings.ToLower(p.ID)
		installed := "not installed"
		if cur, ok := pins.Current(p.ID); ok {
			installed = strings.TrimSpace(cur.Version + " " + cur.Commit)
			switch {
			case cur.Failing != "":
				installed += " fails-install-checks"
			case cur.Refused:
				installed += " refused"
			}
		}
		check, running := pins.LastCheck(p.ID)
		result := check.Result
		switch {
		case running:
			result = session.PluginChecking
		case result == "":
			result = "none"
		}
		out = append(out,
			session.DiagFact{Name: name, Value: installed},
			session.DiagFact{Name: name + " follows", Value: p.Branch},
			session.DiagFact{Name: name + " last check", Value: result})
	}
	return out
}

func versionsShown(helper session.Boxes, pins *npm.Plugins) func(ctx context.Context) []session.DiagFact {
	return func(ctx context.Context) []session.DiagFact {
		out := []session.DiagFact{
			{Name: "go", Value: runtime.Version()},
			{Name: "os engine", Value: runtime.GOOS + "/" + runtime.GOARCH},
			{Name: "os engine kernel", Value: kernelRelease()},
		}
		for _, a := range npm.Pinned {
			out = append(out, session.DiagFact{Name: "pinned " + strings.ToLower(a.ID), Value: a.Version})
		}
		out = append(out, pluginFacts(pins)...)
		if helper == nil {
			return append(out, session.DiagFact{Name: "box helper", Value: "off"})
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := helper.List(lctx)
		cancel()
		if err != nil {
			out = append(out, session.DiagFact{Name: "box helper", Value: "unreachable"})
		}
		if h, ok := helper.(aboutHelper); ok {
			a := h.About()
			out = append(out,
				session.DiagFact{Name: "docker", Value: a.Docker},
				session.DiagFact{Name: "image", Value: a.Image},
				session.DiagFact{Name: "os docker", Value: a.OS},
				session.DiagFact{Name: "os docker type", Value: a.OSType},
				session.DiagFact{Name: "os docker arch", Value: a.Arch},
				session.DiagFact{Name: "os docker kernel", Value: a.Kernel},
				session.DiagFact{Name: "box runtime", Value: a.Runtime},
			)
		}
		return out
	}
}

func diagnosticsPage(parts engineParts, reg *session.Registry, updates session.UpdatesPage, logger shared.Logger) session.DiagnosticsPage {
	shown := settingsShown(os.Getenv)
	settings := func() []session.DiagFact { return append(shown(), updateFacts(updates)...) }
	cfg := session.DiagnosticsConfig{Version: Version, Versions: versionsShown(parts.boxes, parts.plugins), Settings: settings, Sessions: reg.Sessions, Usage: reg.Limits().UsageFacts}
	if parts.repos != nil {
		cfg.Projects = parts.repos.List
	}
	if parts.work != nil {
		cfg.Project = parts.work.Project
	}
	if parts.reviews != nil {
		cfg.Reviews = func() (map[review.Key]session.ReviewEntry, error) { return parts.reviews.Entries() }
	}
	if r, ok := logger.(shared.Recent); ok {
		cfg.Events = r.Recent
	}
	return session.NewDiagnostics(cfg)
}
