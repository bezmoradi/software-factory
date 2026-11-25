package egress

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	Version          = "2"
	ControlKeyHeader = "X-SF-Control-Key"
	maxControlBody   = 64 << 10
	maxKeyFile       = 256
)

type sideJSON struct {
	Allow   []string         `json:"allow"`
	Ports   []string         `json:"ports,omitempty"`
	Expires map[string]int64 `json:"expires"`
	Git     []string         `json:"git,omitempty"`
}

type status struct {
	Version string              `json:"version"`
	Boot    string              `json:"boot"`
	Set     bool                `json:"set"`
	Deny    []string            `json:"deny"`
	Sides   map[string]sideJSON `json:"sides"`
	Grants  map[string]sideJSON `json:"grants"`
}

type setJSON struct {
	Version string              `json:"version"`
	Sides   map[string]sideJSON `json:"sides"`
}

type refusalJSON struct {
	Side   string `json:"side"`
	Target string `json:"target"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Subnet string `json:"subnet,omitempty"`
}

type boxesJSON struct {
	Version   string              `json:"version"`
	Subnets   []string            `json:"subnets"`
	Reviewers []string            `json:"reviewers"`
	Actions   []string            `json:"actions,omitempty"`
	Grants    map[string]sideJSON `json:"grants,omitempty"`
	Stack     []string            `json:"stack,omitempty"`
	Git       []string            `json:"git,omitempty"`
}

type refusalsJSON struct {
	Version  string        `json:"version"`
	Refusals []refusalJSON `json:"refusals"`
	Dropped  int           `json:"dropped"`
}

type seenJSON struct {
	Host   string `json:"host"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Last   int64  `json:"last"`
}

type refusedJSON struct {
	Version string                `json:"version"`
	Sides   map[string][]seenJSON `json:"sides"`
	Subnets map[string][]seenJSON `json:"subnets"`
}

type StatusConfig struct {
	Live      *Live
	Books     []*Refusals
	Seen      map[string]*Seen
	KeyFile   string
	Subnets   *Subnets
	Reviewers *Subnets
	Grants    *Grants
	Tried     *SubnetSeen
	Logger    shared.Logger
}

func listsOf(raw map[string]sideJSON) (map[string]Lists, bool) {
	out := map[string]Lists{}
	for key, s := range raw {
		if s.Allow == nil {
			return nil, false
		}
		l := Lists{Allow: s.Allow, Ports: s.Ports, Expires: map[string]time.Time{}, Git: s.Git}
		for h, t := range s.Expires {
			if t <= 0 {
				return nil, false
			}
			l.Expires[h] = time.Unix(t, 0)
		}
		out[key] = l
	}
	return out, true
}

func prefixesOf(raw []string) ([]netip.Prefix, bool) {
	out := make([]netip.Prefix, 0, len(raw))
	for _, r := range raw {
		p, err := netip.ParsePrefix(r)
		if err != nil {
			return nil, false
		}
		out = append(out, p)
	}
	return out, true
}

func gitNamesOf(raw []string) ([]string, bool) {
	if len(raw) > MaxEntries {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		name, err := entryName(r)
		if err != nil || name != r || strings.HasPrefix(name, ".") {
			return nil, false
		}
		out = append(out, name)
	}
	return out, true
}

