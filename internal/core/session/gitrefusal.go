package session

import (
	"slices"
	"strings"
)

const (
	GitHostOnlyEngine = "Software Factory refuses every grant and credential that would open a git host"
	GitToolsWords     = "Software Factory reaches the git host for you: push with factory_push, open a pull request with factory_pull_request or an issue with factory_issue, and read CI with factory_checks; don't reach the git host another way, and tell the person what was refused and why"
	gitDroppedEvent   = "git-host-dropped"
)

func gitAPIHost(host string) string {
	if strings.HasSuffix(host, ".ghe.com") && !strings.HasPrefix(host, "api.") {
		return "api." + host
	}
	return ""
}

func withAPIHosts(hosts []string) []string {
	out := slices.Clone(hosts)
	for _, h := range hosts {
		if api := gitAPIHost(h); api != "" {
			out = append(out, api)
		}
	}
	return out
}

func gitRefusal(a accessTarget) error {
	switch {
	case len(a.gits) == 0:
		return nil
	case a.cred != "":
		return onField("credential", conflict("git host", "Bound to a git host: only Software Factory reaches it."))
	case a.gitAddr != "":
		return onField("host", conflict("git host", "An address of a git host: only Software Factory reaches it."))
	}
	return onField("host", conflict("git host", "A git host: only Software Factory reaches it."))
}

func agentGitWords(a accessTarget) string {
	if a.cred != "" {
		return "a grant can never hand over " + a.cred + ": it is bound to " + hostList(a.gits) + ", a git host, and " + GitHostOnlyEngine + ". " + GitToolsWords
	}
	host := targetHost(a.what())
	why := host + " is a git host"
	switch {
	case a.gitAddr != "":
		why = host + " is an address of " + a.gitAddr + ", a git host"
	case strings.HasPrefix(host, "."):
		why = host + " covers a git host"
	}
	return "a grant can never open " + a.what() + ": " + why + ", and " + GitHostOnlyEngine + ". " + GitToolsWords
}

type droppedCard struct {
	sid  string
	what string
	gits []string
}

func (g *Grants) cardHostsLocked(c *accessCard) ([]string, string) {
	if c.cred == "" {
		return []string{c.target()}, c.target()
	}
	cred, ok := g.credentialByID(c.cred)
	if !ok {
		return nil, ""
	}
	return cred.Bound, cred.Name
}

func (g *Grants) dropGitCardsLocked(git func(string) string) []droppedCard {
	var out []droppedCard
	for sid, list := range g.cards {
		kept := make([]*accessCard, 0, len(list))
		for _, c := range list {
			if c.kind != AccessAction {
				hosts, what := g.cardHostsLocked(c)
				if gits := gitsIn(hosts, git); len(gits) > 0 {
					if c.kind == AccessRequest {
						out = append(out, droppedCard{sid: sid, what: what, gits: gits})
					}
					continue
				}
			}
			kept = append(kept, c)
		}
		if len(kept) == 0 {
			delete(g.cards, sid)
		} else {
			g.cards[sid] = kept
		}
	}
	return out
}

func (g *Grants) tellGitTaken(taken []Grant, targets map[string]GrantTarget, git func(string) string) {
	for _, gr := range taken {
		said := "Software Factory took back " + gr.what() + " for " + scopeWords(gr.Scope, gr.Project) + ": " + hostList(gitsIn(gr.Hosts(), git)) + " became a git host, and " + GitHostOnlyEngine + "."
		for _, t := range targets {
			if !t.Live || !covers(gr, t) {
				continue
			}
			g.mu.Lock()
			notes := append(g.notes[t.SID], AccessNote{ID: newGrantID(), Text: said})
			if len(notes) > MaxAccessNotes {
				notes = notes[len(notes)-MaxAccessNotes:]
			}
			g.notes[t.SID] = notes
			g.mu.Unlock()
			g.cfg.Note(t.SID, said+" "+GitToolsWords+".")
		}
	}
}

func (g *Grants) dropGitOnLoad() {
	git := g.gitHosts()
	gone := map[string]bool{}
	if !g.credsBroken {
		kept := slices.DeleteFunc(slices.Clone(g.creds), func(c Credential) bool {
			gits := gitsIn(c.Bound, git)
			if len(gits) == 0 {
				return false
			}
			gone[c.ID] = true
			g.cfg.Logger.Info("credentials", "event", gitDroppedEvent, "name", c.Name, "host", gits[0])
			return true
		})
		if len(gone) > 0 && g.saveCredsLocked(kept) != nil {
			g.creds = kept
		}
	}
	if !g.broken {
		kept := slices.DeleteFunc(slices.Clone(g.grants), func(gr Grant) bool {
			gits := gitsIn(gr.Hosts(), git)
			if len(gits) == 0 {
				return false
			}
			g.cfg.Logger.Info("grants", "event", gitDroppedEvent, "host", gits[0])
			return true
		})
		if len(kept) != len(g.grants) && g.saveLocked(kept) != nil {
			g.grants = kept
		}
	}
	if !g.presetsBroken {
		kept := slices.DeleteFunc(slices.Clone(g.presets), func(p Preset) bool {
			hosts := []string{p.Host}
			switch {
			case gone[p.Credential]:
				g.cfg.Logger.Info("presets", "event", gitDroppedEvent, "name", p.Name)
				return true
			case p.Credential != "":
				c, ok := g.credentialByID(p.Credential)
				if !ok {
					return false
				}
				hosts = c.Bound
			case p.Port > 0:
				hosts = []string{PortTarget(p.Host, p.Port)}
			}
			gits := gitsIn(hosts, git)
			if len(gits) == 0 {
				return false
			}
			g.cfg.Logger.Info("presets", "event", gitDroppedEvent, "name", p.Name, "host", gits[0])
			return true
		})
		if len(kept) != len(g.presets) && g.savePresetsLocked(kept) != nil {
			g.presets = kept
		}
	}
	if g.cfg.Actions.Store != nil && !g.actionsBroken {
		kept := slices.DeleteFunc(slices.Clone(g.actions), func(a Action) bool {
			host := g.actionGitHost(a.Target)
			if host == "" {
				return false
			}
			g.cfg.Logger.Info("actions", "event", gitDroppedEvent, "name", a.Name, "host", host)
			return true
		})
		if len(kept) != len(g.actions) && g.saveActionsLocked(kept) != nil {
			g.actions = kept
		}
	}
}
