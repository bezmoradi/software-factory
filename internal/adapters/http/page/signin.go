package page

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

const (
	KeyIdle      = 15 * time.Minute
	KeyMax       = 24 * time.Hour
	secretLength = 43
	keptRetired  = 16
)

type retiredKey struct {
	digest [sha256.Size]byte
	why    string
}

type keys struct {
	mu      sync.Mutex
	now     func() time.Time
	active  string
	created time.Time
	lastUse time.Time
	retired []retiredKey
}

func newKeys(now func() time.Time) *keys {
	return &keys{now: now}
}

func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func same(given, expected string) bool {
	return expected != "" && len(given) == len(expected) && subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}

func (k *keys) issue() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	k.retire("replaced")
	k.active = randomSecret()
	k.created = now
	k.lastUse = now
	return k.active
}

func (k *keys) check(given string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(given) != secretLength || !same(given, k.active) {
		return false
	}
	k.lastUse = k.now()
	return true
}

func (k *keys) retire(why string) {
	if k.active == "" {
		return
	}
	k.retired = append(k.retired, retiredKey{digest: sha256.Sum256([]byte(k.active)), why: why})
	if len(k.retired) > keptRetired {
		k.retired = k.retired[len(k.retired)-keptRetired:]
	}
	k.active = ""
}

func (k *keys) why(given string) string {
	if len(given) != secretLength {
		return ""
	}
	digest := sha256.Sum256([]byte(given))
	k.mu.Lock()
	defer k.mu.Unlock()
	why := ""
	for _, r := range k.retired {
		if subtle.ConstantTimeCompare(digest[:], r.digest[:]) == 1 {
			why = r.why
		}
	}
	return why
}

func (k *keys) expire(streams int) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.active == "" {
		return false
	}
	now := k.now()
	if now.Sub(k.created) > KeyMax || (streams == 0 && now.Sub(k.lastUse) > KeyIdle) {
		k.retire("ended")
		return true
	}
	return false
}
