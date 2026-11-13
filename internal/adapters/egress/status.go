package egress

import (
	"encoding/json"
	"net/http"
)

const Version = "1"

type status struct {
	Version string   `json:"version"`
	Allow   []string `json:"allow"`
	Deny    []string `json:"deny"`
}

type refusalJSON struct {
	Side   string `json:"side"`
	Target string `json:"target"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type refusalsJSON struct {
	Version  string        `json:"version"`
	Refusals []refusalJSON `json:"refusals"`
	Dropped  int           `json:"dropped"`
}

func StatusHandler(policy Policy, books ...*Refusals) http.Handler {
	body, _ := json.Marshal(status{Version: Version, Allow: policy.Allow(), Deny: policy.Deny()})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /refusals/{marker}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mark := r.PathValue("marker")
		if !markerPattern.MatchString(mark) {
			http.NotFound(w, r)
			return
		}
		got, dropped, ok := lookup(books, mark)
		if !ok {
			http.NotFound(w, r)
			return
		}
		out := refusalsJSON{Version: Version, Refusals: make([]refusalJSON, 0, len(got)), Dropped: dropped}
		for _, f := range got {
			out.Refusals = append(out.Refusals, refusalJSON(f))
		}
		data, err := json.Marshal(out)
		if err != nil {
			http.Error(w, "refusals couldn't be written", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	return mux
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
