package session

import (
	"maps"
	"slices"
)

const (
	MaxDenied        = 20
	denialsUnreadWhy = "the engine couldn't read its record of denied commits, so it pushes nothing on its own; push from the page"
)

type DenialStore interface {
	Load() (map[string][]string, error)
	Save(map[string][]string) error
}

func (p *Pushes) loadDenials() {
	if p.cfg.Denials == nil {
		return
	}
	saved, err := p.cfg.Denials.Load()
	if err != nil {
		p.denialsUnread = true
		p.cfg.Logger.Warn("push", "event", "denials-not-read")
		return
	}
	for sid, commits := range saved {
		if !sidPattern.MatchString(sid) {
			continue
		}
		for _, commit := range commits {
			if commit != "" {
				p.refuseLocked(sid, commit)
			}
		}
	}
}

func (p *Pushes) refusedLocked(sid, commit string) bool {
	return commit != "" && slices.Contains(p.refused[sid], commit)
}

func (p *Pushes) refuseLocked(sid, commit string) {
	list := slices.DeleteFunc(p.refused[sid], func(c string) bool { return c == commit })
	list = append(list, commit)
	if len(list) > MaxDenied {
		list = slices.Delete(list, 0, len(list)-MaxDenied)
	}
	p.refused[sid] = list
}

func (p *Pushes) unrefuseLocked(sid, commit string) {
	list := slices.DeleteFunc(p.refused[sid], func(c string) bool { return c == commit })
	if len(list) == 0 {
		delete(p.refused, sid)
		return
	}
	p.refused[sid] = list
}

func (p *Pushes) ForgetDenials(sid string) {
	p.mu.Lock()
	_, had := p.refused[sid]
	delete(p.refused, sid)
	p.mu.Unlock()
	if had {
		p.saveDenials()
	}
}

func (p *Pushes) PruneDenials(known func(sid string) bool) {
	p.mu.Lock()
	sids := slices.Collect(maps.Keys(p.refused))
	p.mu.Unlock()
	var gone []string
	for _, sid := range sids {
		if !known(sid) {
			gone = append(gone, sid)
		}
	}
	p.mu.Lock()
	for _, sid := range gone {
		delete(p.refused, sid)
	}
	p.mu.Unlock()
	if len(gone) > 0 {
		p.saveDenials()
	}
}

func (p *Pushes) saveDenials() {
	if p.cfg.Denials == nil {
		return
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	if p.denialsUnread {
		p.mu.Unlock()
		p.cfg.Logger.Warn("push", "event", "denials-not-saved", "why", "unread")
		return
	}
	out := make(map[string][]string, len(p.refused))
	for sid, list := range p.refused {
		out[sid] = slices.Clone(list)
	}
	p.mu.Unlock()
	if err := p.cfg.Denials.Save(out); err != nil {
		p.cfg.Logger.Warn("push", "event", "denials-not-saved")
	}
}
