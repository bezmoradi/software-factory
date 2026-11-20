package session

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	runOnMain = "press Run on main"
)

type MainRepos interface {
	List() ([]Repository, error)
	Main(name string) (string, error)
	Fresh(ctx context.Context, name string) (string, error)
}

type MainVerifyPage interface {
	ViewMain(ctx context.Context, repo string) (VerifyView, error)
	StartMain(ctx context.Context, repo string, kind VerifyKind, by string) (VerifyRun, error)
}

func (v *Verifier) notifyRun(r VerifyRun) {
	if r.SID != "" {
		v.notify(r.SID)
		return
	}
	if v.reg != nil {
		v.reg.Notify(Fields{"type": verifyFrame, "repo": r.Project.Name})
	}
}

func (v *Verifier) repository(name string) (Repository, error) {
	if v.cfg.Mains == nil {
		return Repository{}, explained(conflict("no-repos"), "This engine has no repositories, so it runs nothing on main.")
	}
	list, err := v.cfg.Mains.List()
	if err != nil {
		return Repository{}, explained(conflict("records"), "The engine couldn't read its list of repositories.")
	}
	i := slices.IndexFunc(list, func(r Repository) bool { return r.Name == name })
	if i < 0 {
		return Repository{}, explained(notFound("project"), "That project is no longer in the list.")
	}
	return list[i], nil
}

func (v *Verifier) recordedMain(name string) string {
	if v.cfg.Mains == nil {
		return ""
	}
	main, err := v.cfg.Mains.Main(name)
	if err != nil {
		return ""
	}
	return main
}

func (v *Verifier) mainRunsLocked(id ProjectID) []VerifyRun {
	var out []VerifyRun
	for _, r := range slices.Backward(v.runs) {
		if r.SID == "" && r.Project == id && r.Kind == VerifyCheck {
			out = append(out, r)
		}
	}
	return out
}

func (v *Verifier) mainCurrentLocked(id ProjectID, commit string, cmds ProjectCommands) (VerifyRun, bool) {
	hash, digest := commandHash(cmds.Check), VerifyDigest(cmds.Image)
	for _, r := range v.mainRunsLocked(id) {
		if r.Commit == commit && commit != "" && r.CommandHash == hash && digest != "" && VerifyDigest(r.Image) == digest {
			return r, true
		}
	}
	return VerifyRun{}, false
}

func (v *Verifier) mainLiveLocked(id ProjectID) bool {
	return slices.ContainsFunc(v.mainRunsLocked(id), func(r VerifyRun) bool {
		_, live := v.live[r.ID]
		return live
	})
}

func (v *Verifier) ViewMain(_ context.Context, repo string) (VerifyView, error) {
	r, err := v.repository(repo)
	if err != nil {
		return VerifyView{}, err
	}
	out := VerifyView{Branch: r.Branch, Head: v.recordedMain(repo)}
	id, cmds, known := v.project(repo)
	if !known {
		return out, nil
	}
	out.Command, out.HasCheck, out.Image = shownCommand(cmds.Check), cmds.Check != "", cmds.Image
	v.mu.Lock()
	defer v.mu.Unlock()
	out.Busy = len(v.live) >= MaxVerifyRuns
	runs := v.mainRunsLocked(id)
	out.Runs = runs[:min(len(runs), verifyShown)]
	if cur, ok := v.mainCurrentLocked(id, out.Head, cmds); ok {
		out.Current = &cur
	} else if len(runs) > 0 {
		out.Why = mainStaleWhy(runs[0], out.Head)
	}
	return out, nil
}

func mainStaleWhy(last VerifyRun, main string) string {
	if main != "" && last.Commit != main {
		return "The last run was on " + shortSHA(last.Commit) + ", and main is at " + shortSHA(main) + " now, so it doesn't count for main's commit."
	}
	return "The check command or the project's verification image changed since the last run, so it doesn't count."
}

