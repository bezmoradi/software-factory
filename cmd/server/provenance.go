package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/http/page"
	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/adapters/install/osv"
	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	fakeReviewer   = "sf-fake-claude"
	advisoriesEach = 24 * time.Hour
	advisoriesWait = 30 * time.Second
	mcpListFor     = 60 * time.Second
)

func pinnedProvenance(dev bool) (string, string) {
	plugin, claude := "", ""
	for _, p := range npm.PinnedPlugins {
		if p.ID == "knackpack" {
			plugin = p.Commit
		}
	}
	for _, a := range npm.Pinned {
		if a.ID == "claude" {
			claude = a.Version
		}
	}
	if dev {
		claude = fakeReviewer
	}
	return plugin, claude
}

func reviewerMinimum(plugin string) review.Minimum {
	return review.Minimum{ClaudeCode: npm.MinimumReviewerClaudeCode, Plugin: npm.MinimumReviewerKnackpack, Pinned: plugin}
}

var advisoryPackages = map[string]string{"claude": "@anthropic-ai/claude-code", "codex": "@openai/codex"}

func advisoryQueries(getenv func(string) string, logger shared.Logger) []osv.Query {
	var qs []osv.Query
	for _, a := range npm.Pinned {
		if name, ok := advisoryPackages[a.ID]; ok {
			qs = append(qs, osv.Query{Label: a.Name + " " + a.Version, Ecosystem: "npm", Name: name, Version: a.Version})
		}
	}
	for _, p := range npm.PinnedPlugins {
		if p.ID == "knackpack" {
			qs = append(qs, osv.Query{Label: p.Name + " " + p.Version + " (commit " + p.Commit[:7] + ")", Commit: p.Commit})
		}
	}
	if pin := getenv("SF_DEV_ADVISORY_PIN"); pin != "" {
		at := strings.LastIndex(pin, "@")
		if at > 0 {
			name, version := pin[:at], pin[at+1:]
			for i := range qs {
				if qs[i].Name == name {
					qs[i].Version = version
					qs[i].Label += " (looked up as " + version + ")"
					if logger != nil {
						logger.Warn("development advisory lookup for another version", "variable", "SF_DEV_ADVISORY_PIN")
					}
				}
			}
		}
	}
	return qs
}

type advisories struct {
	mu     sync.Mutex
	view   session.AdvisoryView
	client *http.Client
	api    string
	qs     []osv.Query
	logger shared.Logger
}

func watchAdvisories(ctx context.Context, getenv func(string) string, logger shared.Logger) *advisories {
	a := &advisories{client: osvClient(), api: osv.DefaultAPI, qs: advisoryQueries(getenv, logger), logger: logger}
	for _, q := range a.qs {
		a.view.Sent = append(a.view.Sent, q.Sent())
	}
	if getenv("SF_OSV") == "off" {
		a.view.Off = true
		return a
	}
	go func() {
		wait := advisoriesWait
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			a.check(ctx)
			wait = advisoriesEach
		}
	}()
	return a
}

func (a *advisories) check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	found, more, err := osv.Lookup(ctx, a.client, a.api, a.qs)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.view.Checked = time.Now().UTC()
	if err != nil {
		a.view.Problem = "The lookup at OSV didn't finish: " + err.Error() + ". The engine tries again in 24 hours."
		a.logger.Warn("advisory lookup", "event", "failed")
		return
	}
	a.view.Problem = ""
	a.view.More = more
	a.view.Found = a.view.Found[:0]
	for _, f := range found {
		a.view.Found = append(a.view.Found, session.Advisory{Label: f.Label, ID: f.ID, Summary: f.Summary, Aliases: f.Aliases, Link: f.Link})
	}
	if len(found) > 0 {
		a.logger.Warn("advisory lookup", "event", "found", "count", len(found), "more", more)
	}
}

func (a *advisories) View() session.AdvisoryView {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.view
	v.Found = slices.Clone(v.Found)
	v.Sent = slices.Clone(v.Sent)
	return v
}

type agentReport struct {
	home       *session.HomeSettings
	installer  *npm.Installer
	advisories *advisories
}

func (r agentReport) AgentReport() session.AgentReport {
	out := session.AgentReport{Home: r.home.View(), Advisories: r.advisories.View()}
	names := map[string]string{}
	for _, a := range npm.Pinned {
		names[a.ID] = a.Name
	}
	if r.installer != nil {
		for _, p := range r.installer.Provenance() {
			out.Pins = append(out.Pins, session.PinProvenance{Agent: p.Agent, Name: names[p.Agent], Package: p.Package, Version: p.Version, State: p.State,
				Repository: p.Repository, Workflow: p.Workflow, Ref: p.Ref, Commit: p.Commit, LoggedAt: p.LoggedAt, Note: p.Note})
		}
	}
	for _, p := range npm.PinnedPlugins {
		if p.ID == "knackpack" {
			out.Plugins = append(out.Plugins, session.PinnedPlugin{ID: p.ID, Name: p.Name, Version: p.Version, Commit: p.Commit})
		}
	}
	return out
}

