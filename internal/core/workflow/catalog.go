package workflow

import (
	"crypto/sha256"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

const (
	CatalogFile       = "agents/catalog.json"
	CatalogFormat     = "knackpack-catalog/1"
	MaxCatalogBytes   = 256 << 10
	MaxCatalogAgents  = 100
	MaxCatalogPaths   = 20
	MaxCatalogPattern = 1024
	MaxCatalogSummary = 200
	maxCatalogAll     = 400
	MaxCatalogRepeat  = 100
	MaxCatalogProgram = 1000
	maxCatalogInsts   = 10000
	maxCatalogMatches = 64
	catalogPlugin     = "knackpack:"
)

type CatalogState string

const (
	CatalogNone    CatalogState = "none"
	CatalogUsed    CatalogState = "used"
	CatalogRefused CatalogState = "refused"
	CatalogOff     CatalogState = "off"
)

var catalogName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type CatalogAgent struct {
	Agent   string
	Kind    string
	Summary string
	Always  bool
	Paths   []string
	res     []*regexp.Regexp
	insts   int
}

type Catalog struct {
	State  CatalogState
	Reason string
	Agents []CatalogAgent
	id     uint64
}

type fileMatch struct {
	agent string
	file  string
}

var (
	catalogIDs     atomic.Uint64
	catalogMu      sync.Mutex
	catalogFor     uint64
	catalogMatches = map[[32]byte][]fileMatch{}
)

func NoCatalog() Catalog {
	return Catalog{State: CatalogNone}
}

func OffCatalog() Catalog {
	return Catalog{State: CatalogOff}
}

func RefusedCatalog(reason string) Catalog {
	return Catalog{State: CatalogRefused, Reason: reason}
}

func ParseCatalog(data []byte) Catalog {
	if len(data) > MaxCatalogBytes {
		return RefusedCatalog("it is over " + strconv.Itoa(MaxCatalogBytes>>10) + " KiB")
	}
	value, problem := strictJSON(string(data))
	if problem != "" {
		return RefusedCatalog("it isn't strict JSON: " + problem)
	}
	root, ok := value.(*object)
	if !ok {
		return RefusedCatalog("it must hold one JSON object")
	}
	if f, _ := asString(root.values["format"]); f != CatalogFormat {
		return RefusedCatalog("its \"format\" isn't \"" + CatalogFormat + "\"")
	}
	list, ok := root.values["agents"].([]any)
	if !ok {
		return RefusedCatalog("\"agents\" must be a list")
	}
	if len(list) > MaxCatalogAgents {
		return RefusedCatalog("it lists more than " + strconv.Itoa(MaxCatalogAgents) + " agents")
	}
	out := Catalog{State: CatalogUsed, Agents: []CatalogAgent{}, id: catalogIDs.Add(1)}
	all, insts := 0, 0
	for i, v := range list {
		a, why := catalogAgent(v)
		if why != "" {
			return RefusedCatalog("agent " + strconv.Itoa(i+1) + " " + why)
		}
		if slices.ContainsFunc(out.Agents, func(x CatalogAgent) bool { return x.Agent == a.Agent }) {
			return RefusedCatalog("it lists " + a.Agent + " twice")
		}
		all += len(a.res)
		if all > maxCatalogAll {
			return RefusedCatalog("its agents give more than " + strconv.Itoa(maxCatalogAll) + " path patterns in all")
		}
		if insts += a.insts; insts > maxCatalogInsts {
			return RefusedCatalog("its path patterns compile to more than " + strconv.Itoa(maxCatalogInsts) + " instructions in all")
		}
		out.Agents = append(out.Agents, a)
	}
	return out
}

func catalogAgent(v any) (CatalogAgent, string) {
	o, ok := v.(*object)
	if !ok {
		return CatalogAgent{}, "isn't an object"
	}
	name, _ := asString(o.values["name"])
	if !catalogName.MatchString(name) {
		return CatalogAgent{}, "has no \"name\" of lowercase letters, digits and dashes, at most 64 characters"
	}
	a := CatalogAgent{Agent: catalogPlugin + name}
	if k, ok := o.values["kind"]; ok {
		s, isStr := asString(k)
		if !isStr || (s != "reviewer" && s != "adjudicator") {
			return CatalogAgent{}, "(" + name + ") has a \"kind\" other than \"reviewer\" or \"adjudicator\""
		}
		a.Kind = s
	}
	if s, ok := o.values["summary"]; ok {
		text, isStr := asString(s)
		if !isStr {
			return CatalogAgent{}, "(" + name + ") has a \"summary\" that isn't text"
		}
		a.Summary = catalogText(text)
	}
	applies, ok := o.values["appliesTo"]
	if !ok {
		return a, ""
	}
	ao, isObj := applies.(*object)
	if !isObj {
		return CatalogAgent{}, "(" + name + ") has an \"appliesTo\" that isn't an object"
	}
	if al, ok := ao.values["always"]; ok {
		b, isBool := al.(bool)
		if !isBool {
			return CatalogAgent{}, "(" + name + ") has an \"always\" that isn't true or false"
		}
		a.Always = b
	}
	ps, ok := ao.values["paths"]
	if !ok {
		return a, ""
	}
	paths, isList := ps.([]any)
	if !isList || len(paths) > MaxCatalogPaths {
		return CatalogAgent{}, "(" + name + ") must give \"paths\" as a list of at most " + strconv.Itoa(MaxCatalogPaths) + " patterns"
	}
	for _, p := range paths {
		s, isStr := asString(p)
		if !isStr || s == "" || len(s) > MaxCatalogPattern {
			return CatalogAgent{}, "(" + name + ") has a path pattern that is empty, not text, or over " + strconv.Itoa(MaxCatalogPattern) + " bytes"
		}
		n, why := patternCost(s)
		if why != "" {
			return CatalogAgent{}, "(" + name + ") has a path pattern that " + why
		}
		a.insts += n
		re, err := regexp.Compile(s)
		if err != nil {
			return CatalogAgent{}, "(" + name + ") has a path pattern Go's regexp doesn't compile: " + clip(strings.TrimPrefix(err.Error(), "error parsing regexp: "), 160)
		}
		a.Paths = append(a.Paths, s)
		a.res = append(a.res, re)
	}
	return a, ""
}

func catalogText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxCatalogSummary {
		s = string([]rune(s)[:MaxCatalogSummary]) + "…"
	}
	return s
}

