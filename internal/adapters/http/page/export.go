package page

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	exportRoute     = "/api/export"
	importRoute     = "/api/import"
	backupRoute     = "/api/backup"
	cloneAgainRoute = "/api/clone-again"
	importType      = "application/gzip"
)

var backupRoutes = map[string]bool{exportRoute: true, backupRoute: true, cloneAgainRoute: true}

func (s *Server) UseBackups(b session.BackupPage) {
	s.mu.Lock()
	s.backups = b
	s.mu.Unlock()
}

func (s *Server) backupPage() session.BackupPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backups
}

type droppedJSON struct {
	Store string `json:"store"`
	Count int    `json:"count"`
	Since int64  `json:"since"`
	Last  int64  `json:"last"`
}

type copyJSON struct {
	Version string   `json:"version"`
	Files   []string `json:"files"`
	At      int64    `json:"at"`
}

type importedStoreJSON struct {
	File    string `json:"file"`
	Class   string `json:"class"`
	Version int    `json:"version,omitempty"`
	Label   string `json:"label"`
}

type importReportJSON struct {
	State      string              `json:"state"`
	Why        string              `json:"why,omitempty"`
	At         int64               `json:"at"`
	Engine     string              `json:"engine"`
	ExportedAt int64               `json:"exportedAt"`
	Stores     []importedStoreJSON `json:"stores"`
	Off        []string            `json:"off"`
	KeptOwn    []string            `json:"keptOwn"`
	LeftOut    []string            `json:"leftOut"`
	Projects   int                 `json:"projects"`
	Sessions   int                 `json:"sessions"`
}

func reportJSON(r session.ImportReport) importReportJSON {
	out := importReportJSON{State: r.State, Why: r.Why, At: millis(r.At), Engine: r.Engine, ExportedAt: millis(r.ExportedAt), Stores: []importedStoreJSON{},
		Off: nonNil(r.Off), KeptOwn: nonNil(r.KeptOwn), LeftOut: nonNil(r.LeftOut), Projects: r.Projects, Sessions: r.Sessions}
	for _, st := range r.Stores {
		out.Stores = append(out.Stores, importedStoreJSON{File: st.File, Class: st.Class, Version: st.Version, Label: st.Label})
	}
	return out
}

func backupJSON(v session.BackupView) map[string]any {
	drops := make([]droppedJSON, 0, len(v.Drops))
	for _, d := range v.Drops {
		drops = append(drops, droppedJSON{Store: d.Store, Count: d.Count, Since: millis(d.Since), Last: millis(d.Last)})
	}
	copies := make([]copyJSON, 0, len(v.Copies))
	for _, c := range v.Copies {
		copies = append(copies, copyJSON{Version: c.Version, Files: nonNil(c.Files), At: millis(c.At)})
	}
	var last any
	if v.Last != nil {
		last = reportJSON(*v.Last)
	}
	return map[string]any{"drops": drops, "copies": copies, "last": last, "needClone": nonNil(v.NeedClone), "canImport": v.CanImport, "why": v.Why}
}

type download struct {
	w       http.ResponseWriter
	name    string
	started bool
}

func (d *download) Write(p []byte) (int, error) {
	if !d.started {
		h := d.w.Header()
		h.Set("Content-Type", importType)
		h.Set("Content-Disposition", `attachment; filename="`+d.name+`"`)
		d.w.WriteHeader(http.StatusOK)
		d.started = true
	}
	return d.w.Write(p)
}

func (s *Server) backupAPI(w http.ResponseWriter, r *http.Request, body map[string]any) {
	b := s.backupPage()
	if b == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no state folder to export or import."})
		return
	}
	switch r.URL.Path {
	case exportRoute:
		if !keysWithin(body) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(session.ExportFor))
		d := &download{w: w, name: "software-factory-state-" + s.cfg.Now().UTC().Format("20060102-150405") + ".tar.gz"}
		_, err := b.Export(d)
		if err != nil && d.started {
			shared.For(r.Context(), s.logger).Warn("export", "event", "cut-off")
			panic(http.ErrAbortHandler)
		}
		if err != nil {
			code, p := status(err)
			if code == http.StatusInternalServerError {
				p = problem{Error: "export", Message: "The engine couldn't read its state folder, so it exported nothing."}
			}
			send(w, code, p)
		}
	case backupRoute:
		if !keysWithin(body) {
			refuse(w, http.StatusBadRequest, "fields")
			return
		}
		v, err := b.View()
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusOK, backupJSON(v))
	case cloneAgainRoute:
		f, ok := textFields(w, body, 200, "name")
		if !ok {
			return
		}
		k, err := b.CloneAgain(f["name"])
		if err != nil {
			code, p := status(err)
			send(w, code, p)
			return
		}
		send(w, http.StatusAccepted, map[string]any{"ok": true, "url": k.URL, "name": k.Name})
	}
}

type watchedBody struct {
	r    io.Reader
	over bool
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		b.over = true
	}
	return n, err
}

func importKind(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	kind, params, err := mime.ParseMediaType(values[0])
	return err == nil && len(params) == 0 && kind == importType
}

func (s *Server) importState(w http.ResponseWriter, r *http.Request) {
	if !importKind(r) {
		refuse(w, http.StatusUnsupportedMediaType, "type")
		return
	}
	if len(r.Header.Values("X-SF-Name")) != 0 || len(r.Header.Values("X-SF-Session")) != 0 {
		refuse(w, http.StatusBadRequest, "fields")
		return
	}
	tooBig := problem{Error: "body", Message: session.ImportTooBig}
	if r.ContentLength > session.MaxImport {
		send(w, http.StatusRequestEntityTooLarge, tooBig)
		return
	}
	b := s.backupPage()
	if b == nil {
		send(w, http.StatusNotImplemented, problem{Error: "not-yet", Message: "This engine has no state folder to import into."})
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(session.ImportFor))
	_ = rc.SetWriteDeadline(time.Now().Add(session.ImportFor + writeFor))
	body := &watchedBody{r: http.MaxBytesReader(w, r.Body, session.MaxImport)}
	report, err := b.Import(body)
	if err != nil && body.over {
		send(w, http.StatusRequestEntityTooLarge, tooBig)
		return
	}
	shared.For(r.Context(), s.logger).Info("import", "ok", err == nil)
	if err != nil {
		code, p := status(err)
		send(w, code, p)
		return
	}
	send(w, http.StatusOK, map[string]any{"ok": true, "report": reportJSON(report)})
}
