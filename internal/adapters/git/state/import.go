package state

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

const (
	stagingFolder    = ".import"
	importReportFile = "import-report.json"
	readyMark        = "ready"
	stagedReport     = "report.json"
	maxEntries       = 200
	maxUnpacked      = 512 << 20
	maxReportLines   = 200
	maxReportText    = 300
	keptReviews      = 500
)

type importedStoreJSON struct {
	File    string `json:"file"`
	Class   string `json:"class"`
	Version int    `json:"version,omitempty"`
	Label   string `json:"label"`
}

type importReportJSON struct {
	Version    int                 `json:"version"`
	State      string              `json:"state"`
	Why        string              `json:"why,omitempty"`
	At         time.Time           `json:"at"`
	Engine     string              `json:"engine"`
	ExportedAt time.Time           `json:"exportedAt"`
	Stores     []importedStoreJSON `json:"stores"`
	Off        []string            `json:"off"`
	KeptOwn    []string            `json:"keptOwn"`
	LeftOut    []string            `json:"leftOut"`
	Projects   int                 `json:"projects"`
	Sessions   int                 `json:"sessions"`
	Former     string              `json:"formerPerson,omitempty"`
}

func reportJSONOf(r session.ImportReport) importReportJSON {
	out := importReportJSON{Version: 1, State: r.State, Why: r.Why, At: r.At.UTC(), Engine: r.Engine, ExportedAt: r.ExportedAt.UTC(),
		Stores: []importedStoreJSON{}, Off: lines(r.Off), KeptOwn: lines(r.KeptOwn), LeftOut: lines(r.LeftOut), Projects: r.Projects, Sessions: r.Sessions, Former: r.FormerPerson}
	for _, s := range r.Stores {
		out.Stores = append(out.Stores, importedStoreJSON{File: s.File, Class: s.Class, Version: s.Version, Label: s.Label})
	}
	return out
}

func reportOf(j importReportJSON) session.ImportReport {
	out := session.ImportReport{State: j.State, Why: text(j.Why), At: j.At, Engine: matching(j.Engine, versionText), ExportedAt: j.ExportedAt,
		Off: lines(j.Off), KeptOwn: lines(j.KeptOwn), LeftOut: lines(j.LeftOut), Projects: max(j.Projects, 0), Sessions: max(j.Sessions, 0), FormerPerson: matching(j.Former, personID)}
	for _, s := range j.Stores {
		if len(out.Stores) < maxEntries {
			out.Stores = append(out.Stores, session.ImportedStore{File: text(s.File), Class: text(s.Class), Version: s.Version, Label: text(s.Label)})
		}
	}
	return out
}

