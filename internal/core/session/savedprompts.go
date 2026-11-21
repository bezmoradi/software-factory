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

type SavedPromptState struct {
	Projects map[string][]SavedPrompt
	Removed  []RemovedProject[[]SavedPrompt]
}

type SavedPromptStore interface {
	Load() (SavedPromptState, error)
	Save(SavedPromptState) (int, error)
}

type SavedPromptsPage interface {
	Prompts(repo string) ([]SavedPrompt, error)
	SavePrompt(repo, id, name, text string) ([]SavedPrompt, bool, error)
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

var (
	_ SavedPromptsPage = (*SavedPrompts)(nil)
	_ ProjectData      = (*SavedPrompts)(nil)
)

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

func (s *SavedPrompts) load() (SavedPromptState, error) {
	if s.cfg.Store == nil {
		return SavedPromptState{}, conflict("no store", "This engine keeps no saved prompts.")
	}
	st, err := s.cfg.Store.Load()
	if err != nil {
		s.cfg.Logger.Warn("saved prompts", "event", "not-read")
		return SavedPromptState{}, conflict("unreadable", "The engine couldn't read its saved prompts, so it changes none.")
	}
	if st.Projects == nil {
		st.Projects = map[string][]SavedPrompt{}
	}
	return st, nil
}

func (s *SavedPrompts) projectData(event, name string, fn func(st *SavedPromptState) bool) {
	if s.cfg.Store == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.cfg.Store.Load()
	if err != nil {
		projectDataNotSaved(s.cfg.Logger, "saved prompts", name, err)
		return
	}
	if st.Projects == nil {
		st.Projects = map[string][]SavedPrompt{}
	}
	if !fn(&st) {
		return
	}
	trimmed, err := s.cfg.Store.Save(st)
	if err != nil {
		projectDataNotSaved(s.cfg.Logger, "saved prompts", name, err)
		return
	}
	s.trimmed(trimmed)
	s.cfg.Logger.Info("saved prompts", "event", event)
}

func (s *SavedPrompts) trimmed(n int) {
	if n > 0 {
		s.cfg.Logger.Warn("saved prompts", "event", "removed-trimmed-to-fit", "prompts", n)
	}
}

func (s *SavedPrompts) ParkProject(id ProjectID) {
	s.projectData("parked", id.Name, func(st *SavedPromptState) bool {
		var parked bool
		st.Removed, _, parked = parkProject(st.Projects, st.Removed, id)
		return parked
	})
}

func (s *SavedPrompts) RestoreProject(id ProjectID) {
	s.projectData("restored", id.Name, func(st *SavedPromptState) bool {
		var changed bool
		st.Removed, changed = restoreProject(st.Projects, st.Removed, id)
		if list, ok := st.Projects[id.Name]; ok && promptBytes(st.Projects) > MaxSavedPromptBytes {
			delete(st.Projects, id.Name)
			st.Removed = append(st.Removed, RemovedProject[[]SavedPrompt]{ID: id, Data: list})
			s.cfg.Logger.Warn("saved prompts", "event", "not-restored", "name", id.Name, "reason", "size")
		}
		return changed
	})
}

func (s *SavedPrompts) KeepProjects(names []string) {
	s.projectData("unlisted-set-aside", "", func(st *SavedPromptState) bool {
		var n int
		st.Removed, n = keepProjects(st.Projects, st.Removed, names)
		return n > 0
	})
}

func (s *SavedPrompts) known(repo string) error {
	if s.cfg.Known == nil || !s.cfg.Known(repo) {
		return invalid("repo", "That isn't a project this engine knows.")
	}
	return nil
}

func (s *SavedPrompts) Prompts(repo string) ([]SavedPrompt, error) {
	if err := s.known(repo); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	return slices.Clone(st.Projects[repo]), nil
}

func (s *SavedPrompts) save(st SavedPromptState) error {
	trimmed, err := s.cfg.Store.Save(st)
	if err != nil {
		s.cfg.Logger.Warn("saved prompts", "event", "not-saved", "err", err.Error())
		return conflict("not saved", "The engine couldn't save the prompts, so nothing changed.")
	}
	s.trimmed(trimmed)
	return nil
}

func PromptsFull(saved int) string {
	if saved < MaxSavedPrompts {
		return ""
	}
	return "A project keeps at most 50 saved prompts"
}

func (s *SavedPrompts) SavePrompt(repo, id, name, text string) ([]SavedPrompt, bool, error) {
	name = strings.TrimSpace(name)
	if err := s.known(repo); err != nil {
		return nil, false, err
	}
	switch {
	case id != "" && !ValidPromptID(id):
		return nil, false, invalid("id", "That isn't the id of a saved prompt. Reload the list and try again.")
	case !ValidPromptName(name):
		return nil, false, onField("name", invalid("name", "One line of 1 to 60 characters."))
	case !ValidPromptText(text):
		return nil, false, onField("text", invalid("text", "Needs 1 to 20,000 characters."))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return nil, false, err
	}
	all := st.Projects
	list := slices.Clone(all[repo])
	edited := SavedPrompt{ID: id, Name: name, Text: text, At: s.cfg.Now()}
	if id == "" {
		if len(list) >= MaxSavedPrompts {
			return nil, false, limited("prompts", "A project keeps at most 50 saved prompts. Delete one first.")
		}
		if _, ok := all[repo]; !ok && len(all) >= MaxPromptProjects {
			return nil, false, limited("projects", "The engine keeps saved prompts for at most 200 projects.")
		}
		edited.ID = newPromptID()
		list = append(list, edited)
	} else {
		i := slices.IndexFunc(list, func(p SavedPrompt) bool { return p.ID == id })
		if i < 0 {
			return nil, false, conflict("gone", "That prompt isn't saved any more. Reload the list.")
		}
		if list[i].Name == name && list[i].Text == text {
			return list, false, nil
		}
		list[i] = edited
	}
	before := all[repo]
	all[repo] = list
	if promptBytes(all) > MaxSavedPromptBytes {
		all[repo] = before
		return nil, false, limited("size", "The saved prompts together would be over 1 MiB, more than the engine keeps. Delete or shorten some first.")
	}
	if err := s.save(st); err != nil {
		return nil, false, err
	}
	s.cfg.Logger.Info("saved prompts", "event", "saved", "new", id == "")
	return slices.Clone(list), true, nil
}

func (s *SavedPrompts) RemovePrompt(repo, id string) ([]SavedPrompt, error) {
	if err := s.known(repo); err != nil {
		return nil, err
	}
	if !ValidPromptID(id) {
		return nil, invalid("id", "That isn't the id of a saved prompt. Reload the list and try again.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	all := st.Projects
	list := slices.DeleteFunc(slices.Clone(all[repo]), func(p SavedPrompt) bool { return p.ID == id })
	if len(list) == len(all[repo]) {
		return slices.Clone(list), nil
	}
	if len(list) == 0 {
		delete(all, repo)
	} else {
		all[repo] = list
	}
	if err := s.save(st); err != nil {
		return nil, err
	}
	s.cfg.Logger.Info("saved prompts", "event", "removed")
	return slices.Clone(list), nil
}
