package session

import (
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const docsShown = 5

func (w *Work) docFolders(sid, repo string) []string {
	out := []string{workflow.KnackpackFolder}
	if f := w.packSetup(sid, repo).setup.Workflow.Folder; f != "" && !strings.EqualFold(f, workflow.KnackpackFolder) {
		out = append(out, f)
	}
	return out
}

func (w *Work) docsChecked(g gathered) bool {
	return g.listed && !g.scan.failed && !g.scan.cut && w.cfg.Contents != nil
}

func (w *Work) carriedDocs(g gathered) []string {
	if g.head == "" || !w.docsChecked(g) {
		return nil
	}
	folders := w.docFolders(g.sid, g.ws.repo)
	var docs []string
	for _, p := range g.scan.paths {
		if workflow.DocumentFolder(p, folders) != "" {
			docs = append(docs, p)
		}
	}
	slices.Sort(docs)
	return docs
}

func (w *Work) docsRefusal(g gathered) string {
	if g.head == "" {
		return ""
	}
	if !w.docsChecked(g) {
		if g.specs && (!g.listed || g.scan.failed) {
			return ""
		}
		return "the engine couldn't list this change or read every commit the push sends (it reads at most 1,000 commits), so it couldn't check them for workflow documents under " + workflow.KnackpackFolder + "/ or a pack's documents folder, which Software Factory never pushes; it pushes nothing it couldn't check, and the person's Allow doesn't change that: have the agent make the change smaller, or fix what the page names, then push again"
	}
	docs := w.carriedDocs(g)
	if len(docs) == 0 {
		return ""
	}
	shown := make([]string, 0, docsShown)
	for _, p := range docs[:min(len(docs), docsShown)] {
		shown = append(shown, Escape([]byte(maskLines(p, true))))
	}
	more := ""
	if len(docs) > docsShown {
		more = " and " + strconv.Itoa(len(docs)-docsShown) + " more"
	}
	return "this push carries workflow documents (" + strings.Join(shown, ", ") + more + "), which Software Factory never pushes: they belong in the session's documents folder, which $SF_DOCS names, outside the worktree; have the agent take them out of every commit on the branch, since deleting them in a new commit still pushes the earlier ones; a push that only deletes them, such as one that removes them from main, is allowed, and neither the person's Allow nor an allowed file changes this"
}

func (w *Work) docWrite(sid string, rels []string) string {
	ws, ok := w.session(sid)
	if !ok {
		return ""
	}
	folders := w.docFolders(sid, ws.repo)
	for _, rel := range rels {
		if f := workflow.DocumentFolder(rel, folders); f != "" {
			where := ""
			if r := w.docReader(); r != nil {
				where = r.DocsPath(sid)
			}
			return rel + " is under " + f + "/, a workflow documents folder, and Software Factory refuses every push that adds or changes a file there, so this write is denied. " + workflow.DocsWhere(where)
		}
	}
	return ""
}

func (w *Work) DocsCheck(sid string) {
	ws, ok := w.session(sid)
	if !ok {
		return
	}
	setup := w.packSetup(sid, ws.repo)
	d := w.readDocs(sid, setup.setup.Workflow, workflow.TypeOr(setup.setup.Types, ws.typeID).Profile())
	key := d.req.key() + "\x00" + d.plan.key()
	w.docMu.Lock()
	seen, had := w.docSeen[sid]
	w.docSeen[sid] = key
	w.docMu.Unlock()
	if had && seen == key {
		return
	}
	w.RecheckApproval(sid)
}

func (w *Work) forgetDocs(sid string) {
	w.docMu.Lock()
	delete(w.docSeen, sid)
	w.docMu.Unlock()
}