func text(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	if len(s) > maxReportText {
		s = s[:maxReportText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return controlChar.ReplaceAllString(s, " ")
}

func lines(list []string) []string {
	out := []string{}
	for _, l := range list {
		if t := text(l); t != "" && len(out) < maxReportLines {
			out = append(out, t)
		}
	}
	return out
}

type capped struct {
	r    io.Reader
	left int64
}

var errUnpacked = errors.New("the file unpacks to more than an export holds")

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errUnpacked
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (a Archive) staging() string { return filepath.Join(a.Dir, stagingFolder) }

func (a Archive) Discard() error {
	return os.RemoveAll(a.staging())
}

func createOwned(dir, name string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (a Archive) Stage(r io.Reader) (session.ImportReport, error) {
	if !ownDir(a.Dir) {
		return session.ImportReport{}, errDir
	}
	stage := a.staging()
	if err := os.RemoveAll(stage); err != nil {
		return session.ImportReport{}, errors.New("an earlier import's folder couldn't be removed")
	}
	in, out := filepath.Join(stage, "in"), filepath.Join(stage, "out")
	for _, d := range []string{stage, in, filepath.Join(in, importedFolder), out, filepath.Join(out, importedFolder)} {
		if err := ownFolder(d); err != nil {
			return session.ImportReport{}, errors.New("a folder for the import couldn't be made")
		}
	}
	report, err := unpack(r, in)
	if err != nil {
		return session.ImportReport{}, err
	}
	report.At = a.now()
	report.State = session.ImportStaged
	if err := a.checkStaged(in, &report); err != nil {
		return session.ImportReport{}, err
	}
	if err := a.transform(in, out, &report); err != nil {
		return session.ImportReport{}, err
	}
	data, err := encode(reportJSONOf(report))
	if err != nil {
		return session.ImportReport{}, err
	}
	if err := writeFile(out, stagedReport, data); err != nil {
		return session.ImportReport{}, errors.New("the import's report couldn't be written")
	}
	if err := writeFile(out, readyMark, []byte("1\n")); err != nil {
		return session.ImportReport{}, errors.New("the import couldn't be marked ready")
	}
	return report, nil
}

func unpack(r io.Reader, in string) (session.ImportReport, error) {
	var report session.ImportReport
	zr, err := gzip.NewReader(r)
	if err != nil {
		return report, errors.New("it isn't an export file from Software Factory")
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(&capped{r: zr, left: maxUnpacked})
	h, err := tr.Next()
	if err != nil || h.Name != manifestName || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > maxManifest {
		return report, errors.New("it doesn't start with an export's manifest")
	}
	raw, err := io.ReadAll(io.LimitReader(tr, maxManifest))
	if err != nil {
		return report, errors.New("its manifest can't be read")
	}
	var m manifestJSON
	if err := json.Unmarshal(raw, &m); err != nil {
		return report, errors.New("its manifest isn't one this engine reads")
	}
	if m.Format < 1 || m.Format > ArchiveFormat {
		return report, fmt.Errorf("it is export format %d; this engine reads format %d", m.Format, ArchiveFormat)
	}
	if !versionText.MatchString(m.Engine) {
		return report, errors.New("its manifest names no engine version")
	}
	report.Engine, report.ExportedAt = m.Engine, m.ExportedAt
	for _, l := range m.LeftOut {
		report.LeftOut = append(report.LeftOut, text(l.Name)+": "+text(l.Why)+" (left out by the exporting engine)")
	}
	seen := map[string]bool{}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return report, errors.New("the file is cut off or damaged")
		}
		if n >= maxEntries {
			return report, errors.New("it holds more files than an export does")
		}
		if h.Typeflag != tar.TypeReg {
			return report, errors.New("it holds something other than plain files")
		}
		name, nested, ok := cleanEntryName(h.Name)
		if !ok || seen[h.Name] {
			return report, errors.New("it holds a file name an export doesn't use")
		}
		seen[h.Name] = true
		row, known := rowOf(name)
		switch {
		case !known:
			report.LeftOut = append(report.LeftOut, text(h.Name)+": not a store this engine knows")
			continue
		case row.class == classOut:
			report.LeftOut = append(report.LeftOut, text(h.Name)+": "+row.label)
			continue
		case nested && name != reviewsFile:
			report.LeftOut = append(report.LeftOut, text(h.Name)+": not kept from an earlier import")
			continue
		case h.Size < 0 || h.Size > row.limit:
			return report, fmt.Errorf("%s is larger than this engine reads", name)
		}
		dir := in
		if nested {
			dir = filepath.Join(in, importedFolder)
		}
		if err := place(dir, name, tr, h.Size); err != nil {
			return report, err
		}
	}
	return report, nil
}

func place(dir, name string, r io.Reader, size int64) error {
	f, err := createOwned(dir, name)
	if err != nil {
		return fmt.Errorf("%s couldn't be written for the import", name)
	}
	_, err = io.CopyN(f, r, size)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, errUnpacked) {
			return errUnpacked
		}
		return fmt.Errorf("%s couldn't be read from the file", name)
	}
	return nil
}

func staged(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out
}

