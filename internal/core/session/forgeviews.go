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

func (r *Registry) SetAccess(sid string, v AccessShown) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	a.access = slices.Clone(v.Cards)
	a.accessMarker = v.Marker
	a.accessNotes = slices.Clone(v.Notes)
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

func (v *FollowView) clone() *FollowView {
	out := *v
	out.Checks = slices.Clone(v.Checks)
	out.Comments = slices.Clone(v.Comments)
	return &out
}

func (r *Registry) SetFollow(sid string, v *FollowView) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.admitted[sid]
	if a == nil {
		return
	}
	if v != nil {
		v = v.clone()
	}
	a.follow = v
	r.announce(r.now())
}