func (v *Verifier) StartMain(ctx context.Context, repo string, kind VerifyKind, by string) (VerifyRun, error) {
	if kind != VerifyCheck {
		return VerifyRun{}, explained(invalid("kind"), "The engine runs verification runs of one kind on main: check.")
	}
	if v.cfg.Host == nil || v.cfg.Trees == nil {
		return VerifyRun{}, explained(conflict("no-helper"), "This engine has no box helper or verification folder, so it runs no verification.")
	}
	if why := v.halted(); why != "" {
		return VerifyRun{}, explained(conflict("halted"), why)
	}
	r, err := v.repository(repo)
	if err != nil {
		return VerifyRun{}, err
	}
	id, cmds, err := v.mainCommands(repo)
	if err != nil {
		return VerifyRun{}, err
	}
	v.mu.Lock()
	switch {
	case v.mains[repo] || v.mainLiveLocked(id):
		v.mu.Unlock()
		return VerifyRun{}, explained(conflict("running"), "A run of the check on main is running for this project. Wait for it.")
	case len(v.live) >= MaxVerifyRuns:
		v.mu.Unlock()
		return VerifyRun{}, explained(limited("verify-busy"), "The engine runs at most "+strconv.Itoa(MaxVerifyRuns)+" verification runs at once. Try again when one finishes.")
	}
	if v.mains == nil {
		v.mains = map[string]bool{}
	}
	v.mains[repo] = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.mains, repo)
		v.mu.Unlock()
	}()
	commit, err := v.cfg.Mains.Fresh(ctx, repo)
	if err != nil {
		if e, ok := errors.AsType[*Error](err); ok {
			return VerifyRun{}, explained(e, strings.Replace(e.Message, ", so no session started", ", so it ran nothing", 1))
		}
		return VerifyRun{}, explained(conflict("fetch"), "The engine couldn't fetch the project's main branch, so it ran nothing.")
	}
	if id, cmds, err = v.mainCommands(repo); err != nil {
		return VerifyRun{}, err
	}
	if why := v.halted(); why != "" {
		return VerifyRun{}, explained(conflict("halted"), why)
	}
	run, err := NewBox()
	if err != nil {
		return VerifyRun{}, explained(conflict("random"), "The engine couldn't make an id for the run.")
	}
	rec := VerifyRun{ID: run, Project: id, Branch: r.Branch, Commit: commit, Kind: kind, CommandHash: commandHash(cmds.Check), Image: cmds.Image,
		State: RunRunning, By: by, Started: v.cfg.Now()}
	live := &verifyLive{stop: make(chan struct{})}
	v.mu.Lock()
	if len(v.live) >= MaxVerifyRuns {
		v.mu.Unlock()
		return VerifyRun{}, explained(limited("verify-busy"), "The engine runs at most "+strconv.Itoa(MaxVerifyRuns)+" verification runs at once. Try again when one finishes.")
	}
	v.live[run] = live
	v.runs = append(v.runs, rec)
	if over := len(v.runs) - MaxVerifyRecords; over > 0 {
		v.runs = slices.Delete(v.runs, 0, over)
	}
	v.mu.Unlock()
	v.save()
	v.audit(ctx, rec, "started")
	shared.For(ctx, v.cfg.Logger).Info("verify", "event", "started", "run", run, "kind", string(kind), "by", by, "on", "main")
	v.notifyRun(rec)
	req := shared.RequestOf(ctx)
	t := PushTarget{Repo: r.Path, Name: repo, Branch: r.Branch}
	v.wg.Go(func() { v.execute(shared.WithRequest(v.cfg.Base, req), rec, t, cmds.Check, live) })
	return rec, nil
}

func (v *Verifier) mainCommands(repo string) (ProjectID, ProjectCommands, error) {
	id, cmds, known := v.project(repo)
	switch {
	case !known:
		return id, cmds, explained(notFound("project"), "The engine has no record of this project.")
	case cmds.Check == "":
		return id, cmds, explained(conflict("no-check"), "Set a check command first: this project has none, so there is nothing to run on main. Set one under Check and setup commands on the project's card.")
	case VerifyDigest(cmds.Image) == "":
		return id, cmds, explained(conflict("no-image"), "Set an image for verification runs first: this project names none, so it can't run one. Set an image pinned by digest under Check and setup commands on the project's card.")
	}
	return id, cmds, nil
}