func mcpList(cfg PageConfig) func() *session.Command {
	return func() *session.Command {
		for _, a := range session.Agents() {
			if a.Method == session.MethodPlugin && len(a.Command) > 0 && cfg.Workspace != "" {
				return &session.Command{Argv: []string{a.Command[0], "mcp", "list"}, Dir: cfg.Workspace, Pass: a.Pass, Timeout: mcpListFor}
			}
		}
		return nil
	}
}

func useAgentReports(base context.Context, srv *page.Server, parts engineParts, cfg PageConfig, logger shared.Logger) {
	report := agentReport{installer: parts.installer, advisories: watchAdvisories(base, os.Getenv, logger)}
	var rules session.AllowRulesPage
	if parts.routes != nil && parts.dispatcher != nil && parts.repos != nil {
		report.home = &session.HomeSettings{Store: state.NewHomeSettings(cfg.StateDir), MCP: mcpList(cfg), Logger: logger}
		parts.dispatcher.UseHomeSettings(report.home)
		rules = &session.AllowRules{Hosts: parts.dispatcher.RuleHosts, Repos: parts.repos, Audit: state.NewAudit(cfg.StateDir)}
	}
	srv.UseAgentReports(report, rules)
}

func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if first := via[0].URL; req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("refused a redirect from %s to another host or scheme", first.Host)
	}
	return nil
}

func osvClient() *http.Client {
	c := osv.NewClient()
	c.CheckRedirect = sameOriginRedirect
	return c
}

func CheckPins(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check-pins", flag.ContinueOnError)
	registry := fs.String("registry", npm.DefaultRegistry, "the npm registry to read attestations from")
	file := fs.String("attestations", "", "check this saved attestations file against one pinned package instead of the registry's")
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
	client := &http.Client{Timeout: time.Minute, CheckRedirect: sameOriginRedirect, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext}}
	if *file != "" {
		return checkSaved(*file, fs.Args(), say)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("check-pins: unexpected argument %q", fs.Arg(0))
	}
	var problems []error
	seen := map[string]bool{}
	for _, a := range npm.Pinned {
		for _, arch := range []string{"amd64", "arm64"} {
			for _, p := range a.Builds[arch].Packages {
				key := p.Name + "@" + p.Version
				if seen[key] {
					continue
				}
				seen[key] = true
				data, published, err := npm.FetchAttestations(ctx, client, *registry, p)
				if err != nil {
					say("%s: %s", key, err)
					problems = append(problems, err)
					continue
				}
				r := npm.Judge(a, p, data, published)
				say("%s", npm.ProvenanceLine(r))
				if r.State == npm.ProvenanceFailed {
					problems = append(problems, errors.New(key+": provenance failed"))
				}
			}
		}
	}
	for _, p := range npm.PinnedPlugins {
		say("%s %s: pinned by git commit %s; npm provenance doesn't apply to it, the commit is the pin.", p.Name, p.Version, p.Commit)
	}
	qs := advisoryQueries(os.Getenv, nil)
	sent := make([]string, 0, len(qs))
	for _, q := range qs {
		sent = append(sent, q.Sent())
	}
	say("Asking OSV (%s) about: %s.", osv.DefaultAPI, strings.Join(sent, ", "))
	found, more, err := osv.Lookup(ctx, osvClient(), osv.DefaultAPI, qs)
	switch {
	case err != nil:
		say("OSV: %s.", err)
		problems = append(problems, err)
	case len(found) == 0:
		say("OSV: no known advisory for a pinned version.")
	default:
		for _, f := range found {
			say("OSV: %s has %s %s: %s", f.Label, f.ID, strings.Join(f.Aliases, " "), f.Summary)
		}
		if more > 0 {
			say("OSV: and %d more advisories this list stops short of; read them at https://osv.dev.", more)
		}
		problems = append(problems, fmt.Errorf("%d known advisories for pinned versions", len(found)+more))
	}
	switch len(problems) {
	case 0:
		return nil
	case 1:
		return errors.New("check-pins: 1 problem; don't accept these pins until it is resolved")
	}
	return fmt.Errorf("check-pins: %d problems; don't accept these pins until each is resolved", len(problems))
}

func checkSaved(file string, args []string, say func(string, ...any)) error {
	if len(args) != 1 {
		return errors.New("check-pins: with -attestations, name one pinned package as name@version")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	for _, a := range npm.Pinned {
		for _, b := range a.Builds {
			for _, p := range b.Packages {
				if p.Name+"@"+p.Version != args[0] {
					continue
				}
				r := npm.Judge(a, p, data, true)
				say("%s", npm.ProvenanceLine(r))
				if r.State == npm.ProvenanceFailed {
					return errors.New("check-pins: provenance failed")
				}
				return nil
			}
		}
	}
	return fmt.Errorf("check-pins: %s isn't a pinned package", args[0])
}
