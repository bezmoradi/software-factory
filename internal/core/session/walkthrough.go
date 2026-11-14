package session

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"
)

const (
	MaxWalkSteps    = 30
	MaxWalkField    = 400
	MaxWalkLists    = 200
	testListStopped = "the engine stopped before it finished"
	walkForm        = "1. What to do, in one or two sentences.\n   Expected: what the person should see when the change works.\n   Input: the sample input to use, or none.\n"
)

type WalkStep struct {
	Do     string
	Expect string
	Input  string
	Done   bool
}

type Walkthroughs interface {
	Load() (map[string]TestListView, error)
	Save(map[string]TestListView) error
}

type WalkthroughChecker interface {
	CheckStep(ctx context.Context, sid, commit string, step int, done bool) error
}

var (
	walkStart = regexp.MustCompile(`^\s{0,3}(?:\*\*)?(\d{1,3})[.)](?:\*\*)?\s+(.*)$`)
	walkLabel = regexp.MustCompile(`(?i)^\s*(?:[-*]\s+)?(?:\*\*|__|_)?(expected result|expected|expect|you should see|sample input|inputs|input)(?:\*\*|__|_)?\s*:\s*(?:\*\*|__|_)?\s*(.*)$`)
)

func walkClean(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "**", ""))
	return CleanLine(s, MaxWalkField)
}

func WalkStepsOf(text string) []WalkStep {
	var steps []WalkStep
	field := ""
	add := func(s *WalkStep, which, line string) {
		target := &s.Do
		switch which {
		case "expect":
			target = &s.Expect
		case "input":
			target = &s.Input
		}
		if *target == "" {
			*target = line
		} else if line != "" {
			*target += " " + line
		}
	}
	for l := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := walkStart.FindStringSubmatch(l); m != nil {
			if len(steps) == MaxWalkSteps {
				break
			}
			steps = append(steps, WalkStep{Do: strings.TrimSpace(m[2])})
			field = "do"
			continue
		}
		if len(steps) == 0 {
			continue
		}
		s := &steps[len(steps)-1]
		if m := walkLabel.FindStringSubmatch(l); m != nil {
			field = "expect"
			if strings.Contains(strings.ToLower(m[1]), "input") {
				field = "input"
			}
			add(s, field, strings.TrimSpace(m[2]))
			continue
		}
		add(s, field, strings.TrimSpace(l))
	}
	out := steps[:0]
	for _, s := range steps {
		s.Do, s.Expect, s.Input = walkClean(s.Do), walkClean(s.Expect), walkClean(s.Input)
		if strings.EqualFold(strings.Trim(s.Input, " .`"), "none") {
			s.Input = ""
		}
		if s.Do != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *Reviews) loadTests() {
	if r.cfg.Walkthroughs == nil {
		return
	}
	kept, err := r.cfg.Walkthroughs.Load()
	if err != nil {
		r.cfg.Logger.Warn("walkthrough", "event", "not-read")
		return
	}
	for sid, v := range kept {
		if v.State == TestListRunning {
			v.State, v.Reason, v.Text, v.Steps = TestListFailed, testListStopped, "", nil
			kept[sid] = v
		}
	}
	r.mu.Lock()
	r.tests = kept
	r.mu.Unlock()
}

func (r *Reviews) saveTests() {
	if r.cfg.Walkthroughs == nil {
		return
	}
	r.testStore.Lock()
	defer r.testStore.Unlock()
	r.mu.Lock()
	all := maps.Clone(r.tests)
	r.mu.Unlock()
	if err := r.cfg.Walkthroughs.Save(all); err != nil {
		r.cfg.Logger.Warn("walkthrough", "event", "not-saved")
	}
}

func (r *Reviews) ShowKept(sids []string) {
	for _, sid := range sids {
		r.mu.Lock()
		_, kept := r.tests[sid]
		view, shown := r.views[sid]
		r.mu.Unlock()
		if kept && !shown {
			r.show(sid, view)
		}
	}
}

func (r *Reviews) CheckStep(sid, commit string, step int, done bool) error {
	r.testStore.Lock()
	defer r.testStore.Unlock()
	r.mu.Lock()
	v, ok := r.tests[sid]
	switch {
	case !ok || v.State != TestListDone || len(v.Steps) == 0:
		r.mu.Unlock()
		return explained(conflict("no walkthrough"), "This session has no walkthrough to check off. Press Write a manual test list first.")
	case v.Commit != commit:
		r.mu.Unlock()
		return explained(conflict("moved"), "A newer walkthrough replaced this one, so the step wasn't changed. Read the new one first.")
	case step < 1 || step > len(v.Steps):
		r.mu.Unlock()
		return explained(invalid("step"), "That step isn't in this session's walkthrough.")
	}
	v.Steps = slices.Clone(v.Steps)
	v.Steps[step-1].Done = done
	all := maps.Clone(r.tests)
	all[sid] = v
	r.mu.Unlock()
	if r.cfg.Walkthroughs != nil {
		if err := r.cfg.Walkthroughs.Save(all); err != nil {
			r.cfg.Logger.Warn("walkthrough", "event", "not-saved")
			return explained(conflict("not saved"), "The engine couldn't save the check-off, so the step wasn't changed.")
		}
	}
	r.mu.Lock()
	if cur, ok := r.tests[sid]; ok && cur.Commit == v.Commit && cur.At.Equal(v.At) {
		r.tests[sid] = v
	}
	view := r.views[sid]
	r.mu.Unlock()
	r.show(sid, view)
	r.cfg.Logger.Info("walkthrough", "event", "checked", "done", done)
	return nil
}

func keptLists(tests map[string]TestListView, keep string) {
	for len(tests) > MaxWalkLists {
		oldest := ""
		for sid, v := range tests {
			if sid != keep && (oldest == "" || v.At.Before(tests[oldest].At)) {
				oldest = sid
			}
		}
		if oldest == "" {
			return
		}
		delete(tests, oldest)
	}
}

func (d *Dispatcher) CheckStep(_ context.Context, sid, commit string, step int, done bool) error {
	if d.cfg.Reviews == nil {
		return explained(conflict("no reviewer"), "This engine has no reviewer container, so it keeps no walkthroughs.")
	}
	if _, ok := d.PushTarget(sid); !ok {
		return explained(conflict("not started here"), "Only a session this engine started has a walkthrough here.")
	}
	return d.cfg.Reviews.CheckStep(sid, commit, step, done)
}
