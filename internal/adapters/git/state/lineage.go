package state

import "github.com/bezmoradi/software-factory/internal/core/session"

const maxOrigin = 200

func groupOK(group, kind string) bool {
	return kind == session.GroupGoal && sessionID.MatchString(group)
}

func lineageInto(v *session.SavedRun, r runJSON) {
	if cleanText(r.Origin, maxOrigin) {
		v.Origin = r.Origin
	}
	if groupOK(r.Group, r.GroupKind) {
		v.Group, v.GroupKind = r.Group, r.GroupKind
	}
}

func lineageJSONInto(out *runJSON, r session.SavedRun) {
	if cleanText(r.Origin, maxOrigin) {
		out.Origin = r.Origin
	}
	if groupOK(r.Group, r.GroupKind) {
		out.Group, out.GroupKind = r.Group, r.GroupKind
	}
}
