package review

import (
	"regexp"
	"strings"
)

type Role string

const (
	BugHunter  Role = "bug-hunter"
	Simplicity Role = "simplicity"
)

var Roles = []Role{BugHunter, Simplicity}

func (r Role) Name() string {
	switch r {
	case BugHunter:
		return "bug hunter"
	case Simplicity:
		return "simplicity checker"
	}
	return string(r)
}

type Offer struct {
	Agent  string
	Source string
	Plugin string
}

const knackpackProvider = "knackpack@knackpack"

var (
	repositorySource = regexp.MustCompile(`(?i)^(project|local)`)
	pluginID         = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)
)

func RoleProblem(agent string, o *Offer) string {
	const only = "a role can only be filled by an agent from an installed plugin or your own agents folder"
	if o == nil {
		return agent + " isn't offered in this session: install the plugin that provides it, or put it in your own agents folder"
	}
	if o.Plugin == "project" || repositorySource.MatchString(o.Source) {
		return agent + " is defined in this repository, and " + only
	}
	fromPlugin := o.Source == "plugin" && o.Plugin != "" && o.Plugin != "engine"
	if !fromPlugin && o.Source != "userSettings" && o.Source != "policySettings" {
		switch {
		case o.Source == "built-in" || o.Plugin == "engine":
			return agent + " is one of Claude Code's built-in agents, and " + only
		case o.Source != "":
			return agent + " comes from the " + clip(o.Source, 40) + " source, and " + only
		}
		return agent + " comes from an unknown source, and " + only
	}
	if strings.HasPrefix(agent, "knackpack:") && o.Plugin != knackpackProvider {
		from := "an unknown provider"
		if o.Plugin != "" {
			from = "the plugin " + clip(o.Plugin, 80)
		}
		return agent + " comes from " + from + ", not from knackpack's own marketplace (" + knackpackProvider + ")"
	}
	return ""
}

func spawned(agent, provider string, o *Offer) *Offer {
	if o != nil && o.Agent == agent && o.Plugin == provider {
		return o
	}
	source := ""
	if pluginID.MatchString(provider) {
		source = "plugin"
	}
	return &Offer{Agent: agent, Source: source, Plugin: provider}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
