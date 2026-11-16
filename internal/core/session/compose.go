package session

import "sync/atomic"

var composeProject atomic.Pointer[string]

func ValidComposeProject(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

func SetComposeProject(name string) bool {
	if !ValidComposeProject(name) {
		return false
	}
	composeProject.Store(&name)
	return true
}

func Compose() string {
	if p := composeProject.Load(); p != nil {
		return "docker compose -p " + *p
	}
	return "docker compose"
}
