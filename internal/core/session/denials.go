package session

import "maps"

type DenialStore interface {
	Load() (map[string]string, error)
	Save(map[string]string) error
}

func (p *Pushes) loadDenials() {
	if p.cfg.Denials == nil {
		return
	}
	saved, err := p.cfg.Denials.Load()
	if err != nil {
		p.cfg.Logger.Warn("push", "event", "denials-not-read")
		return
	}
	for sid, commit := range saved {
		if sidPattern.MatchString(sid) && commit != "" {
			p.refused[sid] = commit
		}
	}
}

func (p *Pushes) saveDenials() {
	if p.cfg.Denials == nil {
		return
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	out := maps.Clone(p.refused)
	p.mu.Unlock()
	if err := p.cfg.Denials.Save(out); err != nil {
		p.cfg.Logger.Warn("push", "event", "denials-not-saved")
	}
}
