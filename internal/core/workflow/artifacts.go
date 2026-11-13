package workflow

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	specDir     = regexp.MustCompile(`^specs/[\w.-]{1,100}$`)
	specInDiff  = regexp.MustCompile(`^(specs/[\w.-]{1,100})/spec\.md$`)
	featureKeys = []string{"feature_directory", "featureDirectory", "feature_dir", "directory", "path"}
)

func SpecFeatureDir(featureJSON []byte, changed []string) string {
	if len(featureJSON) > 0 && len(featureJSON) <= 65536 {
		var v map[string]any
		if json.Unmarshal(featureJSON, &v) == nil {
			for _, k := range featureKeys {
				s, ok := v[k].(string)
				if !ok {
					continue
				}
				s = strings.TrimSuffix(strings.TrimPrefix(s, "./"), "/")
				if specDir.MatchString(s) {
					return s
				}
				break
			}
		}
	}
	var found []string
	for _, p := range changed {
		if m := specInDiff.FindStringSubmatch(p); m != nil && !slices.Contains(found, m[1]) {
			found = append(found, m[1])
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

func StepArtifacts(adapter AdapterID, steps []Step, dir string) []string {
	a, ok := Adapters[adapter]
	if !ok || dir == "" {
		return nil
	}
	var out []string
	for _, s := range steps {
		for _, p := range a.Artifacts[s] {
			out = append(out, dir+"/"+p)
		}
		out = append(out, a.RootArtifacts[s]...)
	}
	return out
}

func OnlyArtifacts(paths, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, p := range paths {
		if !artifactPath(p, patterns) {
			return false
		}
	}
	return true
}

func AgentProblems(t *Team, installed []string) []string {
	if t == nil || len(installed) == 0 {
		return nil
	}
	var out []string
	check := func(at, agent string) {
		if !slices.Contains(installed, agent) {
			out = append(out, at+": "+clip(agent, 120)+" isn't an agent of a plugin installed in the reviewer image, and only those can review")
		}
	}
	for i, r := range t.FileRules {
		check("fileRules["+strconv.Itoa(i)+"]", r.Agent)
	}
	for _, a := range t.Agents {
		check("agents", a)
	}
	return out
}
