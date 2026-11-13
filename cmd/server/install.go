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

type InstallConfig struct {
	Dir      string
	Registry string
	AtStart  bool
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
	pinned := pins.List()
	for _, id := range args {
		if !slices.ContainsFunc(known, func(a npm.Agent) bool { return a.ID == id }) && !slices.ContainsFunc(pinned, func(p npm.Plugin) bool { return p.ID == id }) {
			return fmt.Errorf("install-agents: no pinned agent or plugin %q", id)
		}
	}
	var errs []error
	for _, a := range known {
		if len(args) > 0 && !slices.Contains(args, a.ID) {
			continue
		}
		if len(args) == 0 && inst.Installed(a.ID) {
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
	for _, p := range pinned {
		if len(args) > 0 && !slices.Contains(args, p.ID) {
			continue
		}
		if len(args) == 0 && pins.Installed(p.ID) {
			say(fmt.Sprintf("%s %s is installed at commit %s.", p.Name, p.Version, p.Commit))
			continue
		}
		if err := pins.Install(ctx, p.ID, say); err != nil {
			say(err.Error())
			errs = append(errs, err)
			continue
		}
		say(fmt.Sprintf("%s %s is installed at commit %s.", p.Name, p.Version, p.Commit))
	}
	return errors.Join(errs...)
}
