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
	"strings"
	"syscall"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	ArchiveFormat  = 1
	manifestName   = "manifest.json"
	importedFolder = "imported"
	maxManifest    = 1 << 20
	maxAuditFile   = maxAudit + 8<<20
	maxPrefsFile   = 4096
	prefsFile      = "prefs.json"
	KeepFolder     = "keep"
)

type storeClass string

const (
	classKept    storeClass = session.ImportKept
	classDisplay storeClass = session.ImportDisplay
	classOff     storeClass = session.ImportOff
	classDropped storeClass = session.ImportDropped
	classOut     storeClass = session.ImportLeftOut
)

type placing int

const (
	placeReplace placing = iota
	placeKeepOwn
	placeAudit
)

type storeRow struct {
	class   storeClass
	version int
	limit   int64
	place   placing
	label   string
}

func loaded[T any](load func() (T, error)) error {
	_, err := load()
	return err
}

var stores = map[string]storeRow{
	reposFile:           {class: classKept, version: 1, label: "projects, which come back needing a clone"},
	sessionsFile:        {class: classKept, version: 1, label: "sessions, which come back stopped and as history only"},
	workFile:            {class: classKept, version: 1, label: "project settings and work types; approvals come back for display only and automatic sends off"},
	reviewsFile:         {class: classDisplay, version: reviewsVersion, label: "review records and overrides"},
	checksFile:          {class: classDisplay, version: 1, label: "reviewer checks after an update"},
	approvalsFile:       {class: classDisplay, version: 1, label: "start-up approvals"},
	egressFile:          {class: classOff, version: 1, place: placeKeepOwn, label: "added hosts and the gateway"},
	keyRoutesFile:       {class: classOff, version: 1, label: "whether a pasted key may be saved"},
	projectCommandsFile: {class: classOff, version: 1, label: "check and setup commands"},
	reviewerAgentsFile:  {class: classOff, version: 1, label: "reviewer agent files and the roles they fill"},
	prFollowFile:        {class: classOff, version: 1, label: "pull request follows, send on its own and assign"},
	lookupsFile:         {class: classOff, version: 1, label: "public-registry lookups"},
	signInFile:          {class: classOff, version: 1, label: "the expected sign-in methods, reset to the default"},
	pairsFile:           {class: classDisplay, version: 1, label: "sessions started together and benchmarks, for display only"},
	lessonsFile:         {class: classDisplay, version: 1, label: "lessons from repeated findings, for display only; suggested lines not yet approved are dropped"},
	waitingFile:         {class: classDropped, version: 1, label: "automatic pushes waiting on reviews"},
	splitsFile:          {class: classDropped, version: 1, label: "tasks an agent proposed from a goal, waiting for the person"},
	forgeCardsFile:      {class: classDropped, version: 1, label: "waiting issue and pull request cards"},
	queuesFile:          {class: classDropped, version: 1, label: "queued messages"},
	notesFile:           {class: classDropped, version: 1, label: "notes waiting for agents"},
	homeSettingsFile:    {class: classDropped, version: 2, label: "the baseline of the agents' user settings"},
	capsFile:            {class: classKept, version: 1, label: "caps"},
	deniedFile:          {class: classKept, version: deniedVersion, label: "denied commits"},
	haltFile:            {class: classKept, version: 1, place: placeKeepOwn, label: "Stop everything and Pause all"},
	identityFile:        {class: classKept, version: 1, place: placeKeepOwn, label: "the name and email agents commit with"},
	personFile:          {class: classKept, version: 1, label: "the local person's id, which saved records and audit lines carry; the importing engine takes it as its own"},
	portsFile:           {class: classKept, version: 1, label: "port ranges"},
	modelsFile:          {class: classKept, version: 1, label: "model choices"},
	savedPromptsFile:    {class: classKept, version: 1, label: "saved prompts"},
	decisionLogFile:     {class: classKept, version: 1, label: "the person's edits to the decision log"},
	walkthroughsFile:    {class: classKept, version: 1, label: "manual test lists"},
	recapSeenFile:       {class: classKept, version: 1, label: "recap marks"},
	viewedFile:          {class: classKept, version: 1, label: "files marked viewed in the Changes view"},
	dropsFile:           {class: classKept, version: 1, place: placeKeepOwn, label: "counts of dropped records"},
	prefsFile:           {class: classKept, limit: maxPrefsFile, place: placeKeepOwn, label: "the page's preferences"},
	auditFile:           {class: classKept, limit: maxAuditFile, place: placeAudit, label: "the audit"},
	"audit.1.jsonl":     {class: classKept, limit: maxAuditFile, place: placeAudit, label: "the audit's older part"},
	tokensFile:          {class: classOut, label: "git host tokens are never exported"},
	boxesFile:           {class: classOut, label: "session containers belong to the stack that made them"},
	"engine.sock":       {class: classOut, label: "the lock socket is runtime only"},
	stampFile:           {class: classOut, label: "which engine last ran here"},
	importReportFile:    {class: classOut, label: "this engine's own last import"},
	KeepFolder:          {class: classOut, label: "the engine's copies of session commits are never exported"},
	repliesFolder:       {class: classOut, label: "reviewers' whole replies stay with the engine that ran them; each review record keeps its 4,000-character summary"},
	backupFolder:        {class: classOut, label: "this engine's copies from before a format change"},
	stagingFolder:       {class: classOut, label: "an import this engine hasn't applied"},
}

