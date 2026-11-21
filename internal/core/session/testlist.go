package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	TestListRunning  = "running"
	TestListDone     = "done"
	TestListFailed   = "failed"
	TestListWithheld = "withheld"
	maxTestList      = 16000
	testListKey      = "test-list:"
)

type TestListView struct {
	Commit  string
	Branch  string
	State   string
	Text    string
	Reason  string
	CostUSD float64
	At      time.Time
	Steps   []WalkStep
}

func TestListPrompt(base, head, branch string) string {
	from := "base/ holds commit " + base + ", where that branch leaves the remote's main branch"
	if base == "" {
		from = "base/ is empty: the branch shares no history with the remote's main branch"
	}
	return "Write a manual test list for the change in this folder, as a guided walkthrough: the steps a person should try by hand before merging it, " +
		"most important first, and the risky cases the change could break. It isn't a git repository: head/ holds commit " + head + " of the branch " + branch + ", and " + from + ". " +
		"change.diff beside them is that change as a unified diff, written by Software Factory. You can read files but not run commands or write files. " +
		"Give only the numbered steps in your reply, at most " + strconv.Itoa(MaxWalkSteps) + ", each in this form:\n" + walkForm +
		"\nFor each sample input use a real value from the change, such as a file name, a command, a field's text or a URL, and write none when the step needs no input. " +
		"Give no verdict: this isn't a review, and Software Factory's gate doesn't read it."
}

func TestListArgv(command, plugin, prompt string) []string {
	argv := ReviewArgv(command, plugin, "", prompt)
	if i := slices.Index(argv, "--agent"); i >= 0 && i+1 < len(argv) {
		argv = slices.Delete(argv, i, i+2)
	}
	return argv
}

func (d *Dispatcher) TestList(ctx context.Context, sid string, costOK bool) error {
	if err := d.historyOnly(sid); err != nil {
		return err
	}
	if d.cfg.Reviews == nil {
		return conflict("no reviewer", "This engine runs no reviewer containers, so it can't write a manual test list.")
	}
	t, ok := d.PushTarget(sid)
	if !ok {
		return conflict("not started here", "Only a session this engine started can get a manual test list here.")
	}
	return d.cfg.Reviews.TestList(ctx, ReviewTarget{SID: sid, Repo: t.Repo, Name: t.Name, Branch: t.Branch, CostOK: costOK})
}

func (r *Reviews) testListOf(sid string) *TestListView {
	if v, ok := r.tests[sid]; ok {
		return &v
	}
	return nil
}

func (r *Reviews) setTestList(sid string, v TestListView) {
	r.mu.Lock()
	if r.tests == nil {
		r.tests = map[string]TestListView{}
	}
	r.tests[sid] = v
	keptLists(r.tests, sid)
	view := r.views[sid]
	r.mu.Unlock()
	r.saveTests()
	r.show(sid, view)
}

