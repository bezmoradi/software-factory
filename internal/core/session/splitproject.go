package session

import (
	"errors"
	"slices"
	"strings"
	"time"
)

const (
	splitAddingNote   = "Being added to Projects. The task starts there once the clone finishes."
	splitNotAddedNote = "The clone of its project didn't finish, so the task didn't start. Press Start to try again."
)

type SplitRepos interface {
	List() ([]Repository, error)
	View() (RepoView, error)
	Add(raw string) (Cloning, error)
}

type SplitOutcome struct {
	Started
	Adding string
}

func repoAt(list []Repository, u string) (Repository, bool) {
	i := slices.IndexFunc(list, func(r Repository) bool { return sameRepository(r.URL, u) })
	if i < 0 {
		return Repository{}, false
	}
	return list[i], true
}

func (s *Splits) projects() ([]Repository, bool) {
	if s.cfg.Repos == nil {
		return nil, false
	}
	list, err := s.cfg.Repos.List()
	return list, err == nil
}

func (s *Splits) ownProject(sid string, tasks []SplitTask) (string, string) {
	if !slices.ContainsFunc(tasks, func(t SplitTask) bool { return t.Project != "" }) {
		return "", ""
	}
	if s.cfg.Repos == nil {
		return "", "this engine has no workspace, so it can't add projects; propose tasks for this session's project only"
	}
	path, _, _, _, ok := s.cfg.Dispatcher.goalOf(sid)
	if !ok {
		return "", "only a session this engine started can use this tool"
	}
	list, listed := s.projects()
	if !listed {
		return "", "the engine couldn't read its list of projects; propose the tasks again later"
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Path == path })
	if i < 0 {
		return "", "this session's project is no longer in Projects"
	}
	return list[i].URL, ""
}

func splitProject(raw, own string) (string, string) {
	if raw == "" {
		return "", ""
	}
	u, err := NormalizeURL(raw)
	if err != nil {
		return "", strings.TrimSuffix(err.Error(), ".")
	}
	if sameRepository(u, own) {
		return "", ""
	}
	return u, ""
}

func (s *Splits) cloning(u string) (bool, bool) {
	v, err := s.cfg.Repos.View()
	if err != nil {
		return false, false
	}
	return slices.ContainsFunc(v.Cloning, func(k Cloning) bool { return sameRepository(k.URL, u) }), true
}

func (s *Splits) addLocked(c *SplitCard, consent Consent) (SplitOutcome, error) {
	sid, card, u, before := c.Goal, c.Card, c.Project, c.State
	if s.cfg.Repos == nil {
		s.mu.Unlock()
		return SplitOutcome{}, conflict("no workspace", "This engine has no workspace, so it can't add projects.")
	}
	c.State = SplitAdding
	s.mu.Unlock()
	var err error
	if busy, read := s.cloning(u); !read || !busy {
		_, err = s.cfg.Repos.Add(u)
	}
	if e, ok := errors.AsType[*Error](err); ok && (e.Reason == "cloning" || e.Reason == "exists") {
		err = nil
	}
	s.mu.Lock()
	c = s.cardLocked(sid, card)
	if c == nil || c.State != SplitAdding {
		s.mu.Unlock()
		s.show(sid)
		return SplitOutcome{}, conflict("card", "That card isn't waiting any more.")
	}
	if err != nil {
		c.State = before
		s.mu.Unlock()
		s.show(sid)
		return SplitOutcome{}, err
	}
	c.At = s.cfg.Now()
	s.holds[card] = &splitHold{consent: consent}
	s.saveLocked()
	s.mu.Unlock()
	s.show(sid)
	s.cfg.Logger.Info("splits", "event", "adding-project")
	return SplitOutcome{Adding: "Adding " + u + " to Projects. The task starts there once the clone finishes."}, nil
}

func (s *Splits) settleAdding(now time.Time) {
	type adding struct{ goal, card, url string }
	var list []adding
	s.mu.Lock()
	for _, c := range s.cards {
		if c.State == SplitAdding {
			list = append(list, adding{c.Goal, c.Card, c.Project})
		}
	}
	closed := s.closed
	s.mu.Unlock()
	if closed || len(list) == 0 || s.cfg.Repos == nil {
		return
	}
	view, verr := s.cfg.Repos.View()
	repos, lerr := s.cfg.Repos.List()
	if verr != nil || lerr != nil {
		return
	}
	changed := map[string]bool{}
	s.mu.Lock()
	for _, a := range list {
		c := s.cardLocked(a.goal, a.card)
		if c == nil || c.State != SplitAdding || slices.ContainsFunc(view.Cloning, func(k Cloning) bool { return sameRepository(k.URL, a.url) }) {
			continue
		}
		if _, ok := repoAt(repos, a.url); ok {
			c.State, c.At, c.Queued = SplitQueued, now, now
		} else {
			c.State, c.At = SplitWaiting, now
			s.holds[c.Card] = &splitHold{note: splitNotAddedNote}
		}
		changed[a.goal] = true
	}
	if len(changed) > 0 {
		s.saveLocked()
	}
	s.mu.Unlock()
	for goal := range changed {
		s.show(goal)
		s.cfg.Logger.Info("splits", "event", "project-settled")
	}
}

func SplitProjectSaid(name string) string {
	if name != "" {
		return "For another project, " + name + " in Projects: Start starts the session there."
	}
	return "For another project, not in Projects yet: Start adds it, as Add repository does, then starts the session there."
}