func StatusHandler(cfg StatusConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, statusOf(cfg.Live.snapshot(), cfg.Grants))
	})
	mux.HandleFunc("GET /refusals/{marker}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mark := r.PathValue("marker")
		if !markerPattern.MatchString(mark) {
			http.NotFound(w, r)
			return
		}
		got, dropped, ok := lookup(cfg.Books, mark)
		if !ok {
			http.NotFound(w, r)
			return
		}
		out := refusalsJSON{Version: Version, Refusals: make([]refusalJSON, 0, len(got)), Dropped: dropped}
		for _, f := range got {
			out.Refusals = append(out.Refusals, refusalJSON(f))
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("PUT /allow", func(w http.ResponseWriter, r *http.Request) {
		if !keyed(w, r, cfg.KeyFile) {
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "the lists must be JSON", http.StatusUnsupportedMediaType)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxControlBody))
		if err != nil {
			http.Error(w, "the lists are larger than egress reads", http.StatusRequestEntityTooLarge)
			return
		}
		var q setJSON
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&q) != nil || q.Version != Version || q.Sides == nil {
			http.Error(w, "the lists aren't ones egress reads", http.StatusBadRequest)
			return
		}
		lists := map[string]Lists{}
		for name, s := range q.Sides {
			if s.Allow == nil {
				http.Error(w, "a side has no allow list", http.StatusBadRequest)
				return
			}
			l := Lists{Allow: s.Allow, Expires: map[string]time.Time{}, Git: s.Git}
			for h, t := range s.Expires {
				if t <= 0 {
					http.Error(w, "an expiry isn't a time", http.StatusBadRequest)
					return
				}
				l.Expires[h] = time.Unix(t, 0)
			}
			lists[name] = l
		}
		if err := cfg.Live.Replace(lists); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, statusOf(cfg.Live.snapshot(), cfg.Grants))
	})
	mux.HandleFunc("PUT /boxes", func(w http.ResponseWriter, r *http.Request) {
		if !keyed(w, r, cfg.KeyFile) {
			return
		}
		if cfg.Subnets == nil || cfg.Reviewers == nil {
			http.Error(w, "egress takes no box subnets", http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "the subnets must be JSON", http.StatusUnsupportedMediaType)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBoxesBody))
		if err != nil {
			http.Error(w, "the subnets are larger than egress reads", http.StatusRequestEntityTooLarge)
			return
		}
		var q boxesJSON
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&q) != nil || q.Version != Version || q.Subnets == nil || q.Reviewers == nil || len(q.Subnets) > MaxSubnets || len(q.Reviewers) > MaxSubnets {
			http.Error(w, "the subnets aren't ones egress reads", http.StatusBadRequest)
			return
		}
		sessions, ok := prefixesOf(q.Subnets)
		reviewers, rok := prefixesOf(q.Reviewers)
		actions, aok := prefixesOf(q.Actions)
		stack, sok := prefixesOf(q.Stack)
		if !ok || !rok || !aok || !sok || len(stack) > MaxStack || len(actions) > MaxActionSubnets || len(actions) > 0 && cfg.Grants == nil {
			http.Error(w, "a subnet isn't one egress reads", http.StatusBadRequest)
			return
		}
		git, gok := gitNamesOf(q.Git)
		if !gok {
			http.Error(w, "the git hosts aren't host names egress reads", http.StatusBadRequest)
			return
		}
		for _, p := range actions {
			if slices.ContainsFunc(sessions, p.Overlaps) || slices.ContainsFunc(reviewers, p.Overlaps) {
				http.Error(w, "an action container's subnet is also another container's", http.StatusBadRequest)
				return
			}
		}
		boxes := append(slices.Clone(sessions), actions...)
		for _, p := range reviewers {
			if slices.ContainsFunc(boxes, p.Overlaps) {
				http.Error(w, "a subnet is both a session container's and a reviewer container's", http.StatusBadRequest)
				return
			}
		}
		for _, check := range []error{cfg.Subnets.Check(boxes), cfg.Reviewers.Check(reviewers)} {
			if check != nil {
				http.Error(w, check.Error(), http.StatusBadRequest)
				return
			}
		}
		raw, ok := listsOf(q.Grants)
		if !ok || len(q.Grants) > 0 && cfg.Grants == nil {
			http.Error(w, "the grants aren't ones egress reads", http.StatusBadRequest)
			return
		}
		var granted map[string]Policy
		if cfg.Grants != nil {
			if granted, err = cfg.Grants.Check(boxes, raw); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for _, p := range actions {
				l := raw[p.String()]
				if len(l.Allow)+len(l.Ports) != 1 || len(l.Git) > 0 {
					http.Error(w, "an action container's subnet needs exactly one host or host port and no git host", http.StatusBadRequest)
					return
				}
				if entry := slices.Concat(l.Allow, l.Ports)[0]; cfg.Grants.gitTarget(git, entry) {
					delete(granted, p.String())
					if cfg.Logger != nil {
						cfg.Logger.Warn("egress", "event", "action-git-refused", "subnet", p.String(), "target", entry)
					}
				}
			}
		}
		if err := cfg.Subnets.Replace(boxes); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := cfg.Reviewers.Replace(reviewers); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if cfg.Grants != nil {
			cfg.Grants.SetStack(stack)
			cfg.Grants.SetGitHosts(git)
			cfg.Grants.ReplaceAlone(granted, actions)
		}
		cfg.Tried.Keep(boxes)
		writeJSON(w, boxesJSON{Version: Version, Subnets: q.Subnets, Reviewers: q.Reviewers, Actions: q.Actions})
	})
	mux.HandleFunc("GET /refused", func(w http.ResponseWriter, r *http.Request) {
		if !keyed(w, r, cfg.KeyFile) {
			return
		}
		out := refusedJSON{Version: Version, Sides: map[string][]seenJSON{}, Subnets: cfg.Tried.list()}
		for name, book := range cfg.Seen {
			list := []seenJSON{}
			for _, e := range book.List() {
				list = append(list, seenJSON{Host: e.Host, Reason: e.Reason, Count: e.Count, Last: e.Last.Unix()})
			}
			out.Sides[name] = list
		}
		writeJSON(w, out)
	})
	return traced(mux, cfg.Logger)
}

