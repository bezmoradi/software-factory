package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

func (i *Installer) readMarker(a Agent) (manifest, bool) {
	var m manifest
	data, err := os.ReadFile(filepath.Join(i.versionDir(a), marker))
	if err != nil || len(data) > maxMeta || json.Unmarshal(data, &m) != nil {
		return manifest{}, false
	}
	return m, true
}

func provenanceRecorded(m manifest, b Build) bool {
	if len(m.Provenance) != len(b.Packages) {
		return false
	}
	for n, p := range b.Packages {
		r := m.Provenance[n]
		if r.Package != p.Name || r.Version != p.Version || r.State == ProvenanceUnchecked {
			return false
		}
	}
	return true
}

func pinnedIntegrity(b Build) []string {
	out := make([]string, 0, len(b.Packages))
	for _, p := range b.Packages {
		out = append(out, p.Integrity)
	}
	return out
}

func (i *Installer) ProvenanceChecked(id string) bool {
	a, b, ok := i.find(id)
	if !ok || !i.Installed(id) {
		return false
	}
	m, ok := i.readMarker(a)
	return ok && provenanceRecorded(m, b)
}

func (i *Installer) CheckInstalled(ctx context.Context, id string, report func(string)) error {
	if report == nil {
		report = func(string) {}
	}
	a, b, ok := i.find(id)
	if !ok || !i.Installed(id) || i.ProvenanceChecked(id) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, installFor)
	defer cancel()
	unlock, err := lock(ctx, filepath.Join(i.cfg.Dir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := i.checkInstalled(ctx, a, b, report); err != nil {
		i.cfg.Logger.Warn("agent install", "event", "provenance-problem", "agent", a.ID)
		return fmt.Errorf("%s %s: %w", a.Name, a.Version, err)
	}
	i.cfg.Logger.Info("agent install", "event", "provenance-checked", "agent", a.ID, "version", a.Version)
	return nil
}

func (i *Installer) checkInstalled(ctx context.Context, a Agent, b Build, report func(string)) error {
	m, ok := i.readMarker(a)
	if !ok {
		return errors.New("the record of the installed copy can't be read")
	}
	if provenanceRecorded(m, b) {
		return nil
	}
	if m.Agent != a.ID || m.Version != a.Version || m.Arch != i.cfg.Arch || !slices.Equal(m.Integrity, pinnedIntegrity(b)) {
		return errors.New("the record of the installed copy doesn't name the pinned tarballs, so the engine can't vouch for it; remove " + i.versionDir(a) + " and run install-agents " + a.ID + " to install it again")
	}
	report(fmt.Sprintf("Checking the provenance of the installed %s %s against the registry's attestations for the pinned sha512...", a.Name, a.Version))
	var errs []error
	m.Provenance = make([]ProvenanceResult, 0, len(b.Packages))
	for _, p := range b.Packages {
		r, err := i.provenance(ctx, a, p)
		if r.State == "" {
			r = ProvenanceResult{Agent: a.ID, Package: p.Name, Version: p.Version, State: ProvenanceUnchecked, Note: sentence(err.Error())}
		}
		if err != nil {
			errs = append(errs, err)
		}
		report(ProvenanceLine(r))
		m.Provenance = append(m.Provenance, r)
	}
	if err := i.writeMarker(a, m); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (i *Installer) writeMarker(a Agent, m manifest) error {
	data, _ := json.MarshalIndent(m, "", "  ")
	dir := i.versionDir(a)
	f, err := os.CreateTemp(dir, "."+marker+"-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, 0o644)
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(dir, marker))
	}
	if err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the provenance result couldn't be saved: %w", err)
	}
	return nil
}
