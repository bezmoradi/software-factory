package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

const (
	diskEvery      = 2 * time.Minute
	diskFor        = 30 * time.Second
	DiskMeasureFor = 45 * time.Second
)

var errNoDisk = errors.New("the engine has no workspace to measure")

type diskTarget struct {
	sid string
	box string
}

func (l *Limits) diskRoundLocked(now time.Time) []diskTarget {
	if l.cfg.Disk == nil || l.diskRound {
		return nil
	}
	var list []diskTarget
	for sid, k := range l.known {
		s := l.sessions[sid]
		if !k.running || s == nil || !ValidBox(k.box) || l.capsOfLocked(s).DiskGiB <= 0 && s.reached != CapDisk {
			continue
		}
		if since := now.Sub(s.diskTried); since >= 0 && since < diskEvery {
			continue
		}
		s.diskTried = now
		list = append(list, diskTarget{sid: sid, box: k.box})
	}
	if len(list) == 0 {
		return nil
	}
	slices.SortFunc(list, func(a, b diskTarget) int { return strings.Compare(a.sid, b.sid) })
	l.diskRound = true
	return list
}

func (l *Limits) diskRoundRun(list []diskTarget) {
	defer func() {
		l.mu.Lock()
		l.diskRound = false
		l.mu.Unlock()
	}()
	_ = l.measureDisk(context.Background(), list)
}

func (l *Limits) measureDisk(ctx context.Context, list []diskTarget) error {
	select {
	case l.diskTurn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l.diskTurn }()
	l.mu.Lock()
	get, logger := l.cfg.Disk, l.cfg.Logger
	l.mu.Unlock()
	if get == nil {
		return errNoDisk
	}
	var failed error
	for _, t := range list {
		l.mu.Lock()
		if s := l.sessions[t.sid]; s != nil {
			s.diskTried = l.now()
		}
		l.mu.Unlock()
		one, cancel := context.WithTimeout(ctx, diskFor)
		size, err := get(one, t.box)
		cancel()
		if err != nil {
			failed = err
			logger.Info("limits", "event", "disk-not-measured")
			continue
		}
		l.diskSeen(t.sid, size, l.now())
	}
	return failed
}

func (l *Limits) diskSeen(sid string, size TreeSize, at time.Time) {
	l.mu.Lock()
	s := l.sessions[sid]
	if s == nil {
		l.mu.Unlock()
		return
	}
	s.used.DiskBytes, s.used.DiskMore, s.used.DiskAt = max(size.Bytes, 0), size.More, at
	c := l.capsOfLocked(s)
	freed := s.reached == CapDisk && !size.More && !overCap(CapDisk, c, s.used)
	reason := ""
	if freed {
		s.reached, s.reachedAt = "", time.Time{}
		l.unsaved = true
		reason = "the engine measured the session's folder at " + gib(s.used.DiskBytes) + ", under its " + capWords(CapDisk, c) + ", so it passes on messages again and starts automatic reviews and automatic Send findings again"
	}
	l.checkLocked(sid, s)
	l.mu.Unlock()
	if freed {
		l.audit(AuditEntry{Session: sid, Kind: AuditCapAnswer, Result: CapDisk, Verdict: CapFreed, By: ByEngine, Reason: reason})
		_ = l.save()
	}
	l.flush(freed)
}

func (l *Limits) measureSoon(sid, box string) {
	if !ValidBox(box) {
		return
	}
	l.mu.Lock()
	s := l.sessions[sid]
	queued := s == nil || s.diskSoon
	if !queued {
		s.diskSoon = true
	}
	l.mu.Unlock()
	if queued {
		return
	}
	go func() {
		_ = l.measureDisk(context.Background(), []diskTarget{{sid: sid, box: box}})
		l.mu.Lock()
		if s := l.sessions[sid]; s != nil {
			s.diskSoon = false
		}
		l.mu.Unlock()
	}()
}

func (l *Limits) MeasureNow(ctx context.Context, sid string) error {
	if !sidPattern.MatchString(sid) {
		return invalid("sid")
	}
	l.mu.Lock()
	s := l.sessions[sid]
	k, known := l.known[sid]
	disk := l.cfg.Disk
	if s == nil || s.reached != CapDisk {
		l.mu.Unlock()
		return explained(conflict("not reached"), "This session isn't at its disk cap, so there is nothing to check again.")
	}
	l.mu.Unlock()
	if !known {
		known = l.reg.admittedProject(sid, &k)
	}
	if disk == nil || !known || !ValidBox(k.box) {
		return explained(conflict("not measured"), "This engine can't measure the session's folder. Press Raise the cap, or Stop.")
	}
	if err := l.measureDisk(ctx, []diskTarget{{sid: sid, box: k.box}}); err != nil {
		return explained(conflict("not measured"), "The engine couldn't measure the session's folder this time. Press Check again, or Raise the cap.")
	}
	return nil
}
