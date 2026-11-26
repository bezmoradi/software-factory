package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

var HandoverPendingNote = "Software Factory couldn't update the handed-over files in this session's container yet; it tries again every " + strconv.Itoa(int(ExpireWatch/time.Second)) + " seconds."

type HandedCredential struct {
	Name   string
	Type   string
	Bound  []string
	Secret CredentialSecret
	Token  string
}

type HandedAccess struct {
	Credentials []HandedCredential
	Ports       []string
}

type HandoverHost interface {
	Handover(ctx context.Context, box string, h HandedAccess) error
}

func handoverDigest(hand HandedAccess) string {
	list := hand.Credentials
	h := sha256.New()
	for _, p := range hand.Ports {
		_, _ = fmt.Fprintf(h, "%d:%s;", len(p), p)
	}
	_, _ = h.Write([]byte{'#'})
	for _, x := range list {
		s := x.Secret
		parts := []string{x.Name, x.Type, strings.Join(x.Bound, ","), x.Token, s.Variable, s.Value, s.KeyID, s.SecretKey, s.Role, s.Region, s.User, s.PrivateKey, s.Password, s.HostKey, s.Path, s.Mode, s.Contents}
		for _, v := range s.Vars {
			parts = append(parts, v.Name, v.Value)
		}
		for _, p := range parts {
			_, _ = fmt.Fprintf(h, "%d:%s;", len(p), p)
		}
		_, _ = h.Write([]byte{'|'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type handing struct {
	cred Credential
	end  time.Time
}

func grantEnd(covering []Grant, id string) time.Time {
	var end time.Time
	for _, gr := range covering {
		if gr.Credential != id {
			continue
		}
		if gr.Expires.IsZero() {
			return time.Time{}
		}
		if gr.Expires.After(end) {
			end = gr.Expires
		}
	}
	return end
}

func (g *Grants) handingsLocked(now []Grant, git func(string) string) []handing {
	var list []handing
	var taken []string
	slices.SortFunc(now, func(a, b Grant) int { return a.Made.Compare(b.Made) })
	for _, gr := range now {
		c, ok := g.credentialByID(gr.Credential)
		if gr.Credential == "" || !ok || slices.ContainsFunc(list, func(h handing) bool { return h.cred.ID == c.ID }) || len(gitsIn(c.Bound, git)) > 0 {
			continue
		}
		places := c.places()
		if slices.ContainsFunc(places, func(p string) bool { return slices.Contains(taken, p) }) {
			g.cfg.Logger.Warn("grants", "event", "handover-clash")
			continue
		}
		taken = append(taken, places...)
		list = append(list, handing{cred: c, end: grantEnd(now, c.ID)})
	}
	return list
}

func (g *Grants) handed(ctx context.Context, box string, list []handing, now time.Time, used map[string]bool) ([]HandedCredential, []string) {
	out := []HandedCredential{}
	var notes []string
	for _, h := range list {
		c := h.cred
		x := HandedCredential{Name: c.Name, Type: c.Type, Bound: slices.Clone(c.Bound), Secret: c.Secret}
		switch c.Delivery() {
		case DeliverySigned:
			x.Secret = CredentialSecret{User: c.Secret.User, HostKey: c.Secret.HostKey}
		case DeliveryCopy:
			used[copyKey(box, c)] = true
			cp, live := g.copyFor(ctx, box, c, h.end, now)
			if !live {
				if cp.note != "" {
					notes = append(notes, cp.note)
				}
				continue
			}
			x.Secret = CredentialSecret{KeyID: cp.KeyID, SecretKey: cp.SecretKey, Region: c.Secret.Region}
			x.Token = cp.Token
		}
		out = append(out, x)
	}
	slices.SortFunc(out, func(a, b HandedCredential) int { return strings.Compare(a.Name, b.Name) })
	return out, notes
}

func portsOf(now []Grant, handed []HandedCredential, git func(string) string) []string {
	list := []string{}
	slices.SortFunc(now, func(a, b Grant) int { return a.Made.Compare(b.Made) })
	for _, gr := range now {
		if gr.Credential != "" && !slices.ContainsFunc(handed, func(h HandedCredential) bool { return h.Name == gr.Name }) {
			continue
		}
		for _, e := range opens(gr, git) {
			if portEntry(e) && !slices.Contains(list, e) {
				list = append(list, e)
			}
		}
	}
	return list
}

func (g *Grants) deliver(ctx context.Context) {
	g.deliverMu.Lock()
	defer g.deliverMu.Unlock()
	now := g.cfg.Now()
	type send struct {
		box    string
		hand   HandedAccess
		digest string
	}
	type job struct {
		box      string
		covering []Grant
		list     []handing
	}
	var jobs []job
	var due []send
	live := map[string]bool{}
	used := map[string]bool{}
	git := g.gitHosts()
	changed := false
	g.mu.Lock()
	for _, t := range g.cfg.Targets() {
		if !t.Live || t.Box == "" {
			continue
		}
		live[t.Box] = true
		if !t.Running {
			continue
		}
		covering := g.coveringLocked(t, now)
		j := job{box: t.Box, covering: covering}
		if !g.credsBroken {
			j.list = g.handingsLocked(slices.Clone(covering), git)
		}
		jobs = append(jobs, j)
	}
	g.mu.Unlock()
	notes := map[string][]string{}
	for _, j := range jobs {
		hand := HandedAccess{}
		hand.Credentials, notes[j.box] = g.handed(ctx, j.box, j.list, now, used)
		hand.Ports = portsOf(j.covering, hand.Credentials, git)
		due = append(due, send{j.box, hand, handoverDigest(hand)})
	}
	g.mu.Lock()
	ready := due[:0]
	for _, d := range due {
		if !slices.Equal(g.copyNotes[d.box], notes[d.box]) {
			changed = true
			if len(notes[d.box]) == 0 {
				delete(g.copyNotes, d.box)
			} else {
				g.copyNotes[d.box] = notes[d.box]
			}
		}
		was, sent := g.delivered[d.box]
		if len(d.hand.Credentials) == 0 && len(d.hand.Ports) == 0 && !sent || was == d.digest {
			if g.unsent[d.box] {
				delete(g.unsent, d.box)
				changed = true
			}
			continue
		}
		ready = append(ready, d)
	}
	due = ready
	for box := range g.copyNotes {
		if _, ok := notes[box]; !ok {
			delete(g.copyNotes, box)
			changed = true
		}
	}
	g.dropCopiesLocked(live, used, now)
	for box := range g.delivered {
		if !live[box] {
			delete(g.delivered, box)
		}
	}
	for box := range g.unsent {
		if !live[box] {
			delete(g.unsent, box)
			changed = true
		}
	}
	g.mu.Unlock()
	for _, d := range due {
		err := errors.New("no route to the session's container")
		if host, ok := g.cfg.Host(d.box); ok {
			err = host.Handover(ctx, d.box, d.hand)
		}
		g.mu.Lock()
		changed = changed || g.unsent[d.box] != (err != nil)
		if err != nil {
			g.unsent[d.box] = true
		} else {
			delete(g.unsent, d.box)
			g.delivered[d.box] = d.digest
		}
		g.mu.Unlock()
		if err != nil {
			shared.For(ctx, g.cfg.Logger).Warn("grants", "event", "handover-not-set", "box", d.box)
			continue
		}
		shared.For(ctx, g.cfg.Logger).Info("grants", "event", "handover-set", "box", d.box, "credentials", len(d.hand.Credentials), "ports", len(d.hand.Ports))
	}
	if changed {
		g.showAll()
	}
}
