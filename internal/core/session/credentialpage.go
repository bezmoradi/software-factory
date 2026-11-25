package session

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const CredentialsNote = "Secrets an agent can use only after you grant them; never shown again."

type CredentialGrantView struct {
	ID         string
	Session    string
	Project    string
	Scope      string
	ScopeLabel string
	Until      string
	Expires    time.Time
	Who        string
	Lead       string
	Copy       string
}

var deliveryLeads = map[string]string{DeliveryHanded: "Handed over to", DeliverySigned: "Signs SSH logins for", DeliveryCopy: "Short-lived copies handed to"}

type CredentialEntry struct {
	CredentialInfo
	Grants []CredentialGrantView
}

type CredentialsView struct {
	Credentials []CredentialEntry
	Presets     []PresetView
	Projects    []string
	Note        string
	ActionsOn   bool
	Actions     []ActionView
}

type CredentialsPage interface {
	CredentialsView() (CredentialsView, error)
	SaveCredential(ctx context.Context, c Credential) (string, error)
	DeleteCredential(ctx context.Context, id string) (string, error)
	SavePreset(ctx context.Context, d PresetDraft) (string, error)
	DeletePreset(ctx context.Context, id string) (string, error)
	PresetRisk(d PresetDraft) (AccessRisk, error)
	SaveAction(ctx context.Context, d ActionDraft) (string, error)
	DeleteAction(ctx context.Context, id string) (string, error)
}

var _ CredentialsPage = (*Grants)(nil)

func credentialsUnread() error {
	return conflict("credentials", "The engine couldn't read its credentials file, so it saves, deletes and hands over no credential. Move the file out of the engine's state folder and restart the engine.")
}

func (g *Grants) CredentialsView() (CredentialsView, error) {
	now := g.cfg.Now()
	projects := g.cfg.Projects()
	targets := g.cfg.Targets()
	g.mu.Lock()
	defer g.mu.Unlock()
	v := CredentialsView{Credentials: []CredentialEntry{}, Presets: g.presetViewsLocked(AnyProject, false), Projects: projects, Note: CredentialsNote, ActionsOn: g.cfg.Actions.Store != nil, Actions: g.actionViewsLocked()}
	if g.credsBroken {
		return v, credentialsUnread()
	}
	for _, c := range g.creds {
		e := CredentialEntry{CredentialInfo: c.Info(), Grants: []CredentialGrantView{}}
		for _, gr := range g.grants {
			if gr.Credential != c.ID || !grantLive(gr, now) {
				continue
			}
			who := "Given by you"
			if gr.AskedBy == AskedByAgent {
				who = "Asked for by the agent"
			} else if gr.Preset != "" {
				who = "From the preset " + gr.Preset
			}
			view := CredentialGrantView{ID: gr.ID, Session: gr.Session, Project: gr.Project, Scope: gr.Scope, ScopeLabel: choiceLabel(GrantScopes, gr.Scope), Until: untilWords(gr.Expires), Expires: gr.Expires, Who: who, Lead: deliveryLeads[c.Delivery()]}
			if until := g.copyUntilLocked(c.ID, boxesFor(gr, targets), now); c.Delivery() == DeliveryCopy && !until.IsZero() {
				view.Copy = CopyKeptWords(until)
			}
			e.Grants = append(e.Grants, view)
		}
		v.Credentials = append(v.Credentials, e)
	}
	return v, nil
}

func (g *Grants) saveCredsLocked(next []Credential) error {
	if err := g.cfg.Creds.Save(slices.Clone(next)); err != nil {
		g.cfg.Logger.Warn("credentials", "event", "not-saved")
		return conflict("state", "The engine couldn't save its credentials, so nothing changed.")
	}
	g.creds = next
	return nil
}

func (g *Grants) dropCredentialLocked(id string, presets bool) ([]Grant, bool, error) {
	var taken []Grant
	if !g.broken {
		next := slices.DeleteFunc(slices.Clone(g.grants), func(x Grant) bool {
			if x.Credential == id {
				taken = append(taken, x)
				return true
			}
			return false
		})
		if len(taken) > 0 {
			if err := g.saveLocked(next); err != nil {
				return nil, false, err
			}
		}
	}
	dropped := false
	if presets && !g.presetsBroken {
		next := slices.DeleteFunc(slices.Clone(g.presets), func(p Preset) bool { return p.Credential == id })
		if len(next) != len(g.presets) {
			if err := g.savePresetsLocked(next); err != nil {
				return taken, false, err
			}
			dropped = true
		}
	}
	return taken, dropped, nil
}