func (r *Reviews) TestList(ctx context.Context, t ReviewTarget) error {
	t.Req = requestOr(ctx, t.Req)
	if t.Repo == "" || t.Name == "" || t.Branch == "" {
		return conflict("no clone", "The engine doesn't know which clone and branch this session works in, so it can't write a test list for it.")
	}
	key := testListKey + t.SID
	r.mu.Lock()
	if _, busy := r.running[key]; busy {
		r.mu.Unlock()
		return conflict("writing", "A manual test list for this session is being written. Wait for it to finish.")
	}
	if err := r.holdLocked(key); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.running, key)
		r.mu.Unlock()
	}
	signIn, err := r.CheckSignIn(ctx)
	if err != nil {
		release()
		return err
	}
	if err := judge("The reviewers' Claude Code", signIn, r.cfg.Expected.AdoptChecked(ReviewerAgent, signIn), ReviewerSignInStep(), t.CostOK, "the manual test list"); err != nil {
		release()
		return err
	}
	pack, err := r.cfg.Plugins.ReviewerPack(t.Name)
	if err != nil {
		release()
		return conflict("no plugin", clip("The reviewers' plugin isn't ready: "+err.Error(), maxOutput))
	}
	plugin := pack.Path
	head, err := r.cfg.Snapshots.Head(t.Repo, t.Branch)
	if err != nil {
		release()
		return conflict("branch", clip("The engine couldn't read the session's branch "+t.Branch+": "+err.Error(), maxOutput))
	}
	main, err := r.cfg.Mains.Fresh(ctx, t.Name)
	if err != nil {
		release()
		if e, ok := errors.AsType[*Error](err); ok {
			return e
		}
		return conflict("fetch", "The engine couldn't fetch the remote's main branch, so it can't tell what changed.")
	}
	base, err := r.cfg.History.MergeBase(ctx, t.Repo, head, main)
	if err != nil {
		release()
		return conflict("merge base", clip("The engine couldn't find where "+t.Branch+" leaves the remote's main branch: "+err.Error(), maxOutput))
	}
	if base == head {
		release()
		return conflict("no change", "The session's branch has nothing the remote's main branch lacks, so there's nothing to test. Commit the work first.")
	}
	r.setTestList(t.SID, TestListView{Commit: head, Branch: t.Branch, State: TestListRunning, At: r.cfg.Now()})
	shared.For(ctx, r.cfg.Logger).Info("test list", "event", "started")
	r.wg.Go(func() {
		defer release()
		v := r.writeTestList(t, base, head, plugin)
		v.At = r.cfg.Now()
		r.setTestList(t.SID, v)
		shared.For(ctx, r.cfg.Logger).Info("test list", "event", "finished", "state", v.State)
	})
	return nil
}

func (r *Reviews) writeTestList(t ReviewTarget, base, head, plugin string) TestListView {
	v := TestListView{Commit: head, Branch: t.Branch, State: TestListFailed}
	ctx := shared.WithRequest(r.cfg.Base, t.Req)
	if why := r.check(ctx); why != "" {
		v.Reason = "it didn't start: " + why
		return v
	}
	snap, err := r.cfg.Snapshots.Write(t.Repo, base, head, nil)
	if err != nil {
		v.Reason = clip("the engine couldn't write the snapshot: "+err.Error(), 400)
		return v
	}
	defer func() {
		if err := r.cfg.Snapshots.Remove(snap.ID); err != nil {
			r.cfg.Logger.Warn("test list", "event", "snapshot-not-removed")
		}
	}()
	dir := strings.TrimRight(r.cfg.Dir, "/") + "/" + snap.ID
	argv := TestListArgv(r.cfg.Command, plugin, TestListPrompt(base, head, t.Branch))
	rctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout+reviewSlack)
	defer cancel()
	res, err := r.reviewerRun(rctx, Command{Argv: argv, Dir: dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: r.cfg.Timeout}, nil)
	switch {
	case err != nil && ctx.Err() != nil:
		v.Reason = testListStopped
		return v
	case err != nil && rctx.Err() != nil, err == nil && res.TimedOut:
		v.Reason = timedOut(r.cfg.Timeout)
		return v
	case err != nil:
		v.Reason, _ = reviewHostProblem(err)
		return v
	}
	out, ok := claudeResult(res.Stdout)
	if !ok {
		v.Reason = fmt.Sprintf("its output wasn't Claude Code's JSON result (exit code %d)", res.Code)
		return v
	}
	v.CostUSD = out.cost
	text := strings.TrimSpace(out.text)
	if out.failed || res.Code != 0 {
		v.Reason = "Claude Code reported an error"
		if l := firstLine(text); l != "" && !secretText(text) {
			v.Reason += ": " + l
		}
		return v
	}
	if secretText(text) {
		v.State, v.Reason = TestListWithheld, "its reply held something shaped like a token or a private key, so the engine didn't keep or show it"
		r.cfg.Logger.Warn("test list", "event", "withheld", "why", "token-shaped")
		return v
	}
	v.State, v.Text = TestListDone, clip(text, maxTestList)
	if text == "" {
		v.State, v.Reason = TestListFailed, "its reply was empty"
	}
	v.Steps = WalkStepsOf(text)
	return v
}