func (a CatalogAgent) FileReviewer() bool {
	return a.Kind != "adjudicator" && !a.Always && len(a.res) > 0
}

func (c Catalog) Summary(agent string) string {
	if c.State != CatalogUsed {
		return ""
	}
	for _, a := range c.Agents {
		if a.Agent == agent {
			return a.Summary
		}
	}
	return ""
}

func (c Catalog) Clone() Catalog {
	c.Agents = slices.Clone(c.Agents)
	return c
}

func (c Catalog) fileList() []fileReviewer {
	if c.State == CatalogOff {
		return nil
	}
	if c.State != CatalogUsed {
		return fileReviewers
	}
	var out []fileReviewer
	for _, a := range c.Agents {
		if !a.FileReviewer() {
			continue
		}
		for _, re := range a.res {
			out = append(out, fileReviewer{agent: a.Agent, re: re})
		}
	}
	return out
}

func patternCost(s string) (int, string) {
	parsed, err := syntax.Parse(s, syntax.Perl)
	if err != nil {
		return 0, ""
	}
	if expanded(parsed) > MaxCatalogProgram {
		return 0, "repeats more than " + strconv.Itoa(MaxCatalogRepeat) + " times or grows past " + strconv.Itoa(MaxCatalogProgram) + " instructions once its repetitions are expanded"
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return 0, ""
	}
	if len(prog.Inst) > MaxCatalogProgram {
		return 0, "compiles to more than " + strconv.Itoa(MaxCatalogProgram) + " instructions"
	}
	return len(prog.Inst), ""
}

func expanded(re *syntax.Regexp) int {
	const over = MaxCatalogProgram + 1
	n := 1 + len(re.Rune)
	for _, sub := range re.Sub {
		n += expanded(sub)
		if n > MaxCatalogProgram {
			return over
		}
	}
	if re.Op == syntax.OpRepeat {
		if re.Min > MaxCatalogRepeat || re.Max > MaxCatalogRepeat {
			return over
		}
		n *= max(re.Max, re.Min, 1)
	}
	return min(n, over)
}

func (c Catalog) matches(files []string) []fileMatch {
	list := c.fileList()
	if c.State != CatalogUsed || c.id == 0 {
		return matchFiles(list, files)
	}
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	var key [32]byte
	h.Sum(key[:0])
	catalogMu.Lock()
	if catalogFor != c.id {
		catalogFor, catalogMatches = c.id, map[[32]byte][]fileMatch{}
	}
	got, ok := catalogMatches[key]
	catalogMu.Unlock()
	if ok {
		return got
	}
	got = matchFiles(list, files)
	catalogMu.Lock()
	if catalogFor == c.id {
		if len(catalogMatches) >= maxCatalogMatches {
			clear(catalogMatches)
		}
		catalogMatches[key] = got
	}
	catalogMu.Unlock()
	return got
}

func matchFiles(list []fileReviewer, files []string) []fileMatch {
	var out []fileMatch
	for _, rule := range list {
		for _, f := range files {
			if rule.re.MatchString(f) {
				out = append(out, fileMatch{agent: rule.agent, file: f})
				break
			}
		}
	}
	return out
}
