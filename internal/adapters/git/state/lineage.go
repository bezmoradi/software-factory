package state

import (
	"regexp"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const maxOrigin = 200

var pairID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func groupOK(group, kind string) bool {
	switch kind {
	case session.GroupPair:
		return pairID.MatchString(group)
	case session.GroupGoal:
		return sessionID.MatchString(group)
	}
	return false
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
