package state

import (
	"bytes"
	"encoding/json"
	"strings"
)

type ownedPart struct {
	path []string
	only string
}

var ownedRecords = map[string][]ownedPart{
	reposFile:           {{path: []string{"repos", "[]"}}},
	approvalsFile:       {{path: []string{"approvals", "[]"}}},
	signInFile:          {{}},
	reviewerAgentsFile:  {{path: []string{"files", "[]"}}, {path: []string{"roles", "[]"}}},
	workFile:            {{path: []string{"teams", "{}"}}, {path: []string{"projects", "{}"}}, {path: []string{"sessions", "{}"}}},
	identityFile:        {{}},
	reviewsFile:         {{path: []string{"reviews", "[]"}, only: "override"}},
	egressFile:          {{path: []string{"added", "[]"}}, {path: []string{"gateway"}}},
	capsFile:            {{path: []string{"projects", "{}"}}, {path: []string{"sessions", "[]", "own"}}},
	decisionLogFile:     {{path: []string{"projects", "[]", "entries", "[]"}}},
	savedPromptsFile:    {{path: []string{"projects", "[]", "prompts", "[]"}}},
	haltFile:            {{}},
	lookupsFile:         {{}},
	keyRoutesFile:       {{path: []string{"sides", "{}"}}},
	projectCommandsFile: {{path: []string{"projects", "[]"}}},
	modelsFile:          {{path: []string{"roles", "[]"}}},
	prFollowFile:        {{path: []string{"settings", "[]"}}},
	sessionsFile:        {{path: []string{"sessions", "[]"}}},
	deniedFile:          {{path: []string{"denied", "[]"}}},
	pairsFile:           {{path: []string{"pairs", "[]"}}},
}

func StampOwners(dir string) ([]string, error) {
	owner := ownerIn(dir)
	if owner == "" {
		return nil, nil
	}
	var stamped []string
	var first error
	for name, parts := range ownedRecords {
		data, err := read(dir, name)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if data == nil {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var doc any
		if dec.Decode(&doc) != nil {
			continue
		}
		top, ok := doc.(map[string]any)
		if !ok {
			continue
		}
		if have, versioned := formatOf(data); !versioned || !readsVersion(name, have) || !loadsCleanly(name, dir) {
			continue
		}
		n := 0
		for _, p := range parts {
			n += stampAt(top, p.path, p.only, owner)
		}
		if n == 0 {
			continue
		}
		if err := write(dir, name, top); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		stamped = append(stamped, name)
	}
	return stamped, first
}

func stampAt(v any, path []string, only, owner string) int {
	if len(path) == 0 {
		rec, ok := v.(map[string]any)
		if !ok {
			return 0
		}
		if only != "" {
			if s, _ := rec[only].(string); strings.TrimSpace(s) == "" {
				return 0
			}
		}
		if s, _ := rec["owner"].(string); s != "" {
			return 0
		}
		rec["owner"] = owner
		return 1
	}
	n := 0
	switch path[0] {
	case "[]":
		list, _ := v.([]any)
		for _, item := range list {
			n += stampAt(item, path[1:], only, owner)
		}
	case "{}":
		m, _ := v.(map[string]any)
		for _, item := range m {
			n += stampAt(item, path[1:], only, owner)
		}
	default:
		m, _ := v.(map[string]any)
		if item, ok := m[path[0]]; ok && item != nil {
			n += stampAt(item, path[1:], only, owner)
		}
	}
	return n
}

func loadsCleanly(name, dir string) bool {
	switch name {
	case reposFile:
		return loaded(Repos{Dir: dir}.Load) == nil
	case sessionsFile:
		return loaded(NewSessions(dir).Load) == nil
	}
	return checkStore(name, dir, "") == nil
}

func reownKept(dir, from, to string) {
	for name := range ownedRecords {
		row, ok := rowOf(name)
		if !ok || row.place != placeKeepOwn {
			continue
		}
		data, err := read(dir, name)
		if err != nil || data == nil || !loadsCleanly(name, dir) {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var doc any
		if dec.Decode(&doc) != nil {
			continue
		}
		if reown(doc, from, to) > 0 {
			_ = write(dir, name, doc)
		}
	}
}

func reown(v any, from, to string) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if s, ok := item.(string); ok && k == "owner" && s == from {
				x[k] = to
				n++
				continue
			}
			n += reown(item, from, to)
		}
	case []any:
		for _, item := range x {
			n += reown(item, from, to)
		}
	}
	return n
}