type statusAnswer struct {
	http.ResponseWriter
	status int
}

func (a *statusAnswer) WriteHeader(code int) {
	if a.status == 0 {
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *statusAnswer) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	return a.ResponseWriter.Write(b)
}

func traced(next http.Handler, logger shared.Logger) http.Handler {
	if logger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(shared.RequestHeader)
		if len(values) != 1 || !shared.ValidRequest(values[0]) {
			next.ServeHTTP(w, r)
			return
		}
		a := &statusAnswer{ResponseWriter: w}
		next.ServeHTTP(a, r)
		logger.Info("egress control", "route", controlRoute(r.URL.Path), "method", r.Method, "status", a.status, shared.RequestKey, values[0])
	})
}

func controlRoute(path string) string {
	switch {
	case path == "/status", path == "/allow", path == "/boxes", path == "/refused":
		return path
	case strings.HasPrefix(path, "/refusals/"):
		return "/refusals"
	}
	return "other"
}

func statusOf(s snapshot, g *Grants) status {
	out := status{Version: Version, Boot: s.boot, Set: s.set, Deny: s.deny, Sides: map[string]sideJSON{}, Grants: g.snapshot()}
	if out.Deny == nil {
		out.Deny = []string{}
	}
	for _, name := range s.order {
		p := s.sides[name]
		sj := sideJSON{Allow: p.Allow(), Expires: map[string]int64{}}
		for h, t := range p.Expires() {
			sj.Expires[h] = t.Unix()
		}
		out.Sides[name] = sj
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "the answer couldn't be written", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func keyed(w http.ResponseWriter, r *http.Request, file string) bool {
	want := keyHash(file)
	if want == nil {
		http.Error(w, "egress has no control key yet", http.StatusServiceUnavailable)
		return false
	}
	given := r.Header.Values(ControlKeyHeader)
	if len(given) != 1 || len(given[0]) != 64 {
		http.Error(w, "the control key is missing", http.StatusUnauthorized)
		return false
	}
	sum := sha256.Sum256([]byte(given[0]))
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		http.Error(w, "the control key is wrong", http.StatusUnauthorized)
		return false
	}
	return true
}

func keyHash(file string) []byte {
	if file == "" {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyFile))
	if err != nil {
		return nil
	}
	sum, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(sum) != sha256.Size {
		return nil
	}
	return sum
}

func lookup(books []*Refusals, mark string) ([]Refusal, int, bool) {
	var all []Refusal
	dropped, found := 0, false
	for _, book := range books {
		if book == nil {
			continue
		}
		got, n, ok := book.Of(mark)
		if !ok {
			continue
		}
		all = append(all, got...)
		dropped += n
		found = true
	}
	return all, dropped, found
}
