package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/bezmoradi/software-factory/internal/adapters/install/npm"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const DefaultAgentsDir = "/opt/software-factory/agents"

const devKnackpackURL = "SF_DEV_KNACKPACK_URL"

type InstallConfig struct {
	Dir          string
	Registry     string
	AtStart      bool
	DevKnackpack string
	DevGitCA     string
}

func parseInstall(getenv func(string) string) (InstallConfig, error) {
	cfg := InstallConfig{Dir: DefaultAgentsDir, Registry: npm.DefaultRegistry, AtStart: true}
	switch v := getenv("SF_AGENTS_DIR"); {
	case v == "off":
		cfg.Dir = ""
	case v != "":
		if !filepath.IsAbs(v) {
			return cfg, errors.New("SF_AGENTS_DIR must be an absolute path, or off")
		}
		cfg.Dir = filepath.Clean(v)
	}
	if v := getenv("SF_NPM_REGISTRY"); v != "" {
		cfg.Registry = v
	}
	cfg.DevKnackpack, cfg.DevGitCA = getenv(devKnackpackURL), getenv("SF_DEV_GIT_CA")
	switch v := getenv("SF_AGENTS_INSTALL"); v {
	case "", "start":
	case "manual":
		cfg.AtStart = false
	default:
		return cfg, fmt.Errorf("SF_AGENTS_INSTALL must be start or manual, got %q", v)
	}
	return cfg, nil
}

func installer(cfg InstallConfig, logger shared.Logger) (*npm.Installer, error) {
	if cfg.Dir == "" {
		return nil, nil
	}
	return npm.New(npm.Config{Dir: cfg.Dir, Registry: cfg.Registry, Logger: logger})
}

func InstallAgents(ctx context.Context, args []string, logger shared.Logger) error {
	cfg, err := parseInstall(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.Dir == "" {
		return errors.New("install-agents: SF_AGENTS_DIR is off")
	}
	inst, err := installer(cfg, logger)
	if err != nil {
		return err
	}
	pins, err := plugins(cfg, logger)
	if err != nil {
		return err
	}
	say := func(s string) { _, _ = fmt.Fprintln(os.Stdout, s) }
	known := inst.Agents()
	tracked := pins.List()
	for _, id := range args {
		if !slices.ContainsFunc(known, func(a npm.Agent) bool { return a.ID == id }) && !slices.ContainsFunc(tracked, func(p npm.Tracked) bool { return p.ID == id }) {
			return fmt.Errorf("install-agents: no pinned agent or tracked plugin %q", id)
		}
	}
	var errs []error
	for _, a := range known {
		if len(args) > 0 && !slices.Contains(args, a.ID) {
			continue
		}
		if len(args) == 0 && inst.Installed(a.ID) {
			if err := inst.CheckInstalled(ctx, a.ID, say); err != nil {
				say(err.Error())
				errs = append(errs, err)
			}
			say(fmt.Sprintf("%s %s is installed.", a.Name, a.Version))
			continue
		}
		if err := inst.Install(ctx, a.ID, say); err != nil {
			say(err.Error())
			errs = append(errs, err)
			continue
		}
		say(fmt.Sprintf("%s %s is installed.", a.Name, a.Version))
	}
	for _, p := range tracked {
		if len(args) > 0 && !slices.Contains(args, p.ID) {
			continue
		}
		check, err := pins.Update(ctx, p.ID, say)
		cur, ok := pins.Current(p.ID)
		switch {
		case ok && cur.Failing != "":
			say(fmt.Sprintf("Not used: %s %s at commit %s doesn't pass this engine's install checks, so reviews and new sessions don't use it: %s Run install-agents again once the head of %s passes them. %s", p.Name, cur.Version, cur.Commit, cur.Failing, p.Branch, check.Problem))
			errs = append(errs, fmt.Errorf("%s: the installed commit %s fails the install checks", p.Name, cur.Commit))
		case ok && cur.Refused:
			say(fmt.Sprintf("Refused %s %s at commit %s: this engine's list of refused knackpack commits names it, so reviews and new sessions don't use it. %s", p.Name, cur.Version, cur.Commit, check.Problem))
			errs = append(errs, fmt.Errorf("%s: the installed commit %s is refused", p.Name, cur.Commit))
		case err != nil && ok:
			say(fmt.Sprintf("Kept %s %s at commit %s: %s", p.Name, cur.Version, cur.Commit, check.Problem))
			errs = append(errs, err)
		case err != nil:
			say(err.Error())
			errs = append(errs, err)
		default:
			say(fmt.Sprintf("%s %s at %s, the head of its %s branch.", p.Name, cur.Version, cur.Commit, p.Branch))
		}
	}
	return errors.Join(errs...)
}
