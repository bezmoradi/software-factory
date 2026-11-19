package workflow

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
)

const KnackpackFeature = ".knackpack/feature.json"

var knackpackDir = regexp.MustCompile(`^\.knackpack/[a-z0-9][a-z0-9-]{0,63}$`)

func KnackpackFeatureDir(featureJSON []byte) string {
	if len(featureJSON) == 0 || len(featureJSON) > 65536 {
		return ""
	}
	var v map[string]any
	if json.Unmarshal(featureJSON, &v) != nil {
		return ""
	}
	s, ok := v["feature_directory"].(string)
	if !ok || !knackpackDir.MatchString(s) {
		return ""
	}
	return s
}

func StepArtifacts(adapter AdapterID, steps []Step, dir string) []string {
	a, ok := Adapters[adapter]
	if ok && dir == "" {
		dir = a.AnyFolder
	}
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
		if r.Agent != "" {
			check("fileRules["+strconv.Itoa(i)+"]", r.Agent)
		}
	}
	for _, a := range t.Agents {
		check("agents", a)
	}
	return out
}
