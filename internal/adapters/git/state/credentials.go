package state

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const credentialsFile = "access-secrets.json"

type Credentials struct {
	Dir string
	mu  *sync.Mutex
}

func NewCredentials(dir string) Credentials { return Credentials{Dir: dir, mu: &sync.Mutex{}} }

var _ session.CredentialStore = Credentials{}

type credentialVarJSON struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type credentialSecretJSON struct {
	Variable   string              `json:"variable,omitempty"`
	Value      string              `json:"value,omitempty"`
	KeyID      string              `json:"keyId,omitempty"`
	SecretKey  string              `json:"secretKey,omitempty"`
	Role       string              `json:"role,omitempty"`
	Region     string              `json:"region,omitempty"`
	User       string              `json:"user,omitempty"`
	PrivateKey string              `json:"privateKey,omitempty"`
	Password   string              `json:"password,omitempty"`
	HostKey    string              `json:"hostKey,omitempty"`
	Path       string              `json:"path,omitempty"`
	Mode       string              `json:"mode,omitempty"`
	Contents   string              `json:"contents,omitempty"`
	Vars       []credentialVarJSON `json:"vars,omitempty"`
}

type credentialJSON struct {
	ID     string               `json:"id"`
	Name   string               `json:"name"`
	Type   string               `json:"type"`
	Bound  []string             `json:"bound"`
	Tags   []string             `json:"tags,omitempty"`
	Saved  int64                `json:"saved"`
	Secret credentialSecretJSON `json:"secret"`
}

type credentialsJSON struct {
	Version     int              `json:"version"`
	Credentials []credentialJSON `json:"credentials"`
}

var errCredentialsFile = errors.New("access-secrets.json isn't a list of credentials this engine reads")

func credentialNameOK(name string) bool { return session.ValidCredentialName(name) }

func (s Credentials) lock() func() {
	if s.mu == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

func credentialOf(j credentialJSON) (session.Credential, bool) {
	if !grantID.MatchString(j.ID) || !credentialNameOK(j.Name) || !session.ValidCredentialType(j.Type) || !boundOK(j.Bound) || len(j.Tags) > 2 || j.Saved <= 0 {
		return session.Credential{}, false
	}
	for _, t := range j.Tags {
		if t != session.TagWrite && t != session.TagProduction {
			return session.Credential{}, false
		}
	}
	x := j.Secret
	sec := session.CredentialSecret{Variable: x.Variable, Value: x.Value, KeyID: x.KeyID, SecretKey: x.SecretKey, Role: x.Role, Region: x.Region, User: x.User,
		PrivateKey: x.PrivateKey, Password: x.Password, HostKey: x.HostKey, Path: x.Path, Mode: x.Mode, Contents: x.Contents}
	for _, v := range x.Vars {
		sec.Vars = append(sec.Vars, session.CredentialVar{Name: v.Name, Value: v.Value})
	}
	return session.Credential{ID: j.ID, Name: j.Name, Type: j.Type, Bound: j.Bound, Tags: j.Tags, Saved: time.Unix(j.Saved, 0), Secret: sec}, true
}

func (s Credentials) Load() ([]session.Credential, error) {
	defer s.lock()()
	data, err := read(s.Dir, credentialsFile)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []session.Credential{}, nil
	}
	var f credentialsJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Credentials) > session.MaxCredentials {
		return nil, errCredentialsFile
	}
	out := make([]session.Credential, 0, len(f.Credentials))
	for _, j := range f.Credentials {
		c, ok := credentialOf(j)
		if !ok {
			return nil, errCredentialsFile
		}
		out = append(out, c)
	}
	return out, nil
}

func (s Credentials) Save(list []session.Credential) error {
	defer s.lock()()
	f := credentialsJSON{Version: 1, Credentials: make([]credentialJSON, 0, len(list))}
	for _, c := range list {
		x := c.Secret
		sec := credentialSecretJSON{Variable: x.Variable, Value: x.Value, KeyID: x.KeyID, SecretKey: x.SecretKey, Role: x.Role, Region: x.Region, User: x.User,
			PrivateKey: x.PrivateKey, Password: x.Password, HostKey: x.HostKey, Path: x.Path, Mode: x.Mode, Contents: x.Contents}
		for _, v := range x.Vars {
			sec.Vars = append(sec.Vars, credentialVarJSON{Name: v.Name, Value: v.Value})
		}
		f.Credentials = append(f.Credentials, credentialJSON{ID: c.ID, Name: c.Name, Type: c.Type, Bound: c.Bound, Tags: c.Tags, Saved: c.Saved.Unix(), Secret: sec})
	}
	return write(s.Dir, credentialsFile, f)
}
