package session

import (
	"cmp"
	"errors"
	"path"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	DocsFolderName    = "docs"
	DocsVariable      = "SF_DOCS"
	MaxDocBytes       = 400 << 10
	ScratchFolderName = "scratch"
	TempVariable      = "TMPDIR"
	ClaudeTempVar     = "CLAUDE_CODE_TMPDIR"

	KnackpackDocsVariable = "KNACKPACK_DOCS"
	KnackpackDocsOff      = "off"
)

var (
	ErrDocMissing = errors.New("document missing")
	ErrDocTooBig  = errors.New("document too big")
	ErrDocNotFile = errors.New("document not a regular file")
)

type DocFiles interface {
	Read(box, docs, name string, limit int64) ([]byte, error)
	Make(box, docs string) error
	Drop(box, docs string) error
}

type DocState int

const (
	DocAbsent DocState = iota
	DocUnreadable
	DocFound
)

type DocRead struct {
	Path  string
	Text  string
	State DocState
	Why   string
}

type DocReader interface {
	ReadDoc(sid, name string) DocRead
	DocsPath(sid string) string
}

type docBook struct {
	files  DocFiles
	logger shared.Logger
	what   string
}

func DocsFolder(workspace, sid string) string {
	if workspace == "" || !sidPattern.MatchString(sid) {
		return ""
	}
	return path.Join(workspace, DocsFolderName, sid) + "/"
}

func ScratchFolder(workspace, sid string) string {
	if workspace == "" || !sidPattern.MatchString(sid) {
		return ""
	}
	return path.Join(workspace, ScratchFolderName, sid)
}

func docName(name string) bool {
	return name != "" && name != "." && name != ".." && path.Base(name) == name && len(name) <= 120
}

func (r *Registry) UseDocs(files DocFiles, logger shared.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = docBook{files: files, logger: logger}
}

func (r *Registry) docFiles() docBook {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.docs
}

func (r *Registry) UseScratch(files DocFiles, logger shared.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scratch = docBook{files: files, logger: logger, what: "scratch"}
}

func (b docBook) make(docs, box string) {
	if b.files == nil || !sidPattern.MatchString(docs) || !ValidBox(box) {
		return
	}
	if err := b.files.Make(box, docs); err != nil && b.logger != nil {
		b.logger.Warn(cmp.Or(b.what, "documents"), "event", "folder-not-made", "docs", docs)
	}
}

func (b docBook) drop(box, docs string) {
	if b.files == nil || !sidPattern.MatchString(docs) || !ValidBox(box) {
		return
	}
	go func() {
		if err := b.files.Drop(box, docs); err != nil && b.logger != nil {
			b.logger.Warn(cmp.Or(b.what, "documents"), "event", "not-removed", "docs", docs)
		}
	}()
}

func (r *Registry) makeDocs(docs, box string) {
	r.docFiles().make(docs, box)
}

func (r *Registry) makeScratch(docs, box string) {
	r.mu.Lock()
	scratch := r.scratch
	r.mu.Unlock()
	scratch.make(docs, box)
}

func (r *Registry) dropDocs(box, docs string) {
	r.docs.drop(box, docs)
	r.scratch.drop(box, docs)
}

func (d *Dispatcher) boxFor(sid string) string {
	d.mu.Lock()
	r := d.runs[sid]
	box := ""
	if r != nil {
		box = r.box
	}
	d.mu.Unlock()
	if ValidBox(box) {
		return box
	}
	return d.reg.boxOf(sid)
}

func (d *Dispatcher) docsFor(sid string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r := d.runs[sid]; r != nil {
		return r.docsID()
	}
	return sid
}

func (d *Dispatcher) DocsPath(sid string) string {
	box := d.boxFor(sid)
	if box == "" {
		return ""
	}
	return DocsFolder(BoxFolder(d.cfg.Workspace, box), d.docsFor(sid))
}

func (d *Dispatcher) ReadDoc(sid, name string) DocRead {
	folder := d.DocsPath(sid)
	files := d.reg.docFiles().files
	if folder == "" || files == nil || !docName(name) {
		return DocRead{}
	}
	out := DocRead{Path: folder + name}
	data, err := files.Read(d.boxFor(sid), d.docsFor(sid), name, MaxDocBytes)
	switch {
	case errors.Is(err, ErrDocMissing):
		return out
	case errors.Is(err, ErrDocTooBig):
		out.State, out.Why = DocUnreadable, "it is larger than 400 KiB"
	case errors.Is(err, ErrDocNotFile):
		out.State, out.Why = DocUnreadable, "it isn't a regular file: a symbolic link, a folder or something else"
	case err != nil:
		out.State, out.Why = DocUnreadable, "the engine couldn't open it"
	case len(data) > MaxDocBytes:
		out.State, out.Why = DocUnreadable, "it is larger than 400 KiB"
	case !utf8.Valid(data):
		out.State, out.Why = DocUnreadable, "it isn't UTF-8 text"
	default:
		out.State, out.Text = DocFound, string(data)
	}
	return out
}
