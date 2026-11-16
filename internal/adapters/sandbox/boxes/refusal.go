package boxes

import (
	"fmt"
	"net/http"
	"sync"
)

const (
	ReasonLimit     = "limit"
	ReasonRuntime   = "runtime"
	ReasonUnknown   = "unknown"
	ReasonNamed     = "named"
	ReasonRecipe    = "recipe"
	ReasonService   = "service"
	ReasonFolders   = "folders"
	ReasonPool      = "pool"
	ReasonUnhealthy = "unhealthy"
	ReasonDocker    = "docker"
	ReasonNetwork   = "network"
	ReasonInput     = "input"
	ReasonKey       = "key"
	ReasonNoKey     = "no_key"
	ReasonRoute     = "route"
	ReasonMethod    = "method"
	ReasonTooLarge  = "too_large"
)

type Refusal struct {
	Status  int
	Reason  string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(status int, reason, format string, args ...any) *Refusal {
	return &Refusal{Status: status, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func dockerFailed(what string, err error) *Refusal {
	return refuse(http.StatusBadGateway, ReasonDocker, "Docker refused to %s: %s", what, oneLine(err.Error()))
}

type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu    sync.Mutex
	users int
}

func newKeyedLocks() *keyedLocks {
	return &keyedLocks{locks: map[string]*keyedLock{}}
}

func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.users++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.users--
		if l.users == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
