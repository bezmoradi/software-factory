package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	SSHAgentSocket  = "/run/sf/ssh-agent.sock"
	MaxSSHSignData  = 8 << 10
	MaxSSHKeys      = 32
	SSHSigns        = 60
	SSHSignWindow   = time.Minute
	sshUserAuth     = 50
	sshService      = "ssh-connection"
	maxSSHSessionID = 64
)

var sshAuthMethods = []string{"publickey", "publickey-hostbound-v00@openssh.com"}

type SSHKeys interface {
	PublicKey(privateKey string) ([]byte, error)
	Sign(privateKey string, data []byte, flags uint32) (SSHSignature, error)
}

type SSHIdentity struct {
	Key     []byte
	Comment string
}

type SSHSignAsk struct {
	Key   []byte
	Data  []byte
	Flags uint32
}

type SSHSignature struct {
	Format string
	Blob   []byte
	Rest   []byte
}

type sshPublic struct {
	saved time.Time
	key   []byte
}

type signUse struct {
	count int
	last  time.Time
}

type signing struct {
	grant Grant
	cred  Credential
	key   []byte
}

func (g *Grants) publicKey(c Credential) ([]byte, bool) {
	g.mu.Lock()
	p, ok := g.pubs[c.ID]
	g.mu.Unlock()
	if ok && p.saved.Equal(c.Saved) {
		return p.key, true
	}
	key, err := g.cfg.SSH.PublicKey(c.Secret.PrivateKey)
	if err != nil {
		g.cfg.Logger.Warn("ssh", "event", "key-unreadable", "credential", c.Name)
		return nil, false
	}
	g.mu.Lock()
	g.pubs[c.ID] = sshPublic{saved: c.Saved, key: key}
	g.mu.Unlock()
	return key, true
}

func (g *Grants) signingsFor(box string) ([]signing, GrantTarget, bool) {
	sid, ok := g.cfg.InBox(box)
	if !ok {
		return nil, GrantTarget{}, false
	}
	t, ok := g.cfg.Session(sid)
	if !ok || !t.Live || t.Box != box {
		return nil, GrantTarget{}, false
	}
	now := g.cfg.Now()
	g.mu.Lock()
	if g.broken || g.credsBroken {
		g.mu.Unlock()
		return nil, t, false
	}
	var list []signing
	seen := map[string]bool{}
	for _, gr := range g.coveringLocked(t, now) {
		c, ok := g.credentialByID(gr.Credential)
		if gr.Credential == "" || !ok || c.Delivery() != DeliverySigned || seen[c.ID] || len(list) >= MaxSSHKeys {
			continue
		}
		seen[c.ID] = true
		list = append(list, signing{grant: gr, cred: c})
	}
	g.mu.Unlock()
	kept := list[:0]
	for _, s := range list {
		if key, ok := g.publicKey(s.cred); ok {
			s.key = key
			kept = append(kept, s)
		}
	}
	return kept, t, true
}

func (g *Grants) SSHIdentities(box string) []SSHIdentity {
	list, _, _ := g.signingsFor(box)
	out := make([]SSHIdentity, 0, len(list))
	for _, s := range list {
		out = append(out, SSHIdentity{Key: s.key, Comment: s.cred.Name})
	}
	return out
}

func sshString(b []byte) ([]byte, []byte, bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}

func SSHLogin(data []byte, user string, key []byte) bool {
	id, rest, ok := sshString(data)
	if !ok || len(id) == 0 || len(id) > maxSSHSessionID || len(rest) == 0 || rest[0] != sshUserAuth {
		return false
	}
	name, rest, ok := sshString(rest[1:])
	if !ok || string(name) != user {
		return false
	}
	service, rest, ok := sshString(rest)
	if !ok || string(service) != sshService {
		return false
	}
	method, rest, ok := sshString(rest)
	if !ok || string(method) != sshAuthMethods[0] && string(method) != sshAuthMethods[1] || len(rest) == 0 || rest[0] != 1 {
		return false
	}
	alg, rest, ok := sshString(rest[1:])
	if !ok || len(alg) == 0 {
		return false
	}
	blob, rest, ok := sshString(rest)
	if !ok || !bytes.Equal(blob, key) {
		return false
	}
	if string(method) == sshAuthMethods[1] {
		if _, rest, ok = sshString(rest); !ok {
			return false
		}
	}
	return len(rest) == 0
}

func (g *Grants) SSHSign(ctx context.Context, box string, q SSHSignAsk) (SSHSignature, error) {
	log := shared.For(ctx, g.cfg.Logger)
	refuse := func(why string, err error) (SSHSignature, error) {
		log.Warn("ssh", "event", "sign-refused", "why", why)
		return SSHSignature{}, err
	}
	if len(q.Data) == 0 || len(q.Data) > MaxSSHSignData || len(q.Key) == 0 {
		return refuse("data", invalid("data", "Software Factory signs only SSH logins."))
	}
	list, t, ok := g.signingsFor(box)
	if !ok {
		return refuse("session", conflict("session", "No running session of this engine is in this container."))
	}
	var s *signing
	for i := range list {
		if bytes.Equal(list[i].key, q.Key) {
			s = &list[i]
		}
	}
	if s == nil {
		return refuse("key", notFound("key", "No live grant of this session signs with that key."))
	}
	if !SSHLogin(q.Data, s.cred.Secret.User, q.Key) {
		return refuse("not-login", conflict("not a login", "Software Factory signs only SSH logins as "+s.cred.Secret.User+" with "+s.cred.Name+"."))
	}
	now := g.cfg.Now()
	g.mu.Lock()
	allowed := g.signs.allow(box, now)
	g.mu.Unlock()
	if !allowed {
		return refuse("limit", limited("signs", "Software Factory signs at most "+strconv.Itoa(SSHSigns)+" SSH logins a minute for one session."))
	}
	sig, err := g.cfg.SSH.Sign(s.cred.Secret.PrivateKey, q.Data, q.Flags)
	if err != nil {
		return refuse("sign", conflict("sign", "The engine couldn't sign with "+s.cred.Name+"."))
	}
	g.mu.Lock()
	u := g.used[s.grant.ID]
	u.count++
	u.last = now
	g.used[s.grant.ID] = u
	g.mu.Unlock()
	log.Info("ssh", "event", "signed", "credential", s.cred.Name, "session", t.SID)
	return sig, nil
}

func (g *Grants) useWords(gr Grant) string {
	g.mu.Lock()
	u, ok := g.used[gr.ID]
	delete(g.used, gr.ID)
	g.mu.Unlock()
	if gr.Delivery != DeliverySigned && !ok {
		return ""
	}
	switch u.count {
	case 0:
		return "; it signed no SSH login since the engine started"
	case 1:
		return "; it signed 1 SSH login since the engine started, at " + u.last.UTC().Format(time.RFC3339)
	}
	return "; it signed " + strconv.Itoa(u.count) + " SSH logins since the engine started, the last at " + u.last.UTC().Format(time.RFC3339)
}
