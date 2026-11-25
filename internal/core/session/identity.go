package session

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	NoIdentity       = "no commit identity"
	MaxIdentityName  = 100
	MaxIdentityEmail = 254
)

var emailShape = regexp.MustCompile("^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$")

type CommitIdentity struct {
	Name  string
	Email string
}

func (c CommitIdentity) Set() bool { return c.Name != "" && c.Email != "" }

type IdentityStore interface {
	Load() (CommitIdentity, error)
	Save(CommitIdentity) error
}

type IdentityPage interface {
	Identity() (CommitIdentity, error)
	SetIdentity(name, email string) (bool, error)
}

func plainName(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || r == '<' || r == '>' {
			return false
		}
	}
	return true
}

func CheckIdentity(name, email string) (CommitIdentity, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	switch {
	case name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxIdentityName:
		return CommitIdentity{}, onField("name", invalid("name", "Needs 1 to 100 characters."))
	case !plainName(name) || strings.HasPrefix(name, "-"):
		return CommitIdentity{}, onField("name", invalid("name", "Can't hold < or >, line breaks or start with a hyphen."))
	case len(email) > MaxIdentityEmail || strings.HasPrefix(email, "-") || strings.Contains(email, "..") || !emailShape.MatchString(email):
		return CommitIdentity{}, onField("email", invalid("email", "Needs an address like you@example.com."))
	}
	return CommitIdentity{Name: name, Email: email}, nil
}

type Identities struct {
	store  IdentityStore
	logger shared.Logger
	mu     sync.Mutex
}

var _ IdentityPage = (*Identities)(nil)

func NewIdentities(store IdentityStore, logger shared.Logger) *Identities {
	if logger == nil {
		logger = quiet{}
	}
	return &Identities{store: store, logger: logger}
}

func (i *Identities) Identity() (CommitIdentity, error) {
	if i == nil || i.store == nil {
		return CommitIdentity{}, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	c, err := i.store.Load()
	if err != nil {
		return CommitIdentity{}, conflict("state", "The engine couldn't read the name and email agents commit with.")
	}
	return c, nil
}

func (i *Identities) Current() CommitIdentity {
	c, err := i.Identity()
	if err != nil {
		i.logger.Warn("commit identity", "event", "unreadable")
		return CommitIdentity{}
	}
	return c
}

func (i *Identities) SetIdentity(name, email string) (bool, error) {
	if i == nil || i.store == nil {
		return false, conflict("no state", "This engine keeps no settings, so it can't save the name and email agents commit with.")
	}
	c, err := CheckIdentity(name, email)
	if err != nil {
		return false, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if was, err := i.store.Load(); err == nil && was == c {
		return false, nil
	}
	if err := i.store.Save(c); err != nil {
		return false, conflict("state", "The engine couldn't save the name and email agents commit with.")
	}
	i.logger.Info("commit identity", "event", "saved")
	return true, nil
}

func noIdentity() *Error {
	return conflict(NoIdentity, "No session starts or wakes until you set the name and email agents commit with, because an agent with none may commit with the email of its sign-in, which a push makes public. Set them with Git name and email under Settings (the gear), then try again.")
}
