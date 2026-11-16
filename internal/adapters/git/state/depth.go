package state

import (
	"maps"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/review"
	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

type depthJSON struct {
	Advisory  []string       `json:"advisory,omitempty"`
	Runs      map[string]int `json:"runs,omitempty"`
	MaxFiles  int            `json:"maxFiles,omitempty"`
	MaxLines  int            `json:"maxLines,omitempty"`
	Extra     []string       `json:"extra,omitempty"`
	ExtraRuns int            `json:"extraRuns,omitempty"`
}

func depthNames(list []string, limit int) []string {
	var out []string
	for _, a := range list {
		if workflow.AgentName(a) && !slices.Contains(out, a) && len(out) < limit {
			out = append(out, a)
		}
	}
	return out
}

func depthOf(d *depthJSON) workflow.Depth {
	if d == nil {
		return workflow.Depth{}
	}
	out := workflow.Depth{Advisory: depthNames(d.Advisory, workflow.MaxAdvisory), Extra: depthNames(d.Extra, workflow.MaxDeeper),
		MaxFiles: inRange(d.MaxFiles, 0, workflow.MaxDepthSize), MaxLines: inRange(d.MaxLines, 0, workflow.MaxDepthSize)}
	if d.ExtraRuns >= 2 && d.ExtraRuns <= review.MaxRuns {
		out.ExtraRuns = d.ExtraRuns
	}
	for _, a := range slices.Sorted(maps.Keys(d.Runs)) {
		if n := d.Runs[a]; workflow.AgentName(a) && n >= 2 && n <= review.MaxRuns && len(out.Runs) < 20 {
			if out.Runs == nil {
				out.Runs = map[string]int{}
			}
			out.Runs[a] = n
		}
	}
	return out
}

func depthJSONOf(d workflow.Depth) *depthJSON {
	if len(d.Advisory) == 0 && len(d.Runs) == 0 && len(d.Extra) == 0 && d.MaxFiles == 0 && d.MaxLines == 0 && d.ExtraRuns == 0 {
		return nil
	}
	return &depthJSON{Advisory: d.Advisory, Runs: d.Runs, MaxFiles: d.MaxFiles, MaxLines: d.MaxLines, Extra: d.Extra, ExtraRuns: d.ExtraRuns}
}
