package state

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	backupFolder = "backup"
	stampFile    = "engine-version.json"
	keptCopies   = 3
	earlierCopy  = "earlier"
	DevVersioned = savedPromptsFile
)

var (
	devVersions = map[string]int{}

	onDisk   sync.Map
	stampsMu sync.Mutex
	earlier  = map[string]string{}
	copyMu   sync.Mutex
)

func DevStoreVersion(v int) error {
	if v != 2 {
		return errors.New("the development store version must be 2")
	}
	devVersions[DevVersioned] = v
	return nil
}

type stampJSON struct {
	Version int    `json:"version"`
	Engine  string `json:"engine"`
}

func StampEngine(dir, version string) (string, error) {
	if !ownDir(dir) {
		return "", errDir
	}
	stampsMu.Lock()
	defer stampsMu.Unlock()
	before := ""
	if data, err := read(dir, stampFile); err == nil && data != nil {
		var s stampJSON
		if json.Unmarshal(data, &s) == nil && s.Version == 1 && versionText.MatchString(s.Engine) {
			before = s.Engine
		}
	}
	earlier[dir] = before
	if !versionText.MatchString(version) {
		return before, errors.New("the engine version isn't one a backup folder can be named after")
	}
	data, err := encode(stampJSON{Version: 1, Engine: version})
	if err != nil {
		return before, err
	}
	return before, writeFile(dir, stampFile, data)
}

func copyFolder(dir string) string {
	stampsMu.Lock()
	defer stampsMu.Unlock()
	if v := earlier[dir]; v != "" {
		return v
	}
	return earlierCopy
}

func keepOlder(dir, name string) error {
	row, ok := rowOf(name)
	if !ok || row.version == 0 {
		return nil
	}
	key := dir + "\x00" + name
	if v, seen := onDisk.Load(key); seen && v.(int) >= row.version {
		return nil
	}
	data, err := read(dir, name)
	if err != nil {
		return nil
	}
	have, versioned := formatOf(data)
	if data == nil || !versioned || have >= row.version {
		onDisk.Store(key, row.version)
		return nil
	}
	if err := copyOld(dir, name, data); err != nil {
		return fmt.Errorf("%s holds format %d and this engine writes %d, but its copy for a rollback couldn't be made, so it wasn't changed: %w", name, have, row.version, err)
	}
	onDisk.Store(key, row.version)
	return nil
}

func ownFolder(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if !ownDir(dir) {
		return errDir
	}
	return nil
}

func copyOld(dir, name string, data []byte) error {
	copyMu.Lock()
	defer copyMu.Unlock()
	root := filepath.Join(dir, backupFolder)
	if err := ownFolder(root); err != nil {
		return err
	}
	folder := filepath.Join(root, copyFolder(dir))
	if err := ownFolder(folder); err != nil {
		return err
	}
	if err := writeFile(folder, name, data); err != nil {
		return err
	}
	now := time.Now()
	_ = os.Chtimes(folder, now, now)
	return pruneCopies(root)
}

func pruneCopies(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type folder struct {
		name string
		at   time.Time
	}
	var list []folder
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, folder{e.Name(), info.ModTime()})
	}
	slices.SortFunc(list, func(a, b folder) int { return cmp.Compare(b.at.UnixNano(), a.at.UnixNano()) })
	for _, f := range list[min(keptCopies, len(list)):] {
		if err := os.RemoveAll(filepath.Join(root, f.name)); err != nil {
			return err
		}
	}
	return nil
}

func (a Archive) Copies() ([]session.BackupCopy, error) {
	root := filepath.Join(a.Dir, backupFolder)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return []session.BackupCopy{}, nil
	}
	if err != nil || !ownDir(root) {
		return nil, errDir
	}
	out := []session.BackupCopy{}
	for _, e := range entries {
		info, err := e.Info()
		if !e.IsDir() || err != nil {
			continue
		}
		c := session.BackupCopy{Version: e.Name(), At: info.ModTime(), Files: []string{}}
		files, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Type().IsRegular() {
				c.Files = append(c.Files, f.Name())
			}
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b session.BackupCopy) int { return b.At.Compare(a.At) })
	return out, nil
}
