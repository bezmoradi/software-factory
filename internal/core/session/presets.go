package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxPresets    = 32
	MaxPresetName = 60
	AnyProject    = ""
)

type Preset struct {
	ID         string
	Name       string
	Host       string
	Port       int
	Credential string
	Scope      string
	Expires    string
	Project    string
	Auto       bool
	Made       time.Time
}

type PresetStore interface {
	Load() ([]Preset, error)
	Save([]Preset) error
}

type PresetView struct {
	Preset
	What         string
	ScopeLabel   string
	ExpiresLabel string
	Level        string
	Label        string
	Off          string
}

type PresetDraft struct {
	Name       string
	Host       string
	Port       int
	Credential string
	Scope      string
	Expires    string
	Project    string
	Session    string
	Auto       bool
	Confirmed  bool
}

func (g *Grants) savePresetsLocked(next []Preset) error {
	if err := g.cfg.Presets.Save(slices.Clone(next)); err != nil {
		g.cfg.Logger.Warn("presets", "event", "not-saved")
		return conflict("state", "The engine couldn't save its presets, so nothing changed.")
	}
	g.presets = next
	return nil
}

func (g *Grants) presetTargetLocked(p Preset) (accessTarget, bool) {
	switch {
	case p.Credential == "" && p.Port > 0:
		return g.portTarget(p.Host, p.Port), true
	case p.Credential == "":
		return g.hostTarget(p.Host), true
	}
	c, ok := g.credentialByID(p.Credential)
	if !ok || g.credsBroken {
		return accessTarget{}, false
	}
	return g.credentialTarget(c), true
}

func projectWords(project string) string {
	if project == AnyProject {
		return "its project"
	}
	return project
}

func (g *Grants) presetViewsLocked(project string, applicable bool) []PresetView {
	out := []PresetView{}
	if g.presetsBroken {
		return out
	}
	for _, p := range g.presets {
		if applicable && p.Project != AnyProject && p.Project != project {
			continue
		}
		a, ok := g.presetTargetLocked(p)
		if !ok {
			continue
		}
		r := riskFor(a, p.Scope, p.Expires, projectWords(p.Project))
		out = append(out, PresetView{Preset: p, What: a.what(), ScopeLabel: choiceLabel(GrantScopes, p.Scope), ExpiresLabel: choiceLabel(GrantExpiries, p.Expires), Level: r.Level, Label: r.Label})
	}
	return out
}

func cleanPresetName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || !utf8.ValidString(n) || utf8.RuneCountInString(n) > MaxPresetName || hostReasonControl.MatchString(n) {
		return "", onField("name", invalid("name", "Plain text on one line, 1 to 60 characters."))
	}
	return n, nil
}

func (g *Grants) PresetRisk(d PresetDraft) (AccessRisk, error) {
	p, a, err := g.draftPreset(d)
	if err != nil {
		return AccessRisk{}, err
	}
	return riskFor(a, p.Scope, p.Expires, projectWords(p.Project)), nil
}

func (g *Grants) draftPreset(d PresetDraft) (Preset, accessTarget, error) {
	p := Preset{Scope: d.Scope, Expires: d.Expires, Project: d.Project, Auto: d.Auto}
	if d.Session != "" {
		t, err := g.target(d.Session)
		if err != nil {
			return Preset{}, accessTarget{}, err
		}
		p.Project = t.Project
	} else if p.Project != AnyProject && !slices.Contains(g.cfg.Projects(), p.Project) {
		return Preset{}, accessTarget{}, onField("project", notFound("project", "No project of that name."))
	}
	if !grantChoice(GrantScopes, p.Scope) {
		return Preset{}, accessTarget{}, onField("scope", invalid("scope", "Choose this session or this project."))
	}
	if !grantChoice(GrantExpiries, p.Expires) {
		return Preset{}, accessTarget{}, onField("expires", invalid("expires", "Choose 1 hour, 1 day, 1 week or until taken back."))
	}
	if p.Auto && p.Project == AnyProject {
		return Preset{}, accessTarget{}, onField("project", invalid("project", "Needs the project whose sessions it applies to."))
	}
	if (d.Host == "") == (d.Credential == "") || d.Credential != "" && d.Port != 0 {
		return Preset{}, accessTarget{}, onField("credential", invalid("credential", "Choose a credential or give a host."))
	}
	a, err := g.targetOf(grantMaking{host: d.Host, port: d.Port, cred: d.Credential})
	if err != nil {
		return Preset{}, accessTarget{}, err
	}
	if d.Credential != "" {
		c, _ := g.credentialRule(d.Credential)
		p.Credential = c.ID
	} else {
		p.Host, p.Port = a.host, a.port
	}
	return p, a, nil
}

