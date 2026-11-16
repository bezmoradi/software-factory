package server

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/adapters/git/remote"
	"github.com/bezmoradi/software-factory/internal/adapters/git/state"
	"github.com/bezmoradi/software-factory/internal/adapters/install/releases"
	"github.com/bezmoradi/software-factory/internal/adapters/sandbox/boxes"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	devUpdateAPI   = "SF_DEV_UPDATE_API"
	devUpdateReuse = time.Minute
)

func developmentImage(marker string) bool {
	st, err := os.Stat(marker)
	return err == nil && st.Mode().IsRegular()
}

func releaseSource(getenv func(string) string, cfg PageConfig, dev bool, logger shared.Logger) (session.ReleaseSource, bool, error) {
	fake := getenv(devUpdateAPI)
	if fake == "" {
		return releases.Source{Client: releases.NewClient(), API: releases.DefaultAPI}, false, nil
	}
	if !dev {
		logger.Warn("update check", "event", "dev-api-ignored", "why", devUpdateAPI+" points the update check at another server, so the engine honours it only in a development image, and this image has no "+boxes.DevMarker)
		return releases.Source{Client: releases.NewClient(), API: releases.DefaultAPI}, false, nil
	}
	u, err := url.Parse(fake)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, true, errors.New(devUpdateAPI + " must be an https URL with no user, query or fragment, such as https://gitserver.test:8443/release-api")
	}
	client, err := remote.Remote{CAFile: cfg.DevGitCA}.HTTPClient()
	if err != nil {
		return nil, true, err
	}
	logger.Warn("development switch: the update check asks a fake release server, not api.github.com, and reuses a stored result at start for a minute only", "variable", devUpdateAPI)
	return releases.Source{Client: releases.Harden(client), API: strings.TrimSuffix(fake, "/")}, true, nil
}

func newUpdates(getenv func(string) string, cfg PageConfig, logger shared.Logger) *session.Updates {
	dev := developmentImage(boxes.DevMarker)
	ucfg := session.UpdatesConfig{Version: Version, Store: state.NewUpdatePrefs(cfg.StateDir), Forced: getenv("SF_NO_UPDATE_CHECK") != "", Development: dev, Logger: logger}
	if !ucfg.Forced {
		src, fake, err := releaseSource(getenv, cfg, dev, logger)
		if err != nil {
			logger.Warn("update check", "event", "dev-api-refused", "why", err.Error())
		}
		ucfg.Source = src
		if fake {
			ucfg.Reuse = devUpdateReuse
		}
	}
	return session.NewUpdates(ucfg)
}

func updateFacts(u session.UpdatesPage) []session.DiagFact {
	if u == nil {
		return []session.DiagFact{{Name: "update check", Value: "off"}}
	}
	v := u.View()
	on := "on"
	switch {
	case v.Forced:
		on = "off by SF_NO_UPDATE_CHECK"
	case v.Unreadable:
		on = "off: settings unreadable"
	case !v.Check:
		on = "off"
	}
	return []session.DiagFact{{Name: "update check", Value: on}, {Name: "update check result", Value: v.Result}}
}
