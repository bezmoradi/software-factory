package session

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	ByUpdate        = "update"
	MaxCheckModels  = 8
	checksPerAgent  = 6
	checksKept      = 200
	versionFor      = 30 * time.Second
	versionRetry    = 2 * time.Second
	maxChain        = 50
	checkSnapFailed = "the engine couldn't write the check's snapshot: "
)

var reviewVersionText = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,40}$`)

type CheckRunResult struct {
	State   review.State
	Verdict string
	Reason  string
	Reply   string
	Channel review.Channel
}

type ReviewerCheck struct {
	Agent     string
	Role      review.Role
	Versions  review.Versions
	Result    review.CheckResult
	Defect    CheckRunResult
	Clean     CheckRunResult
	Reason    string
	By        string
	StartedAt time.Time
	EndedAt   time.Time
	CostUSD   float64
}

type ReviewerChecks interface {
	Load() ([]ReviewerCheck, error)
	Save([]ReviewerCheck) error
}

type RoleCheckView struct {
	Agent   string
	Role    review.Role
	Check   *ReviewerCheck
	Running bool
}

type ReviewerChecksView struct {
	Roles      []RoleCheckView
	ClaudeCode string
	Plugin     string
	Minimum    review.Minimum
}

type ReviewerChecksPage interface {
	ReviewerChecks() (ReviewerChecksView, error)
	CheckNow(ctx context.Context, agent string, costOK bool) error
}

type dueCheck struct {
	agent    string
	role     review.Role
	versions review.Versions
}

func Rerunnable(e ReviewEntry) bool {
	return e.Record.State == review.Unknown && (e.Record.Channel == review.BelowMinimum || e.Record.Channel == review.Unchecked || e.Record.Channel == review.ApprovalChanged)
}

func versionsOf(e ReviewEntry) review.Versions {
	plugin := e.PluginCommit
	if e.Record.Provider == UploadedProvider {
		plugin = e.Key.Agent
	}
	return review.Versions{ClaudeCode: e.ClaudeCode, Plugin: plugin, Models: slices.Clone(e.Models), Model: e.RunModel, Effort: e.RunEffort}
}

func (r *Reviews) pluginFor(agent string) string {
	if providerFor(r.cfg.AgentFiles, agent) == UploadedProvider {
		return agent
	}
	return r.cfg.PluginCommit
}

func (r *Reviews) Current() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claude, r.cfg.PluginCommit
}

func (r *Reviews) reviewerVersion(ctx context.Context) string {
	v := r.readVersion(ctx)
	if v == "" {
		select {
		case <-time.After(versionRetry):
			v = r.readVersion(ctx)
		case <-ctx.Done():
		}
	}
	if v == "" {
		r.cfg.Logger.Warn("review", "event", "version-unread")
	}
	r.mu.Lock()
	r.claude = v
	r.mu.Unlock()
	return v
}

func (r *Reviews) readVersion(ctx context.Context) string {
	vctx, cancel := context.WithTimeout(ctx, versionFor)
	defer cancel()
	res, err := r.cfg.Host.Run(vctx, Command{Argv: []string{r.cfg.Command, "--version"}, Dir: r.cfg.Dir, Pass: []string{"DISABLE_AUTOUPDATER"}, Timeout: versionFor})
	if err != nil || res.Code != 0 || res.TimedOut {
		return ""
	}
	if f := strings.Fields(res.Stdout); len(f) > 0 && reviewVersionText.MatchString(f[0]) {
		return f[0]
	}
	return ""
}

func (r *Reviews) checkRecords() ([]ReviewerCheck, bool) {
	if r.cfg.Checks == nil {
		return nil, false
	}
	r.checkStore.Lock()
	defer r.checkStore.Unlock()
	list, err := r.cfg.Checks.Load()
	if err != nil {
		r.cfg.Logger.Warn("review check", "event", "records-unreadable")
		return nil, true
	}
	return list, true
}

func (r *Reviews) saveCheck(c ReviewerCheck) {
	if r.cfg.Checks == nil {
		return
	}
	r.checkStore.Lock()
	defer r.checkStore.Unlock()
	list, err := r.cfg.Checks.Load()
	if err != nil {
		r.cfg.Logger.Warn("review check", "event", "records-unreadable")
		return
	}
	list = slices.DeleteFunc(list, func(x ReviewerCheck) bool { return x.Agent == c.Agent && x.StartedAt.Equal(c.StartedAt) })
	list = keptChecks(append(list, c))
	if err := r.cfg.Checks.Save(list); err != nil {
		r.cfg.Logger.Warn("review check", "event", "records-not-saved")
	}
}

func (r *Reviews) settleChecks() error {
	if r.cfg.Checks == nil {
		return nil
	}
	r.checkStore.Lock()
	defer r.checkStore.Unlock()
	list, err := r.cfg.Checks.Load()
	if err != nil {
		r.cfg.Logger.Warn("review check", "event", "records-unreadable")
		return nil
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(c ReviewerCheck) bool { return c.Result == review.CheckRunning })
	if len(kept) == len(list) {
		return nil
	}
	return r.cfg.Checks.Save(kept)
}

func keptChecks(list []ReviewerCheck) []ReviewerCheck {
	slices.SortStableFunc(list, func(a, b ReviewerCheck) int { return b.StartedAt.Compare(a.StartedAt) })
	seen := map[string]int{}
	out := make([]ReviewerCheck, 0, len(list))
	for _, c := range list {
		seen[c.Agent]++
		if seen[c.Agent] <= checksPerAgent && len(out) < checksKept {
			out = append(out, c)
		}
	}
	slices.Reverse(out)
	return out
}

func latestCovering(checks []ReviewerCheck, agent string, v review.Versions) (ReviewerCheck, bool) {
	return newestCovering(checks, agent, v, func(review.CheckResult) bool { return true })
}

func newestCovering(checks []ReviewerCheck, agent string, v review.Versions, keep func(review.CheckResult) bool) (ReviewerCheck, bool) {
	var out ReviewerCheck
	found := false
	for _, c := range checks {
		if c.Agent == agent && c.Versions.Covers(v) && keep(c.Result) && (!found || c.StartedAt.After(out.StartedAt)) {
			out, found = c, true
		}
	}
	return out, found
}

func holdingCheck(checks []ReviewerCheck, agent string, v review.Versions) (ReviewerCheck, bool) {
	if c, ok := newestCovering(checks, agent, v, func(r review.CheckResult) bool { return r == review.CheckRunning }); ok {
		return c, true
	}
	settled := func(r review.CheckResult) bool {
		return r == review.CheckPassed || r == review.CheckWarned || r == review.CheckFailure
	}
	if c, ok := newestCovering(checks, agent, v, settled); ok {
		return c, true
	}
	return latestCovering(checks, agent, v)
}

func latestOf(checks []ReviewerCheck, agent string) *ReviewerCheck {
	var out *ReviewerCheck
	for i := range checks {
		if checks[i].Agent == agent && (out == nil || checks[i].StartedAt.After(out.StartedAt)) {
			c := checks[i]
			out = &c
		}
	}
	return out
}

func (r *Reviews) passHeld(e ReviewEntry, checks []ReviewerCheck, checked bool) (review.Channel, string) {
	v := versionsOf(e)
	if why := r.cfg.Minimum.Below(v, e.Record.Provider != UploadedProvider); why != "" {
		return review.BelowMinimum, "the reviewer that made it is older than the minimum this engine accepts (" + why + "), so the pass doesn't count; it runs again when the agent next asks to push or the person presses Review"
	}
	if !checked {
		return "", ""
	}
	c, ok := holdingCheck(checks, e.Key.Agent, v)
	switch {
	case v.ClaudeCode == "":
		return review.Unchecked, "the reviewer container's Claude Code version couldn't be read when the review started, so no check can cover the pass and it doesn't count; it runs again when the agent next asks to push or the person presses Review"
	case !ok:
		return review.Unchecked, "the engine hasn't checked this reviewer since an update (" + v.Words() + "), so the pass doesn't count yet; it runs again, with a check after it, when the agent next asks to push or the person presses Review"
	case c.Result == review.CheckRunning:
		return review.Checking, "the engine is checking this reviewer after an update (" + v.Words() + "), and the pass counts once the check settles; push again when it ends"
	case c.Result == review.CheckFailure:
		return review.CheckFailed, "this reviewer failed its check after an update (" + c.Versions.Words() + "): it passed a change with a planted defect without reporting it, so its passes don't count until a later check passes; check the change yourself and allow the push with your reason, or press Check now under Reviewer checks"
	}
	return "", ""
}

func (r *Reviews) counted(list []ReviewEntry) []ReviewEntry {
	checks, checked := r.checkRecords()
	index := make(map[review.Key]int, len(list))
	for i, e := range list {
		index[e.Key] = i
	}
	type held struct {
		channel review.Channel
		why     string
	}
	memo := map[int]held{}
	var hold func(i, depth int) held
	hold = func(i, depth int) held {
		if h, ok := memo[i]; ok {
			return h
		}
		e := list[i]
		var h held
		if e.Record.State == review.Pass {
			h.channel, h.why = r.passHeld(e, checks, checked)
			if h.why == "" && e.Since != "" && depth < maxChain {
				if j, ok := index[review.Key{Commit: e.Since, Branch: e.Key.Branch, Agent: e.Key.Agent, Run: e.Key.Run}]; ok && j != i && hold(j, depth+1).why != "" {
					h = held{review.Unchecked, "it reviewed only what changed since commit " + shortSHA(e.Since) + ", whose pass doesn't count, so this one doesn't either; it runs again in full when the agent next asks to push or the person presses Review"}
				}
			}
		}
		memo[i] = h
		return h
	}
	out := slices.Clone(list)
	for i := range out {
		h := hold(i, 0)
		if h.why == "" {
			continue
		}
		out[i].Record.State, out[i].Record.Channel, out[i].Record.Held = review.Unknown, h.channel, h.why
		out[i].Reason = h.why
	}
	return out
}

func (r *Reviews) checksDue(entries []ReviewEntry) []dueCheck {
	if r.cfg.Checks == nil {
		return nil
	}
	checks, _ := r.checkRecords()
	var out []dueCheck
	for _, e := range entries {
		v := versionsOf(e)
		if e.Record.State != review.Pass || e.Record.Advisory || slices.ContainsFunc(out, func(d dueCheck) bool {
			return d.agent == e.Key.Agent && d.versions.Model == v.Model && d.versions.Effort == v.Effort
		}) {
			continue
		}
		if v.ClaudeCode == "" || r.cfg.Minimum.Below(v, e.Record.Provider != UploadedProvider) != "" {
			continue
		}
		if c, ok := latestCovering(checks, e.Key.Agent, v); ok && c.Result != review.CheckRunning {
			continue
		}
		out = append(out, dueCheck{agent: e.Key.Agent, role: e.Role, versions: v})
	}
	return out
}

func (r *Reviews) checkAfter(ctx context.Context, due []dueCheck, plugin string) string {
	notes := make([]string, len(due))
	var wg sync.WaitGroup
	for i, d := range due {
		wg.Go(func() {
			c, ok := r.ensureCheck(ctx, d, plugin)
			name := ReviewerName(workflow.Requirement{Agent: d.agent, Role: d.role})
			if !ok {
				notes[i] = "The " + name + "'s check after an update didn't finish, so its pass doesn't count yet."
				return
			}
			notes[i] = "The engine checked the " + name + " after an update: " + c.Reason + "."
		})
	}
	wg.Wait()
	return strings.Join(notes, " ")
}

func (r *Reviews) ensureCheck(ctx context.Context, d dueCheck, plugin string) (ReviewerCheck, bool) {
	for range 2 {
		r.mu.Lock()
		running := r.checking[d.agent]
		if running == nil {
			done := make(chan struct{})
			r.checking[d.agent] = done
			r.mu.Unlock()
			c := r.runCheck(ctx, d.agent, d.role, d.versions, plugin, ByUpdate)
			r.mu.Lock()
			delete(r.checking, d.agent)
			close(done)
			r.mu.Unlock()
			return c, c.Result != review.CheckRunning
		}
		r.mu.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return ReviewerCheck{}, false
		}
		checks, _ := r.checkRecords()
		if c, ok := latestCovering(checks, d.agent, d.versions); ok && c.Result != review.CheckRunning {
			return c, true
		}
	}
	return ReviewerCheck{}, false
}

func (r *Reviews) runCheck(ctx context.Context, agent string, role review.Role, v review.Versions, plugin, by string) ReviewerCheck {
	c := ReviewerCheck{Agent: agent, Role: role, Versions: v, Result: review.CheckRunning, By: by, StartedAt: r.cfg.Now()}
	r.saveCheck(c)
	r.cfg.Logger.Info("review check", "event", "started", "role", string(role), "by", by)
	defect, clean := checkChanges(role)
	var runs [2]CheckRunResult
	var costs [2]float64
	var models [2][]string
	if why := r.placeAgents(ctx, []plannedRole{{q: workflow.Requirement{Agent: agent, Role: role}}}); why != "" && providerFor(r.cfg.AgentFiles, agent) == UploadedProvider {
		runs[0] = CheckRunResult{State: review.Failed, Reason: why}
		runs[1] = runs[0]
	} else {
		var wg sync.WaitGroup
		for i, ch := range []checkChange{defect, clean} {
			wg.Go(func() {
				runs[i], costs[i], models[i] = r.checkRun(ctx, agent, role, v.ClaudeCode, ModelChoice{Model: v.Model, Effort: v.Effort}, ch, plugin)
			})
		}
		wg.Wait()
	}
	if ctx.Err() != nil {
		return c
	}
	for _, list := range models {
		for _, m := range list {
			if !slices.Contains(c.Versions.Models, m) && len(c.Versions.Models) < MaxCheckModels {
				c.Versions.Models = append(c.Versions.Models, m)
			}
		}
	}
	slices.Sort(c.Versions.Models)
	c.Defect, c.Clean, c.CostUSD = runs[0], runs[1], costs[0]+costs[1]
	caught := review.DefectCaught(role, agent, c.Defect.State, c.Defect.Channel, c.Defect.Verdict, c.Defect.Reply)
	c.Result = review.CheckOutcome(caught, c.Defect.State, c.Clean.State)
	c.Reason = checkReason(c, caught, defect.planted)
	c.EndedAt = r.cfg.Now()
	r.saveCheck(c)
	r.cfg.Logger.Info("review check", "event", "finished", "role", string(role), "result", string(c.Result))
	return c
}

func (r *Reviews) checkRun(ctx context.Context, agent string, role review.Role, claude string, m ModelChoice, ch checkChange, plugin string) (CheckRunResult, float64, []string) {
	snap, err := r.cfg.Snapshots.WriteMade(ch.base, ch.head, ch.commits)
	if err != nil {
		return CheckRunResult{State: review.Failed, Reason: clip(checkSnapFailed+err.Error(), 400)}, 0, nil
	}
	defer func() {
		if err := r.cfg.Snapshots.Remove(snap.ID); err != nil {
			r.cfg.Logger.Warn("review check", "event", "snapshot-not-removed")
		}
	}()
	provider := providerFor(r.cfg.AgentFiles, agent)
	e := ReviewEntry{Key: review.Key{Commit: ch.headSHA, Branch: checkBranch, Agent: agent}, Role: role, Base: ch.baseSHA, Covers: ch.baseSHA, StartedAt: r.cfg.Now(),
		Record: review.Record{Provider: provider, State: review.Running}, ClaudeCode: claude, PluginCommit: r.cfg.PluginCommit}
	e.Record.Shaped = shapedFor(review.RuleFor(agent), snap, nil)
	prompt := promptFor(e, reviewGiven{rules: rulesGiven{main: ch.baseSHA}})
	if provider == UploadedProvider {
		prompt += AgentVerdictAsk
	}
	e, _ = r.reviewAs(ctx, e, strings.TrimRight(r.cfg.Dir, "/")+"/"+snap.ID, plugin, prompt, m, nil)
	return CheckRunResult{State: e.Record.State, Verdict: e.Record.Verdict, Reason: e.Reason, Reply: e.Message, Channel: e.Record.Channel}, e.CostUSD, e.Models
}

func checkReason(c ReviewerCheck, caught bool, planted string) string {
	did := "blocked"
	if c.Defect.State != review.Block {
		did = "reported"
	}
	switch c.Result {
	case review.CheckFailure:
		return "it passed a change with a planted defect (" + planted + ") without reporting it, so its passes don't count until a later check passes"
	case review.CheckWarned:
		return "it " + did + " the planted defect but also blocked a clean change; its passes still count, but read its reply to the clean change"
	case review.CheckPassed:
		return "it " + did + " the planted defect and passed a clean change"
	}
	run, which := c.Defect, "the planted defect"
	if caught {
		run, which = c.Clean, "the clean change"
	}
	why := "it gave no verdict"
	if run.Reason != "" {
		why = run.Reason
	}
	return "the check isn't settled: on " + which + ", " + clip(why, 300) + "; its passes keep counting, and Check now tries once more"
}

func (r *Reviews) offeredChecks(checks []ReviewerCheck) []RoleCheckView {
	var out []RoleCheckView
	add := func(agent string, role review.Role) {
		if agent != "" && !slices.ContainsFunc(out, func(v RoleCheckView) bool { return v.Agent == agent }) {
			out = append(out, RoleCheckView{Agent: agent, Role: role})
		}
	}
	for _, role := range r.roles() {
		add(r.cfg.Agents[role], role)
	}
	rest := slices.Clone(checks)
	slices.SortFunc(rest, func(a, b ReviewerCheck) int { return strings.Compare(a.Agent, b.Agent) })
	for _, c := range rest {
		add(c.Agent, c.Role)
	}
	return out
}

func (r *Reviews) ReviewerChecks() (ReviewerChecksView, error) {
	checks, checked := r.checkRecords()
	if !checked {
		return ReviewerChecksView{}, explained(conflict("no checks"), "This engine keeps no reviewer checks.")
	}
	claude, plugin := r.Current()
	v := ReviewerChecksView{ClaudeCode: claude, Plugin: plugin, Minimum: r.cfg.Minimum}
	r.mu.Lock()
	running := map[string]bool{}
	for agent := range r.checking {
		running[agent] = true
	}
	r.mu.Unlock()
	for _, rc := range r.offeredChecks(checks) {
		rc.Check = latestOf(checks, rc.Agent)
		rc.Running = running[rc.Agent]
		v.Roles = append(v.Roles, rc)
	}
	return v, nil
}

func (r *Reviews) CheckNow(ctx context.Context, agent string, costOK bool) error {
	checks, checked := r.checkRecords()
	if !checked {
		return explained(conflict("no checks"), "This engine keeps no reviewer checks.")
	}
	offered := r.offeredChecks(checks)
	i := slices.IndexFunc(offered, func(v RoleCheckView) bool { return v.Agent == agent })
	if i < 0 {
		return explained(conflict("not offered"), "The engine checks only the reviewers it lists under Reviewer checks.")
	}
	role := offered[i].Role
	if AgentFileID.MatchString(agent) && providerFor(r.cfg.AgentFiles, agent) != UploadedProvider {
		return explained(conflict("removed"), "That agent file was removed, so the engine can't check it.")
	}
	r.mu.Lock()
	if r.checking[agent] != nil {
		r.mu.Unlock()
		return explained(conflict("checking"), "A check of this reviewer is running. Wait for it to finish.")
	}
	done := make(chan struct{})
	r.checking[agent] = done
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		delete(r.checking, agent)
		close(done)
		r.mu.Unlock()
	}
	signIn, err := r.CheckSignIn(ctx)
	if err != nil {
		release()
		return err
	}
	if err := judge("The reviewer container's Claude Code", signIn, r.cfg.Expected.Adopt(ReviewerAgent, signIn.Method), ReviewerSignInStep(), costOK, "the check"); err != nil {
		release()
		return err
	}
	plugin, err := r.cfg.Plugins.PluginPath(r.cfg.Plugin)
	if err != nil {
		release()
		return explained(conflict("no plugin"), clip("The reviewers' plugin isn't ready: "+err.Error(), maxOutput))
	}
	v := review.Versions{ClaudeCode: r.reviewerVersion(ctx), Plugin: r.pluginFor(agent)}
	if last := latestOf(checks, agent); last != nil {
		v.Model, v.Effort = last.Versions.Model, last.Versions.Effort
	}
	r.cfg.Logger.Info("review check", "event", "asked", "role", string(role))
	r.wg.Go(func() {
		defer release()
		base := r.cfg.Base
		if why := r.check(base); why != "" {
			now := r.cfg.Now()
			r.saveCheck(ReviewerCheck{Agent: agent, Role: role, Versions: v, Result: review.CheckUnsettled, By: ByPage, StartedAt: now, EndedAt: now,
				Reason: "the check isn't settled: it didn't start, because " + why + "; its passes keep counting, and Check now tries once more"})
			return
		}
		r.runCheck(base, agent, role, v, plugin, ByPage)
	})
	return nil
}
