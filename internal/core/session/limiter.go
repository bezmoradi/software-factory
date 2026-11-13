package session

import "time"

const maxLimiterKeys = 1000

type limiter struct {
	count  int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(count int, window time.Duration) *limiter {
	return &limiter{count: count, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string, now time.Time) bool {
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.count {
		l.hits[key] = kept
		return false
	}
	if _, known := l.hits[key]; !known && len(l.hits) >= maxLimiterKeys {
		l.prune(now)
		if len(l.hits) >= maxLimiterKeys {
			return false
		}
	}
	l.hits[key] = append(kept, now)
	return true
}

func (l *limiter) prune(now time.Time) {
	for k, list := range l.hits {
		if len(list) == 0 || now.Sub(list[len(list)-1]) >= l.window {
			delete(l.hits, k)
		}
	}
}
