package workflow

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/review"
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

const (
	RequirementsMarker      = "knackpack-requirements/1"
	RequirementsFormatSince = "0.40.0"
	requirementsSection     = "### Requirements and acceptance criteria"
	questionsSection        = "### Open questions and risks"
)

type Readiness string

const (
	BriefReady   Readiness = "ready"
	BriefBlocked Readiness = "blocked"
	BriefInvalid Readiness = "invalid"
	BriefNone    Readiness = "none"
)

type BriefCheck struct {
	Kind     Readiness
	Blocking []string
	Problem  string
}

var (
	briefRequirementID = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]{1,4}$`)
	briefQuestionID    = regexp.MustCompile(`^Q-[0-9]{1,4}$`)
)

func WritesRequirementsFormat(knackpackVersion func() string) bool {
	if RequirementsFormatSince == "" {
		return false
	}
	v := knackpackVersion()
	return v != "" && !review.Older(v, RequirementsFormatSince)
}

func RequirementsReadiness(text string) BriefCheck {
	r := briefReader{requirements: map[string]bool{}, questions: map[string]bool{}}
	for line := range strings.SplitSeq(strings.TrimPrefix(text, "\ufeff"), "\n") {
		r.line(strings.TrimSuffix(line, "\r"))
	}
	r.endRequirement()
	switch {
	case r.markers == 0:
		return BriefCheck{Kind: BriefNone}
	case r.problem != "":
		return BriefCheck{Kind: BriefInvalid, Problem: r.problem}
	case !r.requirementsSeen:
		return BriefCheck{Kind: BriefInvalid, Problem: "the brief has no section headed \"" + requirementsSection + "\""}
	case !r.questionsSeen:
		return BriefCheck{Kind: BriefInvalid, Problem: "the brief has no section headed \"" + questionsSection + "\""}
	case r.count == 0:
		return BriefCheck{Kind: BriefInvalid, Problem: "the brief lists no requirement"}
	case len(r.blocking) > 0:
		return BriefCheck{Kind: BriefBlocked, Blocking: r.blocking}
	}
	return BriefCheck{Kind: BriefReady}
}

type briefSection int

const (
	outsideBrief briefSection = iota
	inRequirements
	inQuestions
)

type briefReader struct {
	fence            byte
	fenceLen         int
	markers          int
	section          briefSection
	requirementsSeen bool
	questionsSeen    bool
	count            int
	current          string
	accepted         bool
	requirements     map[string]bool
	questions        map[string]bool
	blocking         []string
	problem          string
}

func (r *briefReader) fail(problem string) {
	if r.problem == "" {
		r.problem = problem
	}
}

func (r *briefReader) endRequirement() {
	if r.current != "" && !r.accepted {
		r.fail(clip(r.current, 40) + " has no acceptance criterion")
	}
	r.current = ""
}

func (r *briefReader) line(l string) {
	char, n, rest := fenceRun(l)
	if r.fence != 0 {
		if char == r.fence && n >= r.fenceLen && !hasText(rest) {
			r.fence, r.fenceLen = 0, 0
		}
		return
	}
	if n >= 3 {
		r.fence, r.fenceLen = char, n
		return
	}
	if strings.Trim(l, " \t") == RequirementsMarker {
		r.markers++
		if r.markers == 2 {
			r.fail("the line " + RequirementsMarker + " appears more than once")
		}
		return
	}
	level := headingLevel(l)
	switch {
	case level >= 1 && level <= 3:
		r.sectionHeading(strings.TrimRight(l, " \t"))
	case level == 4:
		if r.section == inRequirements {
			r.requirement(l)
		}
	case r.section == inRequirements:
		if after, ok := strings.CutPrefix(l, "Acceptance:"); ok && r.current != "" && hasText(after) {
			r.accepted = true
		}
	case r.section == inQuestions:
		r.question(l)
	}
}

func (r *briefReader) sectionHeading(h string) {
	next := outsideBrief
	switch h {
	case requirementsSection:
		if r.requirementsSeen {
			r.fail("the brief has more than one section headed \"" + requirementsSection + "\"")
		}
		next, r.requirementsSeen = inRequirements, true
	case questionsSection:
		if r.questionsSeen {
			r.fail("the brief has more than one section headed \"" + questionsSection + "\"")
		}
		next, r.questionsSeen = inQuestions, true
	}
	r.endRequirement()
	r.section = next
}

func (r *briefReader) requirement(l string) {
	id, ok := idAndText(strings.TrimPrefix(l, "#### "), briefRequirementID)
	switch {
	case !ok:
		r.fail("a heading under the requirements isn't `#### <ID>: …`")
	case r.requirements[id]:
		r.fail(clip(id, 40) + " appears twice")
	}
	r.endRequirement()
	if ok {
		r.count++
		r.requirements[id], r.current, r.accepted = true, id, false
	}
}

func (r *briefReader) question(l string) {
	number, ok := strings.CutPrefix(l, "- Q-")
	if !ok || number == "" || number[0] < '0' || number[0] > '9' {
		return
	}
	id, ok := idAndText(l[2:], briefQuestionID)
	if !ok {
		r.fail("a question isn't `- Q-<n>: …` with 1 to 4 digits")
		return
	}
	shown := clip(id, 40)
	if r.questions[id] {
		r.fail(shown + " appears twice")
	}
	r.questions[id] = true
	end := strings.TrimRight(l, " \t")
	switch count := strings.Count(l, "Blocking:"); {
	case count > 1:
		r.fail(shown + " says whether it blocks more than once")
	case strings.HasSuffix(end, "Blocking: yes"):
		r.blocking = append(r.blocking, id)
	case !strings.HasSuffix(end, "Blocking: no"):
		r.fail(shown + " doesn't say whether it blocks")
	}
}

func idAndText(s string, form *regexp.Regexp) (string, bool) {
	id, after, found := strings.Cut(s, ":")
	text, spaced := strings.CutPrefix(after, " ")
	return id, found && spaced && hasText(text) && form.MatchString(id)
}

func hasText(s string) bool {
	return strings.Trim(s, " \t") != ""
}

func fenceRun(l string) (byte, int, string) {
	i := 0
	for i < len(l) && i < 4 && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	if i > 3 || i == len(l) || (l[i] != '`' && l[i] != '~') {
		return 0, 0, ""
	}
	n := i
	for n < len(l) && l[n] == l[i] {
		n++
	}
	return l[i], n - i, l[n:]
}

func headingLevel(l string) int {
	n := 0
	for n < len(l) && n < 7 && l[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (n < len(l) && l[n] != ' ' && l[n] != '\t') {
		return 0
	}
	return n
}
