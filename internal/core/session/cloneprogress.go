package session

import (
	"sync"
	"time"
)

const (
	CloneShowAfter = time.Second
	CloneEvery     = 500 * time.Millisecond
)

type CloneProgress struct {
	Received int64
	Objects  int64
	Total    int64
	Arrived  bool
	Checked  int64
}

type ClonePhase string

const (
	CloneWaiting     ClonePhase = "waiting"
	CloneDownloading ClonePhase = "downloading"
	ClonePreparing   ClonePhase = "preparing"
)

type CloneShown struct {
	Phase      ClonePhase
	Percent    int
	HasPercent bool
	Said       string
}

func (p CloneProgress) Phase() ClonePhase {
	switch {
	case p.Arrived:
		return ClonePreparing
	case p.Received > 0 || p.Total > 0:
		return CloneDownloading
	}
	return CloneWaiting
}

func percentOf(n, total int64) int {
	if total <= 0 || n <= 0 {
		return 0
	}
	if n >= total {
		return 100
	}
	return int(n * 100 / total)
}

func (p CloneProgress) Shown(host string) CloneShown {
	s := CloneShown{Phase: p.Phase()}
	switch s.Phase {
	case CloneDownloading:
		s.HasPercent = p.Total > 0
		s.Percent = percentOf(p.Objects, p.Total)
	case ClonePreparing:
		s.HasPercent = p.Total > 0 && p.Checked > 0
		s.Percent = percentOf(p.Checked, p.Total)
	}
	s.Said = CloneWords(p, host)
	return s
}

func cloneFields(s CloneShown) Fields {
	f := Fields{"phase": string(s.Phase), "said": s.Said}
	if s.HasPercent {
		f["percent"] = s.Percent
	}
	return f
}

type cloneWatch struct {
	repos   *Repos
	url     string
	name    string
	host    string
	mu      sync.Mutex
	latest  CloneProgress
	sent    CloneShown
	sentAny bool
	shown   bool
	stopped bool
	stop    chan struct{}
	done    chan struct{}
}

func (c *Repos) watchClone(k Cloning) *cloneWatch {
	w := &cloneWatch{repos: c, url: k.URL, name: k.Name, host: hostName(k.URL), stop: make(chan struct{}), done: make(chan struct{})}
	go w.run()
	return w
}

func (w *cloneWatch) report(p CloneProgress) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	before := w.latest.Phase()
	w.latest = p
	if w.shown && p.Phase() != before {
		w.emitLocked()
	}
}

func (w *cloneWatch) run() {
	defer close(w.done)
	first := time.NewTimer(CloneShowAfter)
	defer first.Stop()
	select {
	case <-w.stop:
		return
	case <-first.C:
	}
	w.mu.Lock()
	if !w.stopped {
		w.shown = true
		w.emitLocked()
	}
	w.mu.Unlock()
	tick := time.NewTicker(CloneEvery)
	defer tick.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-tick.C:
			w.mu.Lock()
			if !w.stopped {
				w.emitLocked()
			}
			w.mu.Unlock()
		}
	}
}

func (w *cloneWatch) emitLocked() {
	s := w.latest.Shown(w.host)
	if w.sentAny && s == w.sent {
		return
	}
	w.sent, w.sentAny = s, true
	w.repos.progressed(w.url, w.name, s)
}

func (w *cloneWatch) end() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	close(w.stop)
	<-w.done
}

func (c *Repos) progressed(u, name string, s CloneShown) {
	c.mu.Lock()
	k, ok := c.cloning[u]
	if ok {
		k.Progress = s
		c.cloning[u] = k
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	c.cfg.Notify(Fields{"type": "repo", "state": "progress", "url": u, "name": name, "progress": cloneFields(s)})
}