func versionCheck(dir, name string, row storeRow) (int, error) {
	if row.version == 0 {
		return 0, nil
	}
	data, err := read(dir, name)
	if err != nil {
		return 0, err
	}
	v, ok := formatOf(data)
	switch {
	case !ok || v < 1:
		return 0, fmt.Errorf("%s has no format version", name)
	case v > row.version:
		return v, fmt.Errorf("%s is format %d, newer than this engine reads (%d). Import it into the engine version that exported it, or a newer one", name, v, row.version)
	}
	return v, nil
}

func (a Archive) checkStaged(in string, report *session.ImportReport) error {
	for _, name := range staged(in) {
		row, _ := rowOf(name)
		if row.class == classDropped {
			report.Stores = append(report.Stores, session.ImportedStore{File: name, Class: string(row.class), Label: row.label})
			continue
		}
		v, err := versionCheck(in, name, row)
		if err != nil {
			return err
		}
		if err := checkStore(name, in, a.Workspace); err != nil {
			return fmt.Errorf("%s isn't a record this engine reads: %w", name, err)
		}
		report.Stores = append(report.Stores, session.ImportedStore{File: name, Class: string(row.class), Version: v, Label: row.label})
	}
	nested := filepath.Join(in, importedFolder)
	if slices.Contains(staged(nested), reviewsFile) {
		row, _ := rowOf(reviewsFile)
		if _, err := versionCheck(nested, reviewsFile, row); err != nil {
			return err
		}
		if _, err := (Reviews{Dir: nested}).Load(); err != nil {
			return fmt.Errorf("imported/reviews.json isn't a record this engine reads: %w", err)
		}
	}
	return nil
}

