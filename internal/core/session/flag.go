package session

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	MaxFlaggedFile  = 1 << 20
	FlagBudget      = 32 << 20
	maxFlagVersions = 20
	maxFlagged      = 200
	keptScans       = 16
)

type FlagKind string

const (
	FlagName   FlagKind = "name"
	FlagLarge  FlagKind = "large"
	FlagSecret FlagKind = "secret"
)

var (
	flagName = regexp.MustCompile(`(?i)(^|/)([^/]+\.(pem|key|p12|pfx|keystore|jks|tfstate|tfstate\.backup)|id_(rsa|dsa|ecdsa|ed25519)|credentials[^/]*)$`)
	flagText = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-ant-[A-Za-z0-9_-]{20,}|sk-(proj-)?[A-Za-z0-9_-]{32,}|glpat-[A-Za-z0-9_-]{20,})\b|\bAIza[0-9A-Za-z_-]{35}`)
	flagWhy  = map[FlagKind]string{
		FlagName:   "its name is one that usually holds secrets",
		FlagLarge:  "it is larger than 1 MiB",
		FlagSecret: "it contains what looks like a private key or an access token",
	}
)

type PushedFile struct {
	Path string
	SHA  string
	Mode push.Mode
	Size int64
	Data []byte
}

type PushedSet struct {
	Files      []PushedFile
	Attributes map[string][]byte
	Complete   bool
}

type PushContents interface {
	Pushed(repo, base, head string, budget int64) (PushedSet, error)
}

type Flag struct {
	Path     string
	Kind     FlagKind
	Why      string
	Size     int64
	Versions []string
	Full     bool
}

type FlagView struct {
	Flag
	Allowed bool
}

type scanned struct {
	flags    []Flag
	complete bool
	lfs      []string
}

func secretName(p string) bool {
	base := strings.ToLower(path.Base(p))
	if base == ".env" {
		return true
	}
	if rest, ok := strings.CutPrefix(base, ".env."); ok && rest != "" {
		return rest != "example" && rest != "sample" && rest != "template"
	}
	return flagName.MatchString(p)
}

func flagOf(f PushedFile) (FlagKind, bool) {
	switch {
	case secretName(f.Path):
		return FlagName, true
	case f.Size < 0 || f.Size > MaxFlaggedFile:
		return FlagLarge, true
	case f.Data != nil && flagText.Match(f.Data):
		return FlagSecret, true
	}
	return "", false
}

func scanPushed(set PushedSet) scanned {
	out := scanned{complete: set.Complete}
	byPath := map[string]int{}
	var paths []string
	for _, f := range set.Files {
		if f.Mode == push.ModeSubmodule || f.Mode == push.ModeFolder {
			continue
		}
		if !slices.Contains(paths, f.Path) {
			paths = append(paths, f.Path)
		}
		if f.Data == nil && f.Size >= 0 && f.Size <= MaxFlaggedFile && !secretName(f.Path) {
			out.complete = false
			continue
		}
		kind, flagged := flagOf(f)
		if !flagged {
			continue
		}
		i, seen := byPath[f.Path]
		if !seen {
			if len(out.flags) >= maxFlagged {
				out.complete = false
				continue
			}
			byPath[f.Path] = len(out.flags)
			out.flags = append(out.flags, Flag{Path: f.Path, Kind: kind, Why: flagWhy[kind], Size: f.Size})
			i = len(out.flags) - 1
		}
		fl := &out.flags[i]
		if f.Size > fl.Size {
			fl.Size = f.Size
		}
		switch {
		case fl.Full || slices.Contains(fl.Versions, f.SHA):
		case len(fl.Versions) >= maxFlagVersions:
			fl.Full, fl.Versions = true, nil
		default:
			fl.Versions = append(fl.Versions, f.SHA)
		}
	}
	slices.SortFunc(out.flags, func(a, b Flag) int { return strings.Compare(a.Path, b.Path) })
	out.lfs = workflow.LFSPaths(set.Attributes, paths)
	return out
}

func allowedFlag(f Flag, allowed map[string]map[string]bool) bool {
	set := allowed[f.Path]
	if f.Full || len(f.Versions) == 0 || set == nil {
		return false
	}
	for _, v := range f.Versions {
		if !set[v] {
			return false
		}
	}
	return true
}
