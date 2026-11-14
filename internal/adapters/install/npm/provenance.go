package npm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ProvenanceVerified  = "verified"
	ProvenanceNone      = "none"
	ProvenanceUnpinned  = "unpinned"
	ProvenanceFailed    = "failed"
	ProvenanceUnchecked = "unchecked"
	maxAttestations     = 4 << 20
)

type ProvenanceResult struct {
	Agent      string    `json:"agent"`
	Package    string    `json:"package"`
	Version    string    `json:"version"`
	State      string    `json:"state"`
	Repository string    `json:"repository,omitempty"`
	Workflow   string    `json:"workflow,omitempty"`
	Ref        string    `json:"ref,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	LoggedAt   time.Time `json:"loggedAt,omitzero"`
	Note       string    `json:"note,omitempty"`
}

func attestationsURL(registry string, p Package) string {
	return strings.TrimSuffix(registry, "/") + "/-/npm/v1/attestations/" + strings.Replace(p.Name, "/", "%2f", 1) + "@" + url.PathEscape(p.Version)
}

func FetchAttestations(ctx context.Context, client *http.Client, registry string, p Package) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attestationsURL(registry, p), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("the registry didn't answer for the attestations of %s@%s", p.Name, p.Version)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("the registry answered %d for the attestations of %s@%s", resp.StatusCode, p.Name, p.Version)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttestations+1))
	if err != nil || len(data) > maxAttestations {
		return nil, false, fmt.Errorf("the attestations of %s@%s couldn't be read", p.Name, p.Version)
	}
	return data, true, nil
}

func Judge(a Agent, p Package, data []byte, published bool) ProvenanceResult {
	r := ProvenanceResult{Agent: a.ID, Package: p.Name, Version: p.Version}
	switch {
	case !published && !a.Source.Set():
		r.State = ProvenanceNone
		r.Note = "The registry has no provenance for this version, and none is pinned."
		return r
	case !published:
		r.State = ProvenanceFailed
		r.Note = "The registry has no provenance for this version, though the pin names a source to check it against."
		return r
	}
	if !a.Source.Set() {
		_, err := VerifyAttestations(data, p.Name, p.Version, p.Integrity, Source{})
		r.State = ProvenanceUnpinned
		r.Note = "The registry has provenance for this version, but the pin names no source, so it wasn't checked against one."
		if errors.Is(err, ErrNoBuildProvenance) {
			r.State = ProvenanceNone
			r.Note = "The registry has a publish attestation for this version but no build provenance."
		}
		return r
	}
	v, err := VerifyAttestations(data, p.Name, p.Version, p.Integrity, a.Source)
	if err != nil {
		r.State = ProvenanceFailed
		r.Note = sentence(err.Error())
		return r
	}
	r.State = ProvenanceVerified
	r.Repository, r.Workflow, r.Ref, r.Commit, r.LoggedAt = v.Repository, v.Workflow, v.Ref, v.Commit, v.LoggedAt
	return r
}

func (i *Installer) provenance(ctx context.Context, a Agent, p Package) (ProvenanceResult, error) {
	data, published, err := FetchAttestations(ctx, i.cfg.Client, i.cfg.Registry, p)
	if err != nil {
		if a.Source.Set() {
			return ProvenanceResult{}, err
		}
		return ProvenanceResult{Agent: a.ID, Package: p.Name, Version: p.Version, State: ProvenanceUnchecked, Note: sentence(err.Error())}, nil
	}
	r := Judge(a, p, data, published)
	if r.State == ProvenanceFailed {
		return r, fmt.Errorf("the provenance of %s@%s doesn't check out: %s", p.Name, p.Version, r.Note)
	}
	return r, nil
}

func (i *Installer) Provenance() []ProvenanceResult {
	var out []ProvenanceResult
	for _, a := range i.cfg.Agents {
		b, ok := a.Builds[i.cfg.Arch]
		if !ok {
			continue
		}
		var m manifest
		have := false
		installed := i.Installed(a.ID)
		if installed {
			if got, ok := i.readMarker(a); ok && len(got.Provenance) == len(b.Packages) {
				m, have = got, true
			}
		}
		for n, p := range b.Packages {
			if have && m.Provenance[n].Package == p.Name && m.Provenance[n].Version == p.Version {
				r := m.Provenance[n]
				r.Agent = a.ID
				if r.State == ProvenanceUnchecked {
					r.Note = strings.TrimSpace(r.Note + " " + checkAgain(a.ID))
				}
				out = append(out, r)
				continue
			}
			note := "Not installed yet."
			if installed {
				note = "Installed before the engine checked provenance. " + checkAgain(a.ID)
			}
			out = append(out, ProvenanceResult{Agent: a.ID, Package: p.Name, Version: p.Version, State: ProvenanceUnchecked, Note: note})
		}
	}
	return out
}

func checkAgain(id string) string {
	return "Run docker compose exec engine software-factory install-agents " + id + " to check it without installing it again; the engine also checks it at its next start unless SF_AGENTS_INSTALL is manual."
}

func ProvenanceLine(r ProvenanceResult) string {
	switch r.State {
	case ProvenanceVerified:
		return fmt.Sprintf("%s@%s: provenance verified, built by %s %s at %s.", r.Package, r.Version, r.Repository, r.Workflow, r.Ref)
	case ProvenanceNone:
		return fmt.Sprintf("%s@%s: no provenance. %s", r.Package, r.Version, r.Note)
	}
	return fmt.Sprintf("%s@%s: provenance %s. %s", r.Package, r.Version, r.State, r.Note)
}
