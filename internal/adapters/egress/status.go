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
	"strings"
	"time"
)

const (
	Version          = "2"
	ControlKeyHeader = "X-SF-Control-Key"
	maxControlBody   = 64 << 10
	maxKeyFile       = 256
)

type sideJSON struct {
	Allow   []string         `json:"allow"`
	Expires map[string]int64 `json:"expires"`
}

type status struct {
	Version string              `json:"version"`
	Boot    string              `json:"boot"`
	Set     bool                `json:"set"`
	Deny    []string            `json:"deny"`
	Sides   map[string]sideJSON `json:"sides"`
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
	Version string   `json:"version"`
	Subnets []string `json:"subnets"`
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
}

type StatusConfig struct {
	Live    *Live
	Books   []*Refusals
	Seen    map[string]*Seen
	KeyFile string
	Subnets *Subnets
}

func StatusHandler(cfg StatusConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, statusOf(cfg.Live.snapshot()))
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
			l := Lists{Allow: s.Allow, Expires: map[string]time.Time{}}
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
		writeJSON(w, statusOf(cfg.Live.snapshot()))
	})
	mux.HandleFunc("PUT /boxes", func(w http.ResponseWriter, r *http.Request) {
		if !keyed(w, r, cfg.KeyFile) {
			return
		}
		if cfg.Subnets == nil {
			http.Error(w, "egress takes no box subnets", http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "the subnets must be JSON", http.StatusUnsupportedMediaType)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxControlBody))
		if err != nil {
			http.Error(w, "the subnets are larger than egress reads", http.StatusRequestEntityTooLarge)
			return
		}
		var q boxesJSON
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&q) != nil || q.Version != Version || q.Subnets == nil || len(q.Subnets) > MaxSubnets {
			http.Error(w, "the subnets aren't ones egress reads", http.StatusBadRequest)
			return
		}
		list := make([]netip.Prefix, 0, len(q.Subnets))
		for _, raw := range q.Subnets {
			p, err := netip.ParsePrefix(raw)
			if err != nil {
				http.Error(w, "a subnet isn't one egress reads", http.StatusBadRequest)
				return
			}
			list = append(list, p)
		}
		if err := cfg.Subnets.Replace(list); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, boxesJSON{Version: Version, Subnets: q.Subnets})
	})
	mux.HandleFunc("GET /refused", func(w http.ResponseWriter, r *http.Request) {
		if !keyed(w, r, cfg.KeyFile) {
			return
		}
		out := refusedJSON{Version: Version, Sides: map[string][]seenJSON{}}
		for name, book := range cfg.Seen {
			list := []seenJSON{}
			for _, e := range book.List() {
				list = append(list, seenJSON{Host: e.Host, Reason: e.Reason, Count: e.Count, Last: e.Last.Unix()})
			}
			out.Sides[name] = list
		}
		writeJSON(w, out)
	})
	return mux
}

func statusOf(s snapshot) status {
	out := status{Version: Version, Boot: s.boot, Set: s.set, Deny: s.deny, Sides: map[string]sideJSON{}}
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
