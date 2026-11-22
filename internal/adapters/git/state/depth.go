package state

import (
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/workflow"
)

type depthJSON struct {
	Always    []string `json:"always,omitempty"`
	Advisory  []string `json:"advisory,omitempty"`
	MaxLines  int      `json:"maxLines,omitempty"`
	Extra     []string `json:"extra,omitempty"`
	Twice     bool     `json:"twice,omitempty"`
	ExtraRuns int      `json:"extraRuns,omitempty"`
}

func depthNames(list []string, taken *[]string) []string {
	var out []string
	for _, a := range list {
		if workflow.AgentName(a) && !slices.Contains(*taken, a) && len(*taken) < workflow.MaxAdded {
			out = append(out, a)
			*taken = append(*taken, a)
		}
	}
	return out
}

func depthOf(d *depthJSON) workflow.Depth {
	if d == nil {
		return workflow.Depth{}
	}
	var taken []string
	always := depthNames(d.Always, &taken)
	extra := depthNames(d.Extra, &taken)
	return workflow.Depth{Always: always, Extra: extra, Advisory: depthNames(d.Advisory, &taken),
		MaxLines: inRange(d.MaxLines, 0, workflow.MaxDepthSize), Twice: d.Twice || d.ExtraRuns >= workflow.TwiceRuns}
}

func depthJSONOf(d workflow.Depth) *depthJSON {
	if len(d.Always) == 0 && len(d.Advisory) == 0 && len(d.Extra) == 0 && d.MaxLines == 0 && !d.Twice {
		return nil
	}
	return &depthJSON{Always: d.Always, Advisory: d.Advisory, MaxLines: d.MaxLines, Extra: d.Extra, Twice: d.Twice}
}
