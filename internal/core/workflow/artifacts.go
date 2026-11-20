package workflow

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	KnackpackFeature = ".knackpack/feature.json"
	RootCauseFile    = "ROOT-CAUSE.md"
	Confirmed        = "CONFIRMED"
)

var featureName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func KnackpackFeatureDir(featureJSON []byte) string {
	return FeatureDir(featureJSON, ".knackpack")
}

func FeatureDir(featureJSON []byte, folder string) string {
	if folder == "" || len(featureJSON) == 0 || len(featureJSON) > 65536 {
		return ""
	}
	var v map[string]any
	if json.Unmarshal(featureJSON, &v) != nil {
		return ""
	}
	s, ok := v["feature_directory"].(string)
	name, inside := strings.CutPrefix(s, folder+"/")
	if !ok || !inside || !featureName.MatchString(name) {
		return ""
	}
	return s
}

func StepArtifacts(adapter AdapterID, steps []Step, dir string) []string {
	a, ok := Adapters[adapter]
	if !ok {
		return nil
	}
	return StepArtifactsOf(a, steps, dir)
}

func StepArtifactsOf(a Adapter, steps []Step, dir string) []string {
	ok := a.AnyFolder != ""
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

func RootCauseStatus(text string) string {
	under := false
	blank := func(r rune) bool { return r == ' ' || r == '\t' }
	for l := range strings.SplitSeq(text, "\n") {
		switch {
		case !under:
			under = strings.TrimRight(l, " \t") == "## Status"
		case strings.HasPrefix(l, "#"):
			return ""
		default:
			if f := strings.FieldsFunc(l, blank); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}