func (g *Grants) SaveCredential(ctx context.Context, raw Credential) (string, error) {
	c, err := g.checkCredential(raw)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	if g.credsBroken {
		g.mu.Unlock()
		return "", credentialsUnread()
	}
	at := -1
	if c.ID != "" {
		at = slices.IndexFunc(g.creds, func(x Credential) bool { return x.ID == c.ID })
		if at < 0 {
			g.mu.Unlock()
			return "", notFound("credential", "That credential isn't there any more. Reload the page.")
		}
	}
	if slices.ContainsFunc(g.creds, func(x Credential) bool { return x.Name == c.Name && x.ID != c.ID }) {
		g.mu.Unlock()
		return "", onField("name", conflict("name", "A credential of that name is saved."))
	}
	if at < 0 && len(g.creds) >= MaxCredentials {
		g.mu.Unlock()
		return "", limited("credentials", "Software Factory keeps at most "+strconv.Itoa(MaxCredentials)+" credentials. Delete one first.")
	}
	c.Saved = g.cfg.Now().Truncate(time.Second)
	next := slices.Clone(g.creds)
	var taken []Grant
	var old Credential
	dropped := false
	if at >= 0 {
		old = g.creds[at]
		taken, dropped, err = g.dropCredentialLocked(c.ID, old.Type != c.Type || old.Delivery() != c.Delivery() || !slices.Equal(old.Bound, c.Bound))
		if err != nil {
			g.mu.Unlock()
			return "", err
		}
		next[at] = c
	} else {
		c.ID = newGrantID()
		next = append(next, c)
	}
	err = g.saveCredsLocked(next)
	g.mu.Unlock()
	if err != nil {
		return "", err
	}
	ended := 0
	if at >= 0 {
		ended = g.endActionWork(ctx, func(x actionKey) bool { return x.credential == c.ID }, "its credential was replaced")
	}
	for _, x := range taken {
		g.auditGrant(ctx, x, ByPage, ResultTakenBack, x.what()+" for "+scopeWords(x.Scope, x.Project)+", when the credential was replaced")
	}
	shared.For(ctx, g.cfg.Logger).Info("credentials", "event", "saved", "type", c.Type, "replaced", at >= 0)
	said := "Saved " + c.Name + ". The page never shows its secret again."
	if at >= 0 {
		said = "Replaced " + c.Name + "."
		if len(taken) > 0 {
			said += " " + grantCount(len(taken)) + " taken back, and " + g.goneWords(old, taken) + " Give them again to use the new one."
		}
		if dropped {
			said += " Its presets are deleted too, since what it opens changed."
		}
		if ended > 0 {
			said += " Action runs and grants with it that were still going are ended."
		}
	}
	if len(taken) > 0 && g.apply(ctx) != nil {
		return said, conflict("egress", "Saved, but egress didn't take the new lists yet. The engine sets them again within 30 seconds.")
	}
	return said, nil
}

func (g *Grants) DeleteCredential(ctx context.Context, id string) (string, error) {
	g.mu.Lock()
	if g.credsBroken {
		g.mu.Unlock()
		return "", credentialsUnread()
	}
	c, ok := g.credentialByID(id)
	if !ok {
		g.mu.Unlock()
		return "", notFound("credential", "That credential isn't there any more. Reload the page.")
	}
	taken, dropped, err := g.dropCredentialLocked(id, true)
	var acts []Action
	if err == nil {
		acts, err = g.dropActionsOfLocked(id)
	}
	if err == nil {
		err = g.saveCredsLocked(slices.DeleteFunc(slices.Clone(g.creds), func(x Credential) bool { return x.ID == id }))
	}
	g.mu.Unlock()
	if err != nil {
		return "", err
	}
	ended := g.endActionWork(ctx, func(x actionKey) bool { return x.credential == id }, "its credential was deleted")
	for _, x := range taken {
		g.auditGrant(ctx, x, ByPage, ResultTakenBack, x.what()+" for "+scopeWords(x.Scope, x.Project)+", when the credential was deleted")
	}
	shared.For(ctx, g.cfg.Logger).Info("credentials", "event", "deleted")
	said := "Deleted " + c.Name + "."
	if len(taken) > 0 {
		said += " " + grantCount(len(taken)) + " taken back, and " + g.goneWords(c, taken)
	}
	if dropped {
		said += " Its presets are deleted too."
	}
	if len(acts) > 0 {
		said += " Its actions are deleted too."
	}
	if ended > 0 {
		said += " Action runs and grants with it that were still going are ended."
	}
	if len(taken) > 0 && g.apply(ctx) != nil {
		return said, conflict("egress", "Deleted, but egress didn't take the new lists yet. The engine sets them again within 30 seconds.")
	}
	return said, nil
}

func (g *Grants) goneWords(c Credential, taken []Grant) string {
	switch c.Delivery() {
	case DeliverySigned:
		return "Software Factory signs no more SSH logins with it."
	case DeliveryCopy:
		targets := g.cfg.Targets()
		now := g.cfg.Now()
		var until time.Time
		g.mu.Lock()
		for _, x := range taken {
			if u := g.copyUntilLocked(c.ID, boxesFor(x, targets), now); u.After(until) {
				until = u
			}
		}
		g.mu.Unlock()
		w := "its short-lived copies are removed when the engine next reaches each session's container."
		if !until.IsZero() {
			w += " " + CopyKeptWords(until)
		}
		return w
	}
	return "its handed-over files and variables are removed when the engine next reaches each session's container; copies the agent made stay valid, so rotate the secret."
}

func grantCount(n int) string {
	if n == 1 {
		return "Its grant is"
	}
	return "Its " + strconv.Itoa(n) + " grants are"
}
