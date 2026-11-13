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

func StatusHandler(policy Policy) http.Handler {
	body, _ := json.Marshal(status{Version: Version, Allow: policy.Allow(), Deny: policy.Deny()})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	return mux
}