func ImportedReviews(dir string) func() ([]session.ReviewEntry, error) {
	return func() ([]session.ReviewEntry, error) {
		shown := filepath.Join(dir, importedFolder)
		if _, err := os.Lstat(shown); errors.Is(err, os.ErrNotExist) {
			return []session.ReviewEntry{}, nil
		}
		return Reviews{Dir: shown}.Load()
	}
}

func rowOf(name string) (storeRow, bool) {
	r, ok := stores[name]
	if ok && r.limit == 0 {
		r.limit = maxFile
	}
	if ok && r.version > 0 {
		if v, bumped := devVersions[name]; bumped {
			r.version = v
		}
	}
	return r, ok
}

func readsVersion(name string, v int) bool {
	r, ok := rowOf(name)
	if !ok || r.version == 0 {
		return v == 1
	}
	return v >= 1 && v <= r.version
}

func writesVersion(name string) int {
	r, ok := rowOf(name)
	if !ok || r.version == 0 {
		return 1
	}
	return r.version
}

func checkStore(name, d, workspace string) error {
	var err error
	switch name {
	case reposFile:
		err = loaded(Repos{Dir: d}.Load)
		if err == nil {
			err = checkImportedRepos(d)
		}
	case sessionsFile:
		err = loaded(NewSessions(d).Load)
		if err == nil {
			err = checkImportedSessions(d, workspace)
		}
	case workFile:
		err = loaded(NewWork(d).Load)
	case reviewsFile:
		err = loaded(Reviews{Dir: d}.Load)
	case checksFile:
		err = loaded(Checks{Dir: d}.Load)
	case approvalsFile:
		err = loaded(NewApprovals(d).Load)
	case egressFile:
		_, _, err = NewEgress(d).Load()
	case keyRoutesFile:
		err = loaded(NewKeyRoutes(d).Load)
	case projectCommandsFile:
		err = loaded(NewProjectCommands(d).Load)
	case reviewerAgentsFile:
		err = loaded(NewReviewerAgents(d).Load)
	case prFollowFile:
		err = loaded(NewPRFollows(d).Load)
	case lookupsFile:
		err = loaded(NewPackageLookups(d).Load)
	case signInFile:
		err = loaded(NewExpectations(d).Load)
	case capsFile:
		err = loaded(NewCaps(d).Load)
	case deniedFile:
		err = loaded(NewDenials(d).Load)
	case haltFile:
		err = loaded(NewHalt(d).Load)
	case identityFile:
		err = loaded(NewIdentity(d).Load)
	case personFile:
		_, err = loadPerson(d)
	case portsFile:
		err = loaded(NewPorts(d).Load)
	case modelsFile:
		err = loaded(NewModels(d).Load)
	case savedPromptsFile:
		err = loaded(NewSavedPrompts(d).Load)
	case walkthroughsFile:
		err = loaded(NewWalkthroughs(d).Load)
	case recapSeenFile:
		err = loaded(NewRecapSeen(d).Load)
	case viewedFile:
		err = loaded(NewViewed(d).Load)
	case decisionLogFile:
		err = loaded(NewDecisionLog(d).Load)
	case pairsFile:
		err = loaded(NewPairs(d).Load)
	case lessonsFile:
		err = loaded(NewLessons(d).Load)
	case dropsFile:
		err = loaded(Drops{Dir: d}.Load)
	case prefsFile:
		err = checkPrefs(d)
	}
	return err
}

func checkPrefs(d string) error {
	data, err := readLimited(d, prefsFile, maxPrefsFile)
	if err != nil {
		return err
	}
	var v map[string]any
	if json.Unmarshal(data, &v) != nil {
		return errors.New("prefs.json isn't a JSON object")
	}
	return nil
}