func copyRaw(from, to, name string, limit int64) error {
	f, size, err := openOwned(from, name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if size > limit {
		return fmt.Errorf("%s is larger than this engine reads", name)
	}
	return place(to, name, f, size)
}

func (a Archive) transform(in, out string, report *session.ImportReport) error {
	shown := filepath.Join(out, importedFolder)
	for _, name := range staged(in) {
		row, _ := rowOf(name)
		var err error
		switch {
		case row.class == classDropped:
		case name == sessionsFile:
			err = historyOnly(in, out, report)
		case name == workFile:
			err = workOff(in, out, report)
			if err == nil {
				err = copyRaw(in, shown, name, row.limit)
			}
		case name == reviewsFile:
			err = displayReviews(in, shown)
		case name == pairsFile:
			err = pairsHistory(in, out, report)
		case name == lessonsFile:
			err = lessonsShown(in, out, report)
		case row.class == classDisplay:
			err = copyRaw(in, shown, name, row.limit)
		case row.class == classOff:
			report.Off = append(report.Off, offLines(name, in)...)
			err = copyRaw(in, shown, name, row.limit)
			if err == nil && name == egressFile {
				err = egressAgents(in, out)
			}
		default:
			if name == reposFile {
				recs, lerr := (Repos{Dir: in}).Load()
				report.Projects, err = len(recs), lerr
			}
			if err == nil {
				err = copyRaw(in, out, name, row.limit)
			}
		}
		if err != nil {
			return fmt.Errorf("%s couldn't be prepared: %w", name, err)
		}
	}
	if !slices.Contains(staged(in), reviewsFile) && slices.Contains(staged(filepath.Join(in, importedFolder)), reviewsFile) {
		return displayReviews(in, shown)
	}
	return nil
}

func historyOnly(in, out string, report *session.ImportReport) error {
	runs, err := NewSessions(in).Load()
	if err != nil {
		return err
	}
	for i := range runs {
		runs[i].Sandboxed = false
		runs[i].CodexThread = ""
		runs[i].InterruptedAt = time.Time{}
		runs[i].InTurnSince = time.Time{}
		runs[i].HistoryOnly = true
	}
	report.Sessions = len(runs)
	return NewSessions(out).Save(runs)
}

func workOff(in, out string, report *session.ImportReport) error {
	st, err := NewWork(in).Load()
	if err != nil {
		return err
	}
	if n := len(st.Teams); n > 0 {
		report.Off = append(report.Off, plural(n, "team file approval")+" kept for display only: approve the team file again")
	}
	st.Teams = map[string]session.TeamApproval{}
	for name, p := range st.Projects {
		if p.Findings {
			report.Off = append(report.Off, name+": sending blocking findings to the agent on its own is off")
		}
		if p.PRComment {
			report.Off = append(report.Off, name+": the comment on the pull request after each push is off")
		}
		if depthJSONOf(p.Depth) != nil {
			report.Off = append(report.Off, name+": advisory roles, repeated runs and deeper review for larger changes are off; set them again to turn them on")
		}
		p.Findings, p.PRComment, p.Depth = false, false, workflow.Depth{}
		st.Projects[name] = p
	}
	for i := range st.Removed {
		p := &st.Removed[i].Data
		p.Findings, p.PRComment, p.Depth = false, false, workflow.Depth{}
	}
	approvals := 0
	for sid, w := range st.Sessions {
		if w.Requirements != nil || w.Plan != nil || len(w.Allowed) > 0 {
			approvals++
		}
		w.Requirements, w.Plan, w.Allowed = nil, nil, nil
		st.Sessions[sid] = w
	}
	if approvals > 0 {
		report.Off = append(report.Off, plural(approvals, "session")+" had requirement, plan or flagged-file approvals, kept for display only")
	}
	return NewWork(out).Save(st)
}

func pairsHistory(in, out string, report *session.ImportReport) error {
	list, err := NewPairs(in).Load()
	if err != nil {
		return err
	}
	for i := range list {
		list[i].History = true
	}
	if n := len(list); n > 0 {
		report.Off = append(report.Off, plural(n, "pair or benchmark")+" kept for display only: this engine starts no second session for them and keeps no choice between them")
	}
	return NewPairs(out).Save(list)
}

func lessonsShown(in, out string, report *session.ImportReport) error {
	st, err := NewLessons(in).Load()
	if err != nil {
		return err
	}
	dropped := 0
	settled := func(p session.LessonProject) session.LessonProject {
		n := len(p.Lines)
		p.Lines = slices.DeleteFunc(p.Lines, func(l session.LessonLine) bool { return l.State == session.LessonOffered })
		dropped += n - len(p.Lines)
		return p
	}
	for name, p := range st.Projects {
		st.Projects[name] = settled(p)
	}
	for i, r := range st.Removed {
		st.Removed[i].Data = settled(r.Data)
	}
	if dropped > 0 {
		report.Off = append(report.Off, plural(dropped, "suggested instruction line")+" not yet approved dropped; ask for suggestions again")
	}
	return NewLessons(out).Save(st)
}

func egressAgents(in, out string) error {
	e, ok, err := NewEgress(in).Load()
	if err != nil || !ok {
		return err
	}
	return NewEgress(out).Save(session.EgressSettings{Agents: e.Agents})
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

func offLines(name, in string) []string {
	var out []string
	switch name {
	case egressFile:
		e, _, err := NewEgress(in).Load()
		if err != nil {
			return nil
		}
		for _, h := range e.Added {
			out = append(out, "Network access: the added host "+h.Name+" is off; add it again to turn it on")
		}
		if e.Gateway.URL != "" {
			out = append(out, "Network access: the gateway "+e.Gateway.URL+" is off; set it again to turn it on")
		}
	case keyRoutesFile:
		all, err := NewKeyRoutes(in).Load()
		if err != nil {
			return nil
		}
		for side, k := range all {
			if k.Saved {
				out = append(out, "A key pasted for the "+side+" isn't here; paste it again in the sign-in dialog")
			}
		}
	case projectCommandsFile:
		all, err := NewProjectCommands(in).Load()
		if err != nil {
			return nil
		}
		for id, c := range all {
			if c.Check != "" {
				out = append(out, id.Name+": the check command "+c.Check+" is off; set it again to turn it on")
			}
			if c.Setup != "" {
				out = append(out, id.Name+": the setup command "+c.Setup+" is off; set it again to turn it on")
			}
		}
	case reviewerAgentsFile:
		st, err := NewReviewerAgents(in).Load()
		if err != nil {
			return nil
		}
		for _, f := range st.Files {
			out = append(out, f.Project+": a reviewer agent file added on the page is off; add it again to turn it on")
		}
		for _, r := range st.Roles {
			out = append(out, r.Project+": "+r.Name+" no longer fills the "+string(r.Role)+" role")
		}
	case prFollowFile:
		rec, err := NewPRFollows(in).Load()
		if err != nil {
			return nil
		}
		for id, s := range rec.Projects {
			if s.Auto {
				out = append(out, id.Name+": sending CI results and comments to the agent on its own is off")
			}
			if s.Assign {
				out = append(out, id.Name+": assigning the pull request to you is off")
			}
		}
		if n := len(rec.Follows); n > 0 {
			out = append(out, plural(n, "pull request follow")+" kept for display only; nothing is followed")
		}
	case lookupsFile:
		list, err := NewPackageLookups(in).Load()
		if err != nil {
			return nil
		}
		for _, p := range list {
			out = append(out, p+": public-registry lookups are off; turn them on again on the push card")
		}
	case signInFile:
		all, err := NewExpectations(in).Load()
		if err != nil {
			return nil
		}
		for who, x := range all {
			out = append(out, who+": the expected sign-in ("+string(x)+") is back to the default")
		}
	}
	slices.Sort(out)
	return out
}

func displayReviews(in, shown string) error {
	var list []reviewJSON
	for _, dir := range []string{filepath.Join(in, importedFolder), in} {
		data, err := read(dir, reviewsFile)
		if err != nil {
			return err
		}
		if data == nil {
			continue
		}
		var f reviewsJSON
		if err := json.Unmarshal(data, &f); err != nil || f.Version < 1 || f.Version > reviewsVersion {
			return errors.New("reviews.json isn't a review record this engine reads")
		}
		for _, r := range f.Reviews {
			if f.Version == 1 || r.Run < 2 {
				r.Run = 0
			}
			if f.Version == 1 {
				r.Advisory = false
			}
			list = slices.DeleteFunc(list, func(x reviewJSON) bool {
				return x.Commit == r.Commit && x.Branch == r.Branch && x.Agent == r.Agent && x.Run == r.Run
			})
			list = append(list, r)
		}
	}
	if len(list) > keptReviews {
		list = list[len(list)-keptReviews:]
	}
	f := reviewsJSON{Version: reviewsVersion, Reviews: list}
	for {
		data, err := encode(f)
		if err != nil {
			return err
		}
		if len(data) <= maxFile || len(f.Reviews) == 0 {
			return writeFile(shown, reviewsFile, data)
		}
		f.Reviews = f.Reviews[max(1, len(f.Reviews)/10):]
	}
}

func (a Archive) Last() (session.ImportReport, bool, error) {
	data, err := read(a.Dir, importReportFile)
	if err != nil || data == nil {
		return session.ImportReport{}, false, err
	}
	var j importReportJSON
	if err := json.Unmarshal(data, &j); err != nil || j.Version != 1 {
		return session.ImportReport{}, false, errors.New("import-report.json isn't a report this engine reads")
	}
	return reportOf(j), true, nil
}

func exists(dir, name string) bool {
	_, err := os.Lstat(filepath.Join(dir, name))
	return err == nil
}

func ApplyImport(dir string, now time.Time) (session.ImportReport, bool, error) {
	a := Archive{Dir: dir}
	stage := a.staging()
	out := filepath.Join(stage, "out")
	if !exists(out, readyMark) {
		if exists(dir, stagingFolder) {
			_ = os.RemoveAll(stage)
		}
		return session.ImportReport{}, false, nil
	}
	defer func() { _ = os.RemoveAll(stage) }()
	data, err := read(out, stagedReport)
	if err != nil || data == nil {
		return session.ImportReport{}, false, errors.New("the staged import has no report")
	}
	var j importReportJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return session.ImportReport{}, false, errors.New("the staged import's report can't be read")
	}
	report := reportOf(j)
	report.At = now
	recs, rerr := Repos{Dir: dir}.Load()
	runs, serr := NewSessions(dir).Load()
	switch {
	case rerr != nil || serr != nil:
		report.State, report.Why = session.ImportRefused, "the engine couldn't read its own projects or sessions, so it imported nothing"
	case len(recs) > 0 || len(runs) > 0:
		report.State, report.Why = session.ImportRefused, "a project or session was added before the engine restarted, so it imported nothing"
	default:
		report.State = session.ImportApplied
		if err := apply(dir, out, &report); err != nil {
			report.State, report.Why = session.ImportRefused, text("the import stopped part way: "+err.Error())
		}
	}
	saved, err := encode(reportJSONOf(report))
	if err == nil {
		err = writeFile(dir, importReportFile, saved)
	}
	return report, true, err
}

func apply(dir, out string, report *session.ImportReport) error {
	for _, name := range staged(out) {
		row, known := rowOf(name)
		if !known || name == readyMark || name == stagedReport || row.place == placeAudit {
			continue
		}
		if row.place == placeKeepOwn && exists(dir, name) {
			report.KeptOwn = append(report.KeptOwn, name+": this engine kept its own ("+row.label+")")
			continue
		}
		if err := os.Rename(filepath.Join(out, name), filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("%s couldn't be put in place", name)
		}
		if name == personFile {
			if old, now := adoptPerson(dir); old != "" && old != now {
				report.FormerPerson = old
				reownKept(dir, old, now)
			}
		}
	}
	shown := filepath.Join(dir, importedFolder)
	if err := ownFolder(shown); err != nil {
		return errors.New("the folder for display-only records couldn't be made")
	}
	for _, name := range staged(filepath.Join(out, importedFolder)) {
		if err := os.Rename(filepath.Join(out, importedFolder, name), filepath.Join(shown, name)); err != nil {
			return fmt.Errorf("%s couldn't be put in place", name)
		}
	}
	return mergeAudit(dir, out)
}

func mergeAudit(dir, out string) error {
	var parts []string
	for _, p := range []string{filepath.Join(out, "audit.1.jsonl"), filepath.Join(out, auditFile), filepath.Join(dir, "audit.1.jsonl")} {
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 || (len(parts) == 1 && filepath.Dir(parts[0]) == dir) {
		return nil
	}
	total := int64(0)
	for _, p := range parts {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		total += info.Size()
	}
	f, err := os.CreateTemp(dir, ".audit.1.jsonl.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	t := &tailWriter{w: f, skip: max(total-maxAudit, 0)}
	for _, p := range parts {
		if err := appendTail(t, p); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "audit.1.jsonl")); err != nil {
		return err
	}
	noteDropped(dir, auditFile, t.dropped)
	return nil
}

type tailWriter struct {
	w       io.Writer
	skip    int64
	mid     bool
	dropped int
}

func (t *tailWriter) Write(chunk []byte) (int, error) {
	n := len(chunk)
	for len(chunk) > 0 && (t.skip > 0 || t.mid) {
		i := slices.Index(chunk, '\n')
		if i < 0 {
			t.skip -= int64(len(chunk))
			t.mid = true
			return n, nil
		}
		t.skip -= int64(i + 1)
		t.mid = false
		t.dropped++
		chunk = chunk[i+1:]
	}
	if len(chunk) > 0 {
		if _, err := t.w.Write(chunk); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func appendTail(t *tailWriter, path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(t, f)
	return err
}