func (g *Grants) SavePreset(ctx context.Context, d PresetDraft) (string, error) {
	name, err := cleanPresetName(d.Name)
	if err != nil {
		return "", err
	}
	p, a, err := g.draftPreset(d)
	if err != nil {
		return "", err
	}
	if r := riskFor(a, p.Scope, p.Expires, projectWords(p.Project)); r.Accept != "" && !d.Confirmed {
		return "", onField("confirmed", invalid("confirm", "Tick the box to accept the risk first."))
	}
	p.ID, p.Name, p.Made = newGrantID(), name, g.cfg.Now().Truncate(time.Second)
	g.mu.Lock()
	if g.presetsBroken {
		g.mu.Unlock()
		return "", conflict("state", "The engine couldn't read presets.json, so it saves no preset. Move the file out of the engine's state folder and restart the engine.")
	}
	if slices.ContainsFunc(g.presets, func(x Preset) bool { return strings.EqualFold(x.Name, name) }) {
		g.mu.Unlock()
		return "", onField("name", conflict("name", "A preset of that name is saved."))
	}
	if len(g.presets) >= MaxPresets {
		g.mu.Unlock()
		return "", limited("presets", "Software Factory keeps at most "+strconv.Itoa(MaxPresets)+" presets. Delete one first.")
	}
	err = g.savePresetsLocked(append(slices.Clone(g.presets), p))
	g.mu.Unlock()
	if err != nil {
		return "", err
	}
	shared.For(ctx, g.cfg.Logger).Info("presets", "event", "saved", "auto", p.Auto)
	said := "Saved the preset " + name + "."
	if p.Auto {
		said += " It applies to each session of " + p.Project + " when it starts, with a notification and no card."
	}
	return said, nil
}

func (g *Grants) DeletePreset(ctx context.Context, id string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.presetsBroken {
		return "", conflict("state", "The engine couldn't read presets.json, so it deletes no preset.")
	}
	i := slices.IndexFunc(g.presets, func(p Preset) bool { return p.ID == id })
	if i < 0 {
		return "", notFound("preset", "That preset isn't there any more. Reload the page.")
	}
	name := g.presets[i].Name
	if err := g.savePresetsLocked(slices.Delete(slices.Clone(g.presets), i, i+1)); err != nil {
		return "", err
	}
	shared.For(ctx, g.cfg.Logger).Info("presets", "event", "deleted")
	return "Deleted the preset " + name + ". Grants made with it stay until they end or you take them back.", nil
}

func (g *Grants) presetByID(id string) (Preset, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.presets {
		if p.ID == id && !g.presetsBroken {
			return p, true
		}
	}
	return Preset{}, false
}

func (g *Grants) presetMaking(p Preset, askedBy string) (grantMaking, error) {
	m := grantMaking{host: p.Host, port: p.Port, cred: p.Credential, scope: p.Scope, expires: p.Expires, askedBy: askedBy, preset: p.Name}
	a, err := g.ruledTarget(m)
	if err != nil {
		return grantMaking{}, err
	}
	if len(a.gits) > 0 {
		return grantMaking{}, conflict("git host", "The preset "+p.Name+" opens "+hostList(a.gits)+", which became a git host after it was saved, and "+GitHostOnlyEngine+", so it wasn't applied. Delete the preset.")
	}
	return m, nil
}

func (g *Grants) ApplyPreset(ctx context.Context, sid, id string) (string, error) {
	t, err := g.target(sid)
	if err != nil {
		return "", err
	}
	p, ok := g.presetByID(id)
	if !ok || p.Project != AnyProject && p.Project != t.Project {
		return "", notFound("preset", "That preset isn't there for this session's project. Reload the page.")
	}
	m, err := g.presetMaking(p, AskedByPerson)
	if err != nil {
		return "", err
	}
	gr, err := g.makeGrant(ctx, t, m)
	if err != nil {
		return "", err
	}
	said := "Applied the preset " + p.Name + ": " + gr.what() + " for " + scopeWords(gr.Scope, gr.Project) + ", " + grantForWords[p.Expires] + "."
	if g.apply(ctx) != nil {
		return said, conflict("egress", "Saved, but egress didn't take it yet, so the agent can't reach "+hostList(gr.Hosts())+" until it does. The engine sets it again within 30 seconds.")
	}
	return said, nil
}

func (g *Grants) autoApply(ctx context.Context, targets map[string]GrantTarget) {
	type due struct {
		t GrantTarget
		p Preset
	}
	var list []due
	g.mu.Lock()
	if !g.presetsBroken && !g.broken {
		for _, t := range targets {
			if !t.Live || !t.Running {
				continue
			}
			for _, p := range g.presets {
				if !p.Auto || p.Project != t.Project || g.autoDone[t.SID][p.ID] {
					continue
				}
				if g.autoDone[t.SID] == nil {
					g.autoDone[t.SID] = map[string]bool{}
				}
				g.autoDone[t.SID][p.ID] = true
				list = append(list, due{t, p})
			}
		}
	}
	g.mu.Unlock()
	for _, d := range list {
		m, err := g.presetMaking(d.p, AskedByPreset)
		var gr Grant
		if err == nil {
			gr, err = g.makeGrant(ctx, d.t, m)
		}
		text := "Applied the preset " + d.p.Name + " at the start: " + gr.what() + " for " + scopeWords(gr.Scope, gr.Project) + ", " + grantForWords[d.p.Expires] + "."
		if err != nil {
			shared.For(ctx, g.cfg.Logger).Warn("presets", "event", "not-applied")
			text = "The preset " + d.p.Name + " couldn't be applied at the start: " + errorWords(err)
		}
		g.mu.Lock()
		notes := append(g.notes[d.t.SID], AccessNote{ID: newGrantID(), Text: text})
		if len(notes) > MaxAccessNotes {
			notes = notes[len(notes)-MaxAccessNotes:]
		}
		g.notes[d.t.SID] = notes
		g.mu.Unlock()
	}
}

func errorWords(err error) string {
	if e, ok := errors.AsType[*Error](err); ok && e.Message != "" {
		return e.Message
	}
	return "the engine couldn't make the grant."
}