func readLimited(dir, name string, limit int64) ([]byte, error) {
	f, size, err := openOwned(dir, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if size > limit {
		return nil, fmt.Errorf("%s is larger than this engine reads", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("%s can't be read", name)
	}
	return data, nil
}

func openOwned(dir, name string) (*os.File, int64, error) {
	if !ownDir(dir) {
		return nil, 0, errDir
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("%s can't be opened", name)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%s isn't a 0600 file this user owns", name)
	}
	return f, info.Size(), nil
}

func formatOf(data []byte) (int, bool) {
	var v struct {
		Version *int `json:"version"`
	}
	if json.Unmarshal(data, &v) != nil || v.Version == nil {
		return 0, false
	}
	return *v.Version, true
}

type manifestStoreJSON struct {
	File    string `json:"file"`
	Class   string `json:"class"`
	Version int    `json:"version,omitempty"`
}

type leftOutJSON struct {
	Name string `json:"name"`
	Why  string `json:"why"`
}

type manifestJSON struct {
	Format     int                 `json:"format"`
	Engine     string              `json:"engine"`
	ExportedAt time.Time           `json:"exportedAt"`
	Holds      string              `json:"holds"`
	Stores     []manifestStoreJSON `json:"stores"`
	LeftOut    []leftOutJSON       `json:"leftOut"`
}

const holdsText = "Software Factory's engine state: projects, sessions, review records, settings and the audit, with the name and email agents commit with and the reasons the person typed. No git host token."

type Archive struct {
	Dir       string
	Workspace string
	Version   string
	Now       func() time.Time
}

var _ session.StateArchive = Archive{}

func (a Archive) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

type exportItem struct {
	name    string
	stored  string
	row     storeRow
	version int
}

func (a Archive) plan() ([]exportItem, []leftOutJSON, error) {
	if !ownDir(a.Dir) {
		return nil, nil, errDir
	}
	entries, err := os.ReadDir(a.Dir)
	if err != nil {
		return nil, nil, errors.New("the state folder can't be listed")
	}
	var items []exportItem
	var out []leftOutJSON
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && name == importedFolder {
			items = append(items, a.importedItems()...)
			continue
		}
		row, known := rowOf(name)
		switch {
		case e.IsDir() && known && row.class == classOut:
			out = append(out, leftOutJSON{Name: name + "/", Why: row.label})
		case e.IsDir():
			out = append(out, leftOutJSON{Name: name + "/", Why: "a folder this export doesn't hold"})
		case !known:
			out = append(out, leftOutJSON{Name: name, Why: "not a store this engine's export knows"})
		case row.class == classOut:
			out = append(out, leftOutJSON{Name: name, Why: row.label})
		case !e.Type().IsRegular():
			out = append(out, leftOutJSON{Name: name, Why: "not a regular file"})
		default:
			items = append(items, exportItem{name: name, stored: name, row: row})
		}
	}
	for i := range items {
		if items[i].row.version == 0 {
			continue
		}
		dir, file := filepath.Split(filepath.Join(a.Dir, items[i].stored))
		data, err := read(filepath.Clean(dir), file)
		if err != nil {
			return nil, nil, err
		}
		v, ok := formatOf(data)
		if !ok {
			return nil, nil, fmt.Errorf("%s has no format version", items[i].stored)
		}
		items[i].version = v
	}
	return items, out, nil
}

func (a Archive) importedItems() []exportItem {
	dir := filepath.Join(a.Dir, importedFolder)
	if !ownDir(dir) {
		return nil
	}
	f, _, err := openOwned(dir, reviewsFile)
	if err != nil {
		return nil
	}
	_ = f.Close()
	row, _ := rowOf(reviewsFile)
	return []exportItem{{name: importedFolder + "/" + reviewsFile, stored: filepath.Join(importedFolder, reviewsFile), row: row}}
}

func (a Archive) Export(w io.Writer) (session.ExportResult, error) {
	items, out, err := a.plan()
	if err != nil {
		return session.ExportResult{}, err
	}
	m := manifestJSON{Format: ArchiveFormat, Engine: a.Version, ExportedAt: a.now().UTC(), Holds: holdsText, Stores: []manifestStoreJSON{}, LeftOut: out}
	if m.LeftOut == nil {
		m.LeftOut = []leftOutJSON{}
	}
	for _, it := range items {
		m.Stores = append(m.Stores, manifestStoreJSON{File: it.name, Class: string(it.row.class), Version: it.version})
	}
	head, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return session.ExportResult{}, err
	}
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	at := m.ExportedAt
	if err := entry(tw, manifestName, int64(len(head)), at); err != nil {
		return session.ExportResult{}, err
	}
	if _, err := tw.Write(head); err != nil {
		return session.ExportResult{}, err
	}
	res := session.ExportResult{Stores: len(items), LeftOut: len(out)}
	for _, it := range items {
		n, err := a.copyInto(tw, it, at)
		if err != nil {
			return res, err
		}
		res.Bytes += n
	}
	if err := tw.Close(); err != nil {
		return res, err
	}
	return res, zw.Close()
}

func entry(tw *tar.Writer, name string, size int64, at time.Time) error {
	return tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: at, Typeflag: tar.TypeReg, Format: tar.FormatPAX})
}

func (a Archive) copyInto(tw *tar.Writer, it exportItem, at time.Time) (int64, error) {
	dir, file := filepath.Split(filepath.Join(a.Dir, it.stored))
	f, size, err := openOwned(filepath.Clean(dir), file)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	if size > it.row.limit {
		return 0, fmt.Errorf("%s is larger than an export holds", it.name)
	}
	if err := entry(tw, it.name, size, at); err != nil {
		return 0, err
	}
	n, err := io.CopyN(tw, f, size)
	if err != nil {
		return n, fmt.Errorf("%s couldn't be copied into the export", it.name)
	}
	return n, nil
}

func cleanEntryName(name string) (string, bool, bool) {
	inner, nested := strings.CutPrefix(name, importedFolder+"/")
	if strings.ContainsAny(inner, "/\\\x00") || inner == "" || inner == "." || inner == ".." || strings.HasPrefix(inner, ".") {
		return "", false, false
	}
	return inner, nested, true
}
