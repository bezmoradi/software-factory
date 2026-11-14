package session

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	MaxSavedPrompts     = 50
	MaxPromptName       = 60
	MaxPromptProjects   = 200
	MaxSavedPromptBytes = 1 << 20
)

var promptID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type SavedPrompt struct {
	ID   string
	Name string
	Text string
	At   time.Time
}

type SavedPromptStore interface {
	Load() (map[string][]SavedPrompt, error)
	Save(map[string][]SavedPrompt) error
}

type SavedPromptsPage interface {
	Prompts(repo string) ([]SavedPrompt, error)
	SavePrompt(repo, id, name, text string) ([]SavedPrompt, error)
	RemovePrompt(repo, id string) ([]SavedPrompt, error)
}

type SavedPromptsConfig struct {
	Store  SavedPromptStore
	Known  func(repo string) bool
	Now    func() time.Time
	Logger shared.Logger
}

type SavedPrompts struct {
	cfg SavedPromptsConfig
	mu  sync.Mutex
}

var _ SavedPromptsPage = (*SavedPrompts)(nil)

func NewSavedPrompts(cfg SavedPromptsConfig) *SavedPrompts {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet{}
	}
	return &SavedPrompts{cfg: cfg}
}

func ValidPromptID(id string) bool { return promptID.MatchString(id) }

func ValidPromptName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxPromptName {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

func ValidPromptText(text string) bool {
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf16Len(text) > MaxText {
		return false
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || r == ' ' || r == ' ') {
			return false
		}
	}
	return true
}

func promptBytes(all map[string][]SavedPrompt) int {
	n := 0
	for _, list := range all {
		for _, p := range list {
			n += len(p.Name) + len(p.Text)
		}
	}
	return n
}

func newPromptID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *SavedPrompts) load() (map[string][]SavedPrompt, error) {
	if s.cfg.Store == nil {
		return nil, explained(conflict("no store"), "This engine keeps no saved prompts.")
	}
	all, err := s.cfg.Store.Load()
	if err != nil {
		s.cfg.Logger.Warn("saved prompts", "event", "not-read")
		return nil, explained(conflict("unreadable"), "The engine couldn't read its saved prompts, so it changes none.")
	}
	if all == nil {
		all = map[string][]SavedPrompt{}
	}
	return all, nil
}

func (s *SavedPrompts) known(repo string) error {
	if s.cfg.Known == nil || !s.cfg.Known(repo) {
		return explained(invalid("repo"), "That isn't a project this engine knows.")
	}
	return nil
}

func (s *SavedPrompts) Prompts(repo string) ([]SavedPrompt, error) {
	if err := s.known(repo); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	return slices.Clone(all[repo]), nil
}

func (s *SavedPrompts) save(all map[string][]SavedPrompt) error {
	if err := s.cfg.Store.Save(all); err != nil {
		s.cfg.Logger.Warn("saved prompts", "event", "not-saved")
		return explained(conflict("not saved"), "The engine couldn't save the prompts, so nothing changed.")
	}
	return nil
}

func (s *SavedPrompts) SavePrompt(repo, id, name, text string) ([]SavedPrompt, error) {
	if err := s.known(repo); err != nil {
		return nil, err
	}
	switch {
	case id != "" && !ValidPromptID(id):
		return nil, invalid("id")
	case !ValidPromptName(name):
		return nil, explained(invalid("name"), "A prompt's name is one line of 1 to 60 characters.")
	case !ValidPromptText(text):
		return nil, explained(invalid("text"), "A prompt's text is 1 to 20,000 characters, the length of a message, with no control characters but line breaks and tabs.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	list := slices.Clone(all[repo])
	edited := SavedPrompt{ID: id, Name: name, Text: text, At: s.cfg.Now()}
	if id == "" {
		if len(list) >= MaxSavedPrompts {
			return nil, explained(limited("prompts"), "A project keeps at most 50 saved prompts. Delete one first.")
		}
		if _, ok := all[repo]; !ok && len(all) >= MaxPromptProjects {
			return nil, explained(limited("projects"), "The engine keeps saved prompts for at most 200 projects.")
		}
		edited.ID = newPromptID()
		list = append(list, edited)
	} else {
		i := slices.IndexFunc(list, func(p SavedPrompt) bool { return p.ID == id })
		if i < 0 {
			return nil, explained(conflict("gone"), "That prompt isn't saved any more. Reload the list.")
		}
		list[i] = edited
	}
	before := all[repo]
	all[repo] = list
	if promptBytes(all) > MaxSavedPromptBytes {
		all[repo] = before
		return nil, explained(limited("size"), "The saved prompts together would be over 1 MiB, more than the engine keeps. Delete or shorten some first.")
	}
	if err := s.save(all); err != nil {
		return nil, err
	}
	s.cfg.Logger.Info("saved prompts", "event", "saved", "new", id == "")
	return slices.Clone(list), nil
}

func (s *SavedPrompts) RemovePrompt(repo, id string) ([]SavedPrompt, error) {
	if err := s.known(repo); err != nil {
		return nil, err
	}
	if !ValidPromptID(id) {
		return nil, invalid("id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	list := slices.DeleteFunc(slices.Clone(all[repo]), func(p SavedPrompt) bool { return p.ID == id })
	if len(list) == len(all[repo]) {
		return slices.Clone(list), nil
	}
	if len(list) == 0 {
		delete(all, repo)
	} else {
		all[repo] = list
	}
	if err := s.save(all); err != nil {
		return nil, err
	}
	s.cfg.Logger.Info("saved prompts", "event", "removed")
	return slices.Clone(list), nil
}