func (v *Verifier) MainLine(project, main string) ReadinessLine {
	line := ReadinessLine{Kind: ReadyVerified, State: ReadyUnknown, Commit: main}
	id, cmds, known := v.project(project)
	switch {
	case !known:
		line.Note = "The engine has no record of this project's commands."
		return line
	case cmds.Check == "":
		line.Note = "Set a check command first, under Check and setup commands; then " + runOnMain + "."
		return line
	case VerifyDigest(cmds.Image) == "":
		line.Note = "Set an image for verification runs first, pinned by digest under Check and setup commands; then " + runOnMain + "."
		return line
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	cur, found := v.mainCurrentLocked(id, main, cmds)
	if !found {
		runs := v.mainRunsLocked(id)
		switch {
		case len(runs) == 0 && main == "":
			line.Note = "Not run on main yet; " + runOnMain + "."
			return line
		case len(runs) == 0:
			line.Note = "Not run on main's commit " + shortSHA(main) + "; " + runOnMain + "."
			return line
		}
		last := runs[0]
		if _, live := v.live[last.ID]; live || last.State == RunRunning {
			line.Commit, line.Result, line.At = last.Commit, RunRunning, last.Started
			line.Note = "Running on " + shortSHA(last.Commit) + "."
			return line
		}
		line.Note = mainStaleWhy(last, main) + " " + strings.ToUpper(runOnMain[:1]) + runOnMain[1:] + "."
		return line
	}
	line.Commit = cur.Commit
	if _, live := v.live[cur.ID]; live || cur.State == RunRunning {
		line.Result, line.At, line.Note = RunRunning, cur.Started, "Running on "+shortSHA(cur.Commit)+"."
		return line
	}
	line.At = cur.Ended
	if cur.State == RunPassed {
		line.State, line.Result, line.Found = ReadyFound, RunPassed, []string{"passed on " + shortSHA(cur.Commit)}
		return line
	}
	line.State, line.Result, line.Code, line.Tail = ReadyMissing, RunFailed, cur.Code, cur.Tail
	line.Note = mainFailure(cur)
	return line
}

func mainFailure(r VerifyRun) string {
	on := " on " + shortSHA(r.Commit)
	var text string
	switch r.State {
	case RunFailed:
		text = "The check failed" + on + " with exit code " + strconv.Itoa(r.Code) + "."
	case RunTimedOut:
		text = "The check timed out" + on + "."
	case VerifyOOM:
		text = "The check ran out of memory" + on + ", exit code " + strconv.Itoa(r.Code) + "."
	case VerifyInterrupted:
		text = "The run" + on + " was cut off by an engine restart, so it has no result; " + runOnMain + " again."
	case VerifyStopped:
		text = "The run" + on + " was stopped before it finished; " + runOnMain + " again."
	default:
		text = "The run" + on + " couldn't run."
	}
	if r.Reason != "" && (r.State == RunTimedOut || r.State == VerifyOOM || r.State == RunError) {
		text += " " + r.Reason
	}
	if r.Withheld {
		text += " Its output held a token-shaped string, so the engine kept none of it."
	}
	return text
}

func (v *Verifier) ForgetMain(id ProjectID) {
	v.mu.Lock()
	for _, r := range v.runs {
		if l, live := v.live[r.ID]; live && r.SID == "" && r.Project == id {
			l.cancel()
		}
	}
	before := len(v.runs)
	v.runs = slices.DeleteFunc(v.runs, func(r VerifyRun) bool {
		_, live := v.live[r.ID]
		return r.SID == "" && r.Project == id && !live
	})
	changed := len(v.runs) != before
	v.mu.Unlock()
	if changed {
		v.save()
	}
}
