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
	SetIdentity(name, email string) error
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
		return CommitIdentity{}, explained(invalid("name"), "The name agents commit with needs 1 to 100 characters.")
	case !plainName(name) || strings.HasPrefix(name, "-"):
		return CommitIdentity{}, explained(invalid("name"), "The name can't hold line breaks, control or invisible characters, < or >, or start with a hyphen.")
	case len(email) > MaxIdentityEmail || strings.HasPrefix(email, "-") || strings.Contains(email, "..") || !emailShape.MatchString(email):
		return CommitIdentity{}, explained(invalid("email"), "The email needs the shape name@example.com, at most 254 characters with no spaces, such as GitHub's 12345+you@users.noreply.github.com.")
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
		return CommitIdentity{}, explained(conflict("state"), "The engine couldn't read the name and email agents commit with.")
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

func (i *Identities) SetIdentity(name, email string) error {
	if i == nil || i.store == nil {
		return explained(conflict("no state"), "This engine keeps no settings, so it can't save the name and email agents commit with.")
	}
	c, err := CheckIdentity(name, email)
	if err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.store.Save(c); err != nil {
		return explained(conflict("state"), "The engine couldn't save the name and email agents commit with.")
	}
	i.logger.Info("commit identity", "event", "saved")
	return nil
}

func noIdentity() *Error {
	return explained(conflict(NoIdentity), "No session starts or wakes until you set the name and email agents commit with, because an agent with none may commit with the email of its sign-in, which a push makes public. Set them with Name and email for commits on the Welcome page, then try again.")
}
