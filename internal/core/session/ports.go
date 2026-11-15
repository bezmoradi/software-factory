package session

import (
	"encoding/json"
	"maps"
	"strconv"
)

const (
	PortFirst       = 41000
	PortSpan        = 10
	portTop         = 65535
	maxPortSessions = 1000
)

type PortStore interface {
	Load() (map[string]int, error)
	Save(map[string]int) error
}

func ValidPortBase(base int) bool {
	return base >= PortFirst && (base-PortFirst)%PortSpan == 0 && base+PortSpan-1 <= portTop
}

func (w *SessionWork) loadPorts() {
	if w.cfg.Ports == nil {
		return
	}
	saved, err := w.cfg.Ports.Load()
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "ports-unreadable")
		return
	}
	held := map[int]bool{}
	for sid, base := range saved {
		if sidPattern.MatchString(sid) && ValidPortBase(base) && !held[base] && len(w.ports) < maxPortSessions {
			w.ports[sid] = base
			held[base] = true
		}
	}
}

func (w *SessionWork) savePorts() {
	if w.cfg.Ports == nil {
		return
	}
	w.saveMu.Lock()
	w.mu.Lock()
	snapshot := maps.Clone(w.ports)
	w.mu.Unlock()
	err := w.cfg.Ports.Save(snapshot)
	w.saveMu.Unlock()
	if err != nil {
		w.cfg.Logger.Warn("session work", "event", "ports-not-saved")
	}
}

func (w *SessionWork) reservePorts(key string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if base, ok := w.pending[key]; ok {
		return base
	}
	if len(w.ports)+len(w.pending) >= maxPortSessions {
		return 0
	}
	held := map[int]bool{}
	for _, b := range w.ports {
		held[b] = true
	}
	for _, b := range w.pending {
		held[b] = true
	}
	for base := PortFirst; ValidPortBase(base); base += PortSpan {
		if !held[base] {
			w.pending[key] = base
			return base
		}
	}
	return 0
}

func (w *SessionWork) bindPorts(key, sid string) int {
	w.mu.Lock()
	base, ok := w.pending[key]
	delete(w.pending, key)
	if ok && sid != "" {
		w.ports[sid] = base
	}
	w.mu.Unlock()
	if !ok || sid == "" {
		return 0
	}
	w.savePorts()
	return base
}

func (w *SessionWork) Ports(sid string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ports[sid]
}

func (w *SessionWork) PrunePorts(keep func(sid string) bool) {
	w.mu.Lock()
	dropped := false
	for sid := range w.ports {
		if !keep(sid) {
			delete(w.ports, sid)
			dropped = true
		}
	}
	w.mu.Unlock()
	if dropped {
		w.savePorts()
	}
}

func portNote(base int, inEnv bool) string {
	first, last := strconv.Itoa(base), strconv.Itoa(base+PortSpan-1)
	text := "This session has the ports " + first + " to " + last + " to itself, and other sessions running at the same time have their own. Start any server you run on one of them, beginning with " + first + ", rather than on a default port such as 3000 or 8080."
	if inEnv {
		text += " PORT and SF_PORT_BASE in your shell hold " + first + "."
	}
	return text
}

func (d *Dispatcher) claudeSettings(r *run) string {
	env := map[string]string{}
	if d.cfg.DocsInRepo {
		env["KNACKPACK_DOCS"] = "repo"
	}
	maps.Copy(env, claudePortEnv(r.ports))
	if len(env) == 0 {
		return ""
	}
	if d.cfg.DocsInRepo && len(env) == 1 {
		return DocsSettings
	}
	data, err := json.Marshal(map[string]map[string]string{"env": env})
	if err != nil {
		return ""
	}
	return string(data)
}

func (d *Dispatcher) reservePorts(r *run) {
	if w := d.workOf(); w != nil {
		r.ports = w.reservePorts(r.name)
	}
}

func (d *Dispatcher) startedWork(r *run, err error) {
	w := d.workOf()
	if w == nil {
		return
	}
	base := w.bindPorts(r.name, r.sid)
	if r.sid == "" {
		return
	}
	if base > 0 {
		w.cfg.PortNote(r.sid, portNote(base, r.spec.Method == MethodPlugin && len(claudePortEnv(base)) > 0))
	}
	w.recordStart(r.sid, r.spec.ID, r.model, r.effort)
	if err == nil {
		w.startSetup(r.sid, r.repo, r.cwd)
	}
}
