package session

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
)

const (
	maxExtraCached  = 200
	extraFor        = 90 * time.Second
	maxExtraFiles   = 3000
	maxScannedBlob  = 2 << 20
	maxLockBlob     = 16 << 20
	maxScannedBytes = 96 << 20
)

type BlobData struct {
	Data []byte
	Cut  bool
}

type ChangeBlobs interface {
	ChangedFiles(ctx context.Context, repo, from, to string) ([]push.Change, error)
	Blobs(repo string, shas []string, limit, budget int64) (map[string]BlobData, error)
}

type ChangeNote struct {
	Path string
	What string
}

type FileKind struct {
	Path   string
	Kind   string
	Target string
}

type PushExtra struct {
	Commit           string
	Base             string
	Problems         []string
	Tests            []ChangeNote
	Dependencies     []DependencyChange
	MoreDependencies int
	Kinds            []FileKind
	Workflows        []WorkflowRun
	WorkflowsProblem string
	Lookup           string
	Locked           *LockedChange
}

type PushExtras struct {
	Files     ChangeBlobs
	Workflows WorkflowReader
	Registry  PackageRegistry
	LookupsOn func(project string) bool
	Locks     TestLocks
	Base      context.Context
	Now       func() time.Time
}

type pushExtras struct {
	cfg     PushExtras
	mu      sync.Mutex
	cached  map[string]*PushExtra
	order   []string
	running map[string]bool
}

func (p *Pushes) UseExtras(x PushExtras) {
	if x.Files == nil {
		return
	}
	if x.Base == nil {
		x.Base = context.Background()
	}
	if x.Now == nil {
		x.Now = time.Now
	}
	p.extras.Store(&pushExtras{cfg: x, cached: map[string]*PushExtra{}, running: map[string]bool{}})
}

func extraKey(sid, commit, lock string) string { return sid + " " + commit + " " + lock }

func (p *Pushes) LockMoved(sid string) {
	p.mu.Lock()
	v, ok := p.views[sid]
	p.mu.Unlock()
	if ok && v.Commit != "" && !v.Running {
		p.show(sid, v.clone())
	}
}

func (p *Pushes) extraFor(sid string, v PushView) *PushExtra {
	b := p.extras.Load()
	if b == nil || v.Commit == "" || v.Running {
		return v.Extra
	}
	lock := ""
	if b.cfg.Locks != nil {
		lock = b.cfg.Locks.LockKey(sid)
	}
	key := extraKey(sid, v.Commit, lock)
	b.mu.Lock()
	defer b.mu.Unlock()
	if x := b.cached[key]; x != nil {
		return x
	}
	if b.running[key] {
		return nil
	}
	b.running[key] = true
	go func() {
		x := p.computeExtra(b, sid, v.Commit)
		b.mu.Lock()
		delete(b.running, key)
		b.cached[key] = x
		b.order = append(b.order, key)
		if len(b.order) > maxExtraCached {
			delete(b.cached, b.order[0])
			b.order = b.order[1:]
		}
		b.mu.Unlock()
		p.mu.Lock()
		cur, ok := p.views[sid]
		if !ok || cur.Commit != v.Commit || cur.Running {
			p.mu.Unlock()
			return
		}
		cur.Extra = x
		p.views[sid] = cur.clone()
		p.mu.Unlock()
		p.cfg.Show(sid, cur.clone())
	}()
	return nil
}

func (p *Pushes) computeExtra(b *pushExtras, sid, head string) *PushExtra {
	out := &PushExtra{Commit: head, Lookup: "off"}
	t, ok := p.cfg.Targets(sid)
	if !ok {
		out.Problems = append(out.Problems, "only a session this engine started has these facts")
		return out
	}
	main, err := p.cfg.Repos.Main(t.Name)
	if err != nil {
		out.Problems = append(out.Problems, "the engine couldn't read the repository's record")
		return out
	}
	base := ""
	if main != "" {
		if base, err = p.cfg.History.MergeBase(context.Background(), t.Repo, head, main); err != nil {
			out.Problems = append(out.Problems, clip("the engine couldn't find where the branch leaves the main branch: "+err.Error(), 300))
			return out
		}
	}
	out.Base = base
	if base == "" {
		out.Problems = append(out.Problems, "the branch shares no history with the main branch the engine fetched, so every file counts as new")
	}
	scan := scanChange(b.cfg.Files, t.Repo, base, head)
	out.Problems = append(out.Problems, scan.problems...)
	out.Tests, out.Kinds = scan.tests, scan.kinds
	out.Dependencies, out.MoreDependencies = scan.deps, scan.moreDeps
	if b.cfg.Workflows != nil {
		heads, err := b.cfg.Workflows.Workflows(t.Repo, head)
		if err != nil {
			out.WorkflowsProblem = clip("the engine couldn't read .github/workflows/ in the commit: "+err.Error(), 300)
		} else {
			var bases []WorkflowFile
			compare := base != ""
			if compare {
				if bases, err = b.cfg.Workflows.Workflows(t.Repo, base); err != nil {
					compare = false
				}
			}
			out.Workflows = WorkflowRuns(heads, bases, t.Branch, compare)
		}
	}
	if b.cfg.Registry != nil && b.cfg.LookupsOn != nil && b.cfg.LookupsOn(t.Name) {
		ctx, cancel := context.WithTimeout(b.cfg.Base, extraFor)
		out.Lookup = lookupPackages(ctx, b.cfg.Registry, out.Dependencies, b.cfg.Now())
		cancel()
	}
	if b.cfg.Locks != nil {
		ctx, cancel := context.WithTimeout(b.cfg.Base, extraFor)
		out.Locked = b.cfg.Locks.LockedTests(ctx, sid, head)
		cancel()
	}
	out.Problems = slices.Compact(out.Problems)
	maskExtra(out)
	return out
}

func maskExtra(x *PushExtra) {
	for i := range x.Tests {
		x.Tests[i].Path, x.Tests[i].What = shownPath(x.Tests[i].Path), maskSecrets(x.Tests[i].What)
	}
	for i := range x.Kinds {
		x.Kinds[i].Path, x.Kinds[i].Target = shownPath(x.Kinds[i].Path), shownPath(x.Kinds[i].Target)
	}
	for i := range x.Dependencies {
		d := &x.Dependencies[i]
		d.File, d.Name, d.From, d.To = shownPath(d.File), maskSecrets(d.Name), maskSecrets(d.From), maskSecrets(d.To)
	}
	for i := range x.Workflows {
		w := &x.Workflows[i]
		w.Path, w.Name = shownPath(w.Path), maskSecrets(w.Name)
		for j := range w.Secrets {
			w.Secrets[j] = maskSecrets(w.Secrets[j])
		}
	}
}
