package session

import "slices"

func (r *Registry) SetForge(sid string, list []ForgeView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	a.forge = slices.Clone(list)
	r.announce(r.now())
}

func forgeWaiting(list []ForgeView) int {
	n := 0
	for _, v := range list {
		if v.State == ForgeWaiting {
			n++
		}
	}
	return n
}
